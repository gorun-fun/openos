// ssocallback.go — SSO 回调编排（自 grf-hotel/grf-os 同构实现提炼；协议本体在
// sso.go 的 SSO，本文件只管编排）。
//
//	校验 state → 换 token → userinfo → 组校验（SSO.Callback）
//	  → 身份解析（宿主 Resolve：grf-os 邮箱白名单定角色 / grf-hotel 员工库解析
//	     成员·访客·引导管理员）
//	  → 签发宿主 JWT → openos exchange 桌面令牌（失败仅日志降级，不阻断登录）
//	  → 种 refresh Cookie → BootstrapPage 双令牌中间页 → /#os-token=… 进入桌面
//
// 失败统一 ?sso_error= 回首页（登录卡展示）。回调地址未显式配置时按请求推导
// （RequestRedirectFunc / NewSSOAutoRedirect，X-Forwarded 感知——同一部署经
// http 直连 IP 与 https 域名双形态可达时无须静态写死）。
package hostsdk

import (
	"errors"
	"log"
	"net/http"
)

// VisibleError 业务性拒绝：消息可直接展示给登录页（grf-hotel 的 apiErr 语义，
// 如「所属酒店/门店已被平台停用」）。Resolve 回调返回它时走原话术，其余错误
// 一律兜底话术（不泄露内部细节）。
type VisibleError struct{ Msg string }

func (e *VisibleError) Error() string { return e.Msg }

// SSOCallback 回调编排的宿主回调面。
type SSOCallback struct {
	SSO *SSO
	// Sessions refresh Cookie 内核（nil = 不种；种下后 JWT 过期免跳 SSO 静默复活）。
	Sessions *RefreshSessions
	// Resolve 身份语义回调（宿主业务，唯一的身份权威）：SSO 用户 → 登录身份。
	// 业务性拒绝返回 *VisibleError（消息直达登录页）；其余 error 走兜底话术。
	Resolve func(u SSOUser) (Identity, error)
	// IssueHostToken 签发宿主 JWT（claims 从 id.Host 取，TTL 宿主自定）。
	IssueHostToken func(id Identity) (string, error)
	// Exchange 换 openos 桌面令牌（*Manager.Exchange 委托；失败仅日志降级——
	// 与两宿主一致不阻断登录，桌面以游客态/不可用进入）。
	Exchange func(uid, role, name string) (string, error)
	// HostTokenKey BootstrapPage 宿主令牌 localStorage 键（grfos_token / token）。
	HostTokenKey string
	// AuthFailMsg 回调协议阶段失败话术（默认「统一身份认证失败，请重试」；
	// grf-hotel 用「登录失败，请重试」）。
	AuthFailMsg string
	// ResolveFailMsg 身份解析失败兜底话术（默认「身份解析失败，请稍后重试」）。
	ResolveFailMsg string
}

// HandleSSOCallback GET /api/v1/auth/sso/callback 处理器（高阶函数，身份语义
// 全在 Resolve 回调）。
func HandleSSOCallback(d SSOCallback) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fail := func(msg string) { d.SSO.FailRedirect(w, r, msg) }
		if !d.SSO.Enabled() {
			fail("SSO 未配置")
			return
		}
		u, err := d.SSO.Callback(w, r)
		if err != nil {
			if errors.Is(err, ErrNotInGroup) {
				log.Printf("[sso] 用户 %s 不在允许的组 %s", u.UID, d.SSO.cfg.Group)
				fail("当前账号不在允许的用户组，请联系管理员")
				return
			}
			log.Printf("[sso] 回调失败: %v", err)
			msg := d.AuthFailMsg
			if msg == "" {
				msg = "统一身份认证失败，请重试"
			}
			fail(msg)
			return
		}
		id, err := d.Resolve(u)
		if err != nil {
			log.Printf("[sso] 身份解析失败: %v", err)
			var ve *VisibleError
			if errors.As(err, &ve) {
				fail(ve.Msg)
			} else {
				msg := d.ResolveFailMsg
				if msg == "" {
					msg = "身份解析失败，请稍后重试"
				}
				fail(msg)
			}
			return
		}
		hostToken, err := d.IssueHostToken(id)
		if err != nil {
			fail("签发会话失败")
			return
		}
		// 桌面令牌：openos 子进程 exchange；失败仅日志降级，不阻断登录
		osToken, err := d.Exchange(id.UID, id.Role, id.Name)
		if err != nil {
			log.Printf("[sso] openos exchange 失败（桌面将无法进入或以游客态降级）: %v", err)
		}
		log.Printf("[sso] 登录成功 uid=%s role=%s", id.UID, id.Role)
		// refresh Cookie（30 天滑动）：JWT 过期后免跳 SSO 静默复活（POST /auth/refresh）
		if d.Sessions != nil {
			d.Sessions.SetCookie(w, RefreshClaims{UID: id.UID, Name: id.Name, Email: id.Email, Src: "sso", Extra: id.Extra})
		}
		// 中间页：双令牌 + openos Cookie（goder 通道）+ URL 片段交接进入桌面
		BootstrapPage(w, hostToken, osToken, BootstrapOpts{HostTokenKey: d.HostTokenKey})
	}
}

// RequestRedirectFunc 请求期回调地址推导（X-Forwarded-Proto/Host 感知）：同一
// 部署经 http 直连 IP 与 https 域名双形态可达时，Casdoor 侧登记同值即可，无须
// 静态写死 RedirectURL。callbackPath 形如 /api/v1/auth/sso/callback。
func RequestRedirectFunc(callbackPath string) func(r *http.Request) string {
	return func(r *http.Request) string {
		scheme := "https"
		if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
			scheme = p
		} else if r.TLS == nil {
			scheme = "http"
		}
		host := r.Host
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			host = h
		}
		return scheme + "://" + host + callbackPath
	}
}

// NewSSOAutoRedirect 创建 SSO 客户端：静态 RedirectURL 与 RedirectFunc 均未配时
// 自动注入请求期推导（两宿主 NewApp 的同款接线：显式配置优先，无静态值才按
// 请求推导）。
func NewSSOAutoRedirect(cfg SSOConfig, callbackPath string) *SSO {
	if cfg.RedirectURL == "" && cfg.RedirectFunc == nil {
		cfg.RedirectFunc = RequestRedirectFunc(callbackPath)
	}
	return NewSSO(cfg)
}
