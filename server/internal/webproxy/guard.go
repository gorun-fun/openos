// SSRF 拨号防线与宿主自身地址判定。防线设在拨号层（而非请求解析层）：即使目标
// 经 DNS 解析或重定向落到内网地址也会被拒绝，管理员配置的目标主机除外。
package webproxy

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// cfgHostCtx 携带管理员配置的目标主机名到拨号层（SSRF 防线按主机名放行配置目标）。
type cfgHostCtx struct{}

// guardedDial 统一拨号函数（HTTP 与 WS 两条转发链共用）：域名单次解析、逐 IP
// 校验后按 IP 直连——杜绝「校验用第一条解析、实际拨号二次解析被 DNS rebinding
// 落到内网」的 TOCTOU 窗口（安全审计 2026-10）。管理员配置的目标主机放行
//（可为内网，显式选择），但同样按解析出的 IP 直连。
func guardedDial(ctx context.Context, network, addr, cfgHost string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("webproxy: 非法拨号地址 %q", addr)
	}
	ips, err := resolveGuarded(ctx, host, cfgHost)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	var lastErr error
	for _, ip := range ips {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// resolveGuarded 解析并校验拨号主机（guardedDial 的校验半部，单测直测）：
// 返回可拨 IP 列表。IP 直写则直接判定；域名经 DefaultResolver 单次解析。
// 配置目标主机放行（可为内网——管理员显式选择）；其余主机的解析结果只要
// 含内网/回环/链路本地地址一律拒绝。
func resolveGuarded(ctx context.Context, host, cfgHost string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) && (cfgHost == "" || !strings.EqualFold(host, cfgHost)) {
			return nil, fmt.Errorf("webproxy: 拒绝拨号内网地址 %s（非管理员配置目标）", host)
		}
		return []net.IP{ip}, nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("webproxy: 域名解析失败 %s: %v", host, err)
	}
	if cfgHost == "" || !strings.EqualFold(host, cfgHost) {
		for _, ip := range ips {
			if !isPublicIP(ip.IP) {
				return nil, fmt.Errorf("webproxy: 拒绝拨号 %s（解析含内网地址，非管理员配置目标）", host)
			}
		}
	}
	out := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.IP)
	}
	return out, nil
}

func isPublicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified())
}

// isSelfHost 判断地址 host 是否 OpenOS 宿主自身：与请求 Host 头相等，或同为回环
// 形态且端口一致（localhost ↔ 127.0.0.1 ↔ [::1]——页面读到的 origin 与请求 Host
// 的字符串形态可能不一致；端口不同则是别的内网服务，不是宿主）。目标站 JS 在
// 代理页里读 location.origin 拼自我引用（登录回调/continueUrl 等）时拿到的就是
// 宿主地址。无端口形态（location.hostname 拼接，含生产站硬编码 https:// 的跨
// scheme 形态——美团 ebooking 工作台 sub-app 子应用入口实测）按主机名一致或同为
// 回环判定。
func isSelfHost(host, selfHost string) bool {
	if selfHost == "" || host == "" {
		return false
	}
	if strings.EqualFold(host, selfHost) {
		return true
	}
	h1, p1, err1 := net.SplitHostPort(host)
	h2, p2, err2 := net.SplitHostPort(selfHost)
	if err1 != nil || err2 != nil {
		h1n, h2n := strings.ToLower(host), strings.ToLower(selfHost)
		if err1 == nil {
			h1n = strings.ToLower(h1)
		}
		if err2 == nil {
			h2n = strings.ToLower(h2)
		}
		return h1n == h2n || (isLoopbackHost(h1n) && isLoopbackHost(h2n))
	}
	if p1 != p2 {
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
