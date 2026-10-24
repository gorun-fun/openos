package webproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"openos/server/internal/store"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("url.Parse(%s): %v", s, err)
	}
	return u
}

func TestCompactRoundtrip(t *testing.T) {
	for _, raw := range []string{
		"https://h.example:8443/a/b%20c?q=1",
		"http://127.0.0.1:18082/",
		"https://h.example",
	} {
		u := mustURL(t, raw)
		got, err := decodeTarget(compactTarget(u))
		if err != nil {
			t.Fatalf("decodeTarget(%q): %v", compactTarget(u), err)
		}
		// 无路径的 https://h.example 往返后为 https://h.example/（语义等价的尾斜杠）
		if got.String() != u.String() && got.String() != u.String()+"/" {
			t.Errorf("roundtrip %q → %q → %q", raw, compactTarget(u), got.String())
		}
	}
	for _, bad := range []string{"", "/", "ftp/h/x", "https/", "https//x"} {
		if _, err := decodeTarget(bad); err == nil {
			t.Errorf("decodeTarget(%q) 应报错", bad)
		}
	}
	// 紧凑段不含 //（ServeMux cleanPath 才不会折叠）
	if strings.Contains(compactTarget(mustURL(t, "https://h/a")), "//") {
		t.Errorf("紧凑形态不应包含 //")
	}
}

