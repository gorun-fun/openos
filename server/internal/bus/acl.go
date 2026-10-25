package bus

import "strings"

// 总线访问控制（安全审计 2026-10）：此前任意客户端可订阅任意主题（含他人
// term:* 终端输出、agent:resp 桥应答）并向任意主题发布（含仿冒 sys:* 系统
// 广播）。本文件把客户端可见面收敛为白名单；服务端自身（Publish/PublishTo）
// 不受限制。

// clientPubSys 客户端允许发布的 sys 精确主题——均为现存合法**本人跨端**功能面：
//
//	sys:open    设置-应用注册表「打开」按钮跨端拉起（SettingsApp）
//	sys:notify  通知中心 broadcast 路径（notifications store，语义=本人其他端弹出）
//
// sys:key 已移出（安全审计 2026-10 第二轮）：统一键盘的按键分发恒 {remote:false}
// 仅本地，服务端放行远程发布没有任何合法消费方，只构成跨用户合成按键注入面。
var clientPubSys = map[string]bool{
	"sys:open": true, "sys:notify": true,
}

// pubSelfDeliver 客户端发布的这些主题只投递给**同一用户**的连接（安全审计
// 2026-10 第二轮）：三个主题的消费语义都是「本人其他端」——多标签页/多设备同步
// （sys:open 拉起 / sys:notify 广播弹通知 / fs:changed 文件变更刷新），此前按
// 订阅全员投递，任意登录用户可冒充任意 appId 给他人弹通知/拉应用/伪造文件事件。
// 服务端 PublishTo 定向推送不受影响（From=system 不走此分支）。
var pubSelfDeliver = map[string]bool{
	"sys:open": true, "sys:notify": true, "fs:changed": true,
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
