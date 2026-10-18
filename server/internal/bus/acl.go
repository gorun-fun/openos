package bus

import "strings"

// 总线访问控制（安全审计 2026-10）：此前任意客户端可订阅任意主题（含他人
// term:* 终端输出、agent:resp 桥应答）并向任意主题发布（含仿冒 sys:* 系统
// 广播）。本文件把客户端可见面收敛为白名单；服务端自身（Publish/PublishTo）
// 不受限制。

// clientPubSys 客户端允许发布的 sys 精确主题——均为现存合法跨端功能面：
//
//	sys:open    设置-应用注册表「打开」按钮跨端拉起（SettingsApp）
//	sys:notify  通知中心 broadcast 路径（notifications store）
//	sys:key     统一键盘未消费按键分发（实际恒 remote:false，仅本地）
var clientPubSys = map[string]bool{
	"sys:open": true, "sys:notify": true, "sys:key": true,
}

// clientPubPrefixes 客户端允许发布的公开业务前缀。
var clientPubPrefixes = []string{"app:", "chat:", "notes:"}

// ownsTopic topic 是否属于 user 的自有主题（term:<user>[:…] / user:<user>[:…]，
// 逐段边界比较，防 "term:aliceevil" 蒙混过 "term:alice"）。
func ownsTopic(user, base, ns string) bool {
	mine := ns + user
	return base == mine || strings.HasPrefix(base, mine+":")
}

// pubAllowed 客户端发布 ACL。topic 必须是具体主题（不允许通配发布）。
func pubAllowed(user, topic string) bool {
	if topic == "" || strings.Contains(topic, "*") {
		return false
	}
	if clientPubSys[topic] {
		return true
	}
	for _, p := range clientPubPrefixes {
		if strings.HasPrefix(topic, p) {
			return true
		}
	}
	switch topic {
	case "fs:changed", "agent:resp":
		return true
	}
	// topic 走到这里必不含 "*"（上方已拒绝通配），直接按原串判自有主题
	if ownsTopic(user, topic, "term:") || ownsTopic(user, topic, "user:") {
		return true
	}
	// 其余（sys:cmd / sys:agent / sys:apps-changed / sys:presence / 他人 term:* 等）
	// 一律收归服务端
	return false
}

// subAllowed 客户端订阅 ACL。pattern 可含 "*" 前缀通配。
// 拒绝：agent:*（桥应答仅服务端处理器消费）、他人 term:*/user:* 私有主题、
// 裸 "*"（匹配一切，等于旁路本 ACL）。公开主题（sys:* / fs:changed / app:* /
// chat:* / notes:* 等）放行。
func subAllowed(user, pattern string) bool {
	if pattern == "" || pattern == "*" {
		return false
	}
	base := strings.TrimSuffix(pattern, "*")
	if strings.HasPrefix(base, "agent:") {
		return false
	}
	if strings.HasPrefix(base, "term:") && !ownsTopic(user, base, "term:") {
		return false
	}
	if strings.HasPrefix(base, "user:") && !ownsTopic(user, base, "user:") {
		return false
	}
	return true
}
