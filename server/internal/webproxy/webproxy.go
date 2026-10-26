// Package webproxy 实现统一代理：把外部站点/接口经服务端转发到 OpenOS 同源路径下，
// 解决浏览器的跨域（CORS）与 iframe 嵌套（X-Frame-Options / CSP frame-ancestors）
// 两类限制。两种入口形态共用同一套转发引擎（engine.go）：
//
//	/app/<id>/<scheme>/<host>/<路径>   应用反代（config.kind=proxy 的网页应用）
//	/px/<scheme>/<host>/<路径>          通用转发（网络请求类应用解决 CORS 的通道）
//
// 应用反代面向「整页嵌入」：剥掉拒绝嵌入的响应头、重写 HTML/CSS 里的资源地址、
// 注入 hook.js 包装 JS 动态构造 URL 的入口，使原本禁止 iframe 嵌入的页面可以在
// OpenOS 窗体内打开。通用转发面向「接口请求」：纯透传（无重写无注入），供
// http-client / 声明式应用 dataSource 等网络请求默认走代理发送，目标接口无需
// 放行跨域；不重写不注入也让它不适合当页面嵌入通道（地址不会被映射回代理路径）。
//
// 三层地址覆盖（覆盖 JS 动态构造的 URL，应用反代形态）：
//  1. 服务端重写 HTML/CSS 中的静态资源地址（含根绝对路径，这是 <base> 注入做不到的）
//  2. 注入 hook.js 包装 fetch/XHR/WebSocket/window.open/history/URL 属性等动态构造入口
//  3. 相对地址天然按 /app/<id>/<目标URL> 前缀解析，JS 动态拼接的相对路径无需拦截即命中
//
// 文件布局（按职责划分）：
//   - target.go   紧凑目标地址编解码（/app/<id>/ 与 /px/ 之后的地址空间）
//   - engine.go   统一转发主链路（双模式：读入、解压、重写、透传）
//   - adapter.go  站点适配层（特定目标站点的特殊处理，从转发链路解耦）
//   - config.go   代理配置解析与自定义请求头清洗
//   - guard.go    SSRF 拨号防线与宿主自身地址判定
//   - catch.go    逃逸回捕（CatchEscape）与 hook 还原标记
//   - request.go  转发请求的清洗与改写（Referer/Cookie/OAuth 回跳参数）
//   - response.go 转发响应的头改写（Set-Cookie/Location/Refresh/Referrer-Policy）
//   - ws.go       WebSocket 泵转
//   - hook.go     注入脚本下发
//   - rewrite.go  HTML/CSS 地址重写（字节级标签扫描）
//
// 安全边界：
//   - 应用反代的目标只来自管理员在应用注册中的配置（kind=proxy），不接受请求方
//     传 URL；配置目标本身允许内网地址（管理员的显式选择）
//   - 通用转发与目标以外的主机（子资源/重定向/OAuth 跳转）只允许公网地址，内网
//     一律拒绝（防 SSRF）
//   - 两种形态都要求登录态；转发前剥离 OpenOS 自身会话（openos_token 等 Cookie
//     与 Authorization 头）
//   - 经代理执行的第三方 JS 与桌面同源，仅应配置可信应用（信任模型与 goder 内嵌一致）
package webproxy

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"openos/server/internal/auth"
	"openos/server/internal/store"
)

// Proxy 应用代理服务。挂载方式：mux.Handle("/app/{id}/", authSvc.Middleware(px.Handler))
type Proxy struct {
	db     *store.DB
	client *http.Client
	ck     *cookieReg // Cookie 登记簿（「清除缓存」端点配套，见 cookies.go）
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
	return &Proxy{db: db, ck: newCookieReg(db), client: &http.Client{
		Transport: tr,
		// 不自动跟随重定向：30x 的 Location 重写后交还浏览器，保持地址语义
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Close 停机兜底：Cookie 登记簿（清除缓存端点配套）防抖窗口内的登记立即落库，
// 防优雅停机丢最后一批（main 停机序列调用；OPENOS_PROXY=off 时 px 为 nil，
// 空接收者安全）。
func (p *Proxy) Close() {
	if p == nil {
		return
	}
	p.ck.Close()
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
	// 请求侧 Cookie 登记（清除端点配套）：本请求携带的 Cookie 名 × 路径前缀
	// 入册——升级部署前就已种下的旧 Cookie（登录票据）被携带一次即入册
	p.ck.recordRequestCookies(id, r)
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
	case rest == pxClearPath || strings.HasPrefix(rest, pxClearPath+"?"):
		// 「清除缓存」端点：登记在册 Cookie 整批下发过期头（不可见深路径 Cookie
		// 的唯一清法，见 cookies.go）。分批：?n= 每批条数（默认 150），响应体为
		// 剩余条数，调用方循环取批清完
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		_, remain := p.ck.expireBatch(w, id, n)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strconv.Itoa(remain)))
	case rest == hookPath || strings.HasPrefix(rest, hookPath+"?"):
		serveHook(w, r, id, cfg)
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
		// 嵌套代理前缀自愈：目标站 JS 把「已前缀化的代理路径」当相对路径拼进
		// 当前目标源，形成 /app/<id>/<s>/<host>/app/<id>/<s2>/<host2>/… 双层前缀
		// （美团工作台子应用的 CDN 切换拼接实测）——内层才是真实目标（host2），
		// 不剥则外层主机按自身路径应答、资源 404/回落页，样式脚本全歪。302 到
		// 内层目标（多层嵌套剥一层、浏览器跟随自愈）；目标站自身路径恰好叫
		// /app/<本应用id>/ 的概率可忽略（id 为系统生成）
		if inner := strings.TrimPrefix(tu.Path, "/app/"+id+"/"); inner != "" && len(inner) < len(tu.Path) {
			if itu, ierr := decodeTarget(inner); ierr == nil {
				itu.RawQuery = tu.RawQuery
				w.Header().Set("Location", "/app/"+id+"/"+compactTarget(itu))
				w.WriteHeader(http.StatusFound)
				return
			}
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

// APIHandler 处理 /px/<scheme>/<host>/<路径> 全部子路径：统一代理的通用转发
// 端点（无应用绑定）。网络请求类应用（http-client、声明式应用 dataSource 等）
// 把跨域绝对地址映射到本路径发送，服务端代为转发——同源请求无 CORS 限制、
// 响应头全量可读。挂载方式：authSvc.Middleware（须登录）。
//
// 与应用反代的差异（其余链路完全一致，见 engine.go）：
//   - 不做 HTML/CSS 地址重写、不注入 hook（面向接口而非页面）
//   - 无管理员配置：不附加自定义请求头；SSRF 防线无配置目标放行，仅公网可达
//   - Cookie 收归 /px/<scheme>/<host>/ 路径（与浏览器对目标站的直连会话天然隔离）
func (p *Proxy) APIHandler(w http.ResponseWriter, r *http.Request) {
	// 用 EscapedPath 重建目标原始地址（保留 %XX 编码），查询串拼回
	rest := strings.TrimPrefix(r.URL.EscapedPath(), "/px/")
	if r.URL.RawQuery != "" {
		rest += "?" + r.URL.RawQuery
	}
	tu, err := decodeTarget(rest)
	if err != nil {
		http.Error(w, "代理目标地址不合法："+err.Error(), http.StatusBadRequest)
		return
	}
	if tu.Scheme == "ws" || tu.Scheme == "wss" {
		p.serveWS(w, r, tu, nil)
		return
	}
	p.serveForward(w, r, tu, forwardOpts{prefix: "/px/"})
}
