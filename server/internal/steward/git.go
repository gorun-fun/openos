// git.go — 仓库工作区 git 操作。凭据安全（2026-10 安全审计第二版）：remote 与
// 命令行恒为净 URL，clone/fetch/push 的凭据经 GIT_CONFIG_* 环境变量注入一次性
// credential helper——token 不进 argv（多用户主机 ps 可见）、不进 URL（git 网络
// 报错的 stderr 会把 URL 原样吐进运行日志与 API 错误回显）、不落 .git/config；
// 输出流另做 token 兜底脱敏（redactWriter），双保险。
package steward

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"openos/server/internal/safeenv"
)

// runCmd 通用子进程：stdout 返回调用方，stderr 同时写日志并进错误信息。
func (e *Engine) runCmd(ctx context.Context, dir string, logw io.Writer, name string, args ...string) (string, error) {
	return e.runCmdEnv(ctx, dir, logw, nil, nil, name, args...)
}

// runCmdEnv 带环境与脱敏清单的子进程：env 非空时**整体替换**子进程环境
// （调用方经 safeenv/envGitAuth 构造）；secrets 非空时 stderr 尾部与日志流中的
// 敏感值替换为 ***（错误串会进 Run.Error → 管理面/上报接口回显，必须过筛）。
func (e *Engine) runCmdEnv(ctx context.Context, dir string, logw io.Writer, env, secrets []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = env
	}
	var out bytes.Buffer
	var errBuf bytes.Buffer
	lw := logwOrDiscard(logw)
	if len(secrets) > 0 {
		lw = &redactWriter{w: lw, secrets: secrets}
	}
	if logw != nil {
		cmd.Stdout = io.MultiWriter(&out, lw)
	} else {
		cmd.Stdout = &out
	}
	cmd.Stderr = io.MultiWriter(&errBuf, lw)
	if err := cmd.Run(); err != nil {
		tail := strings.TrimSpace(errBuf.String())
		for _, sec := range secrets {
			if sec != "" {
				tail = strings.ReplaceAll(tail, sec, "***")
			}
		}
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		if tail != "" {
			return out.String(), fmt.Errorf("%s: %w: %s", name, err, tail)
		}
		return out.String(), err
	}
	return out.String(), nil
}

// redactWriter 把流经内容中的敏感值替换为 ***（覆盖日志落盘路径）。
type redactWriter struct {
	w       io.Writer
	secrets []string
}

