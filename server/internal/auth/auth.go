// Package auth 提供 JWT 签发校验、登录（含防爆破）、用户管理与权限守卫。
//
// 产品级要点（登录态稳定化重构 2026-10：身份与授权分离）：
//   - JWT 只承载身份（sub/tv/exp），token_version 吊销仅发生在密码变更/重置；
//   - 角色/权限/禁用/删号靠每请求实时核对（10s 缓存 + 变更即失效）即时生效，
//     **不再把已登录用户踢下线**——宿主同步角色、权限投影、显示名调整全不掉线，
//     变化经 sys:user 总线推送让前端就地回正会话信息；
//   - 登录失败计数 + 锁定（每 用户名+IP 10 分钟 8 次失败锁 15 分钟）；
//   - 最后一个管理员不可删除 / 降级。
//
// 文件划分：auth.go（JWT/中间件/登录/续期/短票/防爆破/守卫）、users.go（用户
// CRUD/批量同步/自助接口 + 统一变更内核 patchUser）、hostapi.go（exchange/
// introspect 与 /api/host/* 宿主机对机接口）。
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"openos/server/internal/store"
)

// Claims JWT 载荷。
type Claims struct {
	Sub string `json:"sub"`
	// Role/Perms 为签发时点的展示快照：服务端鉴权一律走实时库值
	//（currentUserState），二者不参与任何失效判定（身份与授权分离）。
	Role  string   `json:"role"`
	Perms []string `json:"perms,omitempty"`
	TV    int64    `json:"tv"`
	Exp   int64    `json:"exp"`
	Iat   int64    `json:"iat"`
}

// Service 认证服务。
type Service struct {
	db    *store.DB
	mu    sync.RWMutex
	secret []byte
	ttl    time.Duration
	// exchangeSecret 非空时启用令牌交换（宿主应用集成：SSO 会话换 OpenOS JWT）。
	exchangeSecret string
	// exchangeRoles 非空时限制 exchange 可指定的角色（白名单，逗号分隔），
	// 防止共享密钥泄露后被用于铸造 admin。
	exchangeRoles map[string]bool
	// externalAuth 外部身份模式（OPENOS_AUTH_MODE=external）：身份托管于宿主/IdP，
	// 账密登录与本地自助改密整体下线、演示账号种子跳过；用户只能经 exchange 开通。
	externalAuth bool

	// 用户态缓存（角色/token_version/禁用态），TTL 10s
	cacheMu sync.Mutex
	cache   map[string]cacheEntry

	// 登录防爆破
	bruteMu sync.Mutex
	brute   map[string]*attempt

	// 一次性短票（见 ticketStore）：/ws 与 query 鉴权端点用短票替代长期 JWT 入 URL
	tickets ticketStore

	// 用户删除后的级联清理钩子（main 注入：清理磁盘沙箱 / goder 数据）
	onUserDeleted func(username string)
	// 用户身份（角色/权限/显示名/禁用）变化钩子（main 注入：总线 sys:user 定向
	// 推送，角色变化另广播 sys:apps-changed）——授权变化不再吊销令牌，前端靠
	// 这条推送就地更新，不再依赖 401/重登感知。
	onIdentityChanged func(username string, roleChanged bool, snapshot map[string]any)
}

// ticketStore 一次性短票：30s 有效、用后即焚。供无法携带 header 的场景
//（/ws 升级、附件/商店取件等 query 鉴权）——长期 JWT 不再出现在 URL/日志里
//（安全审计 2026-10）。纯内存，重启即失效（30s 窗口内客户端可重取）。
type ticketStore struct {
	mu sync.Mutex
	m  map[string]ticketEntry
}

type ticketEntry struct {
	user    string
	expires time.Time
}

const ticketTTL = 30 * time.Second

