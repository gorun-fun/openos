// Package auth 提供 JWT 签发校验、登录（含防爆破）、用户管理与权限守卫。
//
// 产品级要点：
//   - JWT 携带 token_version，角色变更 / 删除用户 / 改密即时失效（带 10s 缓存）
//   - 登录失败计数 + 锁定（每 用户名+IP 10 分钟 8 次失败锁 15 分钟）
//   - 最后一个管理员不可删除 / 降级
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
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"openos/server/internal/store"
)

// Claims JWT 载荷。
type Claims struct {
	Sub  string `json:"sub"`
	Role string `json:"role"`
	TV   int64  `json:"tv"`
	Exp  int64  `json:"exp"`
	Iat  int64  `json:"iat"`
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

	// 用户删除后的级联清理钩子（main 注入：清理磁盘沙箱 / goder 数据）
	onUserDeleted func(username string)
}

type cacheEntry struct {
	role     string
	tv       int64
	disabled bool
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

// ---- JWT ----

func (s *Service) sign(data []byte) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Issue 签发 Token。
func (s *Service) Issue(username, role string, tv int64) (string, error) {
	now := time.Now()
	c := Claims{Sub: username, Role: role, TV: tv, Iat: now.Unix(), Exp: now.Add(s.ttl).Unix()}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	body := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + s.sign([]byte(body)), nil
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

// VerifySession 校验令牌并实时核对用户态（存在性 / 角色 / token_version / 禁用态），
// 与 REST 中间件同一套判定。供 WebSocket 升级、连接周期复核等非中间件场景复用。
func (s *Service) VerifySession(token string) (*Identity, error) {
	c, err := s.Verify(token)
	if err != nil {
		return nil, err
	}
	exists, role, tv, disabled := s.currentUserRole(c.Sub)
	if !exists || role != c.Role || tv != c.TV || disabled {
		return nil, errors.New("凭证已失效")
	}
	return &Identity{Username: c.Sub, Role: role}, nil
}

// currentUserRole 取用户当前角色、token 版本与禁用态（10s 缓存）。
func (s *Service) currentUserRole(username string) (exists bool, role string, tv int64, disabled bool) {
	s.cacheMu.Lock()
	if e, ok := s.cache[username]; ok && time.Since(e.at) < 10*time.Second {
		s.cacheMu.Unlock()
		return true, e.role, e.tv, e.disabled
	}
	s.cacheMu.Unlock()

	info, err := s.db.GetUserAuth(username)
	if err != nil || !info.Exists {
		return false, "", 0, false
	}
	s.cacheMu.Lock()
	s.cache[username] = cacheEntry{role: info.Role, tv: info.TokenVersion, disabled: info.Disabled, at: time.Now()}
	s.cacheMu.Unlock()
	return true, info.Role, info.TokenVersion, info.Disabled
}

// InvalidateCache 用户态变更后调用，立即生效。
func (s *Service) InvalidateCache(username string) {
	s.cacheMu.Lock()
	delete(s.cache, username)
	s.cacheMu.Unlock()
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

// pbkdf2 迭代次数：当前 20000；4096 用于兼容升级前的存量 hash。
const pbkdf2Iters = 20000

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

// checkPassword 校验密码，兼容旧参数（4096 轮）的存量 hash。
func checkPassword(password, salt, hash string) bool {
	if hashWithIters(password, salt, pbkdf2Iters) == hash {
		return true
	}
	return hashWithIters(password, salt, 4096) == hash
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
}

// Middleware 校验 Bearer Token（或 openos_token Cookie——供 goder 等同源嵌入组件自动携带）并实时核对用户态。
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
		exists, role, tv, disabled := s.currentUserRole(c.Sub)
		if !exists || role != c.Role || tv != c.TV || disabled {
			jsonError(w, http.StatusUnauthorized, "凭证已失效，请重新登录")
			return
		}
		next.ServeHTTP(w, r.WithContext(contextWithIdentity(r, &Identity{Username: c.Sub, Role: role})))
	})
}

// TokenCookie 供嵌入组件（goder 等）同源自动携带的令牌 Cookie 名。
const TokenCookie = "openos_token"

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
	http.SetCookie(w, &http.Cookie{
		Name:     TokenCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.ttl.Seconds()),
	})
	s.db.Audit(req.Username, "login.ok", "ip="+ip, ip)
	writeJSON(w, map[string]any{"token": token, "user": map[string]any{
		"username": req.Username, "role": info.Role, "displayName": info.DisplayName,
	}})
}

