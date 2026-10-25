// github_test.go — GitHub issue 同步单测：接管仓（配 owner/repo）抓取 open issue
// 转问题（字段映射 / PR 过滤 / gh:<o/r>#<n> 去重 / label 过滤 / auto 直接入队 /
// Token 鉴权头 / 404 可读错误）。全部走 httptest 假 GitHub（引擎 ghAPI 字段注入），
// 不触外网；同步类用例直写状态构造仓库，避免与 UpsertRepo 异步首同步竞态
// （与 gitlab_test.go 同款纪律）。
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

// newFakeGitHub 假 GitHub issues API：带 Token 时校验 Bearer；oct/demo 出
// 2 issue + 1 PR；oct/none 404。
func newFakeGHIssues(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/oct/demo/issues", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("缺 GitHub Accept 头")
		}
		if r.Header.Get("Authorization") != "Bearer gh-t" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Query().Get("state") != "open" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"number": 11, "title": "崩溃", "body": "打开就崩", "html_url": "http://x/11",
				"labels": []map[string]any{{"name": "bug"}}, "user": map[string]any{"login": "wenjun"}},
			{"number": 12, "title": "带 PR 的条目", "html_url": "http://x/12",
				"pull_request": map[string]any{"url": "http://x/12/pull"}, "user": map[string]any{"login": "li"}},
			{"number": 13, "title": "文档", "html_url": "http://x/13",
				"labels": []map[string]any{{"name": "docs"}}, "user": map[string]any{"login": "wenjun"}},
		})
	})
	mux.HandleFunc("GET /repos/oct/none/issues", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

// addGHRepo 直写状态构造配 GitHub 同步的仓库（绕开 UpsertRepo 异步首同步）。
func addGHRepo(t *testing.T, eng *Engine, mode string, labels []string) string {
	t.Helper()
	var id string
	_ = eng.st.Mutate(func(s *State) error {
		r := &Repo{ID: newRepoID(s), Name: "demo", CloneURL: "https://github.com/oct/demo.git",
			Mode: mode, GHOwner: "oct", GHRepo: "demo", GHLabels: labels}
		s.Repos = append(s.Repos, r)
		id = r.ID
		return nil
	})
	return id
}

func newGHEngine(t *testing.T, base string) *Engine {
	t.Helper()
	eng, err := New(Options{
		DataDir: filepath.Join(t.TempDir(), "steward"), Timeout: time.Minute, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	eng.ghAPI = base
	// 同步令牌走设置页字段（githubToken 解析链末端），测试直写状态注入
	_ = eng.st.Mutate(func(s *State) error { s.Settings.GitHubToken = "gh-t"; return nil })
	return eng
}

func TestGitHubSync映射去重与PR过滤(t *testing.T) {
	srv, hits := newFakeGHIssues(t)
	eng := newGHEngine(t, srv.URL)
	id := addGHRepo(t, eng, ModeManual, nil)

	n, err := eng.syncRepoIssues(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 { // PR 条目不算
		t.Fatalf("期望收 2 个 issue（PR 过滤），得到 %d", n)
	}
	var probs []*Problem
	eng.st.View(func(s *State) { probs = s.Problems })
	if len(probs) != 2 {
		t.Fatalf("问题数 %d", len(probs))
	}
	byKey := map[string]*Problem{}
	for _, p := range probs {
		byKey[p.ExtKey] = p
	}
	p := byKey["gh:oct/demo#11"]
	if p == nil || p.Title != "崩溃" || p.Body != "打开就崩" || p.Reporter != "wenjun" ||
		p.Source != SrcGitHub || p.State != PNew || p.ExtURL != "http://x/11" {
		t.Fatalf("#11 字段映射不对: %+v", p)
	}
	if len(p.Labels) != 1 || p.Labels[0] != "bug" {
		t.Fatalf("#11 labels 应带 bug: %v", p.Labels)
	}
	if _, ok := byKey["gh:oct/demo#12"]; ok {
		t.Fatal("PR 不应转问题")
	}

	// 第二轮：去重（不重复建）
	if _, err := eng.syncRepoIssues(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	eng.st.View(func(s *State) { probs = s.Problems })
	if len(probs) != 2 {
		t.Fatalf("第二轮应去重，问题数 %d", len(probs))
	}
	if h := atomic.LoadInt32(hits); h != 2 {
		t.Fatalf("应恰好请求两轮，实际 %d", h)
	}
}

func TestGitHubSyncLabel过滤(t *testing.T) {
	srv, _ := newFakeGHIssues(t)
	eng := newGHEngine(t, srv.URL)
	id := addGHRepo(t, eng, ModeManual, []string{"Bug"}) // 大小写不敏感

	n, err := eng.syncRepoIssues(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("label 过滤后应只收 bug 一条，得到 %d", n)
	}
	eng.st.View(func(s *State) {
		if len(s.Problems) != 1 || !strings.Contains(s.Problems[0].Title, "崩溃") {
			t.Fatalf("过滤结果不对: %+v", s.Problems)
		}
	})
}

func TestGitHubSyncAuto直接入队(t *testing.T) {
	srv, _ := newFakeGHIssues(t)
	eng := newGHEngine(t, srv.URL)
	id := addGHRepo(t, eng, ModeAuto, nil)

	if _, err := eng.syncRepoIssues(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	eng.st.View(func(s *State) {
		for _, p := range s.Problems {
			if p.State != PConfirmed {
				t.Fatalf("auto 模式新问题应 confirmed 直接入队，实际 %s", p.State)
			}
		}
	})
}

func TestGitHubSync错误(t *testing.T) {
	srv, _ := newFakeGHIssues(t)
	eng := newGHEngine(t, srv.URL)

	// 404 → 可读错误（仓库不存在/无权限）
	id := addGHRepo(t, eng, ModeManual, nil)
	_ = eng.st.Mutate(func(s *State) error {
		if r := s.repo(id); r != nil {
			r.GHRepo = "none"
		}
		return nil
	})
	if _, err := eng.syncRepoIssues(context.Background(), id); err == nil || !strings.Contains(err.Error(), "不存在或无权限") {
		t.Fatalf("404 应给可读错误，得到 %v", err)
	}

	// 未配 owner/repo → 明确错误
	id2 := addGHRepo(t, eng, ModeManual, nil)
	_ = eng.st.Mutate(func(s *State) error {
		if r := s.repo(id2); r != nil {
			r.GHOwner, r.GHRepo = "", ""
		}
		return nil
	})
	if _, err := eng.syncRepoIssues(context.Background(), id2); err == nil || !strings.Contains(err.Error(), "未配置 GitHub 同步") {
		t.Fatalf("未配 owner/repo 应明确报错，得到 %v", err)
	}
}
