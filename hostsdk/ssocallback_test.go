// ssocallback_test.go — SSO 回调编排单测（httptest 假 Casdoor，不触外网）。
package hostsdk

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeCasdoor 模拟 Casdoor 的 token 与 userinfo 端点。
func fakeCasdoor(t *testing.T, groups []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("code") != "c" {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		writeTestJSON(w, http.StatusOK, map[string]any{"access_token": "at-1"})
	})
	mux.HandleFunc("GET /api/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		writeTestJSON(w, http.StatusOK, map[string]any{
			"id": "uid-1", "name": "张三", "email": "z@example.com", "groups": groups,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// runSSOCallback 构造编排并执行一次回调请求，返回响应与 exchange 三元组记录。
func runSSOCallback(t *testing.T, group string, mutate func(d *SSOCallback)) (*httptest.ResponseRecorder, *[3]string) {
	t.Helper()
	ep := fakeCasdoor(t, []string{"staff"})
	sso := NewSSO(SSOConfig{
		Endpoint: ep.URL, ClientID: "cid", ClientSecret: "cs",
		RedirectURL: "https://cb.example.com/api/v1/auth/sso/callback", Group: group,
	})
	exchanged := new([3]string)
	d := SSOCallback{
		SSO:      sso,
		Sessions: newTestSessions("s"),
		Resolve: func(u SSOUser) (Identity, error) {
			return Identity{UID: u.UID, Role: "admin", Name: u.Name, Email: u.Email, Src: "sso"}, nil
		},
		IssueHostToken: func(Identity) (string, error) { return "host-t", nil },
		Exchange: func(uid, role, name string) (string, error) {
			*exchanged = [3]string{uid, role, name}
			return "os-t", nil
		},
		HostTokenKey: "grfos_token",
	}
	if mutate != nil {
		mutate(&d)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/sso/callback?code=c&state=st", nil)
	r.AddCookie(&http.Cookie{Name: "sso_state", Value: "st"})
	w := httptest.NewRecorder()
	HandleSSOCallback(d).ServeHTTP(w, r)
	return w, exchanged
}

func TestHandleSSOCallbackSuccess(t *testing.T) {
	w, ex := runSSOCallback(t, "staff", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("登录成功应 200 中间页，实际 %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`localStorage.setItem("grfos_token", "host-t")`,
		`localStorage.setItem('openos_token', "os-t")`,
		`encodeURIComponent("os-t")`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("中间页缺少 %q", want)
		}
	}
	if ex[0] != "uid-1" || ex[1] != "admin" || ex[2] != "张三" {
		t.Fatalf("exchange 三元组不符: %v", ex)
	}
	// 双 Cookie：openos 会话（goder 通道）+ refresh（30 天滑动复活）
	if refreshCookieOf(t, w, "openos_token").Value != "os-t" || refreshCookieOf(t, w, "x_refresh") == nil {
		t.Fatal("回调应种 openos 会话 Cookie 与 refresh Cookie")
	}
}

func TestHandleSSOCallbackNotInGroup(t *testing.T) {
	w, _ := runSSOCallback(t, "vip", nil) // userinfo 组 staff ≠ vip
	if w.Code != http.StatusFound {
		t.Fatalf("组过滤未过应 302，实际 %d", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/?sso_error=") {
		t.Fatalf("失败应带 sso_error 回首页: %s", loc)
	} else if want := "当前账号不在允许的用户组，请联系管理员"; loc != "/?sso_error="+url.QueryEscape(want) {
		t.Fatalf("组过滤话术不符: %s", loc)
	}
}

func TestHandleSSOCallbackProtocolFail(t *testing.T) {
	ep := fakeCasdoor(t, []string{"staff"})
	sso := NewSSO(SSOConfig{Endpoint: ep.URL, ClientID: "cid", ClientSecret: "cs", RedirectURL: "https://cb/x"})
	d := SSOCallback{
		SSO: sso, IssueHostToken: func(Identity) (string, error) { return "t", nil },
		Exchange: func(_, _, _ string) (string, error) { return "o", nil },
	}
	// 无 state cookie → 协议失败
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/sso/callback?code=c&state=st", nil)
	w := httptest.NewRecorder()
	HandleSSOCallback(d).ServeHTTP(w, r)
	if want := "统一身份认证失败，请重试"; w.Code != http.StatusFound ||
		w.Header().Get("Location") != "/?sso_error="+url.QueryEscape(want) {
		t.Fatalf("协议失败默认话术不符: %d %s", w.Code, w.Header().Get("Location"))
	}
	// 自定义话术（grf-hotel「登录失败，请重试」）
	d.AuthFailMsg = "登录失败，请重试"
	w = httptest.NewRecorder()
	HandleSSOCallback(d).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/sso/callback?code=c&state=st", nil))
	if w.Header().Get("Location") != "/?sso_error="+url.QueryEscape("登录失败，请重试") {
		t.Fatalf("自定义话术不符: %s", w.Header().Get("Location"))
	}
}

func TestHandleSSOCallbackResolveFailures(t *testing.T) {
	// 业务性拒绝（VisibleError）：消息直达登录页
	w, _ := runSSOCallback(t, "staff", func(d *SSOCallback) {
		d.Resolve = func(SSOUser) (Identity, error) {
			return Identity{}, &VisibleError{Msg: "所属酒店已被平台停用"}
		}
	})
	if w.Header().Get("Location") != "/?sso_error="+url.QueryEscape("所属酒店已被平台停用") {
		t.Fatalf("VisibleError 应原话术展示: %s", w.Header().Get("Location"))
	}
	// 其余错误：兜底话术（不泄露内部细节）
	w, _ = runSSOCallback(t, "staff", func(d *SSOCallback) {
		d.Resolve = func(SSOUser) (Identity, error) { return Identity{}, errTest("db: connection refused") }
	})
	if w.Header().Get("Location") != "/?sso_error="+url.QueryEscape("身份解析失败，请稍后重试") {
		t.Fatalf("兜底话术不符: %s", w.Header().Get("Location"))
	}
	// 兜底话术可参数化
	w, _ = runSSOCallback(t, "staff", func(d *SSOCallback) {
		d.Resolve = func(SSOUser) (Identity, error) { return Identity{}, errTest("boom") }
		d.ResolveFailMsg = "身份解析失败"
	})
	if w.Header().Get("Location") != "/?sso_error="+url.QueryEscape("身份解析失败") {
		t.Fatalf("自定义兜底话术不符: %s", w.Header().Get("Location"))
	}
}

func TestHandleSSOCallbackIssueFails(t *testing.T) {
	w, _ := runSSOCallback(t, "staff", func(d *SSOCallback) {
		d.IssueHostToken = func(Identity) (string, error) { return "", errTest("jwt") }
	})
	if w.Header().Get("Location") != "/?sso_error="+url.QueryEscape("签发会话失败") {
		t.Fatalf("签发失败话术不符: %s", w.Header().Get("Location"))
	}
}

func TestHandleSSOCallbackExchangeDegrades(t *testing.T) {
	w, _ := runSSOCallback(t, "staff", func(d *SSOCallback) {
		d.Exchange = func(_, _, _ string) (string, error) { return "", errTest("openos down") }
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "os-token") {
		t.Fatalf("exchange 失败应降级进中间页，实际 %d", w.Code)
	}
}

func TestHandleSSOCallbackDisabled(t *testing.T) {
	// SSO 未配置（Endpoint 空）：明确话术，不触碰 Casdoor
	d := SSOCallback{
		SSO: NewSSO(SSOConfig{ClientID: "cid", ClientSecret: "cs"}),
	}
	w := httptest.NewRecorder()
	HandleSSOCallback(d).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/sso/callback?code=c&state=st", nil))
	if w.Header().Get("Location") != "/?sso_error="+url.QueryEscape("SSO 未配置") {
		t.Fatalf("未配置话术不符: %s", w.Header().Get("Location"))
	}
}

func TestRequestRedirectFunc(t *testing.T) {
	f := RequestRedirectFunc("/api/v1/auth/sso/callback")
	// 明文直连（无 TLS、无转发头）→ http（httptest 对 https URL 会预置 TLS，须清掉）
	r := httptest.NewRequest(http.MethodGet, "https://cb.example.com/x", nil)
	r.TLS = nil
	r.Host = "10.8.0.5:58080"
	if got := f(r); got != "http://10.8.0.5:58080/api/v1/auth/sso/callback" {
		t.Fatalf("直连推导不符: %s", got)
	}
	// X-Forwarded-Proto 感知（反代 https 终止）
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := f(r); got != "https://10.8.0.5:58080/api/v1/auth/sso/callback" {
		t.Fatalf("Proto 推导不符: %s", got)
	}
	// X-Forwarded-Host 覆盖（网关改写 Host）
	r.Header.Set("X-Forwarded-Host", "os.example.com")
	if got := f(r); got != "https://os.example.com/api/v1/auth/sso/callback" {
		t.Fatalf("Host 推导不符: %s", got)
	}
	// TLS 直连（无转发头）→ https
	r2 := httptest.NewRequest(http.MethodGet, "https://cb.example.com/x", nil)
	r2.Host = "os.example.com"
	r2.TLS = &tls.ConnectionState{}
	if got := f(r2); got != "https://os.example.com/api/v1/auth/sso/callback" {
		t.Fatalf("TLS 推导不符: %s", got)
	}
}

func TestNewSSOAutoRedirect(t *testing.T) {
	// 无静态 RedirectURL：自动注入请求期推导，Enabled 不再要求静态值
	s := NewSSOAutoRedirect(SSOConfig{Endpoint: "https://sso", ClientID: "c", ClientSecret: "s"}, "/api/v1/auth/sso/callback")
	if !s.Enabled() {
		t.Fatal("注入推导后应视为已配置")
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/sso/login", nil)
	r.Host = "h.example.com"
	if got := s.redirect(r); got != "http://h.example.com/api/v1/auth/sso/callback" {
		t.Fatalf("推导回调地址不符: %s", got)
	}
	// 显式静态值优先，不注入推导
	s2 := NewSSOAutoRedirect(SSOConfig{Endpoint: "https://sso", ClientID: "c", ClientSecret: "s", RedirectURL: "https://static/cb"}, "/api/v1/auth/sso/callback")
	if got := s2.redirect(r); got != "https://static/cb" {
		t.Fatalf("静态值应优先: %s", got)
	}
}
