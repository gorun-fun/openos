// gitlab_test.go — GitLab issue 同步单测：接管仓（platform=gitlab）抓取 open
// issue 转问题（字段映射 / 去重 / auto 模式直接入队 / label 过滤 / 翻页 /
// 鉴权头 / 404 可读错误 / 无凭据引导）、clone 地址平台识别、全局手动同步。
// 全部走 httptest 假 GitLab，不触外网。同步类测试经 addRepoDirect 直写状态
// 造仓库——绕开 UpsertRepo 的异步首同步，避免与显式同步调用竞态。
package steward

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newFakeGitLabIssues 假 GitLab：PAT /user 校验 + 项目 issues 接口（校验 Bearer
// 与 URL 编码的项目路径、state=opened；grp/demo 出两页共 3 条，grp/secret 404）。
func newFakeGitLabIssues(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pat-t" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"username": "wenjun", "name": "Wenjun"})
	})
	mux.HandleFunc("GET /api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("Authorization") != "Bearer pat-t" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("state") != "opened" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/grp%2Fsecret/issues":
			w.WriteHeader(http.StatusNotFound)
		default:
			if !strings.HasSuffix(r.URL.Path, "/issues") {
				http.NotFound(w, r)
				return
			}
			if page := r.URL.Query().Get("page"); page == "" || page == "1" {
				w.Header().Set("X-Next-Page", "2")
				_ = json.NewEncoder(w).Encode([]map[string]any{
					{"iid": 7, "title": "崩溃", "description": "打开就崩", "web_url": "http://x/grp/demo/-/issues/7", "labels": []string{"bug"}, "author": map[string]any{"username": "wenjun"}},
					{"iid": 8, "title": "文档", "web_url": "http://x/grp/demo/-/issues/8", "labels": []string{"docs"}, "author": map[string]any{"username": "wenjun"}},
				})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"iid": 9, "title": "性能", "web_url": "http://x/grp/demo/-/issues/9", "labels": []string{}, "author": map[string]any{"username": "li"}},
			})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

