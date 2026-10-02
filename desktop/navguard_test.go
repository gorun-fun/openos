package main

// 主帧导航守卫判定单测：SSO 登录流（同源请求 302 外出）必须放行，iframe
// framebusting（直接外拔）必须回捕——两者是守卫的核心语义分界，回归即死循环
// （grf-hotel 实测：未登录自动跳 /api/v1/auth/sso/login 被 302 到 Casdoor 后
// 立即回捕，页面无限刷新且永远无法完成登录）。

import (
	"strings"
	"testing"
)

func TestClassifyMainNav(t *testing.T) {
	inGuard := func(u string) bool { return strings.HasPrefix(u, "http://os:8082") }
	cases := []struct {
		name      string
		url       string
		away      bool
		leavePass bool
		want      navDecision
	}{
		{"守卫源内正常导航", "http://os:8082/#/desktop", false, false, navHome},
		{"转场中回到守卫源（登录回调 302 回来）", "http://os:8082/api/v1/auth/sso/callback?code=x", true, true, navHome},
		{"SSO 同源请求 302 外出（提交时已在身份源）", "https://casdoor.example/login", false, true, navLeave},
		{"转场中身份源页内导航（表单提交后中转）", "https://casdoor.example/api/authorize", true, false, navAway},
		{"iframe framebusting 直接外拔（无通行证）", "https://evil.example/", false, false, navHijack},
		{"未知发起方（请求事件丢失/浏览器自身）", "https://x.example/", false, false, navHijack},
	}
	for _, c := range cases {
		if got := classifyMainNav(c.url, c.away, c.leavePass, inGuard); got != c.want {
			t.Errorf("%s: classifyMainNav(%q, away=%v, pass=%v) = %v, want %v",
				c.name, c.url, c.away, c.leavePass, got, c.want)
		}
	}
}

func TestNavRequestInGuard(t *testing.T) {
	inGuard := func(u string) bool { return strings.HasPrefix(u, "http://os:8082") }
	cases := []struct {
		u    string
		want bool
	}{
		{"/api/v1/auth/sso/login", true},    // 相对路径：SSO 跳转的典型形态
		{"http://os:8082/go", true},         // 绝对同源
		{"https://casdoor.example/", false}, // 外部
		{"//evil.example/", false},          // 协议相对 = 外部
		{"", false},                         // 空无从判定，不发证
	}
	for _, c := range cases {
		if got := navRequestInGuard(c.u, inGuard); got != c.want {
			t.Errorf("navRequestInGuard(%q) = %v, want %v", c.u, got, c.want)
		}
	}
}