func TestRewriteURLVal(t *testing.T) {
	base := mustURL(t, "https://t.example/dir/page")
	cases := []struct{ in, want string }{
		{"/logo.png", "/app/x/https/t.example/logo.png"},
		{"img/a.png", "/app/x/https/t.example/dir/img/a.png"},
		{"../up.png", "/app/x/https/t.example/up.png"},
		{"//cdn.example/x.js", "/app/x/https/cdn.example/x.js"},
		{"https://other.example/y?k=1", "/app/x/https/other.example/y?k=1"},
		// 带 hash 的完整地址：fragment 原样保留（SPA 微前端入口的业务路由在
		// hash 里，美团 ebooking 挂 tpt index.html#/statement-… 实测剥掉会让
		// 业务页以无路由形态初始化、降级门户首页并自愈式再挂容器=无限套娃）
		{"https://t.example/eb/index.html#/statement-9", "/app/x/https/t.example/eb/index.html#/statement-9"},
		{"#frag", "#frag"},
		{"javascript:void(0)", "javascript:void(0)"},
		{"data:image/png;base64,xx", "data:image/png;base64,xx"},
		{"about:blank", "about:blank"},
		{"", ""},
	}
	for _, c := range cases {
		got := string(rewriteURLVal([]byte(c.in), "/app/x/", base))
		if got != c.want {
			t.Errorf("rewriteURLVal(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestIsSelfHostNoPort 无端口形态的宿主自身判定：目标站 JS 用 location.hostname
// 拼绝对地址（端口丢失，含硬编码 https:// 的跨 scheme 形态——美团工作台 sub-app
// 子应用入口实测漏判会让代理拨本机被 SSRF 拒绝）。
func TestIsSelfHostNoPort(t *testing.T) {
	cases := []struct {
		host, selfHost string
		want           bool
	}{
		{"localhost", "localhost:8081", true},         // 无端口 + 硬编码 https
		{"localhost:8081", "localhost:8081", true},     // 原有：全等
		{"127.0.0.1", "localhost:8081", true},          // 无端口回环变体
		{"localhost:7712", "localhost:8081", false},    // 端口不同=别的服务（原有语义）
		{"me.meituan.com", "localhost:8081", false},    // 无关域
		{"localhost:8081", "openos.example.com", false}, // 主机名不同
	}
	for _, c := range cases {
		if got := isSelfHost(c.host, c.selfHost); got != c.want {
			t.Errorf("isSelfHost(%q, %q) = %v, want %v", c.host, c.selfHost, got, c.want)
		}
	}
}

func TestRewriteHTML(t *testing.T) {
	base := mustURL(t, "https://t.example/dir/page")
	doc := `<!doctype html>
<html><head><title>示例</title><meta http-equiv="refresh" content="0; url=/next">
<link rel=stylesheet href="/s.css">
</head>
<body><!-- 注释里的 <head> 不应触发注入 -->
<a href="/abs">A</a><a href='rel/x'>B</a>
<img srcset="a.png 1x, /b.png 2x">
<style>body{background:url('/bg.png')} @import "sub.css";</style>
<script>var u = '/not-rewritten';</script>
<img src="/i.png" style="background:url(/st.png)">
<a href="https://cdn.example/z">C</a><a href="#锚">D</a>
</body></html>`
	out := string(rewriteHTML([]byte(doc), "/app/x/", base))
	// hook 注入一次且在 <head> 之后
	if n := strings.Count(out, `src="/app/x/hook.js"`); n != 1 {
		t.Fatalf("hook 注入次数 = %d\n%s", n, out)
	}
	if !strings.Contains(out, "</head>") {
		t.Errorf("文档结构应保留 </head>")
	}
	if h, k := strings.Index(out, "<head>"), strings.Index(out, `src="/app/x/hook.js"`); h < 0 || k < h {
		t.Errorf("hook 应注入在 <head> 之后")
	}
	wantSub := []string{
		`href="/app/x/https/t.example/abs"`,
		`href='/app/x/https/t.example/dir/rel/x'`,
		`/app/x/https/t.example/dir/a.png 1x, /app/x/https/t.example/b.png 2x`,
		`url('/app/x/https/t.example/bg.png')`,
		`@import "/app/x/https/t.example/dir/sub.css"`,
		`src="/app/x/https/t.example/i.png"`,
		`url(/app/x/https/t.example/st.png)`,
		`href="/app/x/https/cdn.example/z"`,
		`content="0; url=/app/x/https/t.example/next"`,
		`href="/app/x/https/t.example/s.css"`,
		`href="#锚"`,
	}
	for _, s := range wantSub {
		if !strings.Contains(out, s) {
			t.Errorf("缺少改写结果 %q\n输出:\n%s", s, out)
		}
	}
	if strings.Contains(out, "t.example/not-rewritten") {
		t.Errorf("script 原文不应被改写")
	}
}

func TestRewriteHTMLNoHead(t *testing.T) {
	out := string(rewriteHTML([]byte("<p>残缺文档</p>"), "/app/x/", mustURL(t, "https://t.example/")))
	if !strings.HasPrefix(out, `<script src="/app/x/hook.js"></script>`) {
		t.Errorf("无 head 文档应前置注入 hook，得到:\n%s", out)
	}
}

func TestRewriteCSSBare(t *testing.T) {
	css := []byte(`a{background:url(  /a.png  )} b{mask:url("q.png")} c{src:url('z.png')}`)
	out := string(rewriteCSS(css, "/app/x/", mustURL(t, "https://t.example/dir/s.css")))
	for _, s := range []string{
		"url(/app/x/https/t.example/a.png)",
		`url("/app/x/https/t.example/dir/q.png")`,
		`url('/app/x/https/t.example/dir/z.png')`,
	} {
		if !strings.Contains(out, s) {
			t.Errorf("缺少 %q，得到:\n%s", s, out)
		}
	}
}

func TestRewriteSetCookie(t *testing.T) {
	target := mustURL(t, "https://t.example/a/b")
	v := "sid=1; Domain=example.com; Path=/; HttpOnly; Secure; SameSite=None"
	got, recPath := rewriteSetCookie(v, "/app/x/", target, false)
	if strings.Contains(got, "Domain=") || strings.Contains(got, "Secure") {
		t.Errorf("http 部署应剥 Domain/Secure：%s", got)
	}
	// 带 Domain= 的是跨子域共享语义（美团 eprt 票据 domain=.meituan.com）：剥域后
	// Path 提升到应用级 /app/<id>/——限在下发域子路径会让其他子域的代理请求
	// （票据下发在 epassport/me、消费在 tpt）永远带不上票据（发票接口 403）
	if !strings.Contains(got, "Path=/app/x/") || !strings.Contains(got, "SameSite=Lax") || !strings.Contains(got, "HttpOnly") {
		t.Errorf("Set-Cookie 跨子域票据应提升到应用级路径：%s", got)
	}
	if recPath != "/app/x/" {
		t.Errorf("跨子域票据登记路径应为应用根：%s", recPath)
	}
	got, _ = rewriteSetCookie(v, "/app/x/", target, true)
	if !strings.Contains(got, "Secure") || !strings.Contains(got, "SameSite=None") {
		t.Errorf("https 部署应保留 Secure/SameSite=None：%s", got)
	}
	// 无 Domain 声明（host-only）：Path 仍收归目标域代理子路径
	if got, rp := rewriteSetCookie("sid=2; Path=/acct", "/app/x/", target, true); !strings.Contains(got, "Path=/app/x/https/t.example/acct") || rp != "/app/x/https/t.example/acct" {
		t.Errorf("host-only Cookie 应收归目标域子路径：%s", got)
	}
	// 无 Path 属性：回落默认（按请求目录），不追加 Path
	if got, rp := rewriteSetCookie("a=b", "/app/x/", target, true); got != "a=b" || rp != "/app/x/https/t.example/a" {
		t.Errorf("无属性 cookie 应原样：%s", got)
	}
}

func TestRewriteLocation(t *testing.T) {
	base := mustURL(t, "https://t.example/a/index")
	if got := rewriteLocation("/login", "/app/x/", base, ""); got != "/app/x/https/t.example/login" {
		t.Errorf("Location 相对路径：%s", got)
	}
	if got := rewriteLocation("https://sso.example/o?x=1", "/app/x/", base, ""); got != "/app/x/https/sso.example/o?x=1" {
		t.Errorf("Location 绝对地址：%s", got)
	}
	if got := rewriteLocation("ftp://x", "/app/x/", base, ""); got != "ftp://x" {
		t.Errorf("非 http 方案应原样：%s", got)
	}
	// 宿主自身绝对地址（目标站 JS 读 location.origin 拼的登录回调/continueUrl）：
	// 挂回当前文档的目标域，而非前缀化宿主地址（会让代理拨号自身、SSRF 拒绝）
	if got := rewriteLocation("http://127.0.0.1:8081/api/gw/cb?code=1", "/app/x/", base, "127.0.0.1:8081"); got != "/app/x/https/t.example/api/gw/cb?code=1" {
		t.Errorf("Location 宿主地址应挂回目标域：%s", got)
	}
	// 回环等价形态（localhost ↔ 127.0.0.1）同样识别
	if got := rewriteLocation("http://localhost:8081/settoken/index.html", "/app/x/", base, "127.0.0.1:8081"); got != "/app/x/https/t.example/settoken/index.html" {
		t.Errorf("Location 回环等价形态：%s", got)
	}
	// 端口不同的回环 / 其他域名 → 保持既有前缀化行为
	if got := rewriteLocation("http://127.0.0.1:9999/x", "/app/x/", base, "127.0.0.1:8081"); got != "/app/x/http/127.0.0.1:9999/x" {
		t.Errorf("非宿主地址应保持前缀化：%s", got)
	}
	if got := rewriteLocation("http://intra.example/x", "/app/x/", base, "127.0.0.1:8081"); got != "/app/x/http/intra.example/x" {
		t.Errorf("普通绝对地址应保持前缀化：%s", got)
	}
	// Fragment 保留：SPA 微前端入口把业务路由放 hash（美团 ebooking 以
	// iframe.src=tpt…/index.html#/statement-… 挂载业务页），剥掉会让业务页以
	// 无路由形态初始化、降级门户首页并自愈式再挂容器（无限套娃的根因）
	if got := rewriteLocation("https://t.example/eb/index.html#/statement-9?tab=1", "/app/x/", base, ""); got != "/app/x/https/t.example/eb/index.html#/statement-9?tab=1" {
		t.Errorf("Location 应保留 fragment：%s", got)
	}
}

// TestMapSelfTarget 宿主地址兜底重映射：非宿主地址、配置目标即宿主等情形不重映射。
func TestMapSelfTarget(t *testing.T) {
	cfg := &proxyConfig{Kind: "proxy", URL: "https://me.example/home"}
	if got := mapSelfTarget(mustURL(t, "http://127.0.0.1:8081/api/gw/cb?code=1"), cfg, "127.0.0.1:8081"); got != "https/me.example/api/gw/cb?code=1" {
		t.Errorf("宿主地址应重映射到配置目标域：%q", got)
	}
	if got := mapSelfTarget(mustURL(t, "http://localhost:8081/ebooking/merchant/home"), cfg, "127.0.0.1:8081"); got != "https/me.example/ebooking/merchant/home" {
		t.Errorf("回环等价形态应识别：%q", got)
	}
	for _, raw := range []string{
		"https://cdn.example/x",
		"http://127.0.0.1:9999/x",
		"http://intra.example/x",
	} {
		if got := mapSelfTarget(mustURL(t, raw), cfg, "127.0.0.1:8081"); got != "" {
			t.Errorf("非宿主地址不应重映射（%s）：%q", raw, got)
		}
	}
	// 配置目标本身是宿主（管理员显式把 OpenOS 自身当代理目标）：保持原语义
	cfgSelf := &proxyConfig{Kind: "proxy", URL: "http://127.0.0.1:8081/self/"}
	if got := mapSelfTarget(mustURL(t, "http://127.0.0.1:8081/x"), cfgSelf, "127.0.0.1:8081"); got != "" {
		t.Errorf("配置目标即宿主时不应重映射：%q", got)
	}
}

func TestParseProxyConfig(t *testing.T) {
	if parseProxyConfig(nil) != nil {
		t.Error("空 config 应返回 nil")
	}
	if parseProxyConfig(json.RawMessage(`{"kind":"web","url":"https://x/"}`)) != nil {
		t.Error("kind=web 非 proxy")
	}
	if parseProxyConfig(json.RawMessage(`{"kind":"proxy","url":"ftp://x/"}`)) != nil {
		t.Error("非 http(s) 目标应拒绝")
	}
	c := parseProxyConfig(json.RawMessage(`{"kind":"proxy","url":"https://x.example/path/"}`))
	if c == nil || c.URL != "https://x.example/path/" {
		t.Errorf("合法配置解析失败：%+v", c)
	}
	// 双重编码历史形态
	if c := parseProxyConfig(json.RawMessage(`"{\"kind\":\"proxy\",\"url\":\"https://x/\"}"`)); c == nil {
		t.Error("双重编码形态应兼容")
	}
	// 自定义请求头：合法条目保留；黑名单（逐跳/传输控制）、非法名、含 CR/LF 的值丢弃
	c = parseProxyConfig(json.RawMessage(`{"kind":"proxy","url":"https://x/","headers":{` +
		`"Authorization":"Bearer t","X-Api-Key":"k","Connection":"keep-alive","Host":"evil",` +
		`"Bad Name":"v","X-Inj":"a\r\nb","":"x"}}`))
	if c == nil || c.Headers["Authorization"] != "Bearer t" || c.Headers["X-Api-Key"] != "k" || len(c.Headers) != 2 {
		t.Errorf("headers 清洗不符合预期：%+v", c.Headers)
	}
}

// TestRestorePxParam OAuth 回跳参数还原：hook 把 redirect_uri 改写为代理路径后，
// 转发上游须还原为注册的原始绝对地址（IdP 白名单校验依赖原始值）；目标站 JS 读
// location.origin 拼出的宿主绝对地址还原为配置目标域（微信 jslogin 即此形态）。
func TestRestorePxParam(t *testing.T) {
	wx := &proxyConfig{Kind: "proxy", URL: "https://wx.qq.com/"}
	// 命中：本应用前缀的值还原为绝对地址（query 重编码属预期）
	in := "redirect_uri=/app/x/https/play.example/api/auth/sso/callback&state=abc"
	got := restorePxParam("x", in, wx, "example.com")
	if v, err := url.ParseQuery(got); err != nil || v.Get("redirect_uri") != "https://play.example/api/auth/sso/callback" || v.Get("state") != "abc" {
		t.Errorf("restorePxParam = %q", got)
	}
	// 驼峰变体同样还原
	got = restorePxParam("x", "redirectUri=/app/x/https/play.example/cb&x=1", wx, "example.com")
	if v, err := url.ParseQuery(got); err != nil || v.Get("redirectUri") != "https://play.example/cb" || v.Get("x") != "1" {
		t.Errorf("restorePxParam(redirectUri) = %q", got)
	}
	// 编码形态（页面 JS encodeURIComponent 后的值）：解码后命中前缀同样还原
	got = restorePxParam("x", "redirectUri=" + url.QueryEscape("/app/x/https/play.example/cb") + "&state=abc", wx, "example.com")
	if v, err := url.ParseQuery(got); err != nil || v.Get("redirectUri") != "https://play.example/cb" || v.Get("state") != "abc" {
		t.Errorf("restorePxParam(编码形态) = %q", got)
	}
	// 宿主自身绝对地址（微信 jslogin 场景：页面 JS 读 location.origin 拼的回跳，
	// http/https 与端口形态等价判定）→ 还原为配置目标域，路径查询保留
	got = restorePxParam("x", "appid=wx782&fun=new&redirect_uri=http%3A%2F%2Fexample.com%2Fcgi-bin%2Fwebwxnewloginpage", wx, "example.com")
	if v, err := url.ParseQuery(got); err != nil || v.Get("redirect_uri") != "https://wx.qq.com/cgi-bin/webwxnewloginpage" || v.Get("appid") != "wx782" {
		t.Errorf("宿主地址还原不符：%q", got)
	}
	got = restorePxParam("x", "appid=wx782&fun=new&redirect_uri=https%3A%2F%2Fexample.com%3A8081%2Fcgi-bin%2Fwebwxnewloginpage", wx, "example.com:8081")
	if v, err := url.ParseQuery(got); err != nil || v.Get("redirect_uri") != "https://wx.qq.com/cgi-bin/webwxnewloginpage" {
		t.Errorf("宿主地址（带端口）还原不符：%q", got)
	}
	// 非本应用前缀 / 已是目标站绝对地址 / 无参数：query 原样（不重编码扰动）
	for _, q := range []string{
		"redirect_uri=/app/y/https/play.example/cb&state=abc",
		"redirect_uri=https://play.example/cb&state=abc",
		"client_id=1&state=abc",
		"",
	} {
		if got := restorePxParam("x", q, wx, "example.com"); got != q {
			t.Errorf("restorePxParam(%q) 应原样，得到 %q", q, got)
		}
	}
	// 非法代理前缀形态：解析失败保持原值
	if got := restorePxParam("x", "redirect_uri=/app/x/ftp/x", wx, "example.com"); got != "redirect_uri=/app/x/ftp/x" {
		t.Errorf("非法形态应保持原值，得到 %q", got)
	}
	// continue/continueUrl（美团 ebooking：epassport oauth-login 的 continue 指向
	// me.meituan.com 的 callback）：宿主地址形态还原为配置目标域——缺失时上游拿到
	// 宿主地址、302 被挂到表单所在域，跨主机 callback 落错误主机 404；代理路径
	// 形态（hook pxQuery 改写后）还原为原始绝对地址，嵌套 query 保留
	mt := &proxyConfig{Kind: "proxy", URL: "https://me.meituan.com/ebooking/merchant/ebIframe"}
	got = restorePxParam("m", "service=eb&continue=http%3A%2F%2Flocalhost%3A8081%2Fapi%2Fgw%2Fv1%2Fbase%2Flogin%2Fcallback%3FclientId%3D1", mt, "localhost:8081")
	if v, err := url.ParseQuery(got); err != nil || v.Get("continue") != "https://me.meituan.com/api/gw/v1/base/login/callback?clientId=1" || v.Get("service") != "eb" {
		t.Errorf("continue 宿主地址还原不符：%q", got)
	}
	got = restorePxParam("m", "continue=" + url.QueryEscape("/app/m/https/me.meituan.com/api/gw/v1/base/login/callback?clientId=1&continueUrl=https%3A%2F%2Fme.meituan.com%2Fsettoken%2Findex.html"), mt, "localhost:8081")
	if v, err := url.ParseQuery(got); err != nil || v.Get("continue") != "https://me.meituan.com/api/gw/v1/base/login/callback?clientId=1&continueUrl=https%3A%2F%2Fme.meituan.com%2Fsettoken%2Findex.html" {
		t.Errorf("continue 代理路径还原不符：%q", got)
	}
	// continue 站内相对值（其他站点把 continue 用作普通站内跳转）不受扰动
	if got := restorePxParam("m", "continue=/home/next&x=1", mt, "localhost:8081"); got != "continue=/home/next&x=1" {
		t.Errorf("continue 相对值应原样，得到 %q", got)
	}
}

// TestRewriteReferer Referer 还原（代理路径前缀形态与 hook 还原态标记形态）。
func TestRewriteReferer(t *testing.T) {
	// 前缀形态：/app/x/<目标>/… → 目标绝对地址（剥 __os_px）
	if got := rewriteReferer("http://127.0.0.1:8081/app/x/https/wx.qq.com/?target=t&__os_px=x~https~wx.qq.com", "x"); got != "https://wx.qq.com/?target=t" {
		t.Errorf("rewriteReferer(前缀形态) = %q", got)
	}
	// 还原态：__os_px 标记 + 目标站路径
	if got := rewriteReferer("http://127.0.0.1:8081/cgi-bin/mmwebwx-bin/login?__os_px=x~https~wx2.qq.com&tip=1", "x"); got != "https://wx2.qq.com/cgi-bin/mmwebwx-bin/login?tip=1" {
		t.Errorf("rewriteReferer(还原态) = %q", got)
	}
	// 非本应用/无关地址：返回空（调用方不发送 Referer）
	for _, ref := range []string{
		"http://127.0.0.1:8081/app/y/https/wx.qq.com/",
		"http://127.0.0.1:8081/?__os_px=y~https~wx.qq.com",
		"http://127.0.0.1:8081/",
		"https://evil.example/x",
	} {
		if got := rewriteReferer(ref, "x"); got != "" {
			t.Errorf("rewriteReferer(%q) 应为空，得到 %q", ref, got)
		}
	}
}

func TestKeepCookies(t *testing.T) {
	got := keepCookies("openos_token=SECRET; sid=7; openos_px=1; theme=dark")
	if got != "sid=7; theme=dark" {
		t.Errorf("keepCookies = %q", got)
	}
	if keepCookies("openos_token=ONLY") != "" {
		t.Errorf("仅会话 cookie 应全部剥掉")
	}
}

func TestResolveGuarded(t *testing.T) {
	ctx := context.Background()
	if ips, err := resolveGuarded(ctx, "127.0.0.1", "127.0.0.1"); err != nil || len(ips) == 0 {
		t.Error("配置目标主机应放行（可为内网）")
	}
	if _, err := resolveGuarded(ctx, "8.8.8.8", ""); err != nil {
		t.Error("公网 IP 应放行")
	}
	for _, host := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.1", "169.254.1.1", "::1", "0.0.0.0"} {
		if _, err := resolveGuarded(ctx, host, ""); err == nil {
			t.Errorf("非配置内网地址应拒绝：%s", host)
		}
	}
	if _, err := resolveGuarded(ctx, "localhost", "127.0.0.1"); err == nil {
		t.Error("localhost 不等于配置主机名，解析到回环应拒绝")
	}
}

// TestServeHTTPProxy 覆盖反代主链路：剥嵌入封锁头、改写 HTML/CSS 与 Set-Cookie、
// Location 重写、会话 Cookie 过滤、gzip 解压改写、SSRF 拒绝非配置内网主机。
func TestServeHTTPProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
			w.Header().Add("Set-Cookie", "sid=7; Domain=t.example; Path=/; HttpOnly")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><head><title>t</title></head><body><a href="/z">z</a></body></html>`))
		case "/gz":
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			_, _ = zw.Write([]byte(`<html><head></head><body><img src="/g.png"></body></html>`))
			zw.Close()
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(buf.Bytes())
		case "/s.css":
			w.Header().Set("Content-Type", "text/css")
			_, _ = w.Write([]byte(`a{background:url('/c.png')}`))
		case "/redir":
			w.Header().Set("Location", "/page")
			w.WriteHeader(http.StatusFound)
		case "/redir-selfhost":
			// 上游 302 到宿主自身绝对地址（目标站 JS 读 location.origin 拼的回调）
			w.Header().Set("Location", "http://example.com/settoken/index.html?redirect=%2Fhome")
			w.WriteHeader(http.StatusFound)
		case "/echo-cookie":
			_, _ = w.Write([]byte("Cookie:" + r.Header.Get("Cookie") + "|AE:" + r.Header.Get("Accept-Encoding")))
		case "/echo-hdr":
			fmt.Fprintf(w, "Auth:%s|Key:%s|CK:%s", r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"), r.Header.Get("Cookie"))
		case "/echo-ref":
			fmt.Fprintf(w, "Ref:%s|SF:%s", r.Header.Get("Referer"), r.Header.Get("Sec-Fetch-Dest"))
		case "/double-ct":
			// 上游 bug 形态：重复下发 Content-Type（微信 jslogin 实测行为），
			// 浏览器按末值解析会让 script 严格 MIME 检查失败
			w.Header().Add("Content-Type", "text/javascript")
			w.Header().Add("Content-Type", "text/html; charset=gbk")
			_, _ = w.Write([]byte(`window.QRLogin.code = 200;`))
		}
	}))
	defer upstream.Close()
	upURL := mustURL(t, upstream.URL)
	upCompact := upURL.Scheme + "/" + upURL.Host // 紧凑目标源，如 http/127.0.0.1:PORT

	p := New(nil)
	// 带自定义请求头的代理配置：固定认证头 + 认证 Cookie（Cookie 应与保留的
	// 目标站会话合并而非顶掉）
	pcfg := &proxyConfig{Kind: "proxy", URL: upstream.URL, Headers: map[string]string{
		"Authorization": "Bearer tk",
		"X-Api-Key":     "k1",
		"Cookie":        "gate=fixed",
	}}
	do := func(compactRest string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/app/x/"+compactRest, nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		tu, err := decodeTarget(compactRest)
		if err != nil {
			t.Fatalf("decodeTarget(%q): %v", compactRest, err)
		}
		p.serveHTTPProxy(w, r, "x", tu, pcfg)
		return w
	}

	// HTML：剥头 + 注入 hook + 改写 href + Set-Cookie 收归代理路径
	w := do(upCompact+"/page", map[string]string{"Cookie": "openos_token=SECRET; sid=7"})
	if w.Code != 200 {
		t.Fatalf("/page code=%d body=%s", w.Code, w.Body.String())
	}
	if v := w.Header().Get("X-Frame-Options"); v != "" {
		t.Errorf("X-Frame-Options 应被剥掉：%q", v)
	}
	if v := w.Header().Get("Content-Security-Policy"); v != "" {
		t.Errorf("CSP 应被剥掉：%q", v)
	}
	sc := w.Header().Get("Set-Cookie")
	// 带 Domain= 的跨子域票据语义：剥域后 Path 提升到应用级（非目标域子路径）
	if !strings.Contains(sc, "Path=/app/x/") || strings.Contains(sc, "Domain=") {
		t.Errorf("Set-Cookie 应提升到应用级路径且剥 Domain：%q", sc)
	}
	body := w.Body.String()
	if !strings.Contains(body, `src="/app/x/hook.js"`) || !strings.Contains(body, `href="/app/x/`+upCompact+`/z"`) {
		t.Errorf("HTML 改写缺失：%s", body)
	}
	if w.Header().Get("Content-Length") == "" {
		t.Errorf("改写后应重算 Content-Length")
	}

	// gzip 响应：解压后改写
	w = do(upCompact+"/gz", nil)
	if !strings.Contains(w.Body.String(), `src="/app/x/hook.js"`) || !strings.Contains(w.Body.String(), "/g.png") {
		t.Errorf("gzip 响应应解压改写：%s", w.Body.String())
	}
	if w.Header().Get("Content-Encoding") != "" {
		t.Errorf("解压改写后应移除 Content-Encoding")
	}

	// CSS
	w = do(upCompact+"/s.css", nil)
	if !strings.Contains(w.Body.String(), "/app/x/"+upCompact+"/c.png") {
		t.Errorf("CSS url 应被改写：%s", w.Body.String())
	}

	// 重定向 Location
	w = do(upCompact+"/redir", nil)
	if w.Code != 302 || w.Header().Get("Location") != "/app/x/"+upCompact+"/page" {
		t.Errorf("Location 应被重写，得到 code=%d loc=%q", w.Code, w.Header().Get("Location"))
	}

	// 重定向 Location 指向宿主自身（httptest.NewRequest 默认 Host=example.com）：
	// 挂回上游目标域而非前缀化宿主地址（否则浏览器跟随后代理拨号自身、SSRF 拒绝）
	w = do(upCompact+"/redir-selfhost", nil)
	if w.Code != 302 || w.Header().Get("Location") != "/app/x/"+upCompact+"/settoken/index.html?redirect=%2Fhome" {
		t.Errorf("宿主地址 Location 应挂回目标域，得到 code=%d loc=%q", w.Code, w.Header().Get("Location"))
	}

	// 会话 Cookie 过滤 + Accept-Encoding 只收 gzip
	w = do(upCompact+"/echo-cookie", map[string]string{"Cookie": "openos_token=SECRET; sid=7"})
	if s := w.Body.String(); strings.Contains(s, "SECRET") || !strings.Contains(s, "sid=7") || !strings.Contains(s, "AE:gzip") {
		t.Errorf("会话头过滤/AE 不符合预期：%s", s)
	}

	// 配置请求头：认证头随转发附加；配置 Cookie 与保留的目标站会话合并，
	// OpenOS 会话依旧不外泄
	w = do(upCompact+"/echo-hdr", map[string]string{"Cookie": "openos_token=SECRET; sid=7"})
	if s := w.Body.String(); s != "Auth:Bearer tk|Key:k1|CK:sid=7; gate=fixed" {
		t.Errorf("配置请求头应用不符合预期：%s", s)
	}

	// 重复 Content-Type：未知站点多值透传（单值收敛已解耦到 weixin-double-ct
	// 适配器按目标主机生效，见 TestSiteAdapters；浏览器按末值解析是站点自身
	// bug 的原生表现，未知站点维持原生语义）
	w = do(upCompact+"/double-ct", nil)
	if cts := w.Header().Values("Content-Type"); len(cts) != 2 {
		t.Errorf("未知站点的重复 Content-Type 应多值透传，得到 %v", cts)
	}

	// Referer 还原 + Sec-Fetch 剥离：代理页面的 Referer 还原为目标站自身地址
	//（微信 webwxnewloginpage 风控依赖 Referer），描述浏览器源关系的 Sec-Fetch-*
	// 在反代后语义失真，整组剥除
	w = do(upCompact+"/echo-ref", map[string]string{
		"Referer":         "http://example.com/app/x/" + upCompact + "/page?__os_px=x~" + upCompact,
		"Sec-Fetch-Dest":  "document",
		"Sec-Fetch-Site":  "same-origin",
		"Sec-Fetch-Mode":  "navigate",
		"Sec-Fetch-User":  "?1",
	})
	if s := w.Body.String(); s != "Ref:"+upstream.URL+"/page|SF:" {
		t.Errorf("Referer 还原/Sec-Fetch 剥离不符：%s", s)
	}
	// 非本应用代理页的 Referer 不原样发送，兜底重建为目标站根地址（kdocs 写
	// 接口要求 Origin/Referer 至少一个匹配自家域，剥光会被风控拒绝）
	w = do(upCompact+"/echo-ref", map[string]string{"Referer": "http://example.com/other"})
	if s := w.Body.String(); s != "Ref:"+upstream.URL+"/|SF:" {
		t.Errorf("无关 Referer 应兜底重建为目标站根地址，得到 %s", s)
	}

	// SSRF：目标是回环但并非配置主机（localhost ≠ 127.0.0.1）→ 拒绝
	r := httptest.NewRequest("GET", "/app/x/https/localhost:1/x", nil)
	w2 := httptest.NewRecorder()
	tu, err := decodeTarget("https/localhost:1/x")
	if err != nil {
		t.Fatalf("decodeTarget: %v", err)
	}
	p.serveHTTPProxy(w2, r, "x", tu, pcfg)
	if w2.Code != http.StatusBadGateway {
		t.Errorf("非配置内网目标应 502，得到 %d", w2.Code)
	}
}

func TestEscapeTarget(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	upURL := mustURL(t, upstream.URL)

	mkReq := func(path, referer string) *http.Request {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "os.example"
		if referer != "" {
			r.Header.Set("Referer", referer)
		}
		return r
	}

	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()
	_ = db.UpsertApp(store.AppDef{ID: "px1", Name: "x", Config: []byte(`{"kind":"proxy","url":"https://t.example/"}`)})
	ref := "http://os.example/app/px1/https/t.example/dir/page"

	// 放行：无 Referer / Referer 路径不在 /app/ 前缀下（db 未触达，nil 即可）
	for _, c := range []struct{ path, rf string }{
		{"/s?wd=x", ""},
		{"/s?wd=x", "https://other.example/some/where"},
		{"/api/auth/sso/login", ""},
	} {
		if got := escapeTarget(nil, mkReq(c.path, c.rf)); got != "" {
			t.Errorf("escapeTarget(%q, ref=%q) 应放行，得到 %q", c.path, c.rf, got)
		}
	}

	// 放行：代理自身命名空间与 WS 端点（回捕会造成前缀自我叠加 / WS 不跟随 307）
	for _, path := range []string{"/app/px1/https/t.example/x", "/app/other/y", "/ws"} {
		if got := escapeTarget(db, mkReq(path, ref)); got != "" {
			t.Errorf("escapeTarget(%q) 应放行，得到 %q", path, got)
		}
	}

	// 回捕：代理应用 Referer + 逃逸路径（含查询串）→ 拼回该应用的目标源。
	// /api/* 等本源真实路由前缀同样回捕（目标站 SSO 常见形态，撞本源路由会 404）
	for _, c := range []struct{ path, want string }{
		{"/s?wd=hello", "/app/px1/https/t.example/s?wd=hello"},
		{"/api/auth/sso/login", "/app/px1/https/t.example/api/auth/sso/login"},
		{"/assets/app.js", "/app/px1/https/t.example/assets/app.js"},
		{"/index.html", "/app/px1/https/t.example/index.html"},
		{"/", "/app/px1/https/t.example/"},
	} {
		if got := escapeTarget(db, mkReq(c.path, ref)); got != c.want {
			t.Errorf("escapeTarget(%q) = %q, want %q", c.path, got, c.want)
		}
	}
	// Referer 与请求 Host 不一致同样回捕：开发模式（vite 代理）与宿主网关下
	// Host 会被 changeOrigin/反代改写，同源判定会误伤
	if got := escapeTarget(db, mkReq("/api/auth/sso/login", "http://localhost:5173/app/px1/https/t.example/")); got != "/app/px1/https/t.example/api/auth/sso/login" {
		t.Errorf("跨 Host 代理 Referer 应回捕，得到 %q", got)
	}
	// hook 还原模式：Referer 是还原后的目标站地址，靠查询串 __os_px 标记回捕；
	// 回捕目标剥掉标记本身
	if got := escapeTarget(db, mkReq("/api/x?a=1&__os_px=px1~https~t.example&b=2", "http://os.example/login?next=/x&__os_px=px1~https~t.example")); got != "/app/px1/https/t.example/api/x?a=1&b=2" {
		t.Errorf("还原模式逃逸回捕 = %q", got)
	}
	// 还原标记指向不存在/非代理应用、或 host 段带非法字符 → 放行
	for _, rf := range []string{
		"http://os.example/login?__os_px=nope~https~t.example",
		"http://os.example/login?__os_px=w1~https~t.example",
		"http://os.example/login?__os_px=px1~https~evil.example%2Fpx1",
		"http://os.example/login?__os_px=px1~ftp~t.example",
	} {
		if got := escapeTarget(db, mkReq("/s", rf)); got != "" {
			t.Errorf("非法还原标记应放行（%q），得到 %q", rf, got)
		}
	}
	// POST（表单逃逸）同样回捕
	r := mkReq("/s", "http://os.example/app/px1/https/t.example/")
	r.Method = "POST"
	if got := escapeTarget(db, r); got != "/app/px1/https/t.example/s" {
		t.Errorf("POST 逃逸回捕 = %q", got)
	}
	// 非代理应用 / 不存在的应用 → 放行
	_ = db.UpsertApp(store.AppDef{ID: "w1", Name: "w", Config: []byte(`{"kind":"web","url":"https://t.example/"}`)})
	if got := escapeTarget(db, mkReq("/s", "http://os.example/app/w1/https/t.example/")); got != "" {
		t.Errorf("非代理应用应放行，得到 %q", got)
	}
	if got := escapeTarget(db, mkReq("/s", "http://os.example/app/nope/https/t.example/")); got != "" {
		t.Errorf("不存在的应用应放行，得到 %q", got)
	}
	_ = upURL
}

func TestRewriteHTMLDropsMetaCSP(t *testing.T) {
	base := mustURL(t, "https://t.example/")
	// meta CSP（含 Report-Only 与大小写/引号变体）应整枚剥除；meta 排在首个外部
	// script 之前时其后的脚本在代理源下会被 CSP 全部拦掉（美团 ebooking 实测）
	doc := `<html><head>
<meta http-equiv="Content-Security-Policy" content="script-src 'unsafe-eval' *.meituan.com">
<meta http-equiv='Content-Security-Policy-Report-Only' content="script-src 'self'">
<meta HTTP-EQUIV=Content-Security-Policy content=script-src>
<meta charset="utf-8">
<meta name="x" content="Content-Security-Policy">
<script src="/a.js"></script>
</head><body></body></html>`
	out := string(rewriteHTML([]byte(doc), "/app/x/", base))
	if strings.Contains(strings.ToLower(out), "http-equiv") {
		t.Errorf("CSP meta 应被剥除（夹具中无其他 http-equiv meta），输出仍含:\n%s", out)
	}
	if !strings.Contains(out, `<meta charset="utf-8">`) {
		t.Errorf("无关 meta 应保留:\n%s", out)
	}
	if !strings.Contains(out, `content="Content-Security-Policy"`) {
		t.Errorf("name/content 属性提及 CSP 的普通 meta 应保留:\n%s", out)
	}
	if !strings.Contains(out, `src="/app/x/https/t.example/a.js"`) {
		t.Errorf("后续脚本应保留并被改写:\n%s", out)
	}
	if n := strings.Count(out, `src="/app/x/hook.js"`); n != 1 {
		t.Errorf("hook 注入次数 = %d\n%s", n, out)
	}
}

func TestMergeCookies(t *testing.T) {
	// 同名：jar（目标站 Set-Cookie 轮换值）优先；配置独有名字兜底附加
	got := mergeCookies("a=1; b=2; c=3", "b=fresh; d=4")
	if got != "b=fresh; d=4; a=1; c=3" {
		t.Errorf("mergeCookies = %q", got)
	}
	if got := mergeCookies("a=1", ""); got != "a=1" {
		t.Errorf("空 jar 应保留全部配置: %q", got)
	}
	if got := mergeCookies("", "a=1"); got != "a=1" {
		t.Errorf("空配置应保留 jar: %q", got)
	}
}

// proxyFor 测试辅助：对指定上游目标直接调 serveHTTPProxy（绕过 Handler 的
// 鉴权/路由层，该层依赖 auth 包私有上下文）。
func proxyFor(t *testing.T, upstreamURL, compactRest string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	p := New(nil)
	pcfg := &proxyConfig{Kind: "proxy", URL: upstreamURL}
	r := httptest.NewRequest("GET", "/app/x/"+compactRest, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	tu, err := decodeTarget(compactRest)
	if err != nil {
		t.Fatalf("decodeTarget(%q): %v", compactRest, err)
	}
	p.serveHTTPProxy(w, r, "x", tu, pcfg)
	return w
}

// TestServeHTTPProxyOverlimit 超限文本响应：不重写、不截断——已读入前段与剩余流
// 原文透传（旧实现按 16MB+1 截断并按截断长度回写 Content-Length，浏览器拿到
// 合法长度但不完整的文档）。
func TestServeHTTPProxyOverlimit(t *testing.T) {
	big := []byte(`<html><head></head><body>` + strings.Repeat("a", maxRewriteBody) + `</body></html>`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(big)
	}))
	defer upstream.Close()
	upURL := mustURL(t, upstream.URL)

	w := proxyFor(t, upstream.URL, upURL.Scheme+"/"+upURL.Host+"/big", nil)
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if got := w.Body.Len(); got != len(big) {
		t.Errorf("超限响应被截断：got %d want %d", got, len(big))
	}
	if strings.Contains(w.Body.String(), "hook.js") {
		t.Errorf("超限响应不应注入 hook")
	}
}

// TestServeHTTPProxyGzipBomb 解压放大防护：压缩态在限内、解压后超限的响应按
// 压缩态原文透传（Content-Encoding 保留），不得无界解压（内存放大）；损坏的
// gzip 流与无法解压的压缩编码（br）同样原文透传而非改写压缩字节。
func TestServeHTTPProxyGzipBomb(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(strings.Repeat("a", maxRewriteBody+64)))
	zw.Close()
	bomb := append([]byte(nil), buf.Bytes()...) // 压缩态很小、解压后超限

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bomb":
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(bomb)
		case "/corrupt":
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("not-gzip-at-all"))
		case "/br":
			w.Header().Set("Content-Encoding", "br")
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("pretend-brotli-bytes"))
		}
	}))
	defer upstream.Close()
	upURL := mustURL(t, upstream.URL)
	compact := upURL.Scheme + "/" + upURL.Host

	// 压缩炸弹：压缩态原文透传（Content-Encoding 保留、无注入、无截断）
	w := proxyFor(t, upstream.URL, compact+"/bomb", nil)
	if w.Code != 200 {
		t.Fatalf("bomb code=%d", w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), bomb) {
		t.Errorf("压缩态应原文透传：got %d bytes", w.Body.Len())
	}
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("Content-Encoding 应保留：%q", w.Header().Get("Content-Encoding"))
	}

	// 损坏 gzip 流 / br 编码：原文透传，不做改写（改写压缩字节只会破坏流）
	for _, path := range []string{"/corrupt", "/br"} {
		w := proxyFor(t, upstream.URL, compact+path, nil)
		if w.Code != 200 {
			t.Fatalf("%s code=%d", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "hook.js") {
			t.Errorf("%s 不应注入 hook", path)
		}
		if w.Header().Get("Content-Encoding") == "" {
			t.Errorf("%s Content-Encoding 应保留", path)
		}
	}
}

// TestSiteAdapters 站点适配层：主机匹配规则与两个内置适配器的钩子行为。
func TestSiteAdapters(t *testing.T) {
	// 匹配：裸域匹配自身与任意子域、精确段、端口剥离、大小写不敏感、未知域不命中
	if got := matchAdapters("me.meituan.com"); len(got) == 0 || got[0].Name != "origin-strict" {
		t.Errorf("me.meituan.com 应命中 origin-strict，得到 %v", got)
	}
	if got := matchAdapters("meituan.com"); len(got) == 0 || got[0].Name != "origin-strict" {
		t.Errorf("meituan.com 裸域应命中自身，得到 %v", got)
	}
	if got := matchAdapters("ME.MEITUAN.COM:443"); len(got) == 0 {
		t.Errorf("大写与端口应被归一后命中，得到 %v", got)
	}
	if got := matchAdapters("login.weixin.qq.com"); len(got) != 2 {
		t.Errorf("微信域应命中 origin-strict + weixin-double-ct 两个适配器，得到 %v", names(got))
	}
	if got := matchAdapters("fake-meituan.com.evil.io"); len(got) != 0 {
		t.Errorf("后缀伪造域名不应命中（须整段匹配），得到 %v", names(got))
	}
	if got := matchAdapters("unknown.example"); len(got) != 0 {
		t.Errorf("未知域不应命中任何适配器，得到 %v", names(got))
	}

	// restoreSelfDomainParam：值恰为宿主自身源的 domain 参数还原为目标站源
	//（美团 ebooking 权限菜单接口按 domain 匹配配置，宿主域命中不到即空权限集，
	// 应用启动判无权限重定向默认页）；无关值原样透传
	tuMe, _ := url.Parse("https://eb.meituan.com/api/v1/x")
	q0 := "bizLine=3&domain=" + url.QueryEscape("https://hotel.gorun.fun") + "&env=beta"
	if got := restoreSelfDomainParam(q0, "hotel.gorun.fun", tuMe); got != "bizLine=3&domain="+url.QueryEscape("https://eb.meituan.com")+"&env=beta" {
		t.Errorf("宿主源 domain 应还原为目标站源，得到 %q", got)
	}
	// 非宿主源（站点真实自身域）不动
	q1 := "domain=" + url.QueryEscape("https://eb.meituan.com")
	if got := restoreSelfDomainParam(q1, "hotel.gorun.fun", tuMe); got != q1 {
		t.Errorf("非宿主源 domain 不应改写，得到 %q", got)
	}
	// 带端口形态的宿主自身域（isSelfHost 的端口归一形态）也还原
	q2 := "domain=" + url.QueryEscape("https://hotel.gorun.fun:443")
	if got := restoreSelfDomainParam(q2, "hotel.gorun.fun", tuMe); got != "domain="+url.QueryEscape("https://eb.meituan.com") {
		t.Errorf("端口形态宿主域 domain 应还原，得到 %q", got)
	}

	// meituan-container-handshake：美团系域全命中，Hook 含握手协议关键字段
	//（探测拦截、合成应答的协议名/iframeInSystem/合成 MessageEvent）
	mtHooks := ""
	for _, a := range matchAdapters("me.meituan.com") {
		if a.Name == "meituan-container-handshake" {
			mtHooks = a.Hook
		}
	}
	for _, kw := range []string{
		"hotel-query-container", "hotel-container-response",
		"iframeInSystem", "MessageEvent", "__os_px",
	} {
		if !strings.Contains(mtHooks, kw) {
			t.Errorf("meituan-container-handshake Hook 应包含 %q", kw)
		}
	}

	// origin-strict：无 Origin 时按「还原后 Referer 的源 > 目标站自身源」兜底
	//（跨域请求的真实 Origin 是发起方页面的源——美团滑块校验请求由
	// epassport 页面发往 verify.meituan.com，补目标站自身会产出真实世界不存在
	// 的「自己向自己发起跨域校验」形态被风控拒绝），已有则不覆盖
	out := httptest.NewRequest("POST", "https://me.meituan.com/api/x", nil)
	out.Header.Del("Origin")
	for _, a := range matchAdapters("me.meituan.com") {
		if a.OnRequest != nil {
			a.OnRequest(out, nil)
		}
	}
	if v := out.Header.Get("Origin"); v != "https://me.meituan.com" {
		t.Errorf("无 Referer 时应补 Origin=目标站自身源，得到 %q", v)
	}
	// 跨域形态：Referer 是另一美团系域（epassport 页面），目标 verify——
	// 真实世界浏览器原生 Origin 即发起方源，兜底须与其一致
	out.Header.Set("Referer", "https://epassport.meituan.com/portal/oauth-login?service=x")
	out.Header.Del("Origin")
	for _, a := range matchAdapters("verify.meituan.com") {
		if a.OnRequest != nil {
			a.OnRequest(out, nil)
		}
	}
	if v := out.Header.Get("Origin"); v != "https://epassport.meituan.com" {
		t.Errorf("跨域形态应补 Origin=Referer 源（发起方），得到 %q", v)
	}
	// 同源形态：Referer 与目标同主机——原生同源 POST 的 Origin 即自身
	out.Header.Set("Referer", "https://me.meituan.com/other/page")
	out.Header.Del("Origin")
	for _, a := range matchAdapters("me.meituan.com") {
		if a.OnRequest != nil {
			a.OnRequest(out, nil)
		}
	}
	if v := out.Header.Get("Origin"); v != "https://me.meituan.com" {
		t.Errorf("同源形态应补 Origin=目标站自身源，得到 %q", v)
	}
	out.Header.Set("Origin", "https://custom.example")
	for _, a := range matchAdapters("me.meituan.com") {
		if a.OnRequest != nil {
			a.OnRequest(out, nil)
		}
	}
	if v := out.Header.Get("Origin"); v != "https://custom.example" {
		t.Errorf("已有 Origin 不应被覆盖，得到 %q", v)
	}
	// 未知站点：通用链路不补 Origin（原生 GET 不携带 Origin）
	out2 := httptest.NewRequest("GET", "https://unknown.example/x", nil)
	if v := out2.Header.Get("Origin"); v != "" {
		t.Errorf("未知站点不应凭空补 Origin，得到 %q", v)
	}

	// weixin-double-ct：重复 Content-Type 收敛为首个（微信 jslogin 上游 bug，
	// 浏览器按末值解析会让 script 严格 MIME 检查拒执行）
	h := http.Header{}
	h.Add("Content-Type", "text/javascript")
	h.Add("Content-Type", "text/html; charset=gbk")
	for _, a := range matchAdapters("login.weixin.qq.com") {
		if a.OnResponse != nil {
			a.OnResponse(h)
		}
	}
	if cts := h.Values("Content-Type"); len(cts) != 1 || cts[0] != "text/javascript" {
		t.Errorf("微信域重复 Content-Type 应收敛为首个，得到 %v", cts)
	}

	// meituan-sso-rewrite：美团系 JS 里的 SSO 域字面量改写为代理路径（含
	// test/st 变体，且先替换更长域防前缀截断），未知域不受影响
	var mtRewrite func([]byte, string) []byte
	for _, a := range matchAdapters("awp-assets.meituan.net") {
		if a.Name == "meituan-sso-rewrite" && a.RewriteJS != nil {
			mtRewrite = a.RewriteJS
		}
	}
	if mtRewrite == nil {
		t.Fatalf("awp-assets.meituan.net 应命中 meituan-sso-rewrite 的 RewriteJS")
	}
	js := []byte(`Ps?"https://ssosv.it.test.sankuai.com/sson/login?":"https://ssosv.sankuai.com/sson/login?",t.CONFIG={SSO_CONFIG_URL:"https://ssosv.sankuai.com/configs"}`)
	got := string(mtRewrite(js, "/app/web-me.meituan.com/"))
	if strings.Contains(got, `"https://ssosv`) {
		t.Errorf("ssosv 绝对地址字面量应全部改写为代理路径，得到 %s", got)
	}
	for _, want := range []string{
		`"/app/web-me.meituan.com/https/ssosv.it.test.sankuai.com/sson/login?`,
		`"/app/web-me.meituan.com/https/ssosv.sankuai.com/sson/login?`,
		`"/app/web-me.meituan.com/https/ssosv.sankuai.com/configs"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("改写结果缺少 %s，得到 %s", want, got)
		}
	}
	// epassport 商家 Epassport 登录分支（login JS 以 location.href= 字面量直跳，
	// 漏改写时重新登录逃逸到真实域、票据种不进代理路径→发票接口 403）
	epJS := []byte(`Oe?"//fepassport.sjst.test.sankuai.com/portal/oauth-login?":"https://epassport.meituan.com/portal/oauth-login?"`)
	epGot := string(mtRewrite(epJS, "/app/web-me.meituan.com/"))
	if strings.Contains(epGot, `"https://epassport.meituan.com`) {
		t.Errorf("epassport 绝对地址字面量应改写为代理路径，得到 %s", epGot)
	}
	if !strings.Contains(epGot, `"/app/web-me.meituan.com/https/epassport.meituan.com/portal/oauth-login?"`) {
		t.Errorf("epassport 改写结果不符，得到 %s", epGot)
	}
	if r := mtRewrite([]byte("https://example.com/x"), "/app/a/"); string(r) != "https://example.com/x" {
		t.Errorf("无关字面量不应被改写，得到 %s", r)
	}
	for _, a := range matchAdapters("unknown.example") {
		if a.RewriteJS != nil {
			t.Errorf("未知域不应命中 RewriteJS，得到 %v", a.Name)
		}
	}

	// music163 RewriteJS：QA 兜底域改写为生产域（签名带 prefix，本用例不依赖它）
	var m163Rewrite func([]byte, string) []byte
	for _, a := range matchAdapters("st.music.163.com") {
		if a.Name == "music163-top-bridge" && a.RewriteJS != nil {
			m163Rewrite = a.RewriteJS
		}
	}
	if m163Rewrite == nil {
		t.Fatalf("st.music.163.com 应命中 music163-top-bridge 的 RewriteJS")
	}
	if r := string(m163Rewrite([]byte("base=//qa.igame.163.com/api"), "/app/music-163/")); r != "base=//music.163.com/api" {
		t.Errorf("music163 QA 域应改写为生产域，得到 %s", r)
	}
}

func names(as []*SiteAdapter) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.Name)
	}
	return out
}