// issue 签发短票（顺手清扫过期项，量小无需独立协程）。
func (t *ticketStore) issue(user string) string {
	id := randomHex(24)
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]ticketEntry{}
	}
	for k, e := range t.m {
		if now.After(e.expires) {
			delete(t.m, k)
		}
	}
	t.m[id] = ticketEntry{user: user, expires: now.Add(ticketTTL)}
	return id
}

// consume 核销短票：不存在/过期/已用 均失败（一次性）。
func (t *ticketStore) consume(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[id]
	if ok {
		delete(t.m, id)
	}
	if !ok || time.Now().After(e.expires) {
		return "", false
	}
	return e.user, true
}

type cacheEntry struct {
	role     string
	tv       int64
	disabled bool
	perms    []string
	at       time.Time
}

type attempt struct {
	fails     int
	firstFail time.Time
	lockedTo  time.Time
}

const (
	maxFails     = 8
	failWindow   = 10 * time.Minute
	lockDuration = 15 * time.Minute
)

// New 创建认证服务；secret 为空时自动生成并持久化到 system_kv（与用户设置数据分离）。
func New(db *store.DB, secret string) *Service {
	s := &Service{
		db:    db,
		ttl:   24 * time.Hour,
		cache: map[string]cacheEntry{},
		brute: map[string]*attempt{},
	}
	if secret == "" {
		v, _ := db.GetSystemKV("jwt_secret")
		if v == "" {
			buf := make([]byte, 32)
			rand.Read(buf)
			v = base64.RawStdEncoding.EncodeToString(buf)
			db.SetSystemKV("jwt_secret", v)
		}
		secret = v
	}
	s.secret = []byte(secret)
	return s
}

// SetTokenTTL 配置令牌有效期（默认 24h；OPENOS_TOKEN_TTL 可设 168h 等长会话，
// login/exchange/refresh 与会话 Cookie 全跟随，响应体带 tokenTtl 供前端自适应
// 续期间隔）。
func (s *Service) SetTokenTTL(d time.Duration) {
	if d >= time.Minute {
		s.ttl = d
	}
}

// SetAuthMode 配置认证模式：external = 外部身份（宿主 SSO / IdP）托管，
// 本地账密通道整体下线；其余值（含空、local）为默认本地账密模式。
func (s *Service) SetAuthMode(mode string) {
	s.externalAuth = strings.EqualFold(strings.TrimSpace(mode), "external")
}

// SetExchangeSecret 配置令牌交换密钥（宿主应用服务端持有，用于换取 OpenOS 身份）；
// 为空（默认）时交换端点返回 503，保持独立部署的安全面不变。
func (s *Service) SetExchangeSecret(v string) { s.exchangeSecret = v }

// CheckExchangeSecret 校验宿主间调用密钥（与令牌交换共用 OPENOS_EXCHANGE_SECRET，
// 恒定时间比对；未启用时一律拒绝）。供宿主侧的其它服务端到服务端接口复用，
// 如事件总线定向推送（POST /api/hub/publish）。
func (s *Service) CheckExchangeSecret(v string) bool {
	return s.exchangeSecret != "" && subtle.ConstantTimeCompare([]byte(v), []byte(s.exchangeSecret)) == 1
}

// ExchangeSecretValue 暴露宿主间密钥本体：仅用于 openos 主动回调宿主的场景
// （宿主角色编辑回写转发），随请求携带 X-Exchange-Secret——宿主侧用同一把密钥
// 校验，双向同源。调用方均为进程内路由处理器，不外发给前端。
func (s *Service) ExchangeSecretValue() string { return s.exchangeSecret }

// SetExchangeRoleWhitelist 配置 exchange 可指定的角色白名单（逗号分隔，空 = 不限制）。
func (s *Service) SetExchangeRoleWhitelist(v string) {
	if v == "" {
		s.exchangeRoles = nil
		return
	}
	m := map[string]bool{}
	for _, r := range strings.Split(v, ",") {
		if r = strings.TrimSpace(r); r != "" {
			m[r] = true
		}
	}
	s.exchangeRoles = m
}

