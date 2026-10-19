// devlogin.go — 开发登录页（DEV_LOGIN=1）+ 固定账号账密端点（自 grf-hotel/grf-os
// 同构实现提炼）。
//
// 两宿主 sso.go 内嵌的玻璃卡片登录页逐字相同，仅品牌标题/背景渐变/宿主令牌
// 键名不同；取 os 令牌的分叉由 OsTokenPath 参数化：grf-os 单段式（登录响应即含
// openos_token，页面一次写入双令牌），grf-hotel 两段式（登录响应只有宿主令牌，
// 页面再带 Bearer 调 os-token 端点自取）。生产环境不注册路由，无攻击面。
package hostsdk

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// DevLoginOpts 开发登录页参数。
type DevLoginOpts struct {
	Brand    string // 品牌名（如 "GRF·管理" / "格瑞风"；标题与卡片标题共用）
	Emoji    string // 卡片标题前缀图标（"🖥️" / "🏨"）
	Accent   string // 背景渐变 CSS（linear-gradient(…) 表达式，宿主品牌色）
	TokenKey string // 宿主令牌 localStorage 键名（grfos_token / token）
	// OsTokenPath os-token 端点路径：非空 = 两段式（登录后带 Bearer 取 os 令牌），
	// 空 = 单段式（登录响应直含 openos_token，随宿主账密端点一并发回）。
	OsTokenPath string
	// LoginPath 账密端点路径（默认 /api/v1/auth/login）。
	LoginPath string
	// Title 页面 <title>（默认 "开发登录 · " + Brand）。
	Title string
}

