// steward_test.go — 引擎单测：store 持久化、修复全链端到端（真实 git + 假 gh
// 脚本 + httptest 假 LLM）、无改动路径、tag 递增、状态机流转。
package steward

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// writeScript 落一个可执行 shell 脚本（充当假 gh）。
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// newFakeLLM 假 OpenAI 兼容 /v1/models。
func newFakeLLM(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			http.Error(w, `{"error":"invalid key"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []map[string]string{{"id": "m-test"}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// seedBare 建一个带一次提交与 tag v0.0.2 的 bare 远端，返回其路径。
func seedBare(t *testing.T, tmp string) string {
	t.Helper()
	bare := filepath.Join(tmp, "origin.git")
	seed := filepath.Join(tmp, "seed")
	runGit(t, "", "init", "--bare", "-b", "main", bare)
	runGit(t, "", "init", "-b", "main", seed)
	runGit(t, seed, "remote", "add", "origin", bare)
	if err := os.WriteFile(filepath.Join(seed, "app.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := []string{"-c", "user.name=t", "-c", "user.email=t@example.com"}
	runGit(t, seed, append(append([]string{}, id...), "add", "-A")...)
	runGit(t, seed, append(append([]string{}, id...), "commit", "-m", "init")...)
	runGit(t, seed, "tag", "-a", "v0.0.2", "-m", "v0.0.2")
	runGit(t, seed, "push", "-q", "origin", "main")
	runGit(t, seed, "push", "-q", "origin", "v0.0.2")
	return bare
}

func newTestEngine(t *testing.T, tmp, ghScript string) *Engine {
	t.Helper()
	eng, err := New(Options{
		DataDir:  filepath.Join(tmp, "steward"),
		GhBin:    ghScript,
		LLMBase:  newFakeLLM(t).URL,
		LLMKey:   "sk-test",
		DefModel: "m-test",
		Timeout:  2 * time.Minute,
		Logf:     t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func TestStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var key1 string
	s1.View(func(s *State) { key1 = s.Settings.ReportKey })
	if len(key1) < 16 {
		t.Fatalf("上报密钥未自动生成: %q", key1)
	}
	if err := s1.Mutate(func(s *State) error {
		s.Repos = append(s.Repos, &Repo{ID: "r-1", Name: "demo", CloneURL: "https://x/y.git", Mode: ModeManual})
		s.Problems = append(s.Problems, &Problem{ID: "p-1", RepoID: "r-1", Title: "t", State: PNew})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// 重新打开：落盘内容完整、密钥稳定
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s2.View(func(s *State) {
		if len(s.Repos) != 1 || s.Repos[0].Name != "demo" {
			t.Fatalf("仓库未持久化: %+v", s.Repos)
		}
		if len(s.Problems) != 1 || s.Problems[0].State != PNew {
			t.Fatalf("问题未持久化: %+v", s.Problems)
		}
		if s.Settings.ReportKey != key1 {
			t.Fatalf("密钥不稳定: %q != %q", s.Settings.ReportKey, key1)
		}
	})
}

func TestRunEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	bare := seedBare(t, tmp)
	gh := writeScript(t, tmp, "fake-gh.sh", "#!/bin/sh\nprintf 'fake fix\\n' >> app.txt\necho '[fake-gh] done'\n")
	eng := newTestEngine(t, tmp, gh)

	repo, err := eng.UpsertRepo(&Repo{Name: "demo", CloneURL: bare, Mode: ModeAuto, AutoRelease: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := eng.CreateProblem(ProblemInput{RepoID: repo.ID, Source: SrcManual, Title: "修一下 app.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if p.State != PConfirmed {
		t.Fatalf("auto 模式新问题应直接入队，得到 %s", p.State)
	}
	if !eng.workOne(context.Background()) {
		t.Fatal("worker 未取到任务")
	}

	prob, _ := eng.GetProblem(p.ID)
	if prob.State != PFixed {
		t.Fatalf("问题状态 = %s（error: %s）", prob.State, prob.Error)
	}
	runs := eng.ListRuns("", repo.ID)
	if len(runs) != 1 {
		t.Fatalf("记录数 = %d", len(runs))
	}
	run := runs[0]
	if run.State != RDone || run.Commit == "" {
		t.Fatalf("记录未完成: %+v", run)
	}
	if run.Tag != "v0.0.3" {
		t.Fatalf("tag 末位+1 失败: %q", run.Tag)
	}
	if run.Files != 1 || run.Ins != 1 {
		t.Fatalf("diff 统计异常: %+v", run)
	}

	// 远端验证：修复提交与 tag 都已推送
	subject := runGit(t, "", "--git-dir", bare, "log", "-1", "--format=%s")
	if !strings.Contains(subject, "fix: 修一下 app.txt") {
		t.Fatalf("远端最新提交异常: %q", subject)
	}
	tags := runGit(t, "", "--git-dir", bare, "tag")
	if !strings.Contains(tags, "v0.0.3") {
		t.Fatalf("远端缺 tag: %q", tags)
	}

	// gh 配置渲染：providers + 无人值守三件
	raw, err := os.ReadFile(ghSettingsPath(filepath.Join(tmp, "steward", "grf-home")))
	if err != nil {
		t.Fatalf("settings.json 未渲染: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["auto_approve"] != true || settings["policy"] != "write-workspace" {
		t.Fatalf("gh 配置缺无人值守项: %s", raw)
	}

	// 运行日志非空
	logRaw, _, err := eng.ReadRunLog(run.ID, 0)
	if err != nil || len(logRaw) == 0 {
		t.Fatalf("运行日志异常: %q %v", logRaw, err)
	}
}

func TestRunNoChanges(t *testing.T) {
	tmp := t.TempDir()
	bare := seedBare(t, tmp)
	gh := writeScript(t, tmp, "fake-gh.sh", "#!/bin/sh\ntrue\n")
	eng := newTestEngine(t, tmp, gh)

	repo, err := eng.UpsertRepo(&Repo{Name: "demo", CloneURL: bare})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := eng.CreateProblem(ProblemInput{RepoID: repo.ID, Source: SrcReport, Title: "空转"})
	if err := eng.ConfirmProblem(p.ID, nil, "tester", "tester-uid"); err != nil {
		t.Fatal(err)
	}
	eng.workOne(context.Background())

	prob, _ := eng.GetProblem(p.ID)
	if prob.State != PFailed {
		t.Fatalf("问题状态 = %s", prob.State)
	}
	runs := eng.ListRuns("", "")
	if runs[0].State != RNoChanges {
		t.Fatalf("记录状态 = %s", runs[0].State)
	}
	// 重试：问题回 confirmed
	if err := eng.RetryRun(runs[0].ID); err != nil {
		t.Fatal(err)
	}
	prob, _ = eng.GetProblem(p.ID)
	if prob.State != PConfirmed {
		t.Fatalf("重试后状态 = %s", prob.State)
	}
}

// TestRunReleaseFailurePreserved 发版失败不推翻修复：run 仍 done，但「发版失败」
// 错误须保留在 run 与问题上（此前先写 r.Error 再被终态 finish 的空串覆盖丢失）。
func TestRunReleaseFailurePreserved(t *testing.T) {
	tmp := t.TempDir()
	bare := seedBare(t, tmp)
	// 远端 pre-receive 拒收 tag：修复提交（分支推送）可过，tag 推送必败
	hook := filepath.Join(bare, "hooks", "pre-receive")
	body := "#!/bin/sh\nwhile read old new ref; do\n  case \"$ref\" in refs/tags/*) echo 'no tags' >&2; exit 1;; esac\ndone\n"
	if err := os.WriteFile(hook, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	gh := writeScript(t, tmp, "fake-gh.sh", "#!/bin/sh\nprintf 'fake fix\\n' >> app.txt\n")
	eng := newTestEngine(t, tmp, gh)

	repo, err := eng.UpsertRepo(&Repo{Name: "demo", CloneURL: bare, Mode: ModeAuto, AutoRelease: true})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := eng.CreateProblem(ProblemInput{RepoID: repo.ID, Source: SrcManual, Title: "修一下"})
	eng.workOne(context.Background())

	prob, _ := eng.GetProblem(p.ID)
	if prob.State != PFixed {
		t.Fatalf("问题状态 = %s（error: %s）", prob.State, prob.Error)
	}
	if !strings.Contains(prob.Error, "发版失败") {
		t.Fatalf("问题侧发版失败错误丢失: %q", prob.Error)
	}
	run := eng.ListRuns("", repo.ID)[0]
	if run.State != RDone || run.Commit == "" || run.Tag != "" {
		t.Fatalf("记录应 done 且无 tag: %+v", run)
	}
	if !strings.Contains(run.Error, "发版失败") {
		t.Fatalf("记录侧发版失败错误丢失: %+v", run)
	}
	// 远端：修复提交已推送、无新 tag
	subject := runGit(t, "", "--git-dir", bare, "log", "-1", "--format=%s")
	if !strings.Contains(subject, "fix: 修一下") {
		t.Fatalf("远端最新提交异常: %q", subject)
	}
	if tags := runGit(t, "", "--git-dir", bare, "tag"); strings.Contains(tags, "v0.0.3") {
		t.Fatalf("远端不应出现新 tag: %q", tags)
	}
}

// TestHasWork 队列粗判：confirmed 问题 / queued 转制才唤醒 workOne（空闲 tick
// 不再进 Mutate 全量落盘 state.json）。
func TestHasWork(t *testing.T) {
	tmp := t.TempDir()
	bare := seedBare(t, tmp)
	gh := writeScript(t, tmp, "fake-gh.sh", "#!/bin/sh\ntrue\n")
	eng := newTestEngine(t, tmp, gh)
	if eng.hasWork() {
		t.Fatal("空队列 hasWork 应为 false")
	}
	repo, err := eng.UpsertRepo(&Repo{Name: "demo", CloneURL: bare})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := eng.CreateProblem(ProblemInput{RepoID: repo.ID, Source: SrcManual, Title: "t"})
	if eng.hasWork() {
		t.Fatal("manual 模式新问题未确认，hasWork 应为 false")
	}
	_ = eng.ConfirmProblem(p.ID, nil, "tester", "tester-uid")
	if !eng.hasWork() {
		t.Fatal("confirmed 后 hasWork 应为 true")
	}
	eng.workOne(context.Background())
	if eng.hasWork() {
		t.Fatal("执行完 hasWork 应回 false")
	}
}

func TestProblemStateTransitions(t *testing.T) {
	tmp := t.TempDir()
	eng := newTestEngine(t, tmp, writeScript(t, tmp, "fake-gh.sh", "#!/bin/sh\ntrue\n"))
	repo, err := eng.UpsertRepo(&Repo{Name: "demo", CloneURL: "https://git.example.com/a/b.git"})
	if err != nil {
		t.Fatal(err)
	}

	// manual 模式：新问题停 new；驳回后不可再确认目标外的动作
	p1, _ := eng.CreateProblem(ProblemInput{RepoID: repo.ID, Source: SrcGitHub, Title: "a"})
	if p1.State != PNew {
		t.Fatalf("manual 新问题 = %s", p1.State)
	}
	if err := eng.RejectProblem(p1.ID, "t"); err != nil {
		t.Fatal(err)
	}
	prob, _ := eng.GetProblem(p1.ID)
	if prob.State != PRejected {
		t.Fatalf("驳回后 = %s", prob.State)
	}

	// 确认可覆盖发版
	p2, _ := eng.CreateProblem(ProblemInput{RepoID: repo.ID, Source: SrcGitHub, Title: "b"})
	no, yes := false, true
	if err := eng.ConfirmProblem(p2.ID, &no, "t", "t-uid"); err != nil {
		t.Fatal(err)
	}
	eng.ConfirmProblem(p2.ID, &yes, "t", "t-uid")
	prob, _ = eng.GetProblem(p2.ID)
	if prob.State != PConfirmed || !prob.Release {
		t.Fatalf("确认结果: %+v", prob)
	}

	// 上报按名定位 + 掩码：token 不出现在 API 视图
	eng.UpsertRepo(&Repo{ID: repo.ID, Name: "demo", CloneURL: "https://u:tok@git.example.com/a/b.git", Mode: ModeManual})
	rp, ok := eng.GetRepo(repo.ID)
	if !ok || rp.GitToken != "" || rp.CloneURL != "https://git.example.com/a/b.git" {
		t.Fatalf("凭据未拆出/未清洗: %+v", rp)
	}
	if !eng.HasGitToken(repo.ID) {
		t.Fatal("HasGitToken 应为真")
	}
	rep, err := eng.CreateReport(ReportInput{Repo: "demo", Title: "外部上报"})
	if err != nil || rep.State != PNew {
		t.Fatalf("上报失败: %v %+v", err, rep)
	}
	if _, err := eng.CreateReport(ReportInput{Repo: "不存在", Title: "x"}); err == nil {
		t.Fatal("未知仓库应报错")
	}
}

// pipeline 桥幂等：ExtKey 录入与按键查询（宿主 /steward/pipe/fix 依赖）。
func TestProblemByExtKey(t *testing.T) {
	tmp := t.TempDir()
	eng := newTestEngine(t, tmp, writeScript(t, tmp, "fake-gh.sh", "#!/bin/sh\ntrue\n"))
	repo, err := eng.UpsertRepo(&Repo{Name: "demo", CloneURL: "https://git.gorun.fun/a/b.git"})
	if err != nil {
		t.Fatal(err)
	}
	key := "gl:39/pipe/1024"
	if _, ok := eng.ProblemByExtKey(key); ok {
		t.Fatal("空库不应命中")
	}
	p, err := eng.CreateProblem(ProblemInput{
		RepoID: repo.ID, Source: SrcPipeline, ExtKey: key, ExtURL: "https://git.gorun.fun/a/b/-/pipelines/1024",
		Title: "[CI] a/b v0.0.18 #1024 失败", Reporter: "op",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != SrcPipeline || p.State != PNew {
		t.Fatalf("pipeline 来源问题: %+v", p)
	}
	got, ok := eng.ProblemByExtKey(key)
	if !ok || got.ID != p.ID {
		t.Fatalf("按键未命中: %+v %v", got, ok)
	}
	// failed 后可再确认复用（桥的重复转报走 ConfirmProblem 重入队）
	if err := eng.ConfirmProblem(p.ID, nil, "op", "op-uid"); err != nil {
		t.Fatal(err)
	}
	eng.st.Mutate(func(s *State) error { s.problem(p.ID).State = PFailed; return nil })
	if err := eng.ConfirmProblem(p.ID, nil, "op", "op-uid"); err != nil {
		t.Fatalf("failed 应可再确认: %v", err)
	}
}

// fakeGoder 假 GoderTasker：CreateTask 后模拟模型改文件，两轮 Poll 后完成。
type fakeGoder struct {
	uid, ws, task, model string
	polls                int
	done                 bool
}

func (f *fakeGoder) CreateTask(_ context.Context, t GoderTask) (string, string, string, error) {
	f.uid, f.ws, f.task, f.model = t.UID, t.Workspace, t.Task, t.Model
	// 直接在工作区制造修复改动（真实链路里由 gh 会话完成）
	if err := os.WriteFile(filepath.Join(t.Workspace, "app.txt"), []byte("fixed by goder\n"), 0o644); err != nil {
		return "", "", "", err
	}
	return "sess-1", "resp-1", "TokenHub/" + t.Model, nil
}

func (f *fakeGoder) PollTask(_ context.Context, uid, respID string) (GoderStatus, error) {
	f.polls++
	if f.polls < 2 {
		return GoderStatus{Status: goderRunning}, nil
	}
	f.done = true
	return GoderStatus{Status: goderCompleted, OutputText: "根因是 X，已改 app.txt。"}, nil
}

func (f *fakeGoder) CancelTask(_ context.Context, uid, respID string) error { return nil }

// goder 执行器端到端：会话归操作者、Run 带会话字段与摘要、日志不含会话转录、
// 提交推送照常。
func TestRunGoderExecutor(t *testing.T) {
	tmp := t.TempDir()
	bare := seedBare(t, tmp)
	fake := &fakeGoder{}
	eng, err := New(Options{
		DataDir:  filepath.Join(tmp, "steward"),
		Executor: "goder",
		Goder:    fake,
		DefModel: "glm-5.3-flash",
		Timeout:  time.Minute,
		Logf:     t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := eng.UpsertRepo(&Repo{Name: "demo", CloneURL: bare})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := eng.CreateProblem(ProblemInput{RepoID: repo.ID, Source: SrcManual, Title: "修一下 app.txt"})
	if err := eng.ConfirmProblem(p.ID, nil, "张三", "zhangsan"); err != nil {
		t.Fatal(err)
	}
	if !eng.workOne(context.Background()) {
		t.Fatal("worker 未取到任务")
	}
	prob, _ := eng.GetProblem(p.ID)
	if prob.State != PFixed {
		t.Fatalf("问题状态 = %s（error: %s）", prob.State, prob.Error)
	}
	run := eng.ListRuns("", repo.ID)[0]
	if run.State != RDone {
		t.Fatalf("记录状态 = %+v", run)
	}
	if run.Executor != "goder" || run.Operator != "zhangsan" || run.SessionID != "sess-1" || run.RespID != "resp-1" {
		t.Fatalf("goder 执行字段缺失: %+v", run)
	}
	if run.Model != "TokenHub/glm-5.3-flash" {
		t.Fatalf("模型应解析为全限定名: %q", run.Model)
	}
	if !strings.Contains(run.Summary, "根因是 X") {
		t.Fatalf("摘要未记录: %+v", run)
	}
	if fake.uid != "zhangsan" || !strings.Contains(fake.task, "修一下 app.txt") {
		t.Fatalf("任务入参异常: uid=%s task=%.40s", fake.uid, fake.task)
	}
	if !filepath.IsAbs(fake.ws) {
		t.Fatalf("工作区应为绝对路径: %q", fake.ws)
	}
	// 远端收到修复提交
	subject := runGit(t, "", "--git-dir", bare, "log", "-1", "--format=%s")
	if !strings.Contains(subject, "fix: 修一下 app.txt") {
		t.Fatalf("远端最新提交异常: %q", subject)
	}
	// 日志只含引擎阶段行：无会话转录、有会话指针
	logRaw, _, _ := eng.ReadRunLog(run.ID, 0)
	if !strings.Contains(string(logRaw), "Goder 会话已建") {
		t.Fatalf("日志缺会话建立行: %q", logRaw)
	}
	if strings.Contains(string(logRaw), "根因是 X") {
		t.Fatalf("日志不应含会话转录: %q", logRaw)
	}
}

// 无操作者兜底未配置时（外部上报/auto 入队），goder 执行给明确错误。
func TestRunGoderNoOperator(t *testing.T) {
	tmp := t.TempDir()
	bare := seedBare(t, tmp)
	eng, err := New(Options{
		DataDir:  filepath.Join(tmp, "steward"),
		Executor: "goder",
		Goder:    &fakeGoder{},
		Timeout:  time.Minute,
		Logf:     t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := eng.UpsertRepo(&Repo{Name: "demo", CloneURL: bare, Mode: ModeAuto})
	p, _ := eng.CreateProblem(ProblemInput{RepoID: repo.ID, Source: SrcReport, Title: "x"})
	eng.workOne(context.Background())
	prob, _ := eng.GetProblem(p.ID)
	if prob.State != PFailed || !strings.Contains(prob.Error, "OPENOS_STEWARD_GODER_UID") {
		t.Fatalf("应失败于缺操作者: %s %s", prob.State, prob.Error)
	}
}

func TestBumpPatchTag(t *testing.T) {
	cases := map[string]string{
		"v0.0.11":  "v0.0.12",
		"1.2.3":    "1.2.4",
		"v2.10.99": "v2.10.100",
		"release":  "v0.0.1",
		"":         "v0.0.1",
	}
	for in, want := range cases {
		if got := bumpPatchTag(in); got != want {
			t.Errorf("bumpPatchTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeDirName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"zhangsan", "zhangsan"},
		{"demo-repo_v2", "demo-repo_v2"},
		{"a/b", "a_b"},
		{`a\b`, "a_b"},
		{"  trim  ", "trim"},
		{"", "fb"},
		{".", "fb"},
		{"..", "fb"},
		{"中文仓库名", "中文仓库名"},
	}
	for _, c := range cases {
		if got := sanitizeDirName(c.in, "fb"); got != c.want {
			t.Errorf("sanitizeDirName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// goder 执行 + homes 根配置：工作区落操作者 home「代码管家」目录（文件 App
// 可见可下载），Repo.Workspace 记录路径，goder 会话 workspace 同址。
func TestRunGoderWorkspaceInOperatorHome(t *testing.T) {
	tmp := t.TempDir()
	bare := seedBare(t, tmp)
	fake := &fakeGoder{}
	eng, err := New(Options{
		DataDir:  filepath.Join(tmp, "steward"),
		HomesDir: filepath.Join(tmp, "homes"),
		Executor: "goder",
		Goder:    fake,
		DefModel: "glm-5.3-flash",
		Timeout:  time.Minute,
		Logf:     t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := eng.UpsertRepo(&Repo{Name: "demo", CloneURL: bare})
	p, _ := eng.CreateProblem(ProblemInput{RepoID: repo.ID, Source: SrcManual, Title: "修一下 app.txt"})
	if err := eng.ConfirmProblem(p.ID, nil, "张三", "zhangsan"); err != nil {
		t.Fatal(err)
	}
	if !eng.workOne(context.Background()) {
		t.Fatal("worker 未取到任务")
	}
	if prob, _ := eng.GetProblem(p.ID); prob.State != PFixed {
		t.Fatalf("问题状态 = %s（error: %s）", prob.State, prob.Error)
	}
	ws := filepath.Join(tmp, "homes", "zhangsan", "代码管家", "demo")
	if fake.ws != ws {
		t.Fatalf("goder 会话工作区 = %q, want %q", fake.ws, ws)
	}
	if _, err := os.Stat(filepath.Join(ws, "app.txt")); err != nil {
		t.Fatalf("工作区文件不可见: %v", err)
	}
	rp, _ := eng.GetRepo(repo.ID)
	if rp.Workspace != ws {
		t.Fatalf("Repo.Workspace = %q, want %q", rp.Workspace, ws)
	}
	// 无操作者问题（未配置兜底）退回内部 ws/
	p2, _ := eng.CreateProblem(ProblemInput{RepoID: repo.ID, Source: SrcReport, Title: "x"})
	if ws2 := eng.workspaceFor(&repo, eng.operatorOf(&p2)); ws2 != eng.workspace(repo.ID) {
		t.Fatalf("无操作者应退回内部 ws/: %q", ws2)
	}
}

// 旧内部工作区 ws/<ID> 惰性迁移：换 homes 布局后首次修复把已有克隆整体搬到
// 操作者 home（marker 随 .git 存活证明是 rename 而非重新 clone）。
func TestWorkspaceLegacyMigration(t *testing.T) {
	tmp := t.TempDir()
	bare := seedBare(t, tmp)
	gh := writeScript(t, tmp, "fake-gh.sh", "#!/bin/sh\nprintf 'fix\\n' >> app.txt\n")
	dataDir := filepath.Join(tmp, "steward")

	// 旧版引擎（无 HomesDir）：工作区落内部 ws/
	old, err := New(Options{DataDir: dataDir, GhBin: gh, LLMBase: newFakeLLM(t).URL, LLMKey: "sk-test", DefModel: "m-test", Timeout: time.Minute, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := old.UpsertRepo(&Repo{Name: "demo", CloneURL: bare})
	p1, _ := old.CreateProblem(ProblemInput{RepoID: repo.ID, Source: SrcReport, Title: "第一次"})
	if err := old.ConfirmProblem(p1.ID, nil, "张三", "zhangsan"); err != nil {
		t.Fatal(err)
	}
	old.workOne(context.Background())
	legacy := old.workspace(repo.ID)
	if _, err := os.Stat(filepath.Join(legacy, ".git")); err != nil {
		t.Fatalf("旧工作区未建立: %v", err)
	}
	marker := filepath.Join(legacy, ".git", "legacy-marker")
	if err := os.WriteFile(marker, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 新版引擎（同 DataDir + HomesDir）：迁移后修复
	eng, err := New(Options{DataDir: dataDir, HomesDir: filepath.Join(tmp, "homes"), GhBin: gh, LLMBase: newFakeLLM(t).URL, LLMKey: "sk-test", DefModel: "m-test", Timeout: time.Minute, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := eng.CreateProblem(ProblemInput{RepoID: repo.ID, Source: SrcManual, Title: "第二次"})
	if err := eng.ConfirmProblem(p2.ID, nil, "张三", "zhangsan"); err != nil {
		t.Fatal(err)
	}
	eng.workOne(context.Background())
	if prob, _ := eng.GetProblem(p2.ID); prob.State != PFixed {
		t.Fatalf("迁移后修复失败: %s（error: %s）", prob.State, prob.Error)
	}
	ws := filepath.Join(tmp, "homes", "zhangsan", "代码管家", "demo")
	if _, err := os.Stat(filepath.Join(ws, ".git", "legacy-marker")); err != nil {
		t.Fatalf("应整目录迁移而非重新 clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(legacy, ".git")); !os.IsNotExist(err) {
		t.Fatalf("旧位置应已清空: %v", err)
	}
}

// 用户手动改动保护：工作区在 home 下可被手改，下次修复前先 stash 备份再重置。
func TestStashManualChangesBeforeReset(t *testing.T) {
	tmp := t.TempDir()
	bare := seedBare(t, tmp)
	gh := writeScript(t, tmp, "fake-gh.sh", "#!/bin/sh\nprintf 'fix2\\n' >> app.txt\n")
	eng, err := New(Options{
		DataDir: filepath.Join(tmp, "steward"), HomesDir: filepath.Join(tmp, "homes"),
		GhBin: gh, LLMBase: newFakeLLM(t).URL, LLMKey: "sk-test", DefModel: "m-test",
		Timeout: time.Minute, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := eng.UpsertRepo(&Repo{Name: "demo", CloneURL: bare})
	p1, _ := eng.CreateProblem(ProblemInput{RepoID: repo.ID, Source: SrcManual, Title: "第一次"})
	if err := eng.ConfirmProblem(p1.ID, nil, "张三", "zhangsan"); err != nil {
		t.Fatal(err)
	}
	eng.workOne(context.Background())

	// 用户在 home 工作区里手动改文件 + 留未跟踪文件
	ws := filepath.Join(tmp, "homes", "zhangsan", "代码管家", "demo")
	if err := os.WriteFile(filepath.Join(ws, "app.txt"), []byte("手动改动\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "my-note.txt"), []byte("note\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p2, _ := eng.CreateProblem(ProblemInput{RepoID: repo.ID, Source: SrcManual, Title: "第二次"})
	if err := eng.ConfirmProblem(p2.ID, nil, "张三", "zhangsan"); err != nil {
		t.Fatal(err)
	}
	eng.workOne(context.Background())
	if prob, _ := eng.GetProblem(p2.ID); prob.State != PFixed {
		t.Fatalf("手改后修复失败: %s（error: %s）", prob.State, prob.Error)
	}
	stashes := runGit(t, ws, "stash", "list")
	if !strings.Contains(stashes, "steward 备份手动改动") {
		t.Fatalf("手动改动未 stash 备份: %q", stashes)
	}
	if _, err := os.Stat(filepath.Join(ws, "my-note.txt")); !os.IsNotExist(err) {
		t.Fatalf("未跟踪文件应随 stash 收起: %v", err)
	}
	// 备份可找回：stash 内容仍含手动改动
	popped := runGit(t, ws, "stash", "show", "-p", "stash@{0}")
	if !strings.Contains(popped, "手动改动") && !strings.Contains(popped, "my-note") {
		t.Fatalf("stash 内容异常: %q", popped)
	}
	// 本次修复的提交不受手改污染
	subject := runGit(t, "", "--git-dir", bare, "log", "-1", "--format=%s")
	if !strings.Contains(subject, "第二次") {
		t.Fatalf("远端最新提交异常: %q", subject)
	}
}