func (rw *redactWriter) Write(p []byte) (int, error) {
	s := string(p)
	for _, sec := range rw.secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	if _, err := rw.w.Write([]byte(s)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func logwOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// shQuote 单引号包裹（内嵌单引号转义为 '\”），保证值作为 shell 单词安全。
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// credHelper 一次性 credential helper 的 shell 片段（经 GIT_CONFIG_VALUE_* 注入）。
// 凭据语义与旧 authURL 一致：无 user 时 token 作用户名（GitHub PAT / Gitea 均接受，
// password 位回填 token 兼容校验双凭据的平台）；有 user 时 user:token。
func credHelper(user, token string) string {
	u := user
	if u == "" {
		u = token
	}
	return "!f() { echo username=" + shQuote(u) + "; echo password=" + shQuote(token) + "; }; f"
}

// envGitAuth 带 git 凭据的子进程环境（safeenv 白名单 + helper 注入）。
// token 为空返回 nil = 沿用默认环境（本地/匿名仓库不受影响）。
func envGitAuth(user, token string) []string {
	if token == "" {
		return nil
	}
	return append(safeenv.Env(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0="+credHelper(user, token),
	)
}

// prepareWorkspace 确保工作区就绪并返回生效分支：目标工作区在操作者 home 下
// （见 workspaceFor），旧内部位置 ws/<ID> 与旧分组目录「自动修复/<仓库名>」
// 存在则先整体迁移过去（保留已克隆数据，失败丢弃重克隆）。首访 clone（随后把
// remote 换回净 URL），此后 fetch + 先 stash 备份未提交的手动改动（工作区对
// 用户可见，可能有手改；备份失败中止修复保护数据）+ reset --hard + clean -fd
// 回到远端分支头。gh 会话目录 .grf/ 写进 .git/info/exclude——既不被 clean
// 清掉，也不会被 add -A 收进提交。
func (e *Engine) prepareWorkspace(ctx context.Context, r *Repo, ws string, logw io.Writer) (string, error) {
	e.migrateLegacyHome(ws, logw)
	e.migrateLegacyWS(r, ws, logw)
	branch := r.Branch
	if _, err := os.Stat(filepath.Join(ws, ".git")); err != nil {
		_ = os.RemoveAll(ws)
		if err := os.MkdirAll(filepath.Dir(ws), 0o755); err != nil {
			return "", fmt.Errorf("创建工作区目录: %w", err)
		}
		args := []string{"clone", "--quiet"}
		if branch != "" {
			args = append(args, "--branch", branch)
		}
		gitUser, gitTok := e.repoAuth(ctx, r)
		args = append(args, r.CloneURL, ws)
		if _, err := e.runCmdEnv(ctx, "", logw, envGitAuth(gitUser, gitTok), []string{gitTok}, e.gitBin(), args...); err != nil {
			return "", fmt.Errorf("clone 失败: %w", err)
		}
		// remote 换回净 URL（clone 命令行的带凭据 URL 会存进 .git/config）
		if _, err := e.runCmd(ctx, ws, logw, e.gitBin(), "remote", "set-url", "origin", r.CloneURL); err != nil {
			return "", fmt.Errorf("清理 remote 凭据: %w", err)
		}
		if branch == "" {
			branch = e.detectDefaultBranch(ctx, ws, logw)
		}
	} else {
		if branch == "" {
			branch = firstNonEmpty(r.DefBranch, "main")
		}
		// 工作区在用户 home 下，可能被手动改过：reset 前先 stash 备份（留在
		// .git 内，用户可 git stash list 找回）；备份不了宁可中止也不毁数据
		if out, err := e.runCmd(ctx, ws, logw, e.gitBin(), "status", "--porcelain"); err == nil && strings.TrimSpace(out) != "" {
			msg := "steward 备份手动改动 " + time.Now().Format("0106 150405")
			if _, err := e.runCmd(ctx, ws, logw, e.gitBin(), "stash", "push", "-u", "-m", msg); err != nil {
				return "", fmt.Errorf("工作区有未提交的手动改动且备份失败，已中止修复保护数据: %w", err)
			}
			if logw != nil {
				fmt.Fprintf(logw, "[%s] 检测到手动改动，已 stash 备份（git stash list 可查看）\n", time.Now().Format("0106 15:04:05"))
			}
		}
		gitUser, gitTok := e.repoAuth(ctx, r)
		if _, err := e.runCmdEnv(ctx, ws, logw, envGitAuth(gitUser, gitTok), []string{gitTok}, e.gitBin(),
			"fetch", "--quiet", r.CloneURL, branch); err != nil {
			return "", fmt.Errorf("fetch %s 失败: %w", branch, err)
		}
		// checkout -B 钉回目标分支：工作区对用户可见，可能被切到别的分支——
		// reset --hard 只拽「当前所在分支」的指针（把用户本地分支指到目标分支
		// 头上）；-B 创建/复位目标分支并切换，改动只落在正确的分支上
		if _, err := e.runCmd(ctx, ws, logw, e.gitBin(), "checkout", "--quiet", "-B", branch, "FETCH_HEAD"); err != nil {
			return "", fmt.Errorf("checkout %s 失败: %w", branch, err)
		}
		if _, err := e.runCmd(ctx, ws, logw, e.gitBin(), "clean", "-fdq"); err != nil {
			return "", fmt.Errorf("clean 失败: %w", err)
		}
	}
	if err := excludeGitSessions(ws); err != nil {
		return "", err
	}
	return branch, nil
}

// migrateLegacyHome 旧分组目录「自动修复/<仓库名>」惰性迁移到「代码管家/」
// （v0.0.32 改名）：目标尚无克隆而旧分组目录下同名克隆存在则整体 rename
// （失败丢弃旧克隆，由调用方重新 clone）。
func (e *Engine) migrateLegacyHome(ws string, logw io.Writer) {
	if e.opts.HomesDir == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(ws, ".git")); err == nil {
		return // 新位置已有克隆
	}
	old := filepath.Join(filepath.Dir(filepath.Dir(ws)), "自动修复", filepath.Base(ws))
	if _, err := os.Stat(filepath.Join(old, ".git")); err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(ws), 0o755)
	if err := os.Rename(old, ws); err != nil {
		_ = os.RemoveAll(old)
		if logw != nil {
			fmt.Fprintf(logw, "[%s] 旧分组目录迁移失败，将重新克隆: %v\n", time.Now().Format("0106 15:04:05"), err)
		}
		return
	}
	if logw != nil {
		fmt.Fprintf(logw, "[%s] 已迁移工作区（自动修复 → 代码管家）：%s\n", time.Now().Format("0106 15:04:05"), ws)
	}
}

// migrateLegacyWS 旧内部工作区（ws/<ID>）惰性迁移到 home 下新位置：目标尚无
// 克隆而旧位置有则整体 rename（同盘原子；失败丢弃旧克隆，由调用方重新 clone）。
func (e *Engine) migrateLegacyWS(r *Repo, ws string, logw io.Writer) {
	if ws == e.workspace(r.ID) {
		return
	}
	if _, err := os.Stat(filepath.Join(ws, ".git")); err == nil {
		return // 新位置已有克隆
	}
	old := e.workspace(r.ID)
	if _, err := os.Stat(filepath.Join(old, ".git")); err != nil {
		return // 旧位置无克隆（或已被其他 operator 迁走），直接全新 clone
	}
	_ = os.MkdirAll(filepath.Dir(ws), 0o755)
	if err := os.Rename(old, ws); err != nil {
		_ = os.RemoveAll(old) // 迁移失败（如跨文件系统）：丢弃旧克隆走全新 clone
		if logw != nil {
			fmt.Fprintf(logw, "[%s] 旧工作区迁移失败，将重新克隆: %v\n", time.Now().Format("0106 15:04:05"), err)
		}
		return
	}
	if logw != nil {
		fmt.Fprintf(logw, "[%s] 已迁移工作区到操作者 home：%s\n", time.Now().Format("0106 15:04:05"), ws)
	}
}

// detectDefaultBranch 取远端默认分支（clone 后 origin/HEAD 指向）。
func (e *Engine) detectDefaultBranch(ctx context.Context, ws string, logw io.Writer) string {
	out, err := e.runCmd(ctx, ws, logw, e.gitBin(), "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if b := strings.TrimSpace(out); err == nil && strings.HasPrefix(b, "origin/") {
		return strings.TrimPrefix(b, "origin/")
	}
	if b := strings.TrimSpace(out); err == nil && b != "" {
		return b
	}
	return "main"
}

// excludeGitSessions 把 .grf/ 写进本仓库克隆的 .git/info/exclude（不改工作树）。
func excludeGitSessions(ws string) error {
	infoDir := filepath.Join(ws, ".git", "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return fmt.Errorf("创建 .git/info: %w", err)
	}
	path := filepath.Join(infoDir, "exclude")
	raw, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == ".grf/" {
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("写 info/exclude: %w", err)
	}
	defer f.Close()
	_, err = f.WriteString("\n# grf-os steward：gh 会话目录不参与提交\n.grf/\n")
	return err
}

// workingChanges 工作区是否有改动（含未跟踪文件；.grf/ 已被 exclude）。
func (e *Engine) workingChanges(ctx context.Context, ws string, logw io.Writer) (bool, error) {
	out, err := e.runCmd(ctx, ws, logw, e.gitBin(), "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return strings.TrimSpace(out) != "", nil
}

// commitAll 提交全部改动，返回新提交哈希。
func (e *Engine) commitAll(ctx context.Context, ws string, msg string, logw io.Writer) (string, error) {
	if _, err := e.runCmd(ctx, ws, logw, e.gitBin(), "add", "-A"); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}
	if _, err := e.runCmd(ctx, ws, logw, e.gitBin(),
		"-c", "user.name="+e.gitName(), "-c", "user.email="+e.gitEmail(),
		"commit", "--quiet", "-m", msg); err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}
	out, err := e.runCmd(ctx, ws, logw, e.gitBin(), "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// pushBranch 推送当前 HEAD 到远端分支（直推配置分支）。
func (e *Engine) pushBranch(ctx context.Context, r *Repo, ws, branch string, logw io.Writer) error {
	gitUser, gitTok := e.repoAuth(ctx, r)
	_, err := e.runCmdEnv(ctx, ws, logw, envGitAuth(gitUser, gitTok), []string{gitTok}, e.gitBin(), "push", "--quiet",
		r.CloneURL, "HEAD:refs/heads/"+branch)
	if err != nil {
		return fmt.Errorf("push %s 失败: %w", branch, err)
	}
	return nil
}

// nextTag 发版版本号：取最新 tag 末位 +1（各项目发布纪律），无 tag / 不合规
// 从 v0.0.1 起。
func (e *Engine) nextTag(ctx context.Context, ws string, logw io.Writer) string {
	out, err := e.runCmd(ctx, ws, logw, e.gitBin(), "describe", "--abbrev=0", "--tags")
	latest := strings.TrimSpace(out)
	if err != nil || latest == "" {
		return "v0.0.1"
	}
	return bumpPatchTag(latest)
}

var semverRe = regexp.MustCompile(`^(v?)(\d+)\.(\d+)\.(\d+)$`)

// bumpPatchTag v0.0.11 → v0.0.12（保留 v 前缀）；不合规回落 v0.0.1。
func bumpPatchTag(tag string) string {
	m := semverRe.FindStringSubmatch(strings.TrimSpace(tag))
	if m == nil {
		return "v0.0.1"
	}
	patch, err := strconv.Atoi(m[4])
	if err != nil {
		return "v0.0.1"
	}
	return fmt.Sprintf("%s%s.%s.%d", m[1], m[2], m[3], patch+1)
}

// releaseTag 打 annotated tag 并推送（此后由各仓 CI 接手构建）。
func (e *Engine) releaseTag(ctx context.Context, r *Repo, ws, tag, msg string, logw io.Writer) error {
	if _, err := e.runCmd(ctx, ws, logw, e.gitBin(),
		"-c", "user.name="+e.gitName(), "-c", "user.email="+e.gitEmail(),
		"tag", "-a", tag, "-m", msg); err != nil {
		return fmt.Errorf("打 tag %s: %w", tag, err)
	}
	gitUser, gitTok := e.repoAuth(ctx, r)
	if _, err := e.runCmdEnv(ctx, ws, logw, envGitAuth(gitUser, gitTok), []string{gitTok}, e.gitBin(), "push", "--quiet",
		r.CloneURL, "refs/tags/"+tag); err != nil {
		return fmt.Errorf("推送 tag %s: %w", tag, err)
	}
	return nil
}

// diffStatOf 统计最近一次提交的改动（文件数 / 增 / 删行）。
func (e *Engine) diffStatOf(ctx context.Context, ws string, logw io.Writer) (files, ins, dels int) {
	out, err := e.runCmd(ctx, ws, logw, e.gitBin(), "diff", "--numstat", "HEAD~1", "HEAD")
	if err != nil {
		// 首个提交无 HEAD~1
		out, _ = e.runCmd(ctx, ws, logw, e.gitBin(), "show", "--numstat", "--format=", "HEAD")
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		files++
		if f[0] != "-" {
			if n, err := strconv.Atoi(f[0]); err == nil {
				ins += n
			}
		}
		if f[1] != "-" {
			if n, err := strconv.Atoi(f[1]); err == nil {
				dels += n
			}
		}
	}
	return
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