// SetUserDeletedHook 注册用户删除后的级联清理（main 注入：磁盘沙箱、goder 数据目录）。
func (s *Service) SetUserDeletedHook(fn func(username string)) { s.onUserDeleted = fn }

// SetIdentityChangedHook 注册用户身份变化推送（main 注入总线发布，见 onIdentityChanged）。
func (s *Service) SetIdentityChangedHook(fn func(username string, roleChanged bool, snapshot map[string]any)) {
	s.onIdentityChanged = fn
}

// ---- JWT ----

func (s *Service) sign(data []byte) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Issue 签发 Token（claims 内含功能级权限点快照：用户级覆盖优先，否则按角色推导）。
func (s *Service) Issue(username, role string, tv int64) (string, error) {
	now := time.Now()
	c := Claims{Sub: username, Role: role, Perms: s.effectivePerms(username, role), TV: tv, Iat: now.Unix(), Exp: now.Add(s.ttl).Unix()}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	body := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + s.sign([]byte(body)), nil
}

// effectivePerms 有效权限点：users.perms 覆盖（宿主 exchange 快照）优先；
// 为空按 role_perms(role) 推导（demo/独立部署的角色权限矩阵）。失败为空集
//（admin 由判定语义直通，不受影响）。
func (s *Service) effectivePerms(username, role string) []string {
	var override string
	if err := s.db.QueryRow(`SELECT perms FROM users WHERE username=?`, username).Scan(&override); err == nil && strings.TrimSpace(override) != "" && override != "null" {
		var perms []string
		if json.Unmarshal([]byte(override), &perms) == nil && len(perms) > 0 {
			return perms
		}
	}
	perms, err := s.db.RolePerms(role)
	if err != nil {
		return nil
	}
	return perms
}

