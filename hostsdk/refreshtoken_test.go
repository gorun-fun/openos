// refreshtoken_test.go — refresh Cookie 内核单测（纯内存，不触 openos）。
package hostsdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestSessions(secret string) *RefreshSessions {
	return NewRefreshSessions(RefreshConfig{CookieName: "x_refresh", Secret: secret})
}

func refreshCookieOf(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatal("响应未种 Cookie: " + name)
	return nil
}

func TestRefreshCookieRoundtrip(t *testing.T) {
	s := newTestSessions("test-secret")
	w := httptest.NewRecorder()
	s.SetCookie(w, RefreshClaims{UID: "u-1", Name: "张三", Email: "z@example.com", Src: "sso"})
	c := refreshCookieOf(t, w, "x_refresh")
	if !c.HttpOnly || c.Path != "/api/v1/auth" || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("Cookie 属性不符合预期: %+v", c)
	}
	if c.MaxAge != int((30 * 24 * time.Hour).Seconds()) {
		t.Fatalf("MaxAge 应为 30 天滑动，实际 %d", c.MaxAge)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	r.AddCookie(c)
	p, ok := s.ParseCookie(r)
	if !ok || p.UID != "u-1" || p.Name != "张三" || p.Email != "z@example.com" || p.Src != "sso" {
		t.Fatalf("回读失败: ok=%v payload=%+v", ok, p)
	}
	if p.Exp <= p.Iat || p.Exp > time.Now().Add(31*24*time.Hour).Unix() {
		t.Fatalf("exp/iat 盖戳异常: %+v", p)
	}
}

// TestRefreshCookiePayloadParity 载荷 JSON 与两宿主历史形态逐字节兼容：
// 不用 email/extra 时与 grf-hotel 载荷同串，用 email 时与 grf-os 同序。
func TestRefreshCookiePayloadParity(t *testing.T) {
	hotel := RefreshClaims{UID: "u-1", Name: "张三", Src: "sso", Exp: 1700000000, Iat: 1600000000}
	b, _ := json.Marshal(hotel)
	if want := `{"uid":"u-1","name":"张三","src":"sso","exp":1700000000,"iat":1600000000}`; string(b) != want {
		t.Fatalf("hotel 形态载荷不符: %s", b)
	}
	osForm := hotel
	osForm.Email = "z@example.com"
	osForm.Extra = map[string]string{"tenant": "t1"}
	b, _ = json.Marshal(osForm)
	if want := `{"uid":"u-1","name":"张三","email":"z@example.com","src":"sso","exp":1700000000,"iat":1600000000,"x":{"tenant":"t1"}}`; string(b) != want {
		t.Fatalf("os 形态载荷不符: %s", b)
	}
}

func TestRefreshCookieExtraRoundtrip(t *testing.T) {
	s := newTestSessions("test-secret")
	w := httptest.NewRecorder()
	s.SetCookie(w, RefreshClaims{UID: "u-2", Src: "sso", Extra: map[string]string{"tenant": "t1", "prop": "p9"}})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	r.AddCookie(refreshCookieOf(t, w, "x_refresh"))
	p, ok := s.ParseCookie(r)
	if !ok || p.Extra["tenant"] != "t1" || p.Extra["prop"] != "p9" {
		t.Fatalf("扩展位回读失败: ok=%v payload=%+v", ok, p)
	}
}

func TestRefreshCookieTamper(t *testing.T) {
	s := newTestSessions("test-secret")
	w := httptest.NewRecorder()
	s.SetCookie(w, RefreshClaims{UID: "u-1", Src: "sso"})
	c := refreshCookieOf(t, w, "x_refresh")
	// 篡改载荷尾字符（签名不匹配）
	c.Value = c.Value[:len(c.Value)-2] + "xA"
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	r.AddCookie(c)
	if _, ok := s.ParseCookie(r); ok {
		t.Fatal("篡改后的 Cookie 不应通过校验")
	}
	// 换密钥签发的服务端也无法通过（密钥隔离）
	if _, ok := newTestSessions("other-secret").ParseCookie(r); ok {
		t.Fatal("异密钥服务端不应认签")
	}
}

func TestRefreshCookieExpired(t *testing.T) {
	s := newTestSessions("test-secret")
	orig := refreshNow
	refreshNow = func() time.Time { return orig().Add(-31 * 24 * time.Hour) }
	w := httptest.NewRecorder()
	s.SetCookie(w, RefreshClaims{UID: "u-old", Src: "sso"})
	refreshNow = orig
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	r.AddCookie(refreshCookieOf(t, w, "x_refresh"))
	if _, ok := s.ParseCookie(r); ok {
		t.Fatal("过期 Cookie 不应通过校验")
	}
}

func TestSetCookieSkipsEmptyUID(t *testing.T) {
	s := newTestSessions("test-secret")
	w := httptest.NewRecorder()
	s.SetCookie(w, RefreshClaims{UID: "", Src: "sso"})
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("空 uid 不应种 Cookie")
	}
}

// refreshTestHarness 内核 handler 的回调假件与响应包裹（宿主 {code,msg,data} 形态）。
type refreshTestHarness struct {
	sessions  *RefreshSessions
	resolve   func(RefreshClaims) (Identity, error)
	issueErr  error
	exchErr   error
	gotRaw    RefreshClaims
	gotID     Identity
	lastBody  map[string]any
	lastFail  map[string]any
	exchanged [3]string
}

