// runner.go — 修复执行状态机：prepare(clone/pull) → fixing(gh) → committing →
// pushing → releasing(可选) → done。全局单 worker 串行执行（并发修复的 LLM 成本
// 与推送冲突不可控）；队列即 store 里 confirmed 态问题（时间序），重启后
// running 中断的记录标记失败可重试。
package steward

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// runLog 运行日志：引擎自带行带时间戳，gh/git 原始输出直写。
type runLog struct{ f *os.File }

func (l *runLog) Write(p []byte) (int, error) { return l.f.Write(p) }

func (l *runLog) Printf(format string, args ...any) {
	fmt.Fprintf(l.f, "[%s] %s\n", time.Now().Format("0106 15:04:05"), fmt.Sprintf(format, args...))
}

func (e *Engine) openRunLog(runID string) (*runLog, error) {
	f, err := os.OpenFile(e.runLogPath(runID), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &runLog{f: f}, nil
}

// runTerminal 修复记录终态。
func runTerminal(state string) bool {
	switch state {
	case RDone, RNoChanges, RFailed, RCanceled:
		return true
	}
	return false
}

// recoverInterrupted 启动恢复：上次停机打断的运行/问题标记失败（可重试）。
func (e *Engine) recoverInterrupted() {
	e.st.Mutate(func(s *State) error {
		now := time.Now()
		for _, r := range s.Runs {
			if !runTerminal(r.State) {
				r.State, r.Error, r.EndedAt = RFailed, "宿主重启中断，可重试", &now
			}
		}
		for _, p := range s.Problems {
			if p.State == PRunning {
				p.State, p.Error = PFailed, "宿主重启中断，可重试"
			}
		}
		for _, c := range s.Conversions {
			if !convTerminal(c.State) {
				c.State, c.Error, c.EndedAt = CFailed, "宿主重启中断，可重新发起", &now
			}
		}
		return nil
	})
}

// workOne 取队执行一个 confirmed 问题；无可做返回 false。
func (e *Engine) workOne(ctx context.Context) bool {
	model := e.resolveModel(ctx) // 先解析（内部拿 store 读锁，不能在 Mutate 里调）
	var runID string
	e.st.Mutate(func(s *State) error {
		var p *Problem
		for _, c := range s.Problems {
			if c.State == PConfirmed {
				p = c
				break
			}
		}
		if p == nil {
			return nil
		}
		if s.repo(p.RepoID) == nil {
			p.State, p.Error = PFailed, "仓库已删除"
			return nil
		}
		p.State = PRunning
		run := &Run{
			ID: newRunID(s), ProblemID: p.ID, RepoID: p.RepoID,
			State: RPrepare, Model: model, Release: p.Release, StartedAt: time.Now(),
		}
		p.RunID = run.ID
		s.Runs = append(s.Runs, run)
		runID = run.ID
		return nil
	})
	if runID == "" {
		return false
	}
	e.execute(ctx, runID)
	return true
}

// execute 单次修复全流程。状态更新一律按 ID 经 Mutate 回查——State 切片会因
// append 扩容，启动时拿的指针可能已指向过期底层数组。
func (e *Engine) execute(ctx context.Context, runID string) {
	var prob Problem
	var repo Repo
	var model string
	e.st.View(func(s *State) {
		if r := s.run(runID); r != nil {
			model = r.Model
			if p := s.problem(r.ProblemID); p != nil {
				prob = *p
			}
			if rr := s.repo(r.RepoID); rr != nil {
				repo = *rr
			}
		}
	})
	if prob.ID == "" || repo.ID == "" {
		return
	}
	lg, err := e.openRunLog(runID)
	if err != nil {
		e.logf("[steward] 打开运行日志失败 %s: %v", runID, err)
		lg = nil
	}
	defer func() {
		if lg != nil {
			_ = lg.f.Sync()
			_ = lg.f.Close()
		}
	}()
	if lg != nil {
		lg.Printf("修复开始：%s（仓库 %s，来源 %s，发版 %v）", prob.Title, repo.Name, prob.Source, prob.Release)
	}

	setRun := func(f func(r *Run)) {
		_ = e.st.Mutate(func(s *State) error {
			if r := s.run(runID); r != nil {
				f(r)
			}
			return nil
		})
	}
	finish := func(runState, probState, errMsg string) {
		now := time.Now()
		setRun(func(r *Run) { r.State, r.Error, r.EndedAt = runState, errMsg, &now })
		if probState != "" {
			_ = e.st.Mutate(func(s *State) error {
				if p := s.problem(prob.ID); p != nil {
					p.State = probState
					p.Error = errMsg
				}
				return nil
			})
		}
		if lg != nil && errMsg != "" {
			lg.Printf("结束（%s）：%s", runState, errMsg)
		}
	}

	// 引擎级 ctx + 单次超时；注册取消口供 API 人工取消
	rctx, cancel := context.WithTimeout(ctx, e.opts.Timeout)
	e.setCurrent(runID, cancel)
	defer func() {
		cancel()
		e.clearCurrent(runID)
	}()

	// interrupted 区分中断原因：宿主停机 vs 人工取消（都不算修复本身失败）
	interrupted := func() (bool, string) {
		if rctx.Err() == nil {
			return false, ""
		}
		if ctx.Err() != nil {
			return true, "宿主停机中断，可重试"
		}
		return true, "人工取消，可重试"
	}

	// 1. prepare：clone 或 fetch+reset 到远端分支头。工作区落操作者 home
	//    「代码管家」目录（用户文件 App 可见可下载），无操作者退回内部 ws/
	op := e.operatorOf(&prob)
	ws := e.workspaceFor(&repo, op)
	branch, err := e.prepareWorkspace(rctx, &repo, ws, lg)
	if err != nil {
		if ok, msg := interrupted(); ok {
			finish(RCanceled, PFailed, msg)
			return
		}
		finish(RFailed, PFailed, "准备工作区失败: "+err.Error())
		return
	}
	if repo.Branch == "" && branch != "" && branch != repo.DefBranch {
		_ = e.st.Mutate(func(s *State) error {
			if r := s.repo(repo.ID); r != nil {
				r.DefBranch = branch
			}
			return nil
		})
	}
	_ = e.st.Mutate(func(s *State) error {
		if r := s.repo(repo.ID); r != nil {
			r.Workspace = ws
		}
		return nil
	})
	if lg != nil {
		lg.Printf("工作区就绪（%s@%s）", ws, branch)
	}

	// 2. fixing：Goder 会话执行（默认；会话落操作者 Goder「代码管家」分组，
	//    引擎只跟状态）或本地 gh 子进程（OPENOS_STEWARD_EXECUTOR=gh 兜底）
	setRun(func(r *Run) { r.State = RFixing })
	var summary string
	if e.useGoder() {
		setRun(func(r *Run) { r.Executor = "goder" })
		var err error
		summary, err = e.goderFix(rctx, runID, &prob, &repo, ws, model, lg)
		if err != nil {
			if ok, msg := interrupted(); ok {
				finish(RCanceled, PFailed, msg)
				return
			}
			if errors.Is(err, errCanceled) {
				finish(RCanceled, PFailed, "人工取消，可重试")
				return
			}
			finish(RFailed, PFailed, err.Error())
			return
		}
		if summary != "" {
			setRun(func(r *Run) { r.Summary = truncateRunesFor(summary, 1200) })
		}
	} else {
		setRun(func(r *Run) { r.Executor = "gh" })
		if err := e.writeGhSettings(rctx, model); err != nil {
			finish(RFailed, PFailed, "gh 配置失败: "+err.Error())
			return
		}
		if lg != nil {
			lg.Printf("gh 修复中（模型 %s）——以下为 gh 原始输出", model)
		}
		if err := e.ghRun(rctx, ws, buildTask(&prob, &repo), lg); err != nil {
			if ok, msg := interrupted(); ok {
				finish(RCanceled, PFailed, msg)
				return
			}
			finish(RFailed, PFailed, "gh 修复失败: "+err.Error())
			return
		}
	}

	// 3. committing
	setRun(func(r *Run) { r.State = RCommitting })
	changed, err := e.workingChanges(rctx, ws, lg)
	if err != nil {
		finish(RFailed, PFailed, "检查工作区失败: "+err.Error())
		return
	}
	if !changed {
		finish(RNoChanges, PFailed, "模型未产生改动（可调整描述后重试）")
		return
	}
	commitMsg := fmt.Sprintf("fix: %s（代码管家 %s）", firstLine(prob.Title), prob.ID)
	commit, err := e.commitAll(rctx, ws, commitMsg, lg)
	if err != nil {
		if ok, msg := interrupted(); ok {
			finish(RCanceled, PFailed, msg)
			return
		}
		finish(RFailed, PFailed, "提交失败: "+err.Error())
		return
	}
	setRun(func(r *Run) { r.Commit = commit })
	if lg != nil {
		lg.Printf("已提交 %s", commit)
	}

	// 4. pushing：直推配置分支
	setRun(func(r *Run) { r.State = RPushing })
	if err := e.pushBranch(rctx, &repo, ws, branch, lg); err != nil {
		if ok, msg := interrupted(); ok {
			finish(RCanceled, PFailed, msg)
			return
		}
		finish(RFailed, PFailed, err.Error())
		return
	}
	if lg != nil {
		lg.Printf("已推送 %s", branch)
	}

	// 5. releasing（可选）：tag 末位+1，此后由各仓 CI 接手构建。commit 已推送，
	//    发版失败不推翻修复本身：错误经 finish 带到 run/problem（此前先写
	//    r.Error 再被终态 finish 的空串覆盖，修复 done 但发版失败信息丢失）
	var runRelease bool
	e.st.View(func(s *State) {
		if r := s.run(runID); r != nil {
			runRelease = r.Release
		}
	})
	relErr := ""
	if runRelease {
		setRun(func(r *Run) { r.State = RReleasing })
		tag := e.nextTag(rctx, ws, lg)
		msg := fmt.Sprintf("release %s（代码管家 %s）", tag, prob.ID)
		if err := e.releaseTag(rctx, &repo, ws, tag, msg, lg); err != nil {
			if ok, m := interrupted(); ok {
				finish(RCanceled, PFailed, m)
				return
			}
			relErr = "发版失败: " + err.Error()
			if lg != nil {
				lg.Printf("发版失败（提交已推送）: %v", err)
			}
		} else {
			setRun(func(r *Run) { r.Tag = tag })
			if lg != nil {
				lg.Printf("已发版 %s", tag)
			}
		}
	}

	files, ins, dels := e.diffStatOf(rctx, ws, lg)
	finish(RDone, PFixed, relErr)
	setRun(func(r *Run) { r.Files, r.Ins, r.Dels = files, ins, dels })
	if lg != nil {
		lg.Printf("修复完成：%d 文件（+%d/-%d）", files, ins, dels)
	}
}

// setCurrent/clearCurrent/CancelCurrent 当前运行注册（人工取消入口）。
func (e *Engine) setCurrent(runID string, cancel context.CancelFunc) {
	e.mu.Lock()
	e.cur.id, e.cur.cancel = runID, cancel
	e.mu.Unlock()
}

func (e *Engine) clearCurrent(runID string) {
	e.mu.Lock()
	if e.cur.id == runID {
		e.cur.id, e.cur.cancel = "", nil
	}
	e.mu.Unlock()
}

// CancelRun 取消正在运行的修复记录；仅当前运行中的可取消。
func (e *Engine) CancelRun(runID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cur.id == runID && e.cur.cancel != nil {
		e.cur.cancel()
		return true
	}
	return false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