// Verify 校验签名与有效期（不查库）。
func (s *Service) Verify(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("bad token")
	}
	body := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(s.sign([]byte(body))), []byte(parts[2])) {
		return nil, errors.New("bad signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var c Claims
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if time.Now().Unix() > c.Exp {
		return nil, errors.New("token expired")
	}
	return &c, nil
}

// VerifySession 校验令牌并实时核对用户态（存在性 / token_version / 禁用态），与
// REST 中间件同一套判定。角色不参与失效比较——授权变化实时生效不掉线（身份与
// 授权分离）。供 WebSocket 升级、连接周期复核等非中间件场景复用。
func (s *Service) VerifySession(token string) (*Identity, error) {
	c, err := s.Verify(token)
	if err != nil {
		return nil, errors.New("凭证已失效")
	}
	exists, role, tv, disabled, perms := s.currentUserState(c.Sub)
	if !exists || tv != c.TV || disabled {
		return nil, errors.New("凭证已失效")
	}
	return &Identity{Username: c.Sub, Role: role, Perms: perms}, nil
}

// currentUserState 取用户当前角色、token 版本、禁用态与有效权限点（10s 缓存；
// 权限与角色同窗缓存——角色/权限变更须 InvalidateCache 立即生效）。
func (s *Service) currentUserState(username string) (exists bool, role string, tv int64, disabled bool, perms []string) {
	s.cacheMu.Lock()
	if e, ok := s.cache[username]; ok && time.Since(e.at) < 10*time.Second {
		s.cacheMu.Unlock()
		return true, e.role, e.tv, e.disabled, e.perms
	}
	s.cacheMu.Unlock()

	info, err := s.db.GetUserAuth(username)
	if err != nil || !info.Exists {
		return false, "", 0, false, nil
	}
	perms = s.effectivePerms(username, info.Role)
	s.cacheMu.Lock()
	s.cache[username] = cacheEntry{role: info.Role, tv: info.TokenVersion, disabled: info.Disabled, perms: perms, at: time.Now()}
	s.cacheMu.Unlock()
	return true, info.Role, info.TokenVersion, info.Disabled, perms
}

// currentUserRole 旧签名兼容（只需角色/版本/禁用态的调用方）。
func (s *Service) currentUserRole(username string) (bool, string, int64, bool) {
	exists, role, tv, disabled, _ := s.currentUserState(username)
	return exists, role, tv, disabled
}

// InvalidateCache 用户态变更后调用，立即生效。
func (s *Service) InvalidateCache(username string) {
	s.cacheMu.Lock()
	delete(s.cache, username)
	s.cacheMu.Unlock()
}

// InvalidateAllCache 角色矩阵级变化（PUT /api/roles、host/declare 投影）后调用：
// 影响一批用户的有效权限，整体清缓存立即生效。
func (s *Service) InvalidateAllCache() {
	s.cacheMu.Lock()
	s.cache = map[string]cacheEntry{}
	s.cacheMu.Unlock()
}

// notifyIdentityChanged 用户角色/权限/显示名/禁用变化后：重取最新身份交注入钩子
// （main → 总线 sys:user 定向推送，角色变化另广播 sys:apps-changed 刷新应用
// 列表）。授权变化不再吊销令牌，前端靠这条推送就地回正会话信息。
func (s *Service) notifyIdentityChanged(username string, roleChanged bool) {
	if s.onIdentityChanged == nil {
		return
	}
	info, err := s.db.GetUserAuth(username)
	if err != nil || !info.Exists {
		return
	}
	perms := s.effectivePerms(username, info.Role)
	if perms == nil {
		perms = []string{}
	}
	s.onIdentityChanged(username, roleChanged, map[string]any{
		"username": username, "role": info.Role, "displayName": info.DisplayName,
		"disabled": info.Disabled, "perms": perms,
	})
}

// ---- 防爆破 ----

func (s *Service) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// locked 返回剩余锁定时间（0 表示未锁）。
func (s *Service) locked(key string) time.Duration {
	s.bruteMu.Lock()
	defer s.bruteMu.Unlock()
	a, ok := s.brute[key]
	if !ok {
		return 0
	}
	if a.lockedTo.After(time.Now()) {
		return time.Until(a.lockedTo)
	}
	if time.Since(a.firstFail) > failWindow {
		delete(s.brute, key) // 窗口已过，清零
	}
	return 0
}

func (s *Service) fail(key string) {
	s.bruteMu.Lock()
	defer s.bruteMu.Unlock()
	a := s.brute[key]
	if a == nil || time.Since(a.firstFail) > failWindow {
		a = &attempt{firstFail: time.Now()}
		s.brute[key] = a
	}
	a.fails++
	if a.fails >= maxFails {
		a.lockedTo = time.Now().Add(lockDuration)
	}
}

func (s *Service) success(key string) {
	s.bruteMu.Lock()
	delete(s.brute, key)
	s.bruteMu.Unlock()
}

// ---- 密码 ----

// pbkdf2 迭代次数：600000（对齐 OWASP 2023 对 PBKDF2-SHA256 的建议，安全审计
// 2026-10 上调）；20000/4096 为历史参数，登录时对存量 hash 透明升级到当前值。
const pbkdf2Iters = 600000

func hashWithIters(password, salt string, iters int) string {
	key, err := pbkdf2.Key(sha256.New, password, []byte(salt), iters, 32)
	if err != nil {
		panic(err)
	}
	return base64.RawStdEncoding.EncodeToString(key)
}

func hashPassword(password, salt string) string {
	return hashWithIters(password, salt, pbkdf2Iters)
}

// legacyIters 历史迭代参数（新→旧），checkPassword 逐级兼容并触发透明升级。
var legacyIters = []int{20000, 4096}

// checkPassword 校验密码，兼容历史参数的存量 hash。
func checkPassword(password, salt, hash string) bool {
	if hashWithIters(password, salt, pbkdf2Iters) == hash {
		return true
	}
	for _, it := range legacyIters {
		if hashWithIters(password, salt, it) == hash {
			return true
		}
	}
	return false
}

func newSalt() string {
	buf := make([]byte, 12)
	rand.Read(buf)
	return base64.RawStdEncoding.EncodeToString(buf)
}

// WeakPassword 产品级密码强度下限：≥8 位且含字母与数字。
func WeakPassword(p string) bool {
	if len(p) < 8 {
		return true
	}
	var hasDigit, hasLetter bool
	for _, r := range p {
		if r >= '0' && r <= '9' {
			hasDigit = true
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
		}
	}
	return !hasDigit || !hasLetter
}

// ---- HTTP 上下文 ----

type ctxKey string

const userKey ctxKey = "user"

// Identity 请求身份。
type Identity struct {
	Username string
	Role     string
	// Perms 功能级权限点（签发/请求时的有效集合：用户级覆盖 > 角色推导）
	Perms []string
}

// Can 功能级权限判定（与 grf-hotel Claims.HasPerm 同语义：admin 直通、* 通配）。
func (id *Identity) Can(perm string) bool {
	if id == nil {
		return false
	}
	return store.HasPermJudge(id.Role, id.PermStore(), perm)
}

// PermStore 返回权限集（nil 安全）。
func (id *Identity) PermStore() []string {
	if id == nil {
		return nil
	}
	return id.Perms
}

// Middleware 校验 Bearer Token（或 openos_token Cookie——供 goder 等同源嵌入组件
// 自动携带）并实时核对用户态（存在性/token_version/禁用）。角色与权限取实时库值
// 注入 Identity：变化即时生效、不掉线（身份与授权分离）。
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || token == r.Header.Get("Authorization") {
			if c, err := r.Cookie(TokenCookie); err == nil {
				token = c.Value
			}
		}
		c, err := s.Verify(token)
		if err != nil {
			jsonError(w, http.StatusUnauthorized, "未认证或登录已过期")
			return
		}
		exists, role, tv, disabled, perms := s.currentUserState(c.Sub)
		if !exists || tv != c.TV || disabled {
			jsonError(w, http.StatusUnauthorized, "凭证已失效，请重新登录")
			return
		}
		next.ServeHTTP(w, r.WithContext(contextWithIdentity(r, &Identity{Username: c.Sub, Role: role, Perms: perms})))
	})
}

