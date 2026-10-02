// SSRF 拨号防线与宿主自身地址判定。防线设在拨号层（而非请求解析层）：即使目标
// 经 DNS 解析或重定向落到内网地址也会被拒绝，管理员配置的目标主机除外。
package webproxy

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// cfgHostCtx 携带管理员配置的目标主机名到拨号层（SSRF 防线按主机名放行配置目标）。
type cfgHostCtx struct{}

// checkDial 拨号前校验：非配置目标且解析落内网 → 拒绝。HTTP 与 WS 两条转发链共用。
func checkDial(cfgHost, addr string) error {
	if host, ok := guardAddr(cfgHost, addr); !ok {
		return fmt.Errorf("webproxy: 拒绝拨号内网地址 %s（非管理员配置目标）", host)
	}
	return nil
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
