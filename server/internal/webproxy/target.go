// 紧凑目标地址编解码：/app/<id>/ 之后的地址空间的唯一实现。
// 紧凑形态 <scheme>/<host>/<路径>?<查询> 用 <scheme>/<host> 而非 scheme://host，
// 避免路径中的 // 被 Go ServeMux 的 cleanPath 折叠（其 307 清洗重定向会破坏
// 目标地址）。压缩与解压必须成对使用本文件的实现，禁止在别处重造。
package webproxy

import (
	"fmt"
	"net/url"
	"strings"
)

// compactTarget 把绝对目标地址映射为代理路径段：https://h:8443/a/b?q →
// https/h:8443/a/b?q。
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

// cfgURLHost 取配置目标 URL 的主机名（供 SSRF 防线放行配置目标）。
func cfgURLHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// validAppID 应用 id 的合法字符集（路径段安全字符）。
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
