// web.go — SSO 回跳中间页 / openos_token Cookie / 开发登录页模板。
package hostsdk

import (
	"fmt"
	"net/http"
)

// TokenCookie 种 openos 会话 Cookie：openos 鉴权中间件接受 Bearer 或该 Cookie；
// goder 嵌入组件（goder.js 同源 fetch 不带 Authorization）依赖它过 /goder-api
// 反代的登录校验。
func TokenCookie(w http.ResponseWriter, token string) {
	if token == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "openos_token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// BootstrapOpts 双令牌中间页参数。
type BootstrapOpts struct {
	// HostTokenKey 宿主自身令牌的 localStorage 键名（如 grf-hotel 用 "token"、
	// grf-os 用 "grfos_token"）；空则不写宿主令牌。
	HostTokenKey string
	// FragmentName URL 片段交接名（openos 框架 createOpenOS({login:{mode:'sso'}})
	// 默认摘取 "os-token"；自定义 tokenFragment 的宿主保持同名）。
	FragmentName string
}

// BootstrapPage 中间页：写入双令牌后进入桌面。宿主令牌与 openos 令牌经
// URL 片段交接（不出现在服务端日志/Referer；openos 侧摘取后立即 replaceState）；
// openos 令牌同时种 HttpOnly Cookie（goder 通道）。页面 noindex 且即时跳转。
func BootstrapPage(w http.ResponseWriter, hostToken, osToken string, opts BootstrapOpts) {
	if opts.FragmentName == "" {
		opts.FragmentName = "os-token"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex")
	TokenCookie(w, osToken)
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>登录中…</title></head><body>
<script>
try {
  %s
  localStorage.setItem('openos_token', %q);
} catch (e) {}
location.replace('/#`+opts.FragmentName+`=' + encodeURIComponent(%q));
</script>正在进入系统…</body></html>`,
		hostTokenScript(opts.HostTokenKey, hostToken), osToken, osToken)
}

func hostTokenScript(key, token string) string {
	if key == "" {
		return ""
	}
	return fmt.Sprintf("localStorage.setItem(%q, %q);", key, token)
}

// LoginPageOpts 开发登录页参数（DEV_LOGIN=1 时宿主挂载；生产恒关）。
type LoginPageOpts struct {
	Title   string // 品牌标题（如 "GRF·管理 开发登录"）
	Brand   string // 副标题/说明
	Error   string // 登录失败提示（非空时展示）
	PostTo  string // 表单提交地址（如 /dev-login）
	ExtraJS string // 宿主附加脚本（如写额外 localStorage 键）
}

// LoginPage 渲染开发登录页（玻璃卡片风格，自 grf-hotel/grf-os 同款提炼）。
// 登录成功后宿主自己 handler 里调 BootstrapPage 完成双令牌交接。
func LoginPage(w http.ResponseWriter, o LoginPageOpts) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	errBox := ""
	if o.Error != "" {
		errBox = `<div class="err">` + htmlEscape(o.Error) + `</div>`
	}
	fmt.Fprintf(w, `<!doctype html><html lang="zh"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<style>
body{margin:0;min-height:100vh;display:grid;place-items:center;font:14px/1.6 -apple-system,"PingFang SC","Microsoft YaHei",sans-serif;
  background:linear-gradient(135deg,#1b2a4a 0%%,#2d3f6e 55%%,#46608f 100%%);color:#e8edf7}
.card{width:340px;padding:34px 32px;border-radius:18px;background:rgba(255,255,255,.09);
  backdrop-filter:blur(18px);-webkit-backdrop-filter:blur(18px);border:1px solid rgba(255,255,255,.16);box-shadow:0 24px 60px rgba(0,0,0,.35)}
h1{margin:0 0 6px;font-size:19px}
.sub{margin:0 0 22px;font-size:12.5px;color:rgba(232,237,247,.62)}
input{width:100%%;box-sizing:border-box;margin:0 0 12px;padding:11px 13px;border-radius:10px;border:1px solid rgba(255,255,255,.2);
  background:rgba(255,255,255,.08);color:#e8edf7;font-size:14px;outline:none}
input:focus{border-color:rgba(120,170,255,.75)}
button{width:100%%;padding:11px 0;border:0;border-radius:10px;background:linear-gradient(135deg,#5b8cff,#3f6ce0);
  color:#fff;font-size:14.5px;font-weight:600;cursor:pointer}
button:hover{filter:brightness(1.07)}
.err{margin:0 0 12px;padding:9px 12px;border-radius:9px;background:rgba(255,69,58,.16);border:1px solid rgba(255,69,58,.4);font-size:12.5px}
</style></head><body>
<div class="card">
<h1>%s</h1><p class="sub">%s</p>%s
<form method="post" action="%s">
<input name="username" placeholder="用户名" autocomplete="username" required>
<input name="password" type="password" placeholder="密码" autocomplete="current-password" required>
<button type="submit">登录</button>
</form></div>
%s
</body></html>`, htmlEscape(o.Title), htmlEscape(o.Title), htmlEscape(o.Brand), errBox, htmlEscape(o.PostTo), o.ExtraJS)
}

func htmlEscape(s string) string {
	out := make([]byte, 0, len(s))
	for _, c := range []byte(s) {
		switch c {
		case '&':
			out = append(out, "&amp;"...)
		case '<':
			out = append(out, "&lt;"...)
		case '>':
			out = append(out, "&gt;"...)
		case '"':
			out = append(out, "&#34;"...)
		case '\'':
			out = append(out, "&#39;"...)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}