// TokenCookie 供嵌入组件（goder 等）同源自动携带的令牌 Cookie 名。
const TokenCookie = "openos_token"

// setSessionCookie 统一种会话 Cookie（HttpOnly + SameSite=Strict；HTTPS 直连或
// 反代声明 https 时加 Secure——安全审计 2026-10 之前缺 Secure，明文 HTTP 可剥）。
func (s *Service) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	c := &http.Cookie{
		Name:     TokenCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	}
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		c.Secure = true
	}
	http.SetCookie(w, c)
}

// IssueTicket 签发一次性短票（登录态调用；见 ticketStore）。
func (s *Service) IssueTicket(user string) string { return s.tickets.issue(user) }

// ConsumeTicketFromRequest 核销 query 短票（?ticket=）并返回用户名；
// 与 VerifySession 组合出完整身份（query 端点鉴权用）。
func (s *Service) ConsumeTicketFromRequest(r *http.Request) (string, bool) {
	return s.tickets.consume(r.URL.Query().Get("ticket"))
}

// CurrentUserRole 用户当前角色与状态（导出给总线短票连接的复核与 query 鉴权）。
func (s *Service) CurrentUserRole(username string) (exists bool, role string, tv int64, disabled bool) {
	return s.currentUserRole(username)
}

// ValidRoleKey 角色键合法（导出：角色目录 CRUD 校验用，与 exchange 同一规则）。
func ValidRoleKey(role string) bool { return validRole(role) }