func newSyncEngine(t *testing.T) *Engine {
	t.Helper()
	eng, err := New(Options{
		DataDir: filepath.Join(t.TempDir(), "steward"), Timeout: time.Minute, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func addGitLabConn(t *testing.T, eng *Engine, base string) string {
	t.Helper()
	v, err := eng.AddConnection(context.Background(), ConnInput{Platform: PlatformGitLab, BaseURL: base, PAT: "pat-t"})
	if err != nil {
		t.Fatal(err)
	}
	return v.ID
}

// addRepoDirect 直写状态造仓库（不经 UpsertRepo 的异步首同步）。
func addRepoDirect(t *testing.T, eng *Engine, r Repo) string {
	t.Helper()
	var id string
	err := eng.st.Mutate(func(s *State) error {
		r.ID = newRepoID(s)
		now := time.Now()
		r.CreatedAt, r.UpdatedAt = now, now
		cp := r
		s.Repos = append(s.Repos, &cp)
		id = r.ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// 基本流：手动模式首拉 3 条（两页）、字段映射、ExtKey 去重。
func TestGitLabIssueSyncFlow(t *testing.T) {
	gl, hits := newFakeGitLabIssues(t)
	eng := newSyncEngine(t)
	ctx := context.Background()
	connID := addGitLabConn(t, eng, gl.URL)
	id := addRepoDirect(t, eng, Repo{
		Name: "demo", CloneURL: gl.URL + "/grp/demo.git",
		Mode: ModeManual, Platform: PlatformGitLab, ConnectionID: connID,
	})
	n, err := eng.SyncIssuesNow(ctx, id)
	if err != nil || n != 3 {
		t.Fatalf("首轮同步 n=%d err=%v", n, err)
	}
	var p7, p9 *Problem
	var total int
	eng.st.View(func(s *State) {
		total = len(s.Problems)
		for _, p := range s.Problems {
			switch p.ExtKey {
			case "gl:grp/demo/issue/7":
				cp := *p
				p7 = &cp
			case "gl:grp/demo/issue/9":
				cp := *p
				p9 = &cp
			}
		}
	})
	if total != 3 || p7 == nil || p9 == nil {
		t.Fatalf("问题落库异常 total=%d p7=%v p9=%v", total, p7, p9)
	}
	if p7.Source != SrcGitLab || p7.State != PNew || p7.Title != "崩溃" || p7.Body != "打开就崩" ||
		p7.Reporter != "wenjun" || len(p7.Labels) != 1 || p7.Labels[0] != "bug" ||
		p7.ExtURL != "http://x/grp/demo/-/issues/7" {
		t.Fatalf("issue#7 字段映射异常: %+v", p7)
	}
	if p9.Labels != nil && len(p9.Labels) != 0 { // 空 labels 归一为空切片而非 [""]
		t.Fatalf("issue#9 labels 异常: %+v", p9.Labels)
	}
	var synced bool
	eng.st.View(func(s *State) { synced = s.repo(id).LastSync != nil })
	if !synced {
		t.Fatal("LastSync 未更新")
	}
	if atomic.LoadInt32(hits) < 2 {
		t.Fatalf("翻页未生效 hits=%d", *hits)
	}
	n, err = eng.SyncIssuesNow(ctx, id)
	if err != nil || n != 0 {
		t.Fatalf("二次同步应全去重 n=%d err=%v", n, err)
	}
}

// auto 模式直接 confirmed；label 过滤只收命中的。
func TestGitLabIssueSyncAutoAndLabels(t *testing.T) {
	gl, _ := newFakeGitLabIssues(t)
	eng := newSyncEngine(t)
	ctx := context.Background()
	connID := addGitLabConn(t, eng, gl.URL)
	id := addRepoDirect(t, eng, Repo{
		Name: "auto", CloneURL: gl.URL + "/grp/auto.git", Mode: ModeAuto,
		Platform: PlatformGitLab, ConnectionID: connID, GHLabels: []string{"Bug "},
	})
	n, err := eng.SyncIssuesNow(ctx, id)
	if err != nil || n != 1 {
		t.Fatalf("label 过滤同步 n=%d err=%v", n, err)
	}
	eng.st.View(func(s *State) {
		for _, p := range s.Problems {
			if p.ExtKey != "gl:grp/auto/issue/7" || p.State != PConfirmed {
				t.Fatalf("auto 模式问题应直接入队: %+v", p)
			}
		}
	})
}

// 错误路径：项目不存在给可读错误；无凭据（引擎无任何 GitLab 连接）引导去仓库页。
func TestGitLabIssueSyncErrors(t *testing.T) {
	gl, _ := newFakeGitLabIssues(t)
	eng := newSyncEngine(t)
	ctx := context.Background()
	connID := addGitLabConn(t, eng, gl.URL)
	id404 := addRepoDirect(t, eng, Repo{
		Name: "secret", CloneURL: gl.URL + "/grp/secret.git",
		Mode: ModeManual, Platform: PlatformGitLab, ConnectionID: connID,
	})
	if _, err := eng.SyncIssuesNow(ctx, id404); err == nil || !strings.Contains(err.Error(), "不存在或无权限") {
		t.Fatalf("404 错误应可读: %v", err)
	}
	bare := newSyncEngine(t)
	idNoCred := addRepoDirect(t, bare, Repo{
		Name: "x", CloneURL: "https://git.example.com/a/b.git", Mode: ModeManual, Platform: PlatformGitLab,
	})
	if _, err := bare.SyncIssuesNow(ctx, idNoCred); err == nil || !strings.Contains(err.Error(), "仓库") {
		t.Fatalf("无凭据应引导连接账号: %v", err)
	}
}

// UpsertRepo 按 clone host 识别平台（连着 GitLab 连接的 host → gitlab；
// github.com → github；陌生 host 留空）；识别出的 GitLab 仓异步首同步落定。
func TestUpsertRepoDetectPlatform(t *testing.T) {
	gl, _ := newFakeGitLabIssues(t)
	eng := newSyncEngine(t)
	addGitLabConn(t, eng, gl.URL)
	cases := []struct {
		url, want string
	}{
		{gl.URL + "/grp/detect.git", PlatformGitLab},
		{"https://github.com/wenjun/x.git", PlatformGitHub},
		{"https://gitee.com/wenjun/x.git", ""},
	}
	var detectID string
	for i, c := range cases {
		r, err := eng.UpsertRepo(&Repo{
			Name: "r" + string(rune('a'+i)), CloneURL: c.url, Mode: ModeManual,
		})
		if err != nil {
			t.Fatal(err)
		}
		if r.Platform != c.want {
			t.Fatalf("%s 平台识别 %q，期望 %q", c.url, r.Platform, c.want)
		}
		if c.want == PlatformGitLab {
			detectID = r.ID
		}
	}
	// 等异步首同步落定（goroutine 不落定会与 TempDir 清理竞争）
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		done := false
		eng.st.View(func(s *State) {
			if r := s.repo(detectID); r != nil && r.LastSync != nil {
				done = true
			}
		})
		if done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("异步首同步未在限时内落定")
}

// 全局手动同步：正常仓计入新增，失败仓（404）进清单且不阻断。
func TestSyncAllIssuesNow(t *testing.T) {
	gl, _ := newFakeGitLabIssues(t)
	eng := newSyncEngine(t)
	ctx := context.Background()
	connID := addGitLabConn(t, eng, gl.URL)
	addRepoDirect(t, eng, Repo{
		Name: "demo", CloneURL: gl.URL + "/grp/demo.git",
		Mode: ModeManual, Platform: PlatformGitLab, ConnectionID: connID,
	})
	addRepoDirect(t, eng, Repo{
		Name: "secret", CloneURL: gl.URL + "/grp/secret.git", Mode: ModeManual, Platform: PlatformGitLab,
	})
	total, errs := eng.SyncAllIssuesNow(ctx)
	if total != 3 {
		t.Fatalf("总新增 %d，期望 3", total)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "secret") {
		t.Fatalf("失败清单异常: %v", errs)
	}
}
