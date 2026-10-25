// stewardapi_test.go — REST 面单测：上报入口密钥自鉴权、管理面登录 + admin
// 门禁、上报→问题落库回环。引擎用临时目录真件，不起 goder。
package stewardapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"openos/server/internal/auth"
	"openos/server/internal/steward"
	"openos/server/internal/store"
)

// newTestHandler 构造真引擎 + 无 goder 的 Handler 与挂好鉴权的 mux（authed =
// authSvc.Middleware(admin mux)；公开 mux 直挂）。
func newTestHandler(t *testing.T) (*Handler, *http.ServeMux, *http.ServeMux, *auth.Service) {
	t.Helper()
	eng, err := steward.New(steward.Options{
		DataDir: filepath.Join(t.TempDir(), "steward"),
		Timeout: time.Minute,
		Logf:    t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{Eng: eng, GL: NewGitLabClient(GlEnv{URL: "https://git.example.com"})}
	authed, public := http.NewServeMux(), http.NewServeMux()
	h.Register(authed, public)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := auth.New(db, "test-secret")
	// 领域测试用户（VerifySession 查实时库值，不在库的令牌一律 401）
	for _, u := range []struct{ name, role string }{{"boss", "admin"}, {"guest", "user"}} {
		if _, err := db.Exec(`INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES(?,?,?,?,?,1)`,
			u.name, "h", "s", u.role, u.name); err != nil {
			t.Fatal(err)
		}
	}
	return h, authed, public, svc
}

// muxServe 带（可选）Bearer 令牌打请求。
func muxServe(t *testing.T, mux http.Handler, method, path, token, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("X-Report-Key", key)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestReportAuthAndRoundtrip(t *testing.T) {
	h, _, public, _ := newTestHandler(t)
	key := h.Eng.EffectiveReportKey()

	// 建仓（经管理面走一遍：不经 store 直写）
	h.Eng.UpsertRepo(&steward.Repo{Name: "demo", CloneURL: "https://git.example.com/grp/demo.git"})

	// 无密钥 / 错密钥 401
	if rec := muxServe(t, public, "POST", "/api/steward/report", "", "", `{}`); rec.Code != 401 {
		t.Fatalf("无密钥应 401，得到 %d", rec.Code)
	}
	if rec := muxServe(t, public, "POST", "/api/steward/report", "", "bad", `{}`); rec.Code != 401 {
		t.Fatalf("错密钥应 401，得到 %d", rec.Code)
	}
	// 带密钥上报成功
	rec := muxServe(t, public, "POST", "/api/steward/report", "", key,
		`{"repo":"demo","title":"修一下","body":"现象","reporter":"巡检"}`)
	if rec.Code != 200 {
		t.Fatalf("上报应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var p struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.ID == "" {
		t.Fatalf("上报响应异常: %s", rec.Body.String())
	}
	// 状态查询（同密钥）
	rec = muxServe(t, public, "GET", "/api/steward/report/"+p.ID, "", key, "")
	if rec.Code != 200 {
		t.Fatalf("状态查询应 200，得到 %d", rec.Code)
	}
}

func TestAdminGuard(t *testing.T) {
	_, authed, public, svc := newTestHandler(t)
	adminTok, _ := svc.Issue("boss", "admin", 1)
	userTok, _ := svc.Issue("guest", "user", 1)

	// 公开 mux 上的管理路径不存在（只在 authed 注册）
	if rec := muxServe(t, public, "GET", "/api/steward/overview", adminTok, "", ""); rec.Code != 404 {
		t.Fatalf("公开 mux 不应答管理面，得到 %d", rec.Code)
	}
	// 未登录 401（authed mux 经中间件）；user 403（RequireAdmin）；admin 200
	mw := svc.Middleware(authed)
	if rec := muxServe(t, mw, "GET", "/api/steward/overview", "", "", ""); rec.Code != 401 {
		t.Fatalf("未登录应 401，得到 %d", rec.Code)
	}
	if rec := muxServe(t, mw, "GET", "/api/steward/overview", userTok, "", ""); rec.Code != 403 {
		t.Fatalf("user 应 403，得到 %d", rec.Code)
	}
	if rec := muxServe(t, mw, "GET", "/api/steward/overview", adminTok, "", ""); rec.Code != 200 {
		t.Fatalf("admin 应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSeedEnvConnectionsOffWhenNoCred(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// 无 env 凭证：种子空跑后标记完成，不建连接
	SeedEnvConnections(h.Eng, GlEnv{URL: "https://git.example.com"}, "")
	if !h.Eng.EnvSeedDone() {
		t.Fatal("无凭证也应标记种子完成（防每轮启动重试）")
	}
	if len(h.Eng.ListConnections()) != 0 {
		t.Fatal("无凭证不应建连接")
	}
}