// WsTicket POST /api/auth/ws-ticket（登录态）—— 签发一次性短票：
// /ws 与附件/商店取件等无法携带 header 的 query 鉴权场景用它替代长期 JWT。
func (s *Service) WsTicket(w http.ResponseWriter, r *http.Request) {
	id := FromContext(r)
	writeJSON(w, map[string]any{"ticket": s.IssueTicket(id.Username), "ttl": int(ticketTTL.Seconds())})
}

// AIProxyKey 每用户的 AI 代理密钥（"uid:hex(HMAC)"，HMAC 用 jwt_secret 派生、
// 跨启动稳定）：goder 托管供应商指向 /api/ai/v1/* 代理端点时作 Bearer 凭据，
// 平台 API Key 只留服务端、不再写进每个 goder 用户的 settings.json。
func (s *Service) AIProxyKey(username string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("ai-proxy:" + username))
	return username + ":" + hex.EncodeToString(mac.Sum(nil))
}

// UserForAIProxyKey 校验代理密钥并返回其绑定的用户名（uid:hex 形态）。
func (s *Service) UserForAIProxyKey(key string) (string, bool) {
	i := strings.IndexByte(key, ':')
	if i <= 0 || i == len(key)-1 {
		return "", false
	}
	user := key[:i]
	if subtle.ConstantTimeCompare([]byte(s.AIProxyKey(user)), []byte(key)) != 1 {
		return "", false
	}
	return user, true
}

// RequireAdmin 要求管理员角色。
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := FromContext(r); id == nil || id.Role != "admin" {
			jsonError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequirePerm 要求功能级权限点（admin 直通；2026-10 起管理端点已按
// users.manage/apps.manage/market.manage/ai.manage/system.manage 实际接管）。
// perms 为空集的非 admin 一律拒绝。
func RequirePerm(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id := FromContext(r); id != nil && id.Can(perm) {
				next.ServeHTTP(w, r)
				return
			}
			jsonError(w, http.StatusForbidden, "需要权限："+perm)
		})
	}
}

// FromContext 取当前身份。
func FromContext(r *http.Request) *Identity {
	id, _ := r.Context().Value(userKey).(*Identity)
	return id
}

// ---- Handlers ----

// Login POST /api/auth/login
func (s *Service) Login(w http.ResponseWriter, r *http.Request) {
	if s.externalAuth {
		jsonError(w, http.StatusForbidden, "本地登录已禁用（OPENOS_AUTH_MODE=external），请经统一身份登录")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := jsonDecode(r, &req); err != nil || req.Username == "" {
		jsonError(w, 400, "参数错误")
		return
	}
	ip := s.clientIP(r)
	key := req.Username + "|" + ip
	if d := s.locked(key); d > 0 {
		s.db.Audit(req.Username, "login.locked", "ip="+ip, ip)
		jsonError(w, http.StatusTooManyRequests, "尝试过于频繁，请 "+d.Round(time.Minute).String()+" 后再试")
		return
	}

	info, err := s.db.GetUserAuth(req.Username)
	if err != nil || !info.Exists || !checkPassword(req.Password, info.Salt, info.PasswordHash) {
		s.fail(key)
		s.db.Audit(req.Username, "login.fail", "ip="+ip, ip)
		jsonError(w, 401, "用户名或密码错误")
		return
	}
	if info.Disabled {
		s.db.Audit(req.Username, "login.denied", "disabled", ip)
		jsonError(w, 403, "账号已被禁用，请联系管理员")
		return
	}
	// 旧参数 hash（4096 轮）透明升级到当前参数
	if hashWithIters(req.Password, info.Salt, pbkdf2Iters) != info.PasswordHash {
		ns := newSalt()
		s.db.Exec(`UPDATE users SET password_hash=?, salt=? WHERE username=?`, hashPassword(req.Password, ns), ns, req.Username)
		s.InvalidateCache(req.Username)
	}
	s.success(key)

	token, err := s.Issue(req.Username, info.Role, info.TokenVersion)
	if err != nil {
		jsonError(w, 500, "签发令牌失败")
		return
	}
	// 同时种 Cookie：goder 等嵌入组件的同源 fetch 自动携带（httpOnly，JS 不可读）
	s.setSessionCookie(w, r, token, int(s.ttl.Seconds()))
	s.db.Audit(req.Username, "login.ok", "ip="+ip, ip)
	perms := s.effectivePerms(req.Username, info.Role)
	writeJSON(w, map[string]any{"token": token, "tokenTtl": int(s.ttl.Seconds()), "user": map[string]any{
		"username": req.Username, "role": info.Role, "displayName": info.DisplayName, "perms": perms,
	}})
}

// Logout GET /api/auth/logout —— 清除令牌 Cookie。
func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: TokenCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		w.Header().Add("Set-Cookie", TokenCookie+"=; Path=/; Max-Age=0; Secure")
	}
	writeJSON(w, ok())
}

