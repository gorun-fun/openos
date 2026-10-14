// 代理应用配置的解析与自定义请求头清洗。配置源自管理员的apps 表 config 字段，
// 此处仍做纵深防御（黑名单头/非法名/头部注入值的丢弃）。
package webproxy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// proxyConfig 应用 config 的代理形态：{"kind":"proxy","url":"https://目标/",
// "headers":{"X-Api-Key":"…"},"hook":"…JS 源码…"}。headers 为管理员配置的自定义
// 请求头，随代理转发附加到上游请求（含 WebSocket 握手），常用于内网系统的固定
// 认证头。hook 为应用级注入脚本（宿主平台的业务接管管道）：随通用 hook.js 之后
// 注入代理页面，__PREFIX__ 占位符同样替换为 /app/<id>/——监听目标站动作、
// postMessage 通知宿主等业务逻辑由宿主（grf-hotel 等）经 /api/host/apps 维护，
// openos 只提供管道。
type proxyConfig struct {
	Kind    string            `json:"kind"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Hook    string            `json:"hook"`
}

// maxAppHook 应用级注入脚本长度上限：业务监听脚本（接口判据 + postMessage）
// 百行级即够，256KB 已极宽裕；超限置空保持应用可用（config 存 SQLite，防
// 无上限膨胀）。
const maxAppHook = 256 << 10

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
// （防头部注入）。值为 "env:<NAME>" 形态时展开为环境变量的当前值——会话 Cookie
// 类敏感头不必再硬编码进应用配置/重启换血（如美团 ebooking Cookie 走
// env:MEITUAN_COOKIE），改环境变量即热更新于下次代理请求。
func sanitizeCfgHeaders(in map[string]string) map[string]string {
	var out map[string]string
	for k, v := range in {
		if bannedCfgHeaders[strings.ToLower(k)] || !validHeaderName(k) || strings.ContainsAny(v, "\r\n\x00") {
			continue
		}
		if name, ok := strings.CutPrefix(v, "env:"); ok && name != "" {
			ev := os.Getenv(name)
			if strings.ContainsAny(ev, "\r\n\x00") {
				continue // 环境变量含非法字符：整头丢弃
			}
			v = ev
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
	if len(c.Hook) > maxAppHook {
		c.Hook = "" // 超限丢弃脚本不拒绝整个应用（代理本身仍可用）
	}
	return &c
}
