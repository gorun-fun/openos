package bus

import "testing"

func TestMatchTopic(t *testing.T) {
	cases := []struct {
		pattern, topic string
		want           bool
	}{
		{"*", "anything", true},
		{"sys:open", "sys:open", true},
		{"sys:open", "sys:other", false},
		{"sys:*", "sys:open", true},
		{"sys:*", "sys:", true},
		{"app:*", "fs:changed", false},
		{"term:admin:out", "term:admin:out", true},
		{"", "x", false},
	}
	for _, c := range cases {
		if got := matchTopic(c.pattern, c.topic); got != c.want {
			t.Errorf("matchTopic(%q, %q) = %v, want %v", c.pattern, c.topic, got, c.want)
		}
	}
}

// TestInvalidClients 验证周期复核的筛选逻辑：未通过复核的连接被选出（吊销/过期踢线）。
// 直接构造连接（无真实 socket），不触发关闭路径。
func TestInvalidClients(t *testing.T) {
	h := New()
	h.revalidate = func(user, role, token string) bool { return token != "bad" }
	alice := &Client{hub: h, User: "alice", Role: "user", token: "good"}
	bob := &Client{hub: h, User: "bob", Role: "user", token: "bad"}
	h.clients[alice] = struct{}{}
	h.clients[bob] = struct{}{}

	bad := h.invalidClients()
	if len(bad) != 1 || bad[0] != bob {
		t.Fatalf("invalidClients() = %v, want 仅 bob", bad)
	}

	// 复核函数返回 true 时不挑选任何连接
	h.revalidate = func(user, role, token string) bool { return true }
	if got := h.invalidClients(); len(got) != 0 {
		t.Fatalf("全部通过时 invalidClients() = %v, want 空", got)
	}
}