func randomHex(n int) string {
	buf := make([]byte, n)
	rand.Read(buf)
	return hex.EncodeToString(buf)
}

// initialTokenVersion 新建用户的起始 token_version：取当前 Unix 秒，
// 防止「删除用户后同名重建（tv 从 0 重新计数）导致删除前签发的旧令牌复活」。
func initialTokenVersion() int64 { return time.Now().Unix() }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Refresh GET /api/auth/refresh（Bearer）—— 滑动续期：回发新 token 并续种 Cookie
// （goder 等靠 Cookie 的同源请求依赖后者）。前端定时调用即可长期在线不掉线，
// 宿主集成（SSO）场景也可用它做 401 自愈，而不必每次回宿主重新交换。
func (s *Service) Refresh(w http.ResponseWriter, r *http.Request) {
	id := FromContext(r)
	info, err := s.db.GetUserAuth(id.Username)
	if err != nil || !info.Exists {
		jsonError(w, 401, "用户不存在")
		return
	}
	token, err := s.Issue(id.Username, info.Role, info.TokenVersion)
	if err != nil {
		jsonError(w, 500, "签发失败")
		return
	}
	s.setSessionCookie(w, r, token, int(s.ttl.Seconds()))
	writeJSON(w, map[string]any{"token": token, "tokenTtl": int(s.ttl.Seconds()), "user": map[string]any{
		"username": id.Username, "role": info.Role, "displayName": info.DisplayName,
		"perms": s.effectivePerms(id.Username, info.Role),
	}})
}

// Me GET /api/auth/me
func (s *Service) Me(w http.ResponseWriter, r *http.Request) {
	id := FromContext(r)
	info, _ := s.db.GetUserAuth(id.Username)
	perms := id.PermStore()
	if perms == nil {
		perms = []string{}
	}
	writeJSON(w, map[string]any{"username": id.Username, "role": id.Role, "displayName": info.DisplayName, "perms": perms})
}

func validRole(r string) bool {
	// 基础三角色之外允许宿主应用注入的自定义角色（如业务角色名），
	// 仅约束为安全字符集，避免注入/越权字符串；admin 语义仅 RequireAdmin 认定。
	return len(r) > 0 && len(r) <= 32 && roleRe.MatchString(r)
}

var roleRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ---- 工具 ----

func contextWithIdentity(r *http.Request, id *Identity) context.Context {
	return context.WithValue(r.Context(), userKey, id)
}

// WithIdentity 把身份注入请求上下文并返回新请求（进程内复用 REST handler 用，
// 如 osmcp 插件以某用户身份调用 vfs handler）。身份须由调用方先行校验。
func WithIdentity(r *http.Request, id *Identity) *http.Request {
	return r.WithContext(contextWithIdentity(r, id))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func jsonDecode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func ok() map[string]string { return map[string]string{"ok": "1"} }
