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
	got := rewriteSetCookie(v, "/app/x/", target, false)
	if strings.Contains(got, "Domain=") || strings.Contains(got, "Secure") {
		t.Errorf("http 部署应剥 Domain/Secure：%s", got)
	}
	if !strings.Contains(got, "Path=/app/x/https/t.example/") || !strings.Contains(got, "SameSite=Lax") || !strings.Contains(got, "HttpOnly") {
		t.Errorf("Set-Cookie 改写不符合预期：%s", got)
	}
	got = rewriteSetCookie(v, "/app/x/", target, true)
	if !strings.Contains(got, "Secure") || !strings.Contains(got, "SameSite=None") {
		t.Errorf("https 部署应保留 Secure/SameSite=None：%s", got)
	}
	// 无 Path 属性：回落默认（按请求目录），不追加 Path
	if got := rewriteSetCookie("a=b", "/app/x/", target, true); got != "a=b" {
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
	if !strings.Contains(sc, "Path=/app/x/"+upCompact+"/") || strings.Contains(sc, "Domain=") {
		t.Errorf("Set-Cookie 应收归代理路径且剥 Domain：%q", sc)
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

	// 重复 Content-Type：只透传首个（微信 jslogin 实测上游 bug，末值会让
	// Chrome 严格 MIME 检查拒绝执行 JSONP）
	w = do(upCompact+"/double-ct", nil)
	if cts := w.Header().Values("Content-Type"); len(cts) != 1 || cts[0] != "text/javascript" {
		t.Errorf("重复 Content-Type 应只透传首个，得到 %v", cts)
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
