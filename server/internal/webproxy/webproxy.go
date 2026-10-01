// Package webproxy 实现网页应用的「应用代理」：把 config.kind=proxy 的目标站点
// 经服务端反代映射到 /app/<id>/<目标URL> 路径下，剥掉 X-Frame-Options / CSP
// frame-ancestors 等拒绝嵌入的响应头，并重写 HTML/CSS 里的资源地址与
// Set-Cookie / Location，使原本禁止 iframe 嵌入的页面可以在 OpenOS 窗体内打开。
//
// 三层地址覆盖（覆盖 JS 动态构造的 URL）：
//  1. 服务端重写 HTML/CSS 中的静态资源地址（含根绝对路径，这是 <base> 注入做不到的）
//  2. 注入 hook.js 包装 fetch/XHR/WebSocket/window.open/history/URL 属性等动态构造入口
//  3. 相对地址天然按 /app/<id>/<目标URL> 前缀解析，JS 动态拼接的相对路径无需拦截即命中
//
// 安全边界：
//   - 代理目标只来自管理员在应用注册中的配置（kind=proxy），不接受请求方传 URL；
//     配置目标本身允许内网地址（管理员的显式选择）
//   - 目标以外的主机（子资源/重定向/OAuth 跳转）只允许公网地址，内网一律拒绝（防 SSRF）
//   - 转发前剥离 OpenOS 自身会话（openos_token 等 Cookie 与 Authorization 头）
//   - 经代理执行的第三方 JS 与桌面同源，仅应配置可信应用（信任模型与 goder 内嵌一致）
package webproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"openos/server/internal/auth"
	"openos/server/internal/store"

	"github.com/gorilla/websocket"
)

//go:embed hook.js
var hookJS []byte

const (
	// maxRewriteBody 需要重写（HTML/CSS）的响应体上限，超出按原文透传
	maxRewriteBody = 16 << 20
	hookPath       = "hook.js"
)

// cfgHostCtx 携带管理员配置的目标主机名到拨号层（SSRF 防线按主机名放行配置目标）。
type cfgHostCtx struct{}

// Proxy 应用代理服务。挂载方式：mux.Handle("/app/{id}/", authSvc.Middleware(px.Handler))
type Proxy struct {
	db     *store.DB
	client *http.Client
}