// DevLoginPage 渲染开发登录页（GET；仅 DEV_LOGIN=1 注册路由）。
func DevLoginPage(o DevLoginOpts) http.Handler {
	if o.LoginPath == "" {
		o.LoginPath = "/api/v1/auth/login"
	}
	title := o.Title
	if title == "" {
		title = "开发登录 · " + o.Brand
	}
	heading := strings.TrimSpace(o.Emoji+" "+o.Brand) + " · 开发登录"
	var tail string
	if o.OsTokenPath == "" {
		// 单段式（grf-os）：账密端点响应 {token, openos_token}，一次写入双令牌
		tail = strings.NewReplacer("__KEY__", htmlEscape(o.TokenKey)).Replace(`    localStorage.setItem('__KEY__',d.data.token);
    const os=d.data.openos_token||'';
    if(os)localStorage.setItem('openos_token',os);
    location.replace('/#os-token='+encodeURIComponent(os));`)
	} else {
		// 两段式（grf-hotel）：账密端点只回宿主令牌，再带 Bearer 取 os 令牌
		tail = strings.NewReplacer(
			"__OSTOKEN__", htmlEscape(o.OsTokenPath),
			"__KEY__", htmlEscape(o.TokenKey),
		).Replace(`    const t=d.data.token;
    const ot=await (await fetch('__OSTOKEN__',{headers:{Authorization:'Bearer '+t}})).json();
    const os=ot.data&&ot.data.token||'';
    localStorage.setItem('__KEY__',t);
    localStorage.setItem('openos_token',os);
    location.replace('/#os-token='+encodeURIComponent(os));`)
	}
	page := strings.NewReplacer(
		"__TITLE__", htmlEscape(title),
		"__ACCENT__", htmlEscape(o.Accent),
		"__HEADING__", htmlEscape(heading),
		"__LOGIN__", htmlEscape(o.LoginPath),
		"__TAIL__", tail,
	).Replace(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>__TITLE__</title>
<style>
body{font-family:system-ui,sans-serif;background:__ACCENT__;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
.card{width:340px;padding:36px 32px;border-radius:20px;background:rgba(255,255,255,.14);backdrop-filter:blur(30px);border:1px solid rgba(255,255,255,.3);color:#fff;text-align:center}
h1{font-size:20px;margin:0 0 18px}input{width:100%;box-sizing:border-box;padding:10px 13px;margin-bottom:10px;border-radius:10px;border:1px solid rgba(255,255,255,.35);background:rgba(255,255,255,.14);color:#fff;font-size:14px}
button{width:100%;padding:10px;border:none;border-radius:10px;background:linear-gradient(180deg,#4da3ff,#0a84ff);color:#fff;font-size:14px;font-weight:600;cursor:pointer}
.err{font-size:12px;color:#ffd7d7;margin-bottom:8px;min-height:14px}
.tip{font-size:11px;opacity:.7;margin-top:14px}
</style></head><body><div class="card">
<h1>__HEADING__</h1>
<input id="u" placeholder="用户名" value="admin">
<input id="p" type="password" placeholder="密码">
<div class="err" id="e"></div>
<button onclick="go()">登 录</button>
<p class="tip">仅开发模式（DEV_LOGIN=1）；生产环境请使用统一身份登录</p>
</div><script>
async function go(){
  const e=document.getElementById('e');
  try{
    const r=await fetch('__LOGIN__',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({username:document.getElementById('u').value,password:document.getElementById('p').value})});
    const d=await r.json();
    if(!r.ok||d.code)throw new Error(d.msg||('HTTP '+r.status));
__TAIL__
  }catch(err){e.textContent=err.message}
}
</script></body></html>`)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Robots-Tag", "noindex")
		_, _ = w.Write([]byte(page))
	})
}

// DevLoginConfig 固定账号开发通道（DEV_USER/DEV_PASSWORD，grf-os 形态）的端点配置。
type DevLoginConfig struct {
	Username string // 开发账号（DEV_USER；恒 admin 角色、Source=dev）
	Password string // 开发密码（DEV_PASSWORD；常数时间比较防时序侧信道）
	// Sessions refresh Cookie 内核（nil = 不种；种下后 JWT 过期免重登静默续期）。
	Sessions *RefreshSessions
	// IssueHostToken 签发宿主 JWT（uid 形如 dev-admin；claims 由回调闭包构造，
	// Role 恒 admin、Source 恒 dev）。
	IssueHostToken func(uid, name string) (string, error)
	// Exchange 换 openos 桌面令牌（*Manager.Exchange 委托；失败仅日志降级——
	// 与 grf-os 一致不阻断登录，桌面令牌为空时页面不写 openos_token）。
	Exchange func(uid, role, name string) (string, error)
	// WriteOK 成功响应写出（data 字段 token/openos_token）。
	WriteOK func(w http.ResponseWriter, data any)
	// WriteFail 错误写出（403 账密错误 / 400 请求体解析失败 / 500 签发失败）。
	WriteFail func(w http.ResponseWriter, status int, msg string)
}

// DevLoginHandler 固定账号账密端点（POST /api/v1/auth/login；仅 DEV_LOGIN=1 注册）。
// 登录成功种 openos 会话 Cookie + refresh Cookie 并回双令牌（单段式页面由此直取）。
func DevLoginHandler(cfg DevLoginConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			cfg.WriteFail(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
			return
		}
		if cfg.Username == "" || cfg.Password == "" ||
			subtle.ConstantTimeCompare([]byte(req.Username), []byte(cfg.Username)) != 1 ||
			subtle.ConstantTimeCompare([]byte(req.Password), []byte(cfg.Password)) != 1 {
			cfg.WriteFail(w, http.StatusForbidden, "账号或密码错误")
			return
		}
		uid := "dev-" + cfg.Username
		name := "开发管理员（" + cfg.Username + "）"
		hostToken, err := cfg.IssueHostToken(uid, name)
		if err != nil {
			cfg.WriteFail(w, http.StatusInternalServerError, err.Error())
			return
		}
		// exchange 显示名不带账号后缀（grf-os 原语义：openos 用户显示名恒
		// 「开发管理员」，refresh 载荷 Name 带全称供前端展示）
		osToken, err := cfg.Exchange(uid, "admin", "开发管理员")
		if err != nil {
			log.Printf("[dev-login] openos exchange 失败（桌面令牌缺省）: %v", err)
		}
		TokenCookie(w, osToken)
		if cfg.Sessions != nil {
			cfg.Sessions.SetCookie(w, RefreshClaims{UID: uid, Name: name, Src: "dev"})
		}
		cfg.WriteOK(w, map[string]any{"token": hostToken, "openos_token": osToken})
	}
}
