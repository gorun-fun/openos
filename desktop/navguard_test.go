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

// 请求头改写单测：OpenOS 源的 Referer 改写为目标站自身源，目标站自身的值原样
// 保留，Sec-Fetch-* 整组剥除。
func TestRewriteHeadersReferer(t *testing.T) {
	in := map[string]string{
		"Referer":        "http://127.0.0.1:8081/",
		"User-Agent":     "test",
		"Sec-Fetch-Site": "cross-site",
	}
	out, changed := rewriteHeaders(in, "https://www.kdocs.cn/api/v3/x", "http://127.0.0.1:8081")
	if !changed {
		t.Fatal("应判定需要改写")
	}
	m := map[string]string{}
	for _, h := range out {
		m[h["name"]] = h["value"]
	}
	if m["Referer"] != "https://www.kdocs.cn/" {
		t.Fatalf("Referer 改写错误: %q", m["Referer"])
	}
	if m["User-Agent"] != "test" {
		t.Fatalf("无关头被误改: %q", m["User-Agent"])
	}
	if _, ok := m["Sec-Fetch-Site"]; ok {
		t.Fatal("Sec-Fetch-Site 应被剥除")
	}
	// 目标站自身的 Referer 不动
	keep := map[string]string{"Referer": "https://me.meituan.com/x"}
	out2, changed2 := rewriteHeaders(keep, "https://me.meituan.com/x", "http://127.0.0.1:8081")
	if changed2 {
		t.Fatal("目标站自身 Referer 不应触发改写")
	}
	_ = out2
	// 发往 OpenOS 自身的请求（反代 /app/*）Referer 本就指向 OpenOS：不改写——
	// continueRequest 整组替换会丢暂停视图里不存在的 Cookie（kdocs 403 修复）
	self := map[string]string{"Referer": "http://127.0.0.1:8081/app/wps-kdocs/https/www.kdocs.cn/latest"}
	_, changed3 := rewriteHeaders(self, "http://127.0.0.1:8081/app/wps-kdocs/https/www.kdocs.cn/api/x", "http://127.0.0.1:8081")
	if changed3 {
		t.Fatal("发往 OpenOS 源的请求不应触发 Referer 改写")
	}
}