// TestAPIForward 通用转发（/px/ 模式）：与统一引擎共用链路——会话 Cookie 剥离、
// Set-Cookie 收归 /px/ 前缀、剥嵌入封锁头；差异面——HTML 不重写不注入 hook。
func TestAPIForward(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/data":
			w.Header().Add("Set-Cookie", "sid=9; Domain=t.example; Path=/")
			w.Header().Set("X-Frame-Options", "DENY")
			_, _ = w.Write([]byte(`{"ok":1}`))
		case "/echo":
			fmt.Fprintf(w, "Cookie:%s|Auth:%s|Origin:%s", r.Header.Get("Cookie"), r.Header.Get("Authorization"), r.Header.Get("Origin"))
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><head></head><body><a href="/z">z</a></body></html>`))
		}
	}))
	defer upstream.Close()
	upURL := mustURL(t, upstream.URL)
	compact := upURL.Scheme + "/" + upURL.Host

	p := New(nil)
	// 通用转发无应用配置；测试上游是回环地址，经 cfg 放行拨号（SSRF 防线按
	// 配置目标主机豁免——生产 /px/ 无 cfg、回环一律拒绝，见下方 APIHandler
	// 直测的 SSRF 用例）
	pcfg := &proxyConfig{Kind: "proxy", URL: upstream.URL}
	do := func(rest string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/px/"+rest, nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		tu, err := decodeTarget(rest)
		if err != nil {
			t.Fatalf("decodeTarget(%q): %v", rest, err)
		}
		p.serveForward(w, r, tu, forwardOpts{prefix: "/px/", cfg: pcfg})
		return w
	}

	// JSON 接口：Set-Cookie 收归 /px/ 前缀 + 剥 Domain；剥嵌入封锁头
	w := do(compact+"/api/data", nil)
	if w.Code != 200 || w.Body.String() != `{"ok":1}` {
		t.Fatalf("/api/data code=%d body=%s", w.Code, w.Body.String())
	}
	if sc := w.Header().Get("Set-Cookie"); !strings.Contains(sc, "Path=/px/") || strings.Contains(sc, "Domain=") {
		t.Errorf("Set-Cookie 应提升到 /px/ 前缀且剥 Domain：%q", sc)
	}
	if v := w.Header().Get("X-Frame-Options"); v != "" {
		t.Errorf("X-Frame-Options 应被剥掉：%q", v)
	}

	// 会话剥离：OpenOS 会话不外泄、其余 Cookie 放行；Authorization 剥除；
	// Referer 兜底为目标站自身（同应用反代语义）
	w = do(compact+"/echo", map[string]string{
		"Cookie":         "openos_token=SECRET; sid=7",
		"Authorization":  "Bearer x",
	})
	if s := w.Body.String(); strings.Contains(s, "SECRET") || !strings.Contains(s, "sid=7") ||
		strings.Contains(s, "Auth:Bearer") || !strings.Contains(s, "Origin:") {
		t.Errorf("会话剥离/Referer 语义不符：%s", s)
	}

	// HTML 不重写不注入（通用转发面向接口，不是页面嵌入通道）
	w = do(compact+"/page", nil)
	if strings.Contains(w.Body.String(), "hook.js") || strings.Contains(w.Body.String(), "/px/"+compact+"/z") {
		t.Errorf("通用转发不应重写 HTML 或注入 hook：%s", w.Body.String())
	}

	// SSRF：无配置目标放行，回环一律拒绝
	w = do("https/localhost:1/x", nil)
	if w.Code != http.StatusBadGateway {
		t.Errorf("内网目标应 502，得到 %d", w.Code)
	}

	// 非法地址
	r := httptest.NewRequest("GET", "/px/", nil)
	w2 := httptest.NewRecorder()
	p.APIHandler(w2, r)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("空目标应 400，得到 %d", w2.Code)
	}
}

// TestRefererTargetHook 站点适配器的 Hook 注入：按 hook.js 请求的 Referer 解析
// 目标主机，命中适配器的文档拿到追加片段、未命中的只拿通用 hook。
func TestRefererTargetHook(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"http://h.example/app/music-163/https/music.163.com/discover", "music.163.com"},
		{"http://h.example/discover?__os_px=music-163~https~music.163.com", "music.163.com"},
		{"http://h.example/app/x/https/unknown.example/p", "unknown.example"},
		{"", ""},
		{"http://h.example/other", ""},
	}
	for _, c := range cases {
		if got, ok := refererTargetHost(c.ref); got != c.want || ok != (c.want != "") {
			t.Errorf("refererTargetHost(%q) = %q,%v，期望 %q", c.ref, got, ok, c.want)
		}
	}

	// music-163 域的 hook.js 响应应包含 top 桥接片段；未知域不包含
	r := httptest.NewRequest("GET", "/app/music-163/hook.js", nil)
	r.Header.Set("Referer", "http://h.example/app/music-163/https/music.163.com/discover")
	w := httptest.NewRecorder()
	serveHook(w, r, "music-163", nil)
	if !strings.Contains(w.Body.String(), "Object.defineProperty(T, k") {
		t.Errorf("music.163.com 的 hook 应包含 top 桥接片段")
	}
	r2 := httptest.NewRequest("GET", "/app/x/hook.js", nil)
	r2.Header.Set("Referer", "http://h.example/app/x/https/unknown.example/p")
	w2 := httptest.NewRecorder()
	serveHook(w2, r2, "x", nil)
	if strings.Contains(w2.Body.String(), "Object.defineProperty(T, k") {
		t.Errorf("未知站点的 hook 不应包含站点适配片段")
	}
	if !strings.Contains(w2.Body.String(), "__openosPx") {
		t.Errorf("通用 hook 主体缺失")
	}
	// __PREFIX__ 占位符全量替换（含适配片段）
	if strings.Contains(w.Body.String(), "__PREFIX__") || strings.Contains(w2.Body.String(), "__PREFIX__") {
		t.Errorf("占位符应被全量替换")
	}
}

// TestAppConfigHook 应用级注入脚本（config.hook）：随通用 hook 与适配器片段
// 之后追加、__PREFIX__ 占位符替换为应用前缀；超限置空不拒整个应用。
func TestAppConfigHook(t *testing.T) {
	cfg := parseProxyConfig([]byte(`{"kind":"proxy","url":"https://t.example/","hook":"window.__biz=1;fetch(__PREFIX__)"}`))
	if cfg == nil {
		t.Fatal("parseProxyConfig 应成功")
	}
	r := httptest.NewRequest("GET", "/app/mt/hook.js", nil)
	w := httptest.NewRecorder()
	serveHook(w, r, "mt", cfg)
	body := w.Body.String()
	if !strings.Contains(body, "window.__biz=1") {
		t.Errorf("应用级脚本应被追加进 hook.js 响应")
	}
	if !strings.Contains(body, `window.__biz=1;fetch(/app/mt/)`) {
		t.Errorf("__PREFIX__ 应替换为应用前缀，得到：%s", body[len(body)-80:])
	}
	if strings.Contains(body, "__PREFIX__") {
		t.Errorf("占位符应被全量替换")
	}

	// 与站点适配器共存：美团域命中适配器片段时应用级脚本仍在
	r2 := httptest.NewRequest("GET", "/app/mt/hook.js", nil)
	r2.Header.Set("Referer", "http://h.example/app/mt/https/ebooking.meituan.com/")
	w2 := httptest.NewRecorder()
	serveHook(w2, r2, "mt", cfg)
	b2 := w2.Body.String()
	if !strings.Contains(b2, "nocode.sankuai.com") || !strings.Contains(b2, "window.__biz=1") {
		t.Errorf("适配器片段与应用级脚本应共存")
	}
	// 应用级脚本在适配器片段之后（可依赖先就位的包装入口）
	if strings.Index(b2, "nocode.sankuai.com") > strings.Index(b2, "window.__biz=1") {
		t.Errorf("应用级脚本应追加在适配器片段之后")
	}

	// 无 hook 的应用不受影响（与旧行为一致）
	cfgNil := parseProxyConfig([]byte(`{"kind":"proxy","url":"https://t.example/"}`))
	w3 := httptest.NewRecorder()
	serveHook(w3, httptest.NewRequest("GET", "/app/x/hook.js", nil), "x", cfgNil)
	if strings.Contains(w3.Body.String(), "window.__biz") {
		t.Errorf("未配置 hook 的应用不应出现业务脚本")
	}

	// 超限置空：应用仍可用（代理语义不受影响），脚本不注入
	big := `{"kind":"proxy","url":"https://t.example/","hook":"` + strings.Repeat("x", maxAppHook+1) + `"}`
	cfgBig := parseProxyConfig([]byte(big))
	if cfgBig == nil || cfgBig.Hook != "" {
		t.Errorf("超限 hook 应置空且应用可用")
	}
}

func TestCookieReg(t *testing.T) {
	r := &cookieReg{m: map[string]map[string]struct{}{}}
	// 请求侧登记：Cookie 名 × 请求路径的逐级目录前缀（含应用根，不含 /）
	req, _ := http.NewRequest("GET", "/app/mei/https/e.example.com/settoken/callback?x=1", nil)
	req.AddCookie(&http.Cookie{Name: "eprt", Value: "t"})
	req.AddCookie(&http.Cookie{Name: "ssoid", Value: "1"})
	r.recordRequestCookies("mei", req)
	other, _ := http.NewRequest("GET", "/", nil) // 非本应用路径：不记
	r.recordRequestCookies("mei", other)
	// 响应侧登记：精确路径（Domain 票据 → 应用根）
	_, cp := rewriteSetCookie("mebsid=1; Domain=.meituan.com; Path=/", "/app/mei/", mustURL(t, "https://me.meituan.com/ebooking/home"), true)
	r.record("mei", "mebsid\t"+cp)
	hostReq, _ := http.NewRequest("GET", "/app/mei/x", nil) // 宿主会话 Cookie：排除登记
	hostReq.AddCookie(&http.Cookie{Name: "openos_token", Value: "x"})
	hostReq.AddCookie(&http.Cookie{Name: "hotel_refresh", Value: "x"})
	r.recordRequestCookies("mei", hostReq)
	w := httptest.NewRecorder()
	emitted, remain := r.expireBatch(w, "mei", 300)
	if emitted == 0 {
		t.Fatalf("应有登记条目")
	}
	if remain != 0 {
		t.Errorf("批内应清完 remain=%d", remain)
	}
	var got []string
	for _, c := range w.Result().Cookies() {
		if c.Value != "" || c.MaxAge != -1 {
			t.Errorf("过期头形态不对：%+v", c)
		}
		if c.Name == "openos_token" || c.Name == "hotel_refresh" {
			t.Errorf("宿主会话 Cookie 不应入清扫面：%s", c.Name)
		}
		got = append(got, c.Name+"@"+c.Path)
	}
	join := strings.Join(got, "\n")
	for _, want := range []string{
		"eprt@/app/mei/", "eprt@/app/mei/https/", "eprt@/app/mei/https/e.example.com/",
		"eprt@/app/mei/https/e.example.com/settoken/",
		"ssoid@/app/mei/https/e.example.com/settoken/",
		"mebsid@/app/mei/",
	} {
		if !strings.Contains(join, want) {
			t.Errorf("缺登记 %s（实得 %s）", want, join)
		}
	}
	if strings.Contains(join, "@/\n") || strings.HasSuffix(join, "@/") { // 根路径宿主 Cookie 不入册
		t.Errorf("不应登记根路径：%s", join)
	}
}
