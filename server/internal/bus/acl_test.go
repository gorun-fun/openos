package bus

import "testing"

// 总线 ACL（安全审计 2026-10）：订阅/发布白名单行为锁定。
// 关键不变量：客户端不得旁路收听他人 term:* / agent:*，不得仿冒服务端 sys:* 广播；
// 现存合法功能面（DEFAULT_REMOTE_SUBS、通知广播、设置页跨端拉起、终端、聊天）全部放行。

func TestPubAllowed(t *testing.T) {
	cases := []struct {
		topic string
		want  bool
	}{
		// 合法功能面
		{"app:calc", true},
		{"app:hotel:refresh", true},
		{"chat:room", true},
		{"notes:pin", true},
		{"fs:changed", true},
		{"agent:resp", true},
		{"sys:open", true},
		{"sys:notify", true},
		{"sys:key", true},
		// 自有终端主题（虚拟/真实终端输入）
		{"term:alice", true},
		{"term:alice:real:in", true},
		// 收归服务端的系统主题
		{"sys:cmd", false},
		{"sys:agent", false},
		{"sys:apps-changed", false},
		{"sys:presence", false},
		{"sys:feedback", false},
		// 他人终端 / 越权
		{"term:bob", false},
		{"term:bob:real:in", false},
		// 前缀蒙混
		{"term:aliceevil", false},
		{"user:bob", false},
		// 通配与空主题
		{"", false},
		{"*", false},
		{"app:*", false},
	}
	for _, c := range cases {
		if got := pubAllowed("alice", c.topic); got != c.want {
			t.Errorf("pubAllowed(alice, %q) = %v, want %v", c.topic, got, c.want)
		}
	}
}

func TestSubAllowed(t *testing.T) {
	cases := []struct {
		pattern string
		want    bool
	}{
		// 默认订阅集与业务主题
		{"sys:*", true},
		{"fs:changed", true},
		{"app:*", true},
		{"chat:*", true},
		{"notes:widgets", true},
		// 自有终端输出（服务端经 PublishTo 定向投递，正常不经订阅，但不应误伤）
		{"term:alice", true},
		{"term:alice:real:out", true},
		// 桥应答与他人终端：拒绝
		{"agent:resp", false},
		{"agent:*", false},
		{"term:bob", false},
		{"term:*", false},
		{"user:bob:*", false},
		{"user:*", false},
		// 裸 * 旁路
		{"*", false},
		{"", false},
	}
	for _, c := range cases {
		if got := subAllowed("alice", c.pattern); got != c.want {
			t.Errorf("subAllowed(alice, %q) = %v, want %v", c.pattern, got, c.want)
		}
	}
}
