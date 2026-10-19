// devlogin_test.go — 开发登录页与固定账号账密端点单测（纯内存，不触 openos）。
package hostsdk

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func renderDevLogin(t *testing.T, o DevLoginOpts) string {
	t.Helper()
	w := httptest.NewRecorder()
	DevLoginPage(o).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dev-login", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("页面应 200，实际 %d", w.Code)
	}
	if w.Header().Get("X-Robots-Tag") != "noindex" {
		t.Fatal("页面应带 noindex")
	}
	return w.Body.String()
}

func TestDevLoginPageSinglePhase(t *testing.T) {
	page := renderDevLogin(t, DevLoginOpts{
		Brand: "GRF·管理", Emoji: "🖥️",
		Accent:   "linear-gradient(160deg,#0f172a,#1e293b 50%,#0f766e)",
		TokenKey: "grfos_token",
	})
	for _, want := range []string{
		"<title>开发登录 · GRF·管理</title>",
		"🖥️ GRF·管理 · 开发登录",
		"linear-gradient(160deg,#0f172a,#1e293b 50%,#0f766e)",
		"localStorage.setItem('grfos_token',d.data.token)",
		"const os=d.data.openos_token||''",
		"fetch('/api/v1/auth/login'",
		"仅开发模式（DEV_LOGIN=1）",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("单段式页面缺少: %q", want)
		}
	}
	if strings.Contains(page, "Bearer") {
		t.Fatal("单段式不应出现 Bearer 二段取令牌")
	}
}

func TestDevLoginPageTwoPhase(t *testing.T) {
	page := renderDevLogin(t, DevLoginOpts{
		Brand: "格瑞风", Emoji: "🏨",
		Accent:      "linear-gradient(160deg,#0f2027,#203a43 50%,#2c5364)",
		TokenKey:    "token",
		OsTokenPath: "/api/v1/auth/os-token",
		Title:       "开发登录 · 格瑞风酒店云",
	})
	for _, want := range []string{
		"<title>开发登录 · 格瑞风酒店云</title>",
		"🏨 格瑞风 · 开发登录",
		"const ot=await (await fetch('/api/v1/auth/os-token',{headers:{Authorization:'Bearer '+t}})).json()",
		"localStorage.setItem('token',t)",
		"localStorage.setItem('openos_token',os)",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("两段式页面缺少: %q", want)
		}
	}
	if strings.Contains(page, "d.data.openos_token") {
		t.Fatal("两段式不应直取登录响应的 openos_token")
	}
}

func TestDevLoginPageEscapesBrand(t *testing.T) {
	page := renderDevLogin(t, DevLoginOpts{Brand: "<script>x</script>", TokenKey: "t'"})
	if strings.Contains(page, "<script>x</script>") {
		t.Fatal("品牌名必须转义（防注入）")
	}
	if strings.Contains(page, `'t'`) {
		t.Fatal("令牌键名含引号必须转义（防 JS 串逃逸）")
	}
}

// devLoginHarness 固定账号端点的回调假件。
type devLoginHarness struct {
	sessions *RefreshSessions
	issueErr error
	exchErr  error
	issued   [2]string
	body     map[string]any
	fail     map[string]any
}

func (h *devLoginHarness) handler() http.HandlerFunc {
	return DevLoginHandler(DevLoginConfig{
		Username: "admin", Password: "dev-pass",
		Sessions: h.sessions,
		IssueHostToken: func(uid, name string) (string, error) {
			h.issued = [2]string{uid, name}
			if h.issueErr != nil {
				return "", h.issueErr
			}
			return "host-tok", nil
		},
		Exchange: func(uid, role, name string) (string, error) {
			if h.exchErr != nil {
				return "", h.exchErr
			}
			return "os-tok", nil
		},
		WriteOK: func(w http.ResponseWriter, data any) { h.body = data.(map[string]any) },
		WriteFail: func(w http.ResponseWriter, status int, msg string) {
			h.fail = map[string]any{"status": status, "msg": msg}
		},
	})
}

func postDevLogin(h *devLoginHarness, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.handler().ServeHTTP(w, r)
	return w
}

func TestDevLoginHandlerSuccess(t *testing.T) {
	h := &devLoginHarness{sessions: newTestSessions("s")}
	w := postDevLogin(h, `{"username":"admin","password":"dev-pass"}`)
	if h.fail != nil {
		t.Fatalf("登录不应失败: %+v", h.fail)
	}
	if h.issued[0] != "dev-admin" || h.issued[1] != "开发管理员（admin）" {
		t.Fatalf("签发身份不符: %v", h.issued)
	}
	if h.body["token"] != "host-tok" || h.body["openos_token"] != "os-tok" {
		t.Fatalf("响应字段不符: %+v", h.body)
	}
	// 双 Cookie：openos 会话 Cookie（goder 通道）+ refresh Cookie（30 天滑动）
	if refreshCookieOf(t, w, "openos_token").Value != "os-tok" {
		t.Fatal("应种 openos 会话 Cookie")
	}
	c := refreshCookieOf(t, w, "x_refresh")
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	r.AddCookie(c)
	if p, ok := h.sessions.ParseCookie(r); !ok || p.UID != "dev-admin" || p.Src != "dev" || p.Email != "" {
		t.Fatalf("refresh 载荷不符: ok=%v %+v", ok, p)
	}
}

func TestDevLoginHandlerWrongPassword(t *testing.T) {
	h := &devLoginHarness{sessions: newTestSessions("s")}
	postDevLogin(h, `{"username":"admin","password":"wrong"}`)
	if h.fail["status"] != http.StatusForbidden || h.fail["msg"] != "账号或密码错误" {
		t.Fatalf("账密错误应 403，实际 %+v", h.fail)
	}
}

func TestDevLoginHandlerUnconfigured(t *testing.T) {
	// 未配置账号（Username/Password 空）恒拒绝——防空账密直登
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"username":"","password":""}`))
	w := httptest.NewRecorder()
	var fail map[string]any
	DevLoginHandler(DevLoginConfig{
		WriteOK: func(_ http.ResponseWriter, _ any) {},
		WriteFail: func(_ http.ResponseWriter, status int, msg string) {
			fail = map[string]any{"status": status, "msg": msg}
		},
	}).ServeHTTP(w, r)
	if fail["status"] != http.StatusForbidden {
		t.Fatalf("未配置应 403，实际 %+v", fail)
	}
}

func TestDevLoginHandlerBadJSON(t *testing.T) {
	h := &devLoginHarness{sessions: newTestSessions("s")}
	postDevLogin(h, `{not-json`)
	if h.fail["status"] != http.StatusBadRequest || !strings.HasPrefix(h.fail["msg"].(string), "请求体解析失败") {
		t.Fatalf("坏请求体应 400，实际 %+v", h.fail)
	}
}

func TestDevLoginHandlerExchangeDegrades(t *testing.T) {
	h := &devLoginHarness{sessions: newTestSessions("s"), exchErr: errTest("openos down")}
	w := postDevLogin(h, `{"username":"admin","password":"dev-pass"}`)
	if h.fail != nil || h.body["token"] != "host-tok" || h.body["openos_token"] != "" {
		t.Fatalf("exchange 失败应降级不阻断: fail=%+v body=%+v", h.fail, h.body)
	}
	if len(w.Result().Cookies()) != 1 { // 仅剩 refresh Cookie；openos Cookie 空令牌不种
		t.Fatalf("空 os 令牌不应种 openos Cookie，实际 %d 枚", len(w.Result().Cookies()))
	}
}
