// git.go — 仓库工作区 git 操作。凭据安全：remote 始终存净 URL，clone/fetch/push
// 时才把 token 拼进命令行 URL（进程环境短暂可见，不落 .git/config 与日志）。
package steward

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// runCmd 通用子进程：stdout 返回调用方，stderr 同时写日志并进错误信息。
func (e *Engine) runCmd(ctx context.Context, dir string, logw io.Writer, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out bytes.Buffer
	var errBuf bytes.Buffer
	if logw != nil {
		cmd.Stdout = io.MultiWriter(&out, logw)
	} else {
		cmd.Stdout = &out
	}
	cmd.Stderr = io.MultiWriter(&errBuf, logwOrDiscard(logw))
	if err := cmd.Run(); err != nil {
		tail := strings.TrimSpace(errBuf.String())
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

func logwOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// authURL 把凭据拼进 https URL（token 作用户名或 user:token）；非 http(s) 或
// 无 token 原样返回（ssh 形态不支持凭据注入，v1 仅 https）。
func authURL(raw, user, token string) string {
	if token == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return raw
	}
	if user == "" {
		u.User = url.User(token) // token 直接作用户名（GitHub PAT / Gitea 均接受）
	} else {
		u.User = url.UserPassword(user, token)
	}
	return u.String()
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
		args = append(args, authURL(r.CloneURL, gitUser, gitTok), ws)
		if _, err := e.runCmd(ctx, "", logw, e.gitBin(), args...); err != nil {
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
		if _, err := e.runCmd(ctx, ws, logw, e.gitBin(), "fetch", "--quiet",
			authURL(r.CloneURL, gitUser, gitTok), branch); err != nil {
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
	_, err := e.runCmd(ctx, ws, logw, e.gitBin(), "push", "--quiet",
		authURL(r.CloneURL, gitUser, gitTok), "HEAD:refs/heads/"+branch)
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
	if _, err := e.runCmd(ctx, ws, logw, e.gitBin(), "push", "--quiet",
		authURL(r.CloneURL, gitUser, gitTok), "refs/tags/"+tag); err != nil {
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