// Logout GET /api/auth/logout —— 清除令牌 Cookie。
func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: TokenCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	writeJSON(w, ok())
}

// Exchange POST /api/auth/exchange —— 宿主应用服务端以共享密钥换取 OpenOS JWT。
//
// 供「宿主应用集成 OpenOS」场景：宿主完成自己的 SSO 认证后，服务端到服务端
// 调用本端点为该用户换取 OpenOS 身份（浏览器无需再过 OpenOS 登录）。
//   - 请求头 X-Exchange-Secret 与 OPENOS_EXCHANGE_SECRET 恒定时间比对；
//   - 配置 OPENOS_EXCHANGE_ROLES 白名单后，共享密钥泄露也无法铸造白名单外角色（如 admin）；
//   - 用户不存在时自动开通（随机不可登录密码，登录只能经宿主，displayName 作为
//     初始显示名写入）；
//   - 角色变化时同步并 Bump 旧令牌；已有用户的显示名不随 exchange 覆盖（登录
//     通道不做展示名管理，改名走 SyncUsers / UpdateUser / 账户页自助接口）；
//   - 响应与登录一致：{token, user}，并种同源 Cookie 供嵌入组件使用。
func (s *Service) Exchange(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.exchangeSecret == "" {
		jsonError(w, http.StatusServiceUnavailable, "令牌交换未启用")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Exchange-Secret")), []byte(s.exchangeSecret)) != 1 {
		s.db.Audit("?", "auth.exchange.deny", "ip="+ip, ip)
		jsonError(w, http.StatusUnauthorized, "交换密钥错误")
		return
	}
	var req struct {
		Username    string `json:"username"`
		Role        string `json:"role"`
		DisplayName string `json:"displayName"`
	}
	if err := jsonDecode(r, &req); err != nil || req.Username == "" {
		jsonError(w, 400, "参数错误")
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	if !validRole(req.Role) {
		jsonError(w, 400, "非法角色")
		return
	}
	if s.exchangeRoles != nil && !s.exchangeRoles[req.Role] {
		s.db.Audit(req.Username, "auth.exchange.deny", "role not allowed: "+req.Role, ip)
		jsonError(w, 400, "角色不在交换白名单内（OPENOS_EXCHANGE_ROLES）")
		return
	}

	info, err := s.db.GetUserAuth(req.Username)
	if err != nil {
		jsonError(w, 500, "读取用户失败")
		return
	}
	if !info.Exists {
		// 自动开通：密码置为随机值（宿主体系外的登录通道不可用）
		pw := randomHex(24)
		salt := newSalt()
		if _, err := s.db.Exec(`INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES(?,?,?,?,?,?)`,
			req.Username, hashPassword(pw, salt), salt, req.Role, req.DisplayName, initialTokenVersion()); err != nil {
			jsonError(w, 400, "开通用户失败（用户名可能已存在）")
			return
		}
		s.InvalidateCache(req.Username)
		s.db.Audit(req.Username, "user.exchange-provision", "role="+req.Role, ip)
	} else {
		if info.Disabled {
			jsonError(w, 403, "用户已被禁用")
			return
		}
		if info.Role != req.Role {
			if _, err := s.db.Exec(`UPDATE users SET role=? WHERE username=?`, req.Role, req.Username); err != nil {
				jsonError(w, 500, "同步角色失败")
				return
			}
			s.db.BumpTokenVersion(req.Username) // 角色变化：旧 OpenOS 令牌即时失效
			s.InvalidateCache(req.Username)
			s.db.Audit(req.Username, "user.role", "exchange → "+req.Role, ip)
		}
	}

	// 以库中最新状态（开通/改角色后的 role 与 tv）签发
	info, err = s.db.GetUserAuth(req.Username)
	if err != nil || !info.Exists {
		jsonError(w, 500, "读取用户失败")
		return
	}
	token, err := s.Issue(req.Username, info.Role, info.TokenVersion)
	if err != nil {
		jsonError(w, 500, "签发令牌失败")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     TokenCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.ttl.Seconds()),
	})
	s.db.Audit(req.Username, "auth.exchange", "role="+info.Role, ip)
	writeJSON(w, map[string]any{"token": token, "user": map[string]any{
		"username": req.Username, "role": info.Role, "displayName": info.DisplayName,
	}})
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
	http.SetCookie(w, &http.Cookie{
		Name: TokenCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(s.ttl.Seconds()),
	})
	writeJSON(w, map[string]any{"token": token, "user": map[string]any{
		"username": id.Username, "role": info.Role, "displayName": info.DisplayName,
	}})
}

