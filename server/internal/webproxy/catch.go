// 逃逸回捕与 hook 还原标记。
//
// 代理页面对 location 的直接赋值（location.href='/x' / location.assign 等，
// 浏览器不允许重定义）会把导航带到本源根路径——目标站用 /api、/assets 等本源
// 常见前缀极普遍，撞上 OpenOS 自身路由会 404 甚至误调系统接口。CatchEscape 按
// Referer 把这类请求 307 回该应用的代理路径。
//
// hook 还原模式下文档地址已被还原为目标站形态（SPA 路由要求），Referer 里
// 携带 __os_px=<id>~<scheme>~<host> 标记作为回捕依据。
package webproxy

import (
	"net/http"
	"net/url"
	"strings"

	"openos/server/internal/store"
)

// CatchEscape 把「从代理页面逃逸到本源路径」的请求 307 回该应用的代理路径。
// 因此须挂在整棵路由树的外层（而非仅 SPA 兜底"/"路由外层，目标站 SSO 常用
// /api/auth/sso/login 这类本源前缀）。判定依据 Referer：同源且指向
// /app/<id>/<scheme>/<host>/…（或还原标记形态）的请求，把当前 请求路径+查询
// 拼回该目标源下。307 保留方法与请求体，表单 POST 逃逸同样能回去。
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
// 代理页面 / 代理自身命名空间等情形，一律放行原处理）。
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

// refererTargetHost 从代理页面的 Referer 中解析目标主机（两种形态见 parsePxMark /
// 紧凑前缀形态），不校验应用存在性（调用方按需自行核实）。
// serveHook 用它选站点适配器的追加注入片段。
func refererTargetHost(ref string) (string, bool) {
	if ref == "" {
		return "", false
	}
	u, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	if strings.HasPrefix(u.Path, "/app/") {
		_, tail, _ := strings.Cut(strings.TrimPrefix(u.Path, "/app/"), "/")
		scheme, hostPath, ok := strings.Cut(tail, "/")
		if !ok || tail == "" {
			return "", false
		}
		host, _, _ := strings.Cut(hostPath, "/")
		scheme = strings.ToLower(scheme)
		if (scheme != "http" && scheme != "https") || host == "" {
			return "", false
		}
		return host, true
	}
	_, _, host, ok := parsePxMark(u.Query())
	return host, ok
}

// refererOrigin 从代理页面的 Referer 中解析出应用 id 与目标源（两种形态见
// parsePxMark / 紧凑前缀形态），并核实该应用存在且为代理形态。
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
		scheme = strings.ToLower(scheme)
		if (scheme != "http" && scheme != "https") || host == "" {
			return "", "", "", false
		}
	} else if id, scheme, host, ok = parsePxMark(u.Query()); !ok {
		return "", "", "", false
	}
	app, found := db.AppByID(id)
	if !found || parseProxyConfig(app.Config) == nil {
		return "", "", "", false
	}
	return id, scheme, host, true
}

// parsePxMark 解析 hook 还原标记 __os_px=<id>~<scheme>~<host[:port]>。标记值经
// URL 解码而来，host 段须为纯地址字符（防注入路径分隔符等回捕/Referer 目标）。
// 逃逸回捕（refererOrigin）与 Referer 还原（rewriteReferer）共用本实现。
func parsePxMark(q url.Values) (id, scheme, host string, ok bool) {
	parts := strings.Split(q.Get("__os_px"), "~")
	if len(parts) != 3 {
		return "", "", "", false
	}
	id, scheme, host = parts[0], strings.ToLower(parts[1]), parts[2]
	if id == "" || host == "" || strings.ContainsAny(host, "/?#&@%") {
		return "", "", "", false
	}
	if scheme != "http" && scheme != "https" {
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
