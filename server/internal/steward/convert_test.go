// convert_test.go — AI 转制上架单测：appId 解析、脚手架、货架发布（版本递增 +
// catalog 合并）、发起校验，以及端到端（真实 git 裸库 + 假 GitLab（建项目/流水线/
// 工件 zip）+ 假 goder 写 App.vue → 货架出现可安装条目）。
package steward

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConvAppID(t *testing.T) {
	cases := map[string]string{
		"https://github.com/user/My_Tool.git": "my-tool",
		"https://gitlab.com/grp/demo":         "demo",
		"https://github.com/a/b/c/deep.repo":  "deep-repo",
		"https://github.com/user/steward":     "steward", // 预置冲突在 CreateConversion 拦
	}
	for in, want := range cases {
		if got := convAppID(in); got != want {
			t.Errorf("convAppID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScaffoldAppProject(t *testing.T) {
	ws := t.TempDir()
	if err := scaffoldAppProject(ws, "demo-tool"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"package.json", "vite.config.js", ".gitlab-ci.yml", "src/main.js", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(ws, f)); err != nil {
			t.Errorf("脚手架缺 %s: %v", f, err)
		}
	}
	ci, _ := os.ReadFile(filepath.Join(ws, ".gitlab-ci.yml"))
	if !strings.Contains(string(ci), convArtifactJob) || !strings.Contains(string(ci), "artifacts") {
		t.Fatalf("CI 配置缺工件声明: %s", ci)
	}
	vc, _ := os.ReadFile(filepath.Join(ws, "vite.config.js"))
	if !strings.Contains(string(vc), "css-inject") || !strings.Contains(string(vc), "demo-tool") {
		t.Fatalf("vite 配置异常: %s", vc)
	}
}

func newConvertEngine(t *testing.T, marketDir string) *Engine {
	t.Helper()
	eng, err := New(Options{
		DataDir: filepath.Join(t.TempDir(), "steward"), Timeout: time.Minute,
		MarketDir: marketDir, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func TestPublishToMarket(t *testing.T) {
	market := t.TempDir()
	eng := newConvertEngine(t, market)
	m := appManifest{AppID: "demo", Name: "演示应用", Icon: "🧩", Category: "工具", Description: "一个演示"}

	v1, err := eng.publishToMarket(m, []byte("// app v1"))
	if err != nil || v1 != "v0.0.1" {
		t.Fatalf("首发版本 = %q %v, want v0.0.1", v1, err)
	}
	v2, err := eng.publishToMarket(m, []byte("// app v2"))
	if err != nil || v2 != "v0.0.2" {
		t.Fatalf("再发版本 = %q %v, want v0.0.2", v2, err)
	}
	cat := readMarketCatalog(market)
	ent := cat.Apps["demo"]
	if ent == nil || ent["entry"] != "files/demo/v0.0.2/demo.js" || ent["name"] != "演示应用" {
		t.Fatalf("catalog 条目异常: %+v", ent)
	}
	if _, has := ent["type"]; has {
		t.Fatalf("转制条目不应带 type=host（商店可安装）: %+v", ent)
	}
	// 旧版本目录保留（回滚能力）
	for _, v := range []string{"v0.0.1", "v0.0.2"} {
		if _, err := os.Stat(filepath.Join(market, "files", "demo", v, "demo.js")); err != nil {
			t.Errorf("版本产物缺失 %s: %v", v, err)
		}
	}
	// 货架清单
	out, err := eng.MarketApps()
	if err != nil {
		t.Fatal(err)
	}
	if out["market_configured"] != true {
		t.Fatalf("market_configured: %+v", out)
	}
}

func TestCreateConversionValidation(t *testing.T) {
	seedConn := func(eng *Engine) {
		t.Helper()
		_ = eng.st.Mutate(func(s *State) error {
			s.Connections = append(s.Connections, &Connection{ID: "c-1", Platform: PlatformGitLab, BaseURL: "https://git.example.com", PAT: "p", Login: "u", Active: true})
			return nil
		})
	}
	// 无活动连接
	eng := newConvertEngine(t, t.TempDir())
	if _, err := eng.CreateConversion(ConvInput{SourceURL: "https://github.com/a/demo", Operator: "op"}); err == nil || !strings.Contains(err.Error(), "GitLab 连接") {
		t.Fatalf("无连接应报错: %v", err)
	}
	// 无货架目录
	engNoMarket, err := New(Options{DataDir: filepath.Join(t.TempDir(), "s"), Timeout: time.Minute, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	seedConn(engNoMarket)
	if _, err := engNoMarket.CreateConversion(ConvInput{SourceURL: "https://github.com/a/demo", Operator: "op"}); err == nil || !strings.Contains(err.Error(), "货架目录") {
		t.Fatalf("无货架目录应报错: %v", err)
	}
	// 坏地址与预置 id
	eng2 := newConvertEngine(t, t.TempDir())
	seedConn(eng2)
	if _, err := eng2.CreateConversion(ConvInput{SourceURL: "ftp://x/y", Operator: "op"}); err == nil {
		t.Fatal("非 https 地址应报错")
	}
	if _, err := eng2.CreateConversion(ConvInput{SourceURL: "https://github.com/a/steward", Operator: "op"}); err == nil || !strings.Contains(err.Error(), "预置") {
		t.Fatalf("预置 id 应报错: %v", err)
	}
}

// fakeConvertGoder 假 GoderTasker：在转制工作区写 App.vue 与 manifest（真实链路
// 由 gh 会话完成）。
type fakeConvertGoder struct{ ws string }

func (f *fakeConvertGoder) CreateTask(_ context.Context, t GoderTask) (string, string, string, error) {
	f.ws = t.Workspace
	_ = os.MkdirAll(filepath.Join(t.Workspace, "src"), 0o755)
	app := `<script setup lang="ts">
import { OsButton } from '@openos/ui'
</script>
<template><div class="app"><OsButton>演示</OsButton></div></template>
`
	if err := os.WriteFile(filepath.Join(t.Workspace, "src", "App.vue"), []byte(app), 0o644); err != nil {
		return "", "", "", err
	}
	appid := filepath.Base(t.Workspace)
	mf := fmt.Sprintf(`{"id":%q,"name":"演示应用","icon":"🧩","category":"工具","description":"端到端测试"}`, appid)
	if err := os.WriteFile(filepath.Join(t.Workspace, "manifest.json"), []byte(mf), 0o644); err != nil {
		return "", "", "", err
	}
	return "sess-cv", "resp-cv", "TokenHub/m", nil
}

func (f *fakeConvertGoder) PollTask(_ context.Context, uid, respID string) (GoderStatus, error) {
	return GoderStatus{Status: goderCompleted, OutputText: "已把源项目包装为演示应用。"}, nil
}

func (f *fakeConvertGoder) CancelTask(_ context.Context, uid, respID string) error { return nil }

// TestConvertEndToEnd 全链路：裸库源 → 假 GitLab（建项目 + 流水线 success + 工件
// zip）→ 假 goder → 货架发布 done。
func TestConvertEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	bare := seedBare(t, tmp) // 源仓库（含一次提交）
	market := filepath.Join(tmp, "market")

	// 目标裸库（假 GitLab 的建项目响应把克隆地址映射到本地裸库，push 直达）
	targetBare := filepath.Join(tmp, "target.git")
	runGit(t, "", "init", "--bare", "-b", "main", targetBare)

	// 工件 zip（dist/demo.js）
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	w, _ := zw.Create("dist/demo.js")
	_, _ = w.Write([]byte("export { default } from 'x'\n// built entry"))
	_ = zw.Close()
	zipBytes := zipBuf.Bytes()

	var createdProject int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-gl", "expires_in": 7200})
	})
	mux.HandleFunc("GET /api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"username": "octocat"})
	})
	mux.HandleFunc("POST /api/v4/projects", func(w http.ResponseWriter, r *http.Request) {
		createdProject++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 99, "name": "openos-app-demo", "path_with_namespace": "wenjun/openos-app-demo",
			"web_url": strings.TrimSuffix(targetBare, ".git"), "default_branch": "main", "visibility": "private",
		})
	})
	mux.HandleFunc("GET /api/v4/projects/99/pipelines", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 7, "status": "success"}})
	})
	mux.HandleFunc("GET /api/v4/projects/99/jobs/artifacts/main/download", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("job") != convArtifactJob {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	})
	gl := httptest.NewServer(mux)
	t.Cleanup(gl.Close)

	eng, err := New(Options{
		DataDir: filepath.Join(tmp, "steward"), Timeout: time.Minute, MarketDir: market,
		HomesDir: filepath.Join(tmp, "homes"), Executor: "goder",
		Goder: &fakeConvertGoder{}, DefModel: "m", Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = eng.st.Mutate(func(s *State) error {
		s.Connections = append(s.Connections, &Connection{ID: "c-1", Platform: PlatformGitLab, BaseURL: gl.URL, PAT: "p", Login: "octocat", Active: true})
		return nil
	})

	cv, err := eng.CreateConversion(ConvInput{SourceURL: bare, Operator: "zhangsan", By: "张三"})
	if err != nil {
		t.Fatal(err)
	}
	if cv.AppID != "origin" { // 裸库路径末段是 origin.git → origin
		t.Fatalf("appid = %q", cv.AppID)
	}
	if !eng.workOneConvert(context.Background()) {
		t.Fatal("worker 未取到转制任务")
	}
	got, _ := eng.GetConversion(cv.ID)
	if got.State != CDone {
		t.Fatalf("转制终态 = %s（error: %s）", got.State, got.Error)
	}
	if got.Version != "v0.0.1" || got.PipelineID != 7 || got.TargetProject != 99 || got.SessionID != "sess-cv" {
		t.Fatalf("转制记录字段异常: %+v", got)
	}
	// 货架条目 + 产物
	ent := readMarketCatalog(market).Apps["origin"]
	if ent == nil || ent["name"] != "演示应用" || ent["entry"] != "files/origin/v0.0.1/origin.js" {
		t.Fatalf("货架条目异常: %+v", ent)
	}
	js, err := os.ReadFile(filepath.Join(market, "files", "origin", "v0.0.1", "origin.js"))
	if err != nil || !strings.Contains(string(js), "built entry") {
		t.Fatalf("货架产物异常: %v", err)
	}
	// 工作区在操作者 home 转制目录
	if !strings.Contains(got.Workspace, filepath.Join("zhangsan", "代码管家", "转制")) {
		t.Fatalf("转制工作区位置异常: %q", got.Workspace)
	}
	if createdProject != 1 {
		t.Fatalf("目标项目创建 %d 次", createdProject)
	}

	// 同 appid 二次转制（升级版本）：目标项目复用（不再撞 GitLab 同名项目），
	// 版本末位 +1
	cv2, err := eng.CreateConversion(ConvInput{SourceURL: bare, Operator: "zhangsan", By: "张三"})
	if err != nil {
		t.Fatal(err)
	}
	if !eng.workOneConvert(context.Background()) {
		t.Fatal("worker 未取到二次转制任务")
	}
	got2, _ := eng.GetConversion(cv2.ID)
	if got2.State != CDone {
		t.Fatalf("二次转制终态 = %s（error: %s）", got2.State, got2.Error)
	}
	if got2.Version != "v0.0.2" {
		t.Fatalf("二次转制版本 = %q，期望 v0.0.2", got2.Version)
	}
	if got2.TargetProject != 99 || createdProject != 1 {
		t.Fatalf("目标项目应复用（创建 %d 次，project %d）", createdProject, got2.TargetProject)
	}
	// 转制详情日志可读（ReadRunLog 此前只认修复记录，转制 id 永远「记录不存在」）
	logRaw, _, err := eng.ReadRunLog(cv2.ID, 0)
	if err != nil || len(logRaw) == 0 {
		t.Fatalf("转制日志读取异常: %q %v", logRaw, err)
	}
}