// New 创建应用代理。db 用于逐请求校验应用存在、代理配置与角色授权。
func New(db *store.DB) *Proxy {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	tr := &http.Transport{
		// SSRF 防线在拨号层生效（而非请求解析层）：即使目标经 DNS 解析或
		// 跳转落到内网地址也会被拒绝，配置目标主机除外。
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			cfgHost, _ := ctx.Value(cfgHostCtx{}).(string)
			if host, ok := guardAddr(cfgHost, addr); !ok {
				return nil, fmt.Errorf("webproxy: 拒绝拨号内网地址 %s（非管理员配置目标）", host)
			}
			return dialer.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Proxy{db: db, client: &http.Client{
		Transport: tr,
		// 不自动跟随重定向：30x 的 Location 重写后交还浏览器，保持地址语义
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// CatchEscape 把「从代理页面逃逸到本源路径」的请求 307 回该应用的代理路径。
// 覆盖 hook 无法包装的跳转（location.href='/x' / location.assign 等对 location
// 的直接赋值，浏览器不允许重定义）。这类跳转的目标是目标站的根绝对路径，而
// 目标站用 /api、/assets 等本源常见前缀极普遍（典型：SSO 登录 location.href=
// '/api/auth/sso/login' 会撞上 OpenOS 自身的 auth 路由 404，路径恰巧重合时甚至
// 会误调 OpenOS 接口），落到 SPA 兜底则把桌面 index.html 套娃进代理 iframe。
// 因此须挂在整棵路由树的外层（而非仅 SPA 兜底"/"路由外层）。判定依据 Referer：
// 同源且指向 /app/<id>/<scheme>/<host>/… 的请求，把当前 请求路径+查询 拼回该
// 目标源下。307 保留方法与请求体，表单 POST 逃逸同样能回去。
func CatchEscape(db *store.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if loc := escapeTarget(db, r); loc != "" {
			w.Header().Set("Location", loc)
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// escapeTarget 解析逃逸目标；空串表示非逃逸请求（无 Referer / Referer 不属于
// 代理页面 / 代理自身命名空间等情形，一律放行原处理）。POST 表单逃逸
// 同样 307 回去（保留方法与请求体）。
func escapeTarget(db *store.DB, r *http.Request) string {
	p := r.URL.EscapedPath()
	// 代理自身命名空间放行：重写后的子资源、Location 跟随、hook.js 请求都落在
	// /app/ 下，回捕会造成代理前缀自我叠加；WS 端点放行：握手不跟随重定向。
	// 其余本源路径（含 /api/* 等真实路由）对代理页面而言都是逃逸，一律回捕。
	if strings.HasPrefix(p, "/app/") || p == "/ws" {
		return ""
	}
	ref := r.Referer()
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	id, scheme, host, ok := refererOrigin(db, u)
	if !ok {
		return ""
	}
	loc := "/app/" + id + "/" + scheme + "/" + host + p
	if q := stripPxMark(r.URL.RawQuery); q != "" {
		loc += "?" + q
	}
	return loc
}

// refererOrigin 从代理页面的 Referer 中解析出应用 id 与目标源。两种形态：
//  1. 路径携带代理前缀：/app/<id>/<scheme>/<host>/…
//  2. hook 还原模式（SPA 客户端路由要求文档地址还原为目标站形态，见 hook.js）：
//     查询串携带 __os_px=<id>~<scheme>~<host[:port]> 标记
//
// 不校验 Referer 与请求 Host 一致：开发模式（vite 代理）与宿主网关下 Host 会被
// changeOrigin/反代改写，同源判定必然误伤回捕。安全性不依赖该校验——伪造该
// Referer 也无增益：回捕目标仍是本源 /app/ 路由（同受登录态与角色校验），且
// 请求本可直接发起。
func refererOrigin(db *store.DB, u *url.URL) (id, scheme, host string, ok bool) {
	if strings.HasPrefix(u.Path, "/app/") {
		var tail, hostPath string
		id, tail, _ = strings.Cut(strings.TrimPrefix(u.Path, "/app/"), "/")
		if !validAppID(id) || tail == "" {
			return "", "", "", false
		}
		scheme, hostPath, _ = strings.Cut(tail, "/")
		host, _, _ = strings.Cut(hostPath, "/")
	} else {
		parts := strings.Split(u.Query().Get("__os_px"), "~")
		if len(parts) != 3 {
			return "", "", "", false
		}
		id, scheme, host = parts[0], strings.ToLower(parts[1]), parts[2]
		// 标记值经 URL 解码而来，host 段须为纯地址字符，防注入回捕地址
		if host == "" || strings.ContainsAny(host, "/?#&@%") {
			return "", "", "", false
		}
	}
	scheme = strings.ToLower(scheme)
	if (scheme != "http" && scheme != "https") || host == "" {
		return "", "", "", false
	}
	app, found := db.AppByID(id)
	if !found || parseProxyConfig(app.Config) == nil {
		return "", "", "", false
	}
	return id, scheme, host, true
}

// stripPxMark 从查询串剥掉 hook 还原标记（回捕目标与上游转发都不需要它）。
// 无标记时原样返回（不重编码，保持 OAuth 等对 query 形态敏感的流程不受扰动）。
func stripPxMark(q string) string {
	if q == "" || !strings.Contains(q, "__os_px") {
		return q
	}
	vals, err := url.ParseQuery(q)
	if err != nil {
		return q
	}
	vals.Del("__os_px")
	return vals.Encode()
}

// rewriteReferer 把代理页面的 Referer 还原为目标站自身的绝对地址，两种形态：
//  1. 路径携带代理前缀：/app/<id>/<scheme>/<host>/…
//  2. hook 还原模式（SPA 客户端路由）：查询串携带 __os_px=<id>~<scheme>~<host> 标记，
//     pathname 即目标站路径
//
// 无法识别（非本应用代理页面）返回空串，调用方按不发 Referer 处理。
func rewriteReferer(ref, id string) string {
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(u.Path, "/app/"+id+"/") {
		tu, err := decodeTarget(strings.TrimPrefix(u.Path, "/app/"+id+"/"))
		if err != nil {
			return ""
		}
		tu.RawQuery = stripPxMark(u.RawQuery)
		return tu.String()
	}
	parts := strings.Split(u.Query().Get("__os_px"), "~")
	if len(parts) == 3 && parts[0] == id && (parts[1] == "http" || parts[1] == "https") && parts[2] != "" {
		vals := u.Query()
		vals.Del("__os_px")
		return (&url.URL{Scheme: parts[1], Host: parts[2], Path: u.Path, RawQuery: vals.Encode()}).String()
	}
	return ""
}

// restorePxParam 把转发上游的 query 中 OAuth 回跳参数（redirect_uri/redirectUri）
// 还原为目标站注册的原始绝对地址，两种命中形态：
//  1. 代理路径值（/app/<id>/…）：hook 会把文档地址里该参数改写为代理路径（防 IdP
//     前端解析参数、拼 code 后 location 赋值跳转时跨源逃逸出代理），上游（Casdoor
//     等）校验 redirect_uri 白名单需要注册的原始值；
//  2. 宿主自身绝对地址（http(s)://<宿主>/…）：目标站 JS 读 location.origin 拼接的
//     回跳（代理还原模式下 origin 是 OpenOS 宿主），还原为管理员配置的目标域——
//     微信 jslogin 即此形态，注册成宿主地址的回跳会被服务端风控拒绝（扫码后
//     webwxnewloginpage 返回「暂不支持使用网页版微信」）。
//
// 无命中时 query 原样返回。注意预检不能按明文前缀匹配 query——参数值经
// encodeURIComponent 编码后是 %2Fapp%2F<id>%2F 形态，判断须在解码后进行。
func restorePxParam(id, q string, cfg *proxyConfig, selfHost string) string {
	if q == "" || !strings.Contains(q, "redirect") {
		return q
	}
	vals, err := url.ParseQuery(q)
	if err != nil {
		return q
	}
	cfgURL, cfgErr := url.Parse(cfg.URL)
	changed := false
	for _, k := range []string{"redirect_uri", "redirectUri"} {
		v := vals.Get(k)
		if v == "" {
			continue
		}
		if strings.HasPrefix(v, "/app/"+id+"/") {
			if tu, err := decodeTarget(strings.TrimPrefix(v, "/app/"+id+"/")); err == nil {
				vals.Set(k, tu.String())
				changed = true
			}
			continue
		}
		if u, err := url.Parse(v); err == nil && u.Host != "" && cfgErr == nil && isSelfHost(u.Host, selfHost) {
			u.Scheme, u.Host = cfgURL.Scheme, cfgURL.Host
			vals.Set(k, u.String())
			changed = true
		}
	}
	if !changed {
		return q
	}
	return vals.Encode()
}

// guardAddr 校验拨号地址：管理员配置的目标主机直接放行（可为内网，这是显式选择）；
// 其余主机解析后逐 IP 检查，回环/内网/链路本地/组播/未指定地址一律拒绝。
func guardAddr(cfgHost, addr string) (string, bool) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if cfgHost != "" && strings.EqualFold(host, cfgHost) {
		return host, true
	}
	if ip := net.ParseIP(host); ip != nil {
		return host, isPublicIP(ip)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return host, false
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return host, false
		}
	}
	return host, true
}

func isPublicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified())
}

// isSelfHost 判断地址 host 是否 OpenOS 宿主自身：与请求 Host 头相等，或同为回环
// 形态且端口一致（localhost ↔ 127.0.0.1 ↔ [::1]——页面读到的 origin 与请求 Host
// 的字符串形态可能不一致；端口不同则是别的内网服务，不是宿主）。目标站 JS 在
// 代理页里读 location.origin 拼自我引用（登录回调/continueUrl 等）时拿到的就是
// 宿主地址。
func isSelfHost(host, selfHost string) bool {
	if selfHost == "" || host == "" {
		return false
	}
	if strings.EqualFold(host, selfHost) {
		return true
	}
	h1, p1, err1 := net.SplitHostPort(host)
	h2, p2, err2 := net.SplitHostPort(selfHost)
	if err1 != nil || err2 != nil || p1 != p2 {
		return false
	}
	return isLoopbackHost(strings.ToLower(h1)) && isLoopbackHost(strings.ToLower(h2))
}

func isLoopbackHost(h string) bool {
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "[::1]" || h == "0.0.0.0"
}

// mapSelfTarget 把「宿主自身绝对地址」的代理目标重映射为配置目标域（保留路径与
// 查询），返回紧凑代理路径段；非宿主地址、或配置目标本身就是宿主（管理员显式把
// OpenOS 自身当代理目标）时返回空串（保持原语义，交给拨号层按配置目标放行）。
func mapSelfTarget(tu *url.URL, cfg *proxyConfig, selfHost string) string {
	if !isSelfHost(tu.Host, selfHost) {
		return ""
	}
	cu, err := url.Parse(cfg.URL)
	if err != nil || isSelfHost(cu.Host, selfHost) {
		return ""
	}
	return compactTarget(&url.URL{
		Scheme: cu.Scheme, Host: cu.Host,
		Path: tu.Path, RawPath: tu.RawPath, RawQuery: tu.RawQuery,
	})
}

// Handler 处理 /app/{id}/... 全部子路径（须在 authSvc.Middleware 内挂载，
// 同源 iframe 的请求经 openos_token Cookie 自动携带登录态）。
func (p *Proxy) Handler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validAppID(id) {
		http.NotFound(w, r)
		return
	}
	app, ok := p.db.AppByID(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	cfg := parseProxyConfig(app.Config)
	if cfg == nil {
		http.Error(w, "该应用未配置为代理模式（config.kind=proxy）", http.StatusNotFound)
		return
	}
	// 访问权与 /api/apps 的可见性同源：角色必须在授权矩阵（role_apps）中
	ident := auth.FromContext(r)
	if ident == nil || !p.db.RoleCanAccessApp(ident.Role, id) {
		http.Error(w, "无权访问该应用", http.StatusForbidden)
		return
	}
	// 用 EscapedPath 重建目标原始地址（保留 %XX 编码），查询串拼回
	rest := strings.TrimPrefix(r.URL.EscapedPath(), "/app/"+id+"/")
	if r.URL.RawQuery != "" {
		rest += "?" + r.URL.RawQuery
	}
	switch {
	case rest == "" || rest == "/":
		// 入口：重定向到配置目标（紧凑形态），窗体 iframe src 固定为 /app/<id>/
		// 手写 Location 而不用 http.Redirect——后者会清洗路径、破坏 scheme/host 段
		cfgURL, _ := url.Parse(cfg.URL)
		w.Header().Set("Location", "/app/"+id+"/"+compactTarget(cfgURL))
		w.WriteHeader(http.StatusFound)
	case rest == hookPath:
		serveHook(w, id)
	case strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://"):
		// 宽容：直接携带 :// 的旧式地址压回紧凑形态
		w.Header().Set("Location", "/app/"+id+"/"+strings.Replace(rest, "://", "/", 1))
		w.WriteHeader(http.StatusFound)
	default:
		tu, err := decodeTarget(rest)
		if err != nil {
			http.Error(w, "代理目标地址不合法："+err.Error(), http.StatusBadRequest)
			return
		}
		// 宿主自身地址兜底：目标站（或其登录链）在代理页里读 location.origin 拼出
		// 的自我引用绝对地址（还原模式只改 pathname，origin 恒为 OpenOS 宿主），
		// 经 Location 重写/hook 前缀化后以宿主为拨号目标进入代理——SSRF 防线会
		// 拒绝，语义上这些路径也属于目标站而非 OpenOS 路由。302 到重映射地址
		// （配置目标域 + 原路径查询），让后续链路建立在正确地址上
		if mapped := mapSelfTarget(tu, cfg, r.Host); mapped != "" {
			w.Header().Set("Location", "/app/"+id+"/"+mapped)
			w.WriteHeader(http.StatusFound)
			return
		}
		if tu.Scheme == "ws" || tu.Scheme == "wss" {
			p.serveWS(w, r, tu, cfg)
		} else {
			p.serveHTTPProxy(w, r, id, tu, cfg)
		}
	}
}

// compactTarget 把绝对目标地址映射为代理路径段：https://h:8443/a/b?q →
// https/h:8443/a/b?q。用 <scheme>/<host> 而非 scheme://host，避免路径中的 //
// 被 Go ServeMux 的 cleanPath 折叠（其 307 清洗重定向会破坏目标地址）。
func compactTarget(u *url.URL) string {
	host := u.Host
	if host == "" {
		host = u.Hostname()
	}
	out := u.Scheme + "/" + host + u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

// decodeTarget 解析 /app/<id>/ 之后的紧凑目标地址（<scheme>/<host>[/路径][?查询]），
// 重建为真实 URL 交给 url.Parse（%XX 与查询串由其正确处理）。
func decodeTarget(rest string) (*url.URL, error) {
	scheme, tail, ok := strings.Cut(rest, "/")
	if !ok || tail == "" {
		return nil, fmt.Errorf("缺少 host 段")
	}
	scheme = strings.ToLower(scheme)
	switch scheme {
	case "http", "https", "ws", "wss":
	default:
		return nil, fmt.Errorf("不支持的目标协议 %q", scheme)
	}
	if !strings.Contains(tail, "/") {
		tail += "/"
	}
	u, err := url.Parse(scheme + "://" + tail)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("目标地址无法解析")
	}
	return u, nil
}

func cfgURLHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func validAppID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// proxyConfig 应用 config 的代理形态：{"kind":"proxy","url":"https://目标/",
// "headers":{"X-Api-Key":"…"}}。headers 为管理员配置的自定义请求头，随代理
// 转发附加到上游请求（含 WebSocket 握手），常用于内网系统的固定认证头。
type proxyConfig struct {
	Kind    string            `json:"kind"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// bannedCfgHeaders 配置请求头黑名单：逐跳与传输控制类头由代理自身管理，
// 允许配置会破坏连接语义（Content-Length/Transfer-Encoding 还构成请求走私）。
var bannedCfgHeaders = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
	"content-length":      true,
	"host":                true,
}

// isTokenChar RFC 7230 token 字符（header 名的合法字符集）。
func isTokenChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// sanitizeCfgHeaders 清洗配置请求头：丢弃黑名单头、非法名（空/含 token 外字符）
// 与含 CR/LF/NUL 的值（防头部注入）。配置虽源自管理员，此处仍做纵深防御。
func sanitizeCfgHeaders(in map[string]string) map[string]string {
	var out map[string]string
	for k, v := range in {
		if bannedCfgHeaders[strings.ToLower(k)] {
			continue
		}
		if k == "" || strings.ContainsAny(v, "\r\n\x00") {
			continue
		}
		ok := len(k) > 0
		for i := 0; i < len(k); i++ {
			if !isTokenChar(k[i]) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(in))
		}
		out[k] = v
	}
	return out
}

// dialHeader 配置头转为 WebSocket 握手请求头（HTTP 反代走 applyCfgHeaders，
// WS 拨号是服务端独立发起、无浏览器会话可言，直接平铺即可）。
func (c *proxyConfig) dialHeader() http.Header {
	h := make(http.Header, len(c.Headers))
	for k, v := range c.Headers {
		h.Set(k, v)
	}
	return h
}

func parseProxyConfig(raw json.RawMessage) *proxyConfig {
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' { // 历史双重编码形态兼容（与前端 parseCfg 同规则）
		var s string
		if json.Unmarshal(raw, &s) == nil {
			raw = []byte(s)
		}
	}
	var c proxyConfig
	if json.Unmarshal(raw, &c) != nil || c.Kind != "proxy" {
		return nil
	}
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil
	}
	c.Headers = sanitizeCfgHeaders(c.Headers)
	return &c
}

func serveHook(w http.ResponseWriter, id string) {
	// 全量替换：源文件注释与代码中均出现占位符，只换首个会漏掉 PREFIX 赋值
	body := bytes.ReplaceAll(hookJS, []byte("__PREFIX__"), []byte("/app/"+id+"/"))
	h := w.Header()
	h.Set("Content-Type", "application/javascript; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// ---- HTTP 反代 ----

// stripResp 透传响应时剥掉的响应头：嵌入封锁类（本功能的目标）与逐跳/长度类（重算）。
var stripResp = map[string]bool{
	"x-frame-options":                     true,
	"content-security-policy":             true,
	"content-security-policy-report-only": true,
	"cross-origin-opener-policy":          true,
	"cross-origin-embedder-policy":        true,
	"cross-origin-resource-policy":        true,
	"strict-transport-security":           true,
	"referrer-policy":                     true, // 由本包统一改写为 same-origin，见 copyRespHeaders 末尾
	"content-length":                      true,
	"transfer-encoding":                   true,
	"connection":                          true,
	"keep-alive":                          true,
}

// hopReq 转发请求时剥掉的逐跳/会话头。
var hopReq = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	"Authorization", "Referer", "Origin",
}

func (p *Proxy) serveHTTPProxy(w http.ResponseWriter, r *http.Request, id string, tu *url.URL, cfg *proxyConfig) {
	cfgHost := cfgURLHost(cfg.URL)
	outReq := r.Clone(context.WithValue(r.Context(), cfgHostCtx{}, cfgHost))
	outReq.URL = tu
	outReq.Host = tu.Host
	outReq.RequestURI = "" // client 发出的请求必须清空
	sanitizeOutRequest(outReq, r, id, cfg)
	// 配置头在剥除 OpenOS 会话/逐跳头之后应用：优先级最高（可覆盖 X-Forwarded-*），
	// 管理员显式写死的目标站凭据（如 Authorization）是本功能的主要用例
	applyCfgHeaders(outReq.Header, cfg.Headers)

	resp, err := p.client.Do(outReq)
	if err != nil {
		http.Error(w, "代理请求失败："+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	secure := r.TLS != nil
	copyRespHeaders(w, resp, "/app/"+id+"/", tu, secure, r.Host)

	ctype := strings.ToLower(resp.Header.Get("Content-Type"))
	isHTML := strings.Contains(ctype, "text/html") || strings.Contains(ctype, "application/xhtml")
	isCSS := !isHTML && strings.Contains(ctype, "text/css")
	if !isHTML && !isCSS {
		w.WriteHeader(resp.StatusCode)
		streamCopy(w, resp.Body)
		return
	}

	// 文本响应：限长读入，gzip 解压后改写地址
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRewriteBody+1))
	if err != nil {
		http.Error(w, "读取上游响应失败", http.StatusBadGateway)
		return
	}
	if enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))); enc == "gzip" {
		if zr, zerr := gzip.NewReader(bytes.NewReader(body)); zerr == nil {
			if ub, uerr := io.ReadAll(zr); uerr == nil {
				body = ub
				w.Header().Del("Content-Encoding")
			}
		}
	}
	if len(body) <= maxRewriteBody {
		if isHTML {
			body = rewriteHTML(body, "/app/"+id+"/", tu)
		} else {
			body = rewriteCSS(body, "/app/"+id+"/", tu)
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// streamCopy 流式透传并逐块冲刷（SSE/大文件/视频等需要即时下发）。
func streamCopy(w http.ResponseWriter, src io.Reader) {
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if rerr != nil {
			return
		}
	}
}

func sanitizeOutRequest(out, orig *http.Request, id string, cfg *proxyConfig) {
	h := out.Header
	for _, k := range hopReq {
		h.Del(k)
	}
	// 只接受 gzip：其余压缩格式（br/zstd）无法在纯 Go 侧解压做地址重写
	h.Set("Accept-Encoding", "gzip")
	// Referer 还原为目标站自身的绝对地址（替代此前的整头剥除）：目标站的敏感
	// 接口常校验 Referer 白名单——微信扫码后的 webwxnewloginpage（发凭证）在无
	// Referer 时被风控拒绝，返回「暂不支持使用网页版微信」提示页。还原值是目标
	// 站自己的地址，不泄露 OpenOS 宿主信息；无法识别的 Referer 仍不发送。
	if ref := orig.Header.Get("Referer"); ref != "" {
		if rr := rewriteReferer(ref, id); rr != "" {
			h.Set("Referer", rr)
		}
	}
	// Sec-Fetch-* 描述浏览器视角的源关系（代理后基准源是 OpenOS 宿主，到达目标站
	// 语义全失真——如跨域跳转显示成 same-origin），整组剥除，服务器按无该头处理
	for k := range h {
		if strings.HasPrefix(k, "Sec-Fetch-") {
			h.Del(k)
		}
	}
	// 剥掉 hook 还原标记：它只服务于文档地址与逃逸回捕，不应到达目标站；
	// 并把回跳参数（redirect_uri，含 hook 改写的代理路径与 JS 拼 host 的宿主
	// 绝对地址两种形态）还原为目标站注册的原始值——白名单/风控校验依赖原始值
	out.URL.RawQuery = restorePxParam(id, stripPxMark(out.URL.RawQuery), cfg, orig.Host)
	// Cookie 过滤：绝不把 OpenOS 会话带给目标站；其余（落在代理路径下的目标会话）放行
	if c := orig.Header.Get("Cookie"); c != "" {
		if kept := keepCookies(c); kept != "" {
			h.Set("Cookie", kept)
		} else {
			h.Del("Cookie")
		}
	}
	if host, _, err := net.SplitHostPort(orig.RemoteAddr); err == nil {
		if xff := h.Get("X-Forwarded-For"); xff != "" {
			h.Set("X-Forwarded-For", xff+", "+host)
		} else {
			h.Set("X-Forwarded-For", host)
		}
	}
	if orig.TLS != nil {
		h.Set("X-Forwarded-Proto", "https")
	} else {
		h.Set("X-Forwarded-Proto", "http")
	}
}

// applyCfgHeaders 把管理员配置的自定义请求头应用到上游请求（在 sanitize 之后，
// 配置优先级最高）；Cookie 特殊处理为合并——配置值在前、保留的目标站会话在后，
// 避免固定认证 Cookie 顶掉用户在目标站的登录态。
func applyCfgHeaders(h http.Header, cfg map[string]string) {
	for k, v := range cfg {
		if strings.EqualFold(k, "Cookie") {
			if old := h.Get("Cookie"); old != "" {
				h.Set("Cookie", v+"; "+old)
				continue
			}
		}
		h.Set(k, v)
	}
}

// keepCookies 剥掉 OpenOS 自身会话 Cookie（openos_token 与 openos_* 系列）。
func keepCookies(cookieHeader string) string {
	var kept []string
	for _, part := range strings.Split(cookieHeader, ";") {
		name := strings.TrimSpace(strings.SplitN(part, "=", 2)[0])
		if name == "" || name == "openos_token" || strings.HasPrefix(name, "openos_") {
			continue
		}
		kept = append(kept, strings.TrimSpace(part))
	}
	return strings.Join(kept, "; ")
}

func copyRespHeaders(w http.ResponseWriter, resp *http.Response, prefix string, target *url.URL, secure bool, selfHost string) {
	h := w.Header()
	for k, vs := range resp.Header {
		lk := strings.ToLower(k)
		if stripResp[lk] {
			continue
		}
		switch lk {
		case "set-cookie":
			for _, v := range vs {
				h.Add("Set-Cookie", rewriteSetCookie(v, prefix, target, secure))
			}
			continue
		case "location":
			for _, v := range vs {
				h.Add(k, rewriteLocation(v, prefix, target, selfHost))
			}
			continue
		case "refresh":
			for _, v := range vs {
				h.Add(k, rewriteRefresh(v, prefix, target, selfHost))
			}
			continue
		case "content-type":
			// 单值语义，只透传首个（与 Header.Get、服务端 HTML/CSS 判定一致）。
			// 上游偶发重复下发（微信 jslogin 同时给 text/javascript 与
			// text/html 两个值）：浏览器按末值解析会让 <script> 的严格 MIME
			// 检查（配 nosniff）拒绝执行 JSONP——实测 Chrome 拿不到 uuid、
			// 二维码不渲染
			if len(vs) > 0 {
				h.Add(k, vs[0])
			}
			continue
		}
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	// Referrer-Policy=same-origin：同源请求携带完整 Referer——这是逃逸回捕
	// （CatchEscape 按 Referer 把 location.href='/x' 类跳转 307 回代理路径）的
	// 判定依据；跨源不发送（转发上游前本就剥 Referer，不外泄）。覆盖全局
	// 中间件的 no-referrer（其会让代理页面的所有导航都不带 Referer）
	h.Set("Referrer-Policy", "same-origin")
}

// rewriteSetCookie 把目标站的 Cookie 收归代理路径下：
// 剥 Domain（落回 OpenOS 源）、Path 改写到 /app/<id>/<目标绝对地址> 前缀（缺省
// Path 本就按请求目录作用域，无需改写）、http 部署下剥 Secure 并把 SameSite=None
// 降级为 Lax（None 依赖 Secure，缺失时浏览器直接丢弃）。
func rewriteSetCookie(v, prefix string, target *url.URL, secure bool) string {
	parts := strings.Split(v, ";")
	var b strings.Builder
	b.WriteString(parts[0])
	for _, p := range parts[1:] {
		t := strings.TrimSpace(p)
		lower := strings.ToLower(t)
		switch {
		case strings.HasPrefix(lower, "domain="): // 剥 Domain
		case strings.HasPrefix(lower, "path="):
			pv := t[len("path="):]
			if strings.HasPrefix(pv, "/") {
				b.WriteString("; Path=" + prefix + target.Scheme + "/" + target.Host + pv)
			}
			// 非法 path 丢弃属性，回落按请求目录的默认作用域
		case lower == "secure" && !secure:
		case strings.HasPrefix(lower, "samesite=none") && !secure:
			b.WriteString("; SameSite=Lax")
		default:
			b.WriteString("; " + t)
		}
	}
	return b.String()
}

func rewriteLocation(v, prefix string, target *url.URL, selfHost string) string {
	u, err := target.Parse(strings.TrimSpace(v))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return v
	}
	u.Fragment = ""
	// 宿主自身绝对地址：目标站 JS 读 location.origin 拼出的自我引用。挂回当前
	// 文档的目标域而非前缀化宿主地址（后者会让代理拨号自身、触发 SSRF 防线）
	if isSelfHost(u.Host, selfHost) {
		u.Scheme, u.Host = target.Scheme, target.Host
	}
	return prefix + compactTarget(u)
}

// rewriteRefresh 处理 Refresh 响应头的 "N; url=X" 形态。
func rewriteRefresh(v, prefix string, target *url.URL, selfHost string) string {
	i := strings.Index(strings.ToLower(v), "url=")
	if i < 0 {
		return v
	}
	head, tail := v[:i+4], strings.TrimSpace(v[i+4:])
	if tail != "" && (tail[0] == '"' || tail[0] == '\'') {
		q := tail[0]
		inner := strings.Trim(tail, string(q))
		return head + string(q) + rewriteLocation(inner, prefix, target, selfHost) + string(q)
	}
	return head + rewriteLocation(tail, prefix, target, selfHost)
}

// ---- WebSocket 反代 ----

func (p *Proxy) serveWS(w http.ResponseWriter, r *http.Request, tu *url.URL, cfg *proxyConfig) {
	cfgHost := cfgURLHost(cfg.URL)
	tu.RawQuery = stripPxMark(tu.RawQuery) // 还原标记不外带
	dialURL := &url.URL{Scheme: tu.Scheme, Host: tu.Host, Path: tu.Path, RawPath: tu.RawPath, RawQuery: tu.RawQuery}
	dialer := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if host, ok := guardAddr(cfgHost, addr); !ok {
				return nil, fmt.Errorf("webproxy: 拒绝拨号内网地址 %s（非管理员配置目标）", host)
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	// 握手同样带上管理员配置的自定义请求头（内网 WS 网关常按头鉴权）
	upconn, _, err := dialer.Dial(dialURL.String(), cfg.dialHeader())
	if err != nil {
		http.Error(w, "上游 WebSocket 连接失败："+err.Error(), http.StatusBadGateway)
		return
	}
	defer upconn.Close()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }} // 登录态已由 Cookie 中间件校验
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	done := make(chan struct{}, 1)
	go func() { wsPump(upconn, conn); done <- struct{}{} }()
	wsPump(conn, upconn)
	<-done
}

// wsPump 单向泵消息（控制帧由 gorilla 各自应答，跨连接透传数据帧即可）。
func wsPump(dst, src *websocket.Conn) {
	for {
		t, data, err := src.ReadMessage()
		if err != nil {
			return
		}
		if err := dst.WriteMessage(t, data); err != nil {
			return
		}
	}
}