// Me GET /api/auth/me
func (s *Service) Me(w http.ResponseWriter, r *http.Request) {
	id := FromContext(r)
	info, _ := s.db.GetUserAuth(id.Username)
	writeJSON(w, map[string]any{"username": id.Username, "role": id.Role, "displayName": info.DisplayName})
}

// ChangePassword PUT /api/users/password
func (s *Service) ChangePassword(w http.ResponseWriter, r *http.Request) {
	if s.externalAuth {
		jsonError(w, http.StatusForbidden, "密码由统一身份系统管理（OPENOS_AUTH_MODE=external），本地改密已停用")
		return
	}
	id := FromContext(r)
	var req struct{ Old, New string }
	jsonDecode(r, &req)
	info, _ := s.db.GetUserAuth(id.Username)
	if !info.Exists || !checkPassword(req.Old, info.Salt, info.PasswordHash) {
		jsonError(w, 400, "原密码错误")
		return
	}
	if WeakPassword(req.New) {
		jsonError(w, 400, "新密码至少 8 位，且需同时包含字母与数字")
		return
	}
	ns := newSalt()
	s.db.Exec(`UPDATE users SET password_hash=?, salt=? WHERE username=?`, hashPassword(req.New, ns), ns, id.Username)
	s.db.BumpTokenVersion(id.Username) // 旧设备全部下线
	s.InvalidateCache(id.Username)
	s.db.Audit(id.Username, "user.password", "self", s.clientIP(r))
	writeJSON(w, ok())
}

// SetDisplayName PUT /api/users/display-name —— 账户页自助修改显示名。
//
// 显示名是展示层字段（不进 JWT、不涉权限），改后无需吊销令牌；审计留痕。
func (s *Service) SetDisplayName(w http.ResponseWriter, r *http.Request) {
	id := FromContext(r)
	var req struct {
		DisplayName string `json:"displayName"`
	}
	if err := jsonDecode(r, &req); err != nil {
		jsonError(w, 400, "参数错误")
		return
	}
	name := strings.TrimSpace(req.DisplayName)
	if name == "" {
		jsonError(w, 400, "显示名不能为空")
		return
	}
	if utf8.RuneCountInString(name) > 64 {
		jsonError(w, 400, "显示名过长（最多 64 个字符）")
		return
	}
	s.db.Exec(`UPDATE users SET display_name=? WHERE username=?`, name, id.Username)
	s.db.Audit(id.Username, "user.displayname", "self → "+name, s.clientIP(r))
	writeJSON(w, ok())
}

