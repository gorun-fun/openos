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
