// refreshtoken.go — refresh Cookie 会话续期内核（自 grf-hotel/grf-os 同构实现提炼：
// grf-os session.go 注释自认「自 grf-hotel 同款移植」，两份逐字同构，此处收敛）。
//
// 宿主 JWT 短时效（12h/24h）硬过期且无续期通道时，每天必有一次全员掉线
// （openos 桌面自带滑动续期，宿主侧没有）。本内核提供 POST /api/v1/auth/refresh
// 形态的双通道自认证续期：
//
//	① Bearer 有效：滑动续期（会话仍活着，重签延长宿主 TTL）；
//	② refresh Cookie 有效：过期复活——宿主 JWT 已过期但 30 天 HttpOnly refresh
//	   Cookie 仍在时，免整页跳 SSO 静默换发全套双令牌；
//	③ 都无：401，前端跳 SSO（是否要输密码由 Casdoor 会话决定）。
//
// 每次成功：重签宿主 JWT + openos exchange 新令牌 + 重种两个 Cookie（openos 会话
// Cookie / refresh Cookie 滑动 30 天）。身份实时重算走宿主 Resolve 回调（离职、
// 停用、角色/白名单变化随续期即时生效，不信任旧 JWT 内快照）。
//
// refresh Cookie 设计：无状态 HMAC-SHA256 签名（密钥 = Secret + "-refresh"，与
// 宿主 JWT 签名隔离），HttpOnly + SameSite=Lax + Path 限定认证端点（只随认证
// 请求发送；Lax 下跨站 POST 不携带，天然免疫 CSRF）。主动吊销不需要：复活时
// 身份实时重算，失去资格即落普通用户/访客或直接拒绝。
package hostsdk

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// 默认值与两宿主实现逐字段一致（Path 限定认证命名空间、30 天滑动）。
const (
	defaultRefreshPath = "/api/v1/auth"
	defaultRefreshTTL  = 30 * 24 * time.Hour
)

// refreshNow 时间函数（包级变量便于测试注入过期场景）。
var refreshNow = time.Now

// RefreshConfig refresh Cookie 内核配置。
type RefreshConfig struct {
	CookieName string        // Cookie 名（宿主各自命名：grfos_refresh / hotel_refresh；必填）
	Secret     string        // 签名密钥源：实际密钥 = Secret + "-refresh"（与宿主 JWT 签名隔离）
	Path       string        // Cookie Path（默认 /api/v1/auth——只随认证端点发送）
	TTL        time.Duration // Cookie MaxAge 与载荷 exp（默认 30 天，每次成功滑动续期）
}

// RefreshClaims refresh Cookie 载荷：身份三元组 + 来源 + payload 扩展位。
// UID/Name/Email/Src/Extra 由宿主任填（Email 供 grf-os 一类宿主复活时按白名单
// 现算角色，不用留空）；Exp/Iat 由 SetCookie 签发时盖戳。JSON 字段序与 omitempty
// 位与两宿主历史载荷逐字节兼容（email/x 不用即不出现，hotel 形态载荷不变）。
type RefreshClaims struct {
	UID   string            `json:"uid"`
	Name  string            `json:"name,omitempty"`
	Email string            `json:"email,omitempty"`
	Src   string            `json:"src"` // sso / dev
	Exp   int64             `json:"exp"`
	Iat   int64             `json:"iat"`
	Extra map[string]string `json:"x,omitempty"` // payload 扩展位（宿主自定义随行数据）
}

// RefreshSessions refresh Cookie 内核：签发/解析助手 + 续期端点编排。
type RefreshSessions struct {
	name   string
	secret string
	path   string
	ttl    time.Duration
}

// NewRefreshSessions 创建内核（Path/TTL 缺省补齐；CookieName/Secret 由宿主注入）。
func NewRefreshSessions(cfg RefreshConfig) *RefreshSessions {
	s := &RefreshSessions{
		name:   cfg.CookieName,
		secret: cfg.Secret,
		path:   cfg.Path,
		ttl:    cfg.TTL,
	}
	if s.path == "" {
		s.path = defaultRefreshPath
	}
	if s.ttl <= 0 {
		s.ttl = defaultRefreshTTL
	}
	return s
}

func refreshHMACKey(secret string) []byte { return []byte(secret + "-refresh") }

// SetCookie 种/滑动续种 refresh Cookie（uid 为空不种——与两宿主实现一致）。
// SSO 回调 / 开发通道登录 / 续期端点共用同一入口。
func (s *RefreshSessions) SetCookie(w http.ResponseWriter, c RefreshClaims) {
	if c.UID == "" {
		return
	}
	c.Exp = refreshNow().Add(s.ttl).Unix()
	c.Iat = refreshNow().Unix()
	body, _ := json.Marshal(c)
	enc := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, refreshHMACKey(s.secret))
	mac.Write([]byte(enc))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, &http.Cookie{
		Name:     s.name,
		Value:    enc + "." + sig,
		Path:     s.path,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.ttl.Seconds()),
	})
}