// ListUsers GET /api/users （admin）
func (s *Service) ListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT id,username,display_name,role,disabled,created_at FROM users ORDER BY id`)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	users := []store.User{}
	for rows.Next() {
		var u store.User
		var disabled int
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &disabled, &u.CreatedAt); err != nil {
			continue
		}
		u.Disabled = disabled != 0
		users = append(users, u)
	}
	writeJSON(w, users)
}

// CreateUser POST /api/users （admin）
func (s *Service) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username, Password, Role, DisplayName string
	}
	if err := jsonDecode(r, &req); err != nil || req.Username == "" {
		jsonError(w, 400, "参数错误")
		return
	}
	// 外部身份模式下 admin 可建占位用户（密码留空 → 随机值，本地登录通道本就已下线）
	if s.externalAuth && req.Password == "" {
		req.Password = randomHex(24)
	}
	if WeakPassword(req.Password) {
		jsonError(w, 400, "密码至少 8 位，且需同时包含字母与数字")
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	if !validRole(req.Role) {
		jsonError(w, 400, "非法角色")
		return
	}
	salt := newSalt()
	_, err := s.db.Exec(`INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES(?,?,?,?,?,?)`,
		req.Username, hashPassword(req.Password, salt), salt, req.Role, req.DisplayName, initialTokenVersion())
	if err != nil {
		jsonError(w, 400, "创建失败（用户名可能已存在）")
		return
	}
	s.InvalidateCache(req.Username)
	s.db.Audit(FromContext(r).Username, "user.create", req.Username+" role="+req.Role, s.clientIP(r))
	writeJSON(w, ok())
}

// SyncUsers POST /api/users/sync （admin）—— 宿主批量幂等同步用户。
//
// 宿主（身份源）按自己的账号体系推送用户清单：不存在则开通（随机密码，登录只能
// 经宿主），存在则按字段更新（角色 / 显示名 / 禁用态，变化即热吊销旧令牌）。
// 不在名单内的用户不动（单向同步语义，删除仍走 DELETE /api/users/{name}）。
func (s *Service) SyncUsers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Users []struct {
			Username    string `json:"username"`
			Role        string `json:"role"`
			DisplayName string `json:"displayName"`
			Disabled    *bool  `json:"disabled"`
		} `json:"users"`
	}
	if err := jsonDecode(r, &req); err != nil || len(req.Users) == 0 {
		jsonError(w, 400, "参数错误：需要 users 数组")
		return
	}
	ip := s.clientIP(r)
	operator := FromContext(r).Username
	created, updated := 0, 0
	skipped := []string{}
	for _, u := range req.Users {
		if u.Username == "" {
			continue
		}
		if u.Role == "" {
			u.Role = "user"
		}
		if !validRole(u.Role) {
			skipped = append(skipped, u.Username+"（非法角色）")
			continue
		}
		info, err := s.db.GetUserAuth(u.Username)
		if err != nil {
			skipped = append(skipped, u.Username+"（读取失败）")
			continue
		}
		if !info.Exists {
			pw := randomHex(24)
			salt := newSalt()
			disabled := u.Disabled != nil && *u.Disabled
			if _, err := s.db.Exec(`INSERT INTO users(username,password_hash,salt,role,display_name,token_version,disabled) VALUES(?,?,?,?,?,?,?)`,
				u.Username, hashPassword(pw, salt), salt, u.Role, u.DisplayName, initialTokenVersion(), boolInt(disabled)); err != nil {
				skipped = append(skipped, u.Username+"（开通失败）")
				continue
			}
			s.InvalidateCache(u.Username)
			s.db.Audit(operator, "user.sync-create", u.Username+" role="+u.Role, ip)
			created++
			continue
		}
		changed := false
		if info.Role != u.Role {
			// 最后管理员保护：不允许把最后一名启用中的管理员降级
			if info.Role == "admin" && u.Role != "admin" && s.db.CountAdmins() <= 1 {
				skipped = append(skipped, u.Username+"（最后一名管理员不可降级）")
				continue
			}
			s.db.Exec(`UPDATE users SET role=? WHERE username=?`, u.Role, u.Username)
			changed = true
		}
		if u.Disabled != nil && *u.Disabled != info.Disabled {
			// 最后管理员保护：不允许禁用最后一名启用中的管理员
			if info.Role == "admin" && *u.Disabled && s.db.CountAdmins() <= 1 {
				skipped = append(skipped, u.Username+"（最后一名管理员不可禁用）")
				continue
			}
			s.db.Exec(`UPDATE users SET disabled=? WHERE username=?`, boolInt(*u.Disabled), u.Username)
			changed = true
		}
		if u.DisplayName != "" && u.DisplayName != info.DisplayName {
			s.db.Exec(`UPDATE users SET display_name=? WHERE username=?`, u.DisplayName, u.Username)
			changed = true
		}
		if changed {
			s.db.BumpTokenVersion(u.Username) // 角色/禁用变化：旧令牌即时失效
			s.InvalidateCache(u.Username)
			s.db.Audit(operator, "user.sync-update", u.Username, ip)
			updated++
		}
	}
	writeJSON(w, map[string]any{"created": created, "updated": updated, "skipped": skipped})
}

// UpdateUser PUT /api/users/{name} （admin：改角色/显示名/重置密码/禁用）
func (s *Service) UpdateUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Role        *string
		DisplayName *string
		Password    *string
		Disabled    *bool
	}
	if err := jsonDecode(r, &req); err != nil {
		jsonError(w, 400, "参数错误")
		return
	}
	info, _ := s.db.GetUserAuth(name)
	if !info.Exists {
		jsonError(w, 404, "用户不存在")
		return
	}
	if req.Role != nil {
		if !validRole(*req.Role) {
			jsonError(w, 400, "非法角色")
			return
		}
		if info.Role == "admin" && *req.Role != "admin" && s.db.CountAdmins() <= 1 {
			jsonError(w, 400, "系统至少需要保留一名管理员")
			return
		}
		s.db.Exec(`UPDATE users SET role=? WHERE username=?`, *req.Role, name)
		s.db.BumpTokenVersion(name) // 角色变更即时生效
		s.InvalidateCache(name)
		s.db.Audit(FromContext(r).Username, "user.role", name+" → "+*req.Role, s.clientIP(r))
	}
	if req.Disabled != nil && *req.Disabled != info.Disabled {
		if info.Role == "admin" && *req.Disabled && s.db.CountAdmins() <= 1 {
			jsonError(w, 400, "不能禁用最后一名管理员")
			return
		}
		s.db.Exec(`UPDATE users SET disabled=? WHERE username=?`, boolInt(*req.Disabled), name)
		s.db.BumpTokenVersion(name) // 禁用即时生效（REST + 总线复核踢线）
		s.InvalidateCache(name)
		s.db.Audit(FromContext(r).Username, "user.disabled", name+" = "+map[bool]string{true: "on", false: "off"}[*req.Disabled], s.clientIP(r))
	}
	if req.DisplayName != nil {
		s.db.Exec(`UPDATE users SET display_name=? WHERE username=?`, *req.DisplayName, name)
	}
	if req.Password != nil && *req.Password != "" {
		if WeakPassword(*req.Password) {
			jsonError(w, 400, "密码至少 8 位，且需同时包含字母与数字")
			return
		}
		salt := newSalt()
		s.db.Exec(`UPDATE users SET password_hash=?, salt=? WHERE username=?`, hashPassword(*req.Password, salt), salt, name)
		s.db.BumpTokenVersion(name)
		s.InvalidateCache(name)
	}
	writeJSON(w, ok())
}

// DeleteUser DELETE /api/users/{name} （admin）
func (s *Service) DeleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	info, _ := s.db.GetUserAuth(name)
	if !info.Exists {
		jsonError(w, 404, "用户不存在")
		return
	}
	if info.Role == "admin" && info.Disabled == false && s.db.CountAdmins() <= 1 {
		jsonError(w, 400, "不能删除最后一名管理员")
		return
	}
	s.db.Exec(`DELETE FROM users WHERE username=?`, name)
	s.db.Exec(`DELETE FROM user_settings WHERE username=?`, name)
	s.db.Exec(`DELETE FROM files WHERE owner=?`, name)
	s.db.Exec(`DELETE FROM chat_messages WHERE username=?`, name)
	s.InvalidateCache(name)
	s.db.Audit(FromContext(r).Username, "user.delete", name, s.clientIP(r))
	// 级联清理磁盘数据（真实终端沙箱 / goder 会话目录），由 main 注入
	if s.onUserDeleted != nil {
		s.onUserDeleted(name)
	}
	writeJSON(w, ok())
}

// SeedDefaults 创建默认演示账号（仅当用户表为空且 OPENOS_SEED_DEMO 未关闭；
// 宿主集成场景应设 OPENOS_SEED_DEMO=0 或 OPENOS_AUTH_MODE=external，账号体系
// 完全交由集成方）。产品部署后请立即修改密码。
func (s *Service) SeedDefaults() error {
	if s.externalAuth || os.Getenv("OPENOS_SEED_DEMO") == "0" {
		return nil
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n > 0 {
		return nil
	}
	defaults := []struct{ u, p, role, disp string }{
		{"admin", "admin123", "admin", "管理员"},
		{"user", "user123", "user", "普通用户"},
		{"guest", "guest123", "guest", "访客"},
	}
	for _, d := range defaults {
		salt := newSalt()
		if _, err := s.db.Exec(`INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES(?,?,?,?,?,?)`,
			d.u, hashPassword(d.p, salt), salt, d.role, d.disp, initialTokenVersion()); err != nil {
			return err
		}
	}
	return nil
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
