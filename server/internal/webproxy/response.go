// 转发响应的头改写：剥嵌入封锁头、Set-Cookie 收归代理路径、Location/Refresh
// 前缀化、Content-Type 单值语义、Referrer-Policy 覆盖（逃逸回捕的判定依据）。
package webproxy

import (
	"net/http"
	"net/url"
	"strings"
)

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
			// 多值透传（罕见形态）：单值收敛不再通用处理——上游真下发重复
			// Content-Type 属站点自身 bug，由 adapter.go 的 weixin-double-ct
			// 适配器按目标主机收敛（微信 jslogin 实测：浏览器按末值解析会让
			// <script> 严格 MIME 检查拒绝执行 JSONP），其余站点维持原生语义
			for _, v := range vs {
				h.Add(k, v)
			}
			continue
		}
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	// Referrer-Policy=same-origin：同源请求携带完整 Referer——这是逃逸回捕
	//（CatchEscape 按 Referer 把 location.href='/x' 类跳转 307 回代理路径）的
	// 判定依据；跨源不发送（转发上游前本就剥 Referer，不外泄）。覆盖全局
	// 中间件的 no-referrer（其会让代理页面的所有导航都不带 Referer）
	h.Set("Referrer-Policy", "same-origin")
	// 剥掉全局中间件附加的 X-Content-Type-Options: nosniff：代理透传的是目标站
	// 内容，须维持其「无 nosniff」的原生宽松 MIME 语义——上游 JSONP/脚本接口
	// 常 MIME 不规范（网易云易盾配置 JSONP 回 application/json、百度 hm.js 回
	// text/plain），原生下浏览器照常执行，nosniff 会按严格 MIME 检查硬阻断脚本
	//（易盾初始化失败 → 登录二维码无法生成）。信任模型不变：经代理的第三方
	// 脚本本就与桌面同源执行（等同 goder 内嵌），nosniff 不提供额外保护
	h.Del("X-Content-Type-Options")
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
