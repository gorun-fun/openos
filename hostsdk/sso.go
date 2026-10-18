// sso.go — Casdoor(OIDC) 授权码登录流（自 grf-hotel/grf-os 逐行同构段提炼）。
// 身份解析（成员/访客/白名单判定等业务语义）留给宿主回调，本文件只管协议。
package hostsdk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// SSOConfig Casdoor 应用配置。
type SSOConfig struct {
	Endpoint     string // Casdoor 根地址（如 https://sso.example.com）
	ClientID     string
	ClientSecret string
	RedirectURL  string // 回调绝对地址（…/api/v1/auth/sso/callback）
	// RedirectFunc 请求期解析回调地址（优先于 RedirectURL）：X-Forwarded-Proto/
	// Host 感知推导的宿主用（同一部署经 http 直连 IP 与 https 域名双形态可达时，
	// Casdoor 侧登记同值即可，无须静态写死）。设置时 Enabled 不再要求 RedirectURL。
	RedirectFunc func(r *http.Request) string
	// Group 允许登录的组（空=不限制）。匹配规则：全名相等或组织前缀路径末段相等
	//（"org/hotel-staff" ↔ "hotel-staff"）。
	Group string
	// AccountPage 账户自助页（改密入口）；空则回落 Endpoint + /account
	AccountPage string
}

// SSOUser 统一身份返回的用户信息。
type SSOUser struct {
	UID    string
	Name   string
	Email  string
	Groups []string
}

// ErrNotInGroup 组过滤未通过（Callback 返回；宿主可选择直接拒绝登录）。
var ErrNotInGroup = errors.New("账号不在允许的用户组")

// ssoStateCookie 登录状态防重放 Cookie。
const ssoStateCookie = "sso_state"
const ssoStateMaxAge = 10 * time.Minute

// SSO 登录流客户端。
type SSO struct {
	cfg SSOConfig
	hc  *http.Client
}

// NewSSO 创建登录流客户端。
func NewSSO(cfg SSOConfig) *SSO {
	return &SSO{cfg: cfg, hc: &http.Client{Timeout: 10 * time.Second}}
}

// Enabled 配置是否完整。
func (s *SSO) Enabled() bool {
	ok := s.cfg.Endpoint != "" && s.cfg.ClientID != "" && s.cfg.ClientSecret != ""
	return ok && (s.cfg.RedirectURL != "" || s.cfg.RedirectFunc != nil)
}

// redirect 请求期回调地址：RedirectFunc 优先于静态 RedirectURL。
func (s *SSO) redirect(r *http.Request) string {
	if s.cfg.RedirectFunc != nil {
		if u := s.cfg.RedirectFunc(r); u != "" {
			return u
		}
	}
	return s.cfg.RedirectURL
}

