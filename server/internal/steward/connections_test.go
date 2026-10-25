// connections_test.go — 平台账号连接单测：账密兑换与缓存、掩码视图、活动
// 切换、凭据解析链（repoAuth）、账号仓库浏览（taken_over 回填）、远程建仓、
// env 种子不复活。全部走 httptest 假 GitLab / 假 GitHub（GHES 形态的自定义
// BaseURL），不触外网。
package steward

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakePlatform 假 GitLab（含 OAuth password grant）与假 GitHub（GHES /api/v3
// 形态）共用的服务端：按路径分发，计数 /oauth/token 兑换次数。
type fakePlatform struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	grants  int
	lastReq map[string]any // 最近一次写请求体（建仓断言用）
}

func newFakeGitLab(t *testing.T) *fakePlatform {
	t.Helper()
	f := &fakePlatform{t: t, lastReq: map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.grants++
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-gl", "expires_in": 7200})
	})
	mux.HandleFunc("GET /api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-gl" && r.Header.Get("Authorization") != "Bearer pat-gl" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"username": "wenjun", "name": "Wenjun", "avatar_url": "https://x/a.png"})
	})
	mux.HandleFunc("GET /api/v4/projects", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Next-Page", "")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": 42, "name": "demo", "path_with_namespace": "grp/demo",
			"web_url": f.srv.URL + "/grp/demo", "default_branch": "main",
			"visibility": "private", "last_activity_at": "2026-10-15T00:00:00Z",
		}})
	})
	mux.HandleFunc("POST /api/v4/projects", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&f.lastReq)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 43, "name": "new-app", "path_with_namespace": "wenjun/new-app",
			"web_url": f.srv.URL + "/wenjun/new-app", "default_branch": "main", "visibility": "private",
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func newFakeGitHub(t *testing.T) *fakePlatform {
	t.Helper()
	f := &fakePlatform{t: t, lastReq: map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghp-x" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "wenjun", "avatar_url": "https://x/b.png"})
	})
	mux.HandleFunc("GET /api/v3/user/repos", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<`+f.srv.URL+`/api/v3/user/repos?page=2>; rel="next"`)
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": 7, "name": "oss-tool", "full_name": "wenjun/oss-tool",
			"clone_url": "https://github.com/wenjun/oss-tool.git", "default_branch": "main",
			"private": true, "html_url": "https://github.com/wenjun/oss-tool", "pushed_at": "2026-10-14T00:00:00Z",
		}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func newConnEngine(t *testing.T) *Engine {
	t.Helper()
	eng, err := New(Options{
		DataDir: filepath.Join(t.TempDir(), "steward"), Timeout: time.Minute, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// 账密连接：验证回填 login、掩码视图、password-grant 兑换且缓存（两次解析
// 一次兑换）、活动切换与接任。
func TestGitLabConnPasswordFlow(t *testing.T) {
	gl := newFakeGitLab(t)
	eng := newConnEngine(t)
	ctx := context.Background()

	v, err := eng.AddConnection(ctx, ConnInput{Platform: PlatformGitLab, BaseURL: gl.srv.URL, User: "wenjun", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Login != "wenjun" || v.Active != true || v.PATSet || !v.PasswordSet {
		t.Fatalf("连接视图异常: %+v", v)
	}
	cred, err := eng.ActiveGitLab(ctx)
	if err != nil || cred == nil || cred.Token != "tok-gl" || cred.PAT {
		t.Fatalf("活动连接解析异常: %+v %v", cred, err)
	}
	if _, err := eng.ActiveGitLab(ctx); err != nil { // 第二次走缓存
		t.Fatal(err)
	}
	if gl.grants != 1 {
		t.Fatalf("password grant 兑换 %d 次，应缓存为 1 次", gl.grants)
	}

	// 第二个连接默认不活动；切换后原连接让位；删除活动连接后剩余接任
	v2, err := eng.AddConnection(ctx, ConnInput{Platform: PlatformGitLab, BaseURL: gl.srv.URL, PAT: "pat-gl"})
	if err != nil {
		t.Fatal(err)
	}
	if v2.Active {
		t.Fatalf("第二个 gitlab 连接不应自动活动: %+v", v2)
	}
	if err := eng.ActivateConnection(v2.ID); err != nil {
		t.Fatal(err)
	}
	if cred, _ := eng.ActiveGitLab(ctx); cred == nil || cred.ConnID != v2.ID || !cred.PAT {
		t.Fatalf("切换后应解析到 PAT 连接: %+v", cred)
	}
	if err := eng.DeleteConnection(v2.ID); err != nil {
		t.Fatal(err)
	}
	if !eng.HasActiveGitLab() {
		t.Fatal("删除活动连接后剩余 gitlab 连接应接任")
	}
}

// 凭据解析链：repo.connection_id → 连接令牌（gitlab oauth2 / github
// x-access-token）；无连接回落仓级凭据。
func TestRepoAuthViaConnection(t *testing.T) {
	gl := newFakeGitLab(t)
	gh := newFakeGitHub(t)
	eng := newConnEngine(t)
	ctx := context.Background()

	cv, err := eng.AddConnection(ctx, ConnInput{Platform: PlatformGitLab, BaseURL: gl.srv.URL, User: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := eng.UpsertRepo(&Repo{Name: "demo", CloneURL: gl.srv.URL + "/grp/demo.git", GitUser: "old", GitToken: "stale"})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ConnectRepo(repo.ID, cv.ID, PlatformGitLab); err != nil {
		t.Fatal(err)
	}
	rp, _ := eng.GetRepo(repo.ID) // ConnectRepo 改的是状态，本地快照需重取
	if u, tok := eng.repoAuth(ctx, &rp); u != "oauth2" || tok != "tok-gl" {
		t.Fatalf("gitlab 连接凭据解析异常: %s %s", u, tok)
	}
	gp, _ := eng.GetRepo(repo.ID)
	if gp.GitToken != "" || gp.GitUser != "" {
		t.Fatalf("挂接连接后应清空仓级拷贝凭据: %+v", gp)
	}

	gv, err := eng.AddConnection(ctx, ConnInput{Platform: PlatformGitHub, BaseURL: gh.srv.URL, PAT: "ghp-x"})
	if err != nil {
		t.Fatal(err)
	}
	ghRepo, _ := eng.UpsertRepo(&Repo{Name: "oss", CloneURL: "https://github.com/wenjun/oss-tool.git"})
	if err := eng.ConnectRepo(ghRepo.ID, gv.ID, PlatformGitHub); err != nil {
		t.Fatal(err)
	}
	ghSnap, _ := eng.GetRepo(ghRepo.ID)
	if u, tok := eng.repoAuth(ctx, &ghSnap); u != "x-access-token" || tok != "ghp-x" {
		t.Fatalf("github 连接凭据解析异常: %s %s", u, tok)
	}

	// 删除连接 → 回落仓级凭据
	if err := eng.DeleteConnection(cv.ID); err != nil {
		t.Fatal(err)
	}
	rp2, _ := eng.GetRepo(repo.ID)
	if rp2.ConnectionID != "" {
		t.Fatalf("连接删除后应回落仓级凭据: %+v", rp2)
	}

	// issue 同步令牌：github 连接 PAT 优先
	if got := eng.githubToken(); got != "ghp-x" {
		t.Fatalf("githubToken 应取连接 PAT: %q", got)
	}
}

// 账号仓库浏览：归一化投影 + taken_over 回填 + 翻页指针（GitHub Link header）。
func TestBrowseConnectionRepos(t *testing.T) {
	gl := newFakeGitLab(t)
	gh := newFakeGitHub(t)
	eng := newConnEngine(t)
	ctx := context.Background()

	cv, err := eng.AddConnection(ctx, ConnInput{Platform: PlatformGitLab, BaseURL: gl.srv.URL, PAT: "pat-gl"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.UpsertRepo(&Repo{Name: "demo", CloneURL: gl.srv.URL + "/grp/demo.git"}); err != nil {
		t.Fatal(err)
	}
	page, err := eng.BrowseConnectionRepos(ctx, cv.ID, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Repos) != 1 || !page.Repos[0].TakenOver || page.Repos[0].Visibility != "private" {
		t.Fatalf("gitlab 浏览投影异常: %+v", page)
	}

	gv, err := eng.AddConnection(ctx, ConnInput{Platform: PlatformGitHub, BaseURL: gh.srv.URL, PAT: "ghp-x"})
	if err != nil {
		t.Fatal(err)
	}
	gpage, err := eng.BrowseConnectionRepos(ctx, gv.ID, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(gpage.Repos) != 1 || gpage.NextPage != 2 || gpage.Repos[0].CloneURL != "https://github.com/wenjun/oss-tool.git" {
		t.Fatalf("github 浏览投影异常: %+v", gpage)
	}

	// 远程建仓：透传名称/可见性，返回归一化仓库
	created, err := eng.CreateConnectionProject(ctx, cv.ID, CreateProjectInput{Name: "new-app", Visibility: "private", AutoInit: true})
	if err != nil {
		t.Fatal(err)
	}
	if created.Path != "wenjun/new-app" || created.CloneURL != gl.srv.URL+"/wenjun/new-app.git" {
		t.Fatalf("建仓返回异常: %+v", created)
	}
	if gl.lastReq["name"] != "new-app" || gl.lastReq["visibility"] != "private" {
		t.Fatalf("建仓请求体异常: %+v", gl.lastReq)
	}
}

// 凭据无效给可读错误、不入库。
func TestConnectionBadCredentials(t *testing.T) {
	eng := newConnEngine(t)
	_, err := eng.AddConnection(context.Background(), ConnInput{Platform: PlatformGitHub, PAT: ""})
	if err == nil {
		t.Fatal("GitHub 缺 PAT 应报错")
	}
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(gl.Close)
	if _, err := eng.AddConnection(context.Background(), ConnInput{Platform: PlatformGitLab, BaseURL: gl.URL, User: "u", Password: "bad"}); err == nil {
		t.Fatal("错误账密应报错")
	}
	if n := len(eng.ListConnections()); n != 0 {
		t.Fatalf("失败连接不应入库: %d", n)
	}
}
