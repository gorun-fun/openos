// 代理应用配置的解析与自定义请求头清洗。配置源自管理员的apps 表 config 字段，
// 此处仍做纵深防御（黑名单头/非法名/头部注入值的丢弃）。
package webproxy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

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

// validHeaderName header 名须为非空 RFC 7230 token。
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isTokenChar(s[i]) {
			return false
		}
	}
	return true
}

// sanitizeCfgHeaders 清洗配置请求头：丢弃黑名单头、非法名与含 CR/LF/NUL 的值
// （防头部注入）。
func sanitizeCfgHeaders(in map[string]string) map[string]string {
	var out map[string]string
	for k, v := range in {
		if bannedCfgHeaders[strings.ToLower(k)] || !validHeaderName(k) || strings.ContainsAny(v, "\r\n\x00") {
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