// LoginRedirect 302 跳 Casdoor 授权页（先种 state Cookie 防重放）。
// devFallback 非空且 SSO 未配置时走宿主回落（如开发登录页）。
func (s *SSO) LoginRedirect(w http.ResponseWriter, r *http.Request, devFallback func(w http.ResponseWriter, r *http.Request)) {
	if !s.Enabled() {
		if devFallback != nil {
			devFallback(w, r)
			return
		}
		http.Error(w, "SSO 未配置", http.StatusServiceUnavailable)
		return
	}
	state := randomHex(16)
	http.SetCookie(w, &http.Cookie{
		Name: ssoStateCookie, Value: state, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(ssoStateMaxAge.Seconds()),
	})
	q := url.Values{}
	q.Set("client_id", s.cfg.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", s.redirect(r))
	q.Set("scope", "read")
	q.Set("state", state)
	http.Redirect(w, r, strings.TrimRight(s.cfg.Endpoint, "/")+"/login/oauth/authorize?"+q.Encode(), http.StatusFound)
}

// AccountRedirect 跳统一身份账户自助页（修改密码的正式入口；Casdoor 自己要求会话）。
func (s *SSO) AccountRedirect(w http.ResponseWriter, r *http.Request) {
	page := s.cfg.AccountPage
	if page == "" && s.Enabled() {
		page = strings.TrimRight(s.cfg.Endpoint, "/") + "/account"
	}
	if page == "" {
		http.Error(w, "SSO 未配置", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, page, http.StatusFound)
}

// Callback 授权码回调：state 校验 → 换 access_token → userinfo（get-account
// 兜底）→ 组过滤。成功返回统一身份用户，宿主在此之后做自己的身份解析与令牌签发。
func (s *SSO) Callback(w http.ResponseWriter, r *http.Request) (SSOUser, error) {
	code, state := r.URL.Query().Get("code"), r.URL.Query().Get("state")
	if code == "" || state == "" {
		return SSOUser{}, fmt.Errorf("缺少授权参数")
	}
	if c, err := r.Cookie(ssoStateCookie); err != nil || c.Value == "" || c.Value != state {
		return SSOUser{}, fmt.Errorf("登录状态校验失败")
	}
	http.SetCookie(w, &http.Cookie{Name: ssoStateCookie, Value: "", Path: "/", MaxAge: -1})

	accessToken, err := s.exchange(r.Context(), code, s.redirect(r))
	if err != nil {
		return SSOUser{}, fmt.Errorf("认证服务通信失败: %w", err)
	}
	u, err := s.userinfo(r.Context(), accessToken)
	if err != nil {
		return SSOUser{}, fmt.Errorf("获取用户信息失败: %w", err)
	}
	if g := s.cfg.Group; g != "" && !InGroup(u.Groups, g) {
		return SSOUser{}, ErrNotInGroup
	}
	return u, nil
}

// FailRedirect 回调失败的统一跳转（?sso_error=<msg> 回首页由登录卡展示）。
func (s *SSO) FailRedirect(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/?sso_error="+url.QueryEscape(msg), http.StatusFound)
}

func (s *SSO) exchange(ctx context.Context, code, redirectURI string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", s.cfg.ClientID)
	form.Set("client_secret", s.cfg.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(s.cfg.Endpoint, "/")+"/api/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("access_token status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("empty access_token")
	}
	return out.AccessToken, nil
}

// ssoUIDSan uid 收敛为可安全入库的字符集。
var ssoUIDSan = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (s *SSO) userinfo(ctx context.Context, accessToken string) (SSOUser, error) {
	var info struct {
		ID     string   `json:"id"`
		Sub    string   `json:"sub"`
		Email  string   `json:"email"`
		Name   string   `json:"name"`
		Groups []string `json:"groups"`
	}
	if err := s.getJSON(ctx, "/api/userinfo", accessToken, &info); err != nil {
		return SSOUser{}, err
	}
	uid := info.ID
	if uid == "" {
		uid = info.Sub
	}
	// 组信息不全时兜底 /api/get-account（Casdoor userinfo 常缺组）
	if uid == "" || info.Email == "" || len(info.Groups) == 0 {
		var acct struct {
			Data struct {
				ID     string   `json:"id"`
				Name   string   `json:"name"`
				Email  string   `json:"email"`
				Groups []string `json:"groups"`
			} `json:"data"`
		}
		if err := s.getJSON(ctx, "/api/get-account", accessToken, &acct); err != nil {
			return SSOUser{}, fmt.Errorf("userinfo 不完整且 get-account 失败: %w", err)
		}
		if uid == "" {
			uid = acct.Data.ID
		}
		if info.Email == "" {
			info.Email = acct.Data.Email
		}
		if info.Name == "" {
			info.Name = acct.Data.Name
		}
		if len(info.Groups) == 0 {
			info.Groups = acct.Data.Groups
		}
	}
	uid = strings.Trim(ssoUIDSan.ReplaceAllString(uid, "-"), "-")
	if uid == "" {
		return SSOUser{}, fmt.Errorf("uid 非法")
	}
	name := strings.TrimSpace(info.Name)
	if name == "" {
		name = uid
	}
	return SSOUser{UID: uid, Name: name, Email: strings.ToLower(strings.TrimSpace(info.Email)), Groups: NormalizeGroups(info.Groups)}, nil
}

func (s *SSO) getJSON(ctx context.Context, apiPath, accessToken string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(s.cfg.Endpoint, "/")+apiPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := s.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s status %d", apiPath, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// NormalizeGroups 兼容 Casdoor 组字段的两种形态（字符串 或 {name:…} 对象序列化）。
func NormalizeGroups(raw []string) []string {
	var out []string
	for _, g := range raw {
		var name struct {
			Name string `json:"name"`
		}
		if strings.HasPrefix(g, "{") && json.Unmarshal([]byte(g), &name) == nil && name.Name != "" {
			g = name.Name
		}
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// InGroup 组匹配：全名相等或组织前缀路径末段相等（"org/hotel-staff" ↔ "hotel-staff"）。
func InGroup(groups []string, want string) bool {
	for _, g := range groups {
		if g == want || strings.HasSuffix(g, "/"+want) {
			return true
		}
	}
	return false
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
