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
// 文件布局（按职责划分）：
//   - target.go   紧凑目标地址编解码（/app/<id>/ 之后的地址空间）
//   - config.go   代理配置解析与自定义请求头清洗
//   - guard.go    SSRF 拨号防线与宿主自身地址判定
//   - catch.go    逃逸回捕（CatchEscape）与 hook 还原标记
//   - request.go  转发请求的清洗与改写（Referer/Cookie/OAuth 回跳参数）
//   - response.go 转发响应的头改写（Set-Cookie/Location/Refresh/Referrer-Policy）
//   - forward.go  HTTP 转发主链路（读入、解压、重写、透传）
//   - ws.go       WebSocket 泵转
//   - hook.go     注入脚本下发
//   - rewrite.go  HTML/CSS 地址重写（字节级标签扫描）
//
// 安全边界：
//   - 代理目标只来自管理员在应用注册中的配置（kind=proxy），不接受请求方传 URL；
//     配置目标本身允许内网地址（管理员的显式选择）
//   - 目标以外的主机（子资源/重定向/OAuth 跳转）只允许公网地址，内网一律拒绝（防 SSRF）
//   - 转发前剥离 OpenOS 自身会话（openos_token 等 Cookie 与 Authorization 头）
//   - 经代理执行的第三方 JS 与桌面同源，仅应配置可信应用（信任模型与 goder 内嵌一致）
package webproxy

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"openos/server/internal/auth"
	"openos/server/internal/store"
)

// Proxy 应用代理服务。挂载方式：mux.Handle("/app/{id}/", authSvc.Middleware(px.Handler))
type Proxy struct {
	db     *store.DB
	client *http.Client
}

// New 创建应用代理。db 用于逐请求校验应用存在、代理配置与角色授权。
func New(db *store.DB) *Proxy {
	tr := &http.Transport{
		// SSRF 防线在拨号层生效（而非请求解析层）：域名单次解析校验后按 IP 直连
		//（堵 DNS rebinding TOCTOU，见 guard.go guardedDial），配置目标主机除外。
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			cfgHost, _ := ctx.Value(cfgHostCtx{}).(string)
			return guardedDial(ctx, network, addr, cfgHost)
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
	case rest == hookPath || strings.HasPrefix(rest, hookPath+"?"):
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