// ParseCookie 校验签名与有效期。
func (s *RefreshSessions) ParseCookie(r *http.Request) (RefreshClaims, bool) {
	c, err := r.Cookie(s.name)
	if err != nil || c.Value == "" {
		return RefreshClaims{}, false
	}
	dot := strings.LastIndex(c.Value, ".")
	if dot < 0 {
		return RefreshClaims{}, false
	}
	enc, sig := c.Value[:dot], c.Value[dot+1:]
	mac := hmac.New(sha256.New, refreshHMACKey(s.secret))
	mac.Write([]byte(enc))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return RefreshClaims{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return RefreshClaims{}, false
	}
	var p RefreshClaims
	if json.Unmarshal(b, &p) != nil || p.UID == "" || refreshNow().Unix() > p.Exp {
		return RefreshClaims{}, false
	}
	return p, true
}

// Identity 宿主重算后的登录身份（续期/SSO 回调/开发通道三处共用）：openos
// exchange 三元组 + refresh Cookie 载荷字段 + Host 透传位（宿主自家 claims 等
// 对象，签发回调自取，内核不感知其类型）。
type Identity struct {
	Host  any
	UID   string            // openos 桌面身份（exchange 用户名；开发通道为 dev-<user>）
	Role  string            // openos 角色（响应 role 字段）
	Name  string            // 显示名（exchange 与 refresh Cookie 载荷）
	Email string            // refresh Cookie 载荷（grf-os 白名单复活用；可空）
	Src   string            // sso / dev
	Extra map[string]string // refresh Cookie payload 扩展位随行
}

// RefreshHandlers 续期端点的宿主回调面（响应包裹 {code,msg,data} 的形状也归宿主）。
type RefreshHandlers struct {
	// ParseBearer 从 Bearer 令牌解析原始身份（宿主自家 JWT；无效/过期返回 !ok，
	// 内核回落 refresh Cookie）。仅取 uid/name/email/src（+扩展位）入载荷。
	ParseBearer func(r *http.Request) (RefreshClaims, bool)
	// Resolve 身份实时重算（查库/邮箱白名单/dev 通道开关判定）；error = 拒绝
	// 续期（401），不信任旧 JWT/载荷内的角色快照。
	Resolve func(raw RefreshClaims) (Identity, error)
	// IssueHostToken 重签宿主 JWT（claims 从 id.Host 取，TTL 宿主自定）。
	IssueHostToken func(id Identity) (string, error)
	// Exchange 换 openos 桌面令牌（*Manager.Exchange 委托）。
	Exchange func(uid, role, name string) (string, error)
	// WriteOK 成功响应写出（data 建议字段 token/openos_token/role；grf-hotel
	// 一类宿主另加 status=member|guest）。
	WriteOK func(w http.ResponseWriter, res RefreshResult)
	// WriteFail 错误写出（401 未认证 / 403 续期失败——前端只认 401 触发自愈
	// 或跳 SSO，语义勿混用）。
	WriteFail func(w http.ResponseWriter, status int, msg string)
}

// RefreshResult 续期产物（WriteOK 入参）。
type RefreshResult struct {
	HostToken string
	OsToken   string
	Role      string
	Identity  Identity
}

// Handler 续期端点处理器（宿主挂 POST /api/v1/auth/refresh，公开路由自认证：
// Bearer 滑动续期 / refresh Cookie 过期复活）。无有效凭证 401；签发失败 403。
func (s *RefreshSessions) Handler(h RefreshHandlers) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := s.resolve(r, h.ParseBearer)
		if !ok {
			h.WriteFail(w, http.StatusUnauthorized, "会话已过期，请重新登录")
			return
		}
		id, err := h.Resolve(raw)
		if err != nil {
			h.WriteFail(w, http.StatusUnauthorized, "会话已过期，请重新登录")
			return
		}
		hostToken, err := h.IssueHostToken(id)
		if err != nil {
			h.WriteFail(w, http.StatusForbidden, "会话续期失败: "+err.Error())
			return
		}
		osToken, err := h.Exchange(id.UID, id.Role, id.Name)
		if err != nil {
			h.WriteFail(w, http.StatusForbidden, "会话续期失败: "+err.Error())
			return
		}
		TokenCookie(w, osToken)
		s.SetCookie(w, RefreshClaims{UID: id.UID, Name: id.Name, Email: id.Email, Src: id.Src, Extra: id.Extra})
		h.WriteOK(w, RefreshResult{HostToken: hostToken, OsToken: osToken, Role: id.Role, Identity: id})
	}
}

// resolve 双通道原始身份解析：Bearer 优先（滑动续期），回落 refresh Cookie
// （过期复活）；均无有效凭证返回 false。
func (s *RefreshSessions) resolve(r *http.Request, parseBearer func(*http.Request) (RefreshClaims, bool)) (RefreshClaims, bool) {
	if parseBearer != nil {
		if p, ok := parseBearer(r); ok && p.UID != "" {
			return p, true
		}
	}
	if p, ok := s.ParseCookie(r); ok {
		return p, true
	}
	return RefreshClaims{}, false
}
