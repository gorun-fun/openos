// pxescape.go — 网页应用代理（openos webproxy）的宿主侧逃逸回捕。
//
// openos 子进程在自己的路由树外层对代理页面逃逸有 CatchEscape 回捕（按 Referer
// 307 回代理路径），但宿主根 mux 上存在大量不转发给子进程的路径（SPA 兜底 /、
// /dev-login、/api/v1 业务等）：代理页里 location.href='/x' 这类 hook.js 无法
// 包装的直接赋值会落到宿主 SPA 兜底，把桌面 index.html 套娃进代理 iframe
// （显示「OpenOS 桌面不能在 iframe 内打开」），撞上 /api/* 甚至会误调宿主或
// openos 的接口。本中间件把同一回捕逻辑挂到宿主根层：Referer 指向代理页面
// （路径 /app/<id>/… 或查询串带 __os_px=<id>~<scheme>~<host> 标记——hook.js 的
// SPA 地址还原模式）时，把当前路径+查询 307 回该应用的代理路径；307 保留方法
// 与请求体，表单 POST 逃逸同样能回去。
//
// 与子进程版差异：不查 openos 应用表（宿主不持有子进程的 store.DB）。安全性
// 不依赖该校验——伪造该 Referer 的增益只是把自己的请求 307 进 /app/<id>/，
// 后续照常过子进程的登录态与角色校验，无越权面。
package hostsdk

import (
	"net/http"
	"net/url"
	"strings"
)

// PxEscape 宿主根层逃逸回捕中间件：包住整个根 Handler（最外层，先于路由分发）。
func PxEscape(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if loc := pxEscapeTarget(r); loc != "" {
			http.Redirect(w, r, loc, http.StatusTemporaryRedirect)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// pxEscapeTarget 解析逃逸目标；空串表示非逃逸请求（放行原处理）。
func pxEscapeTarget(r *http.Request) string {
	p := r.URL.EscapedPath()
	// 代理命名空间与已转发子进程的路径放行：回捕会造成代理前缀自我叠加
	if strings.HasPrefix(p, "/app/") || strings.HasPrefix(p, "/api/") ||
		p == "/ws" || strings.HasPrefix(p, "/vendor/") || strings.HasPrefix(p, "/goder-api/") {
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
	var id, scheme, host string
	if strings.HasPrefix(u.Path, "/app/") {
		var tail, hostPath string
		id, tail, _ = strings.Cut(strings.TrimPrefix(u.Path, "/app/"), "/")
		if tail == "" {
			return ""
		}
		scheme, hostPath, _ = strings.Cut(tail, "/")
		host, _, _ = strings.Cut(hostPath, "/")
	} else {
		parts := strings.Split(u.Query().Get("__os_px"), "~")
		if len(parts) != 3 {
			return ""
		}
		id, scheme, host = parts[0], parts[1], parts[2]
	}
	scheme = strings.ToLower(scheme)
	if !validPxAppID(id) || (scheme != "http" && scheme != "https") ||
		host == "" || strings.ContainsAny(host, "/?#&@%") {
		return ""
	}
	loc := "/app/" + id + "/" + scheme + "/" + host + p
	if q := stripPxMark(r.URL.RawQuery); q != "" {
		loc += "?" + q
	}
	return loc
}

// stripPxMark 从查询串剥掉 hook 还原标记；无标记时原样返回（不重编码，保持
// 对 query 形态敏感的流程不受扰动）。
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

// validPxAppID 与 openos webproxy 的应用 ID 字符集一致（防注入回捕地址）。
func validPxAppID(id string) bool {
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