func (h *refreshTestHarness) handler() http.HandlerFunc {
	return h.sessions.Handler(RefreshHandlers{
		ParseBearer: func(r *http.Request) (RefreshClaims, bool) {
			if tok := r.Header.Get("Authorization"); strings.HasPrefix(tok, "Bearer live.") {
				// 假宿主 JWT：live.<uid>.<name>
				parts := strings.Split(tok, ".")
				return RefreshClaims{UID: parts[1], Name: parts[2], Email: "e@example.com", Src: "sso"}, true
			}
			return RefreshClaims{}, false
		},
		Resolve: func(raw RefreshClaims) (Identity, error) {
			h.gotRaw = raw
			if h.resolve != nil {
				return h.resolve(raw)
			}
			return Identity{UID: raw.UID, Name: raw.Name, Role: "user", Email: raw.Email, Src: raw.Src, Extra: raw.Extra}, nil
		},
		IssueHostToken: func(id Identity) (string, error) {
			h.gotID = id
			if h.issueErr != nil {
				return "", h.issueErr
			}
			return "host-tok", nil
		},
		Exchange: func(uid, role, name string) (string, error) {
			h.exchanged = [3]string{uid, role, name}
			if h.exchErr != nil {
				return "", h.exchErr
			}
			return "os-tok", nil
		},
		WriteOK: func(w http.ResponseWriter, res RefreshResult) {
			h.lastBody = map[string]any{"token": res.HostToken, "openos_token": res.OsToken, "role": res.Role}
			writeTestJSON(w, http.StatusOK, h.lastBody)
		},
		WriteFail: func(w http.ResponseWriter, status int, msg string) {
			h.lastFail = map[string]any{"status": status, "msg": msg}
			writeTestJSON(w, status, h.lastFail)
		},
	})
}

func writeTestJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestRefreshHandlerNoCredentials(t *testing.T) {
	h := &refreshTestHarness{sessions: newTestSessions("s")}
	w := httptest.NewRecorder()
	h.handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil))
	if w.Code != http.StatusUnauthorized || h.lastFail["msg"] != "会话已过期，请重新登录" {
		t.Fatalf("无凭证续期应 401，实际 %d %+v", w.Code, h.lastFail)
	}
}

func TestRefreshHandlerCookieRevival(t *testing.T) {
	h := &refreshTestHarness{sessions: newTestSessions("s")}
	// 先经内核种一枚 cookie（模拟登录期种植）
	sw := httptest.NewRecorder()
	h.sessions.SetCookie(sw, RefreshClaims{UID: "u-9", Name: "李四", Src: "sso", Extra: map[string]string{"k": "v"}})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	r.AddCookie(refreshCookieOf(t, sw, "x_refresh"))
	w := httptest.NewRecorder()
	h.handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("cookie 复活应 200，实际 %d %+v", w.Code, h.lastFail)
	}
	if h.lastBody["token"] != "host-tok" || h.lastBody["openos_token"] != "os-tok" || h.lastBody["role"] != "user" {
		t.Fatalf("响应字段不符: %+v", h.lastBody)
	}
	if h.gotID.UID != "u-9" || h.gotID.Extra["k"] != "v" {
		t.Fatalf("身份重算入参不符: %+v", h.gotID)
	}
	if h.exchanged[0] != "u-9" || h.exchanged[1] != "user" || h.exchanged[2] != "李四" {
		t.Fatalf("exchange 三元组不符: %v", h.exchanged)
	}
	// 成功续期重种双 Cookie（openos 会话 Cookie + refresh Cookie 滑动）
	if refreshCookieOf(t, w, "x_refresh") == nil {
		t.Fatal("续期应重种 refresh Cookie")
	}
	if refreshCookieOf(t, w, "openos_token").Value != "os-tok" {
		t.Fatal("续期应重种 openos 会话 Cookie")
	}
}

func TestRefreshHandlerBearerPriority(t *testing.T) {
	h := &refreshTestHarness{sessions: newTestSessions("s")}
	sw := httptest.NewRecorder()
	h.sessions.SetCookie(sw, RefreshClaims{UID: "from-cookie", Src: "sso"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	r.Header.Set("Authorization", "Bearer live.from-bearer.王五")
	r.AddCookie(refreshCookieOf(t, sw, "x_refresh"))
	h.handler().ServeHTTP(httptest.NewRecorder(), r)
	if h.gotRaw.UID != "from-bearer" || h.gotRaw.Name != "王五" {
		t.Fatalf("Bearer 应优先于 Cookie: %+v", h.gotRaw)
	}
}

func TestRefreshHandlerResolveReject(t *testing.T) {
	h := &refreshTestHarness{sessions: newTestSessions("s")}
	h.resolve = func(RefreshClaims) (Identity, error) { return Identity{}, &VisibleError{Msg: "离职"} }
	// VisibleError 在续期语境同样 401（拒绝续期）
	sw := httptest.NewRecorder()
	h.sessions.SetCookie(sw, RefreshClaims{UID: "u-x", Src: "sso"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	r.AddCookie(refreshCookieOf(t, sw, "x_refresh"))
	w := httptest.NewRecorder()
	h.handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Resolve 拒绝应 401，实际 %d", w.Code)
	}
}

func TestRefreshHandlerIssueFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		harn *refreshTestHarness
	}{
		{"issue", &refreshTestHarness{sessions: newTestSessions("s"), issueErr: errTest("jwt down")}},
		{"exchange", &refreshTestHarness{sessions: newTestSessions("s"), exchErr: errTest("openos down")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.harn
			sw := httptest.NewRecorder()
			h.sessions.SetCookie(sw, RefreshClaims{UID: "u-1", Src: "sso"})
			r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
			r.AddCookie(refreshCookieOf(t, sw, "x_refresh"))
			w := httptest.NewRecorder()
			h.handler().ServeHTTP(w, r)
			if w.Code != http.StatusForbidden || !strings.HasPrefix(h.lastFail["msg"].(string), "会话续期失败: ") {
				t.Fatalf("签发失败应 403 且带前缀，实际 %d %+v", w.Code, h.lastFail)
			}
		})
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
