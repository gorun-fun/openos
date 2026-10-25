// problem.go — 问题队列 CRUD 与确认/驳回（自 steward.go 按域拆分）。
package steward

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ---- 问题 ----

// ProblemInput 问题录入参数（report / manual / pipeline 桥共用）。
type ProblemInput struct {
	RepoID   string
	Source   string
	ExtKey   string // 去重键（同来源外部标识；空 = 不去重）
	ExtURL   string
	Title    string
	Body     string
	Reporter string
	Labels   []string
}

// CreateProblem 录入问题；仓库 auto 模式直接入队。
func (e *Engine) CreateProblem(in ProblemInput) (Problem, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return Problem{}, errors.New("标题必填")
	}
	if len(in.Body) > 64<<10 {
		in.Body = in.Body[:64<<10]
	}
	var out Problem
	err := e.st.Mutate(func(s *State) error {
		r := s.repo(in.RepoID)
		if r == nil {
			return ErrRepoNotFound
		}
		p := &Problem{
			ID: newProblemID(s), RepoID: r.ID, Source: in.Source,
			ExtKey: in.ExtKey, ExtURL: in.ExtURL,
			Title: in.Title, Body: in.Body, Reporter: in.Reporter, Labels: in.Labels,
			State:     initialState(r.Mode),
			Release:   r.AutoRelease,
			CreatedAt: time.Now(),
		}
		s.Problems = append(s.Problems, p)
		out = *p
		return nil
	})
	if err == nil && out.State == PConfirmed {
		e.Notify()
	}
	return out, err
}

// ProblemByExtKey 按去重键查问题（时间正序首个命中；无则 false）。
// 供宿主 pipeline 桥等录入方做幂等：同键问题已存在时复用而非重复创建。
func (e *Engine) ProblemByExtKey(extKey string) (Problem, bool) {
	var out Problem
	var ok bool
	e.st.View(func(s *State) {
		for _, p := range s.Problems {
			if p.ExtKey == extKey {
				out, ok = *p, true
				break
			}
		}
	})
	return out, ok
}

// ReportInput 外部系统上报（可按仓库名或 ID 定位）。
type ReportInput struct {
	Repo     string
	Title    string
	Body     string
	Reporter string
	Labels   []string
}

// CreateReport 外部上报录入（report 来源）。
func (e *Engine) CreateReport(in ReportInput) (Problem, error) {
	var repoID string
	e.st.View(func(s *State) {
		for _, r := range s.Repos {
			if r.ID == in.Repo || r.Name == in.Repo {
				repoID = r.ID
				break
			}
		}
	})
	if repoID == "" {
		return Problem{}, errors.New("未找到目标仓库（repo 传仓库名或 ID）")
	}
	return e.CreateProblem(ProblemInput{
		RepoID: repoID, Source: SrcReport,
		Title: in.Title, Body: in.Body, Reporter: in.Reporter, Labels: in.Labels,
	})
}

// ListProblems 问题列表（时间倒序；state/repo/q 过滤，q 匹配标题与上报人）。
func (e *Engine) ListProblems(state, repoID, q string) []Problem {
	var out []Problem
	q = strings.ToLower(strings.TrimSpace(q))
	e.st.View(func(s *State) {
		for i := len(s.Problems) - 1; i >= 0; i-- {
			p := *s.Problems[i]
			if state != "" && p.State != state {
				continue
			}
			if repoID != "" && p.RepoID != repoID {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(p.Title+" "+p.Reporter), q) {
				continue
			}
			out = append(out, p)
		}
	})
	return out
}

func (e *Engine) GetProblem(id string) (Problem, bool) {
	var out Problem
	var ok bool
	e.st.View(func(s *State) {
		if p := s.problem(id); p != nil {
			out, ok = *p, true
		}
	})
	return out, ok
}

// ConfirmProblem 确认修复（new/failed/canceled → confirmed）；release 空指针 =
// 沿用仓库默认（问题创建时定格的值）。by/byUID 为确认人显示名与 openos 用户名
// （goder 执行器按 byUID 归属会话操作者）。
func (e *Engine) ConfirmProblem(id string, release *bool, by, byUID string) error {
	err := e.st.Mutate(func(s *State) error {
		p := s.problem(id)
		if p == nil {
			return errors.New("问题不存在")
		}
		switch p.State {
		case PNew, PFailed, PCanceled, PConfirmed:
		default:
			return fmt.Errorf("当前状态（%s）不可确认", p.State)
		}
		if release != nil {
			p.Release = *release
		}
		now := time.Now()
		p.State, p.Error, p.DecidedBy, p.DecidedUID, p.DecidedAt = PConfirmed, "", by, byUID, &now
		return nil
	})
	if err == nil {
		e.Notify()
	}
	return err
}

// RejectProblem 驳回问题（不再修复）。
func (e *Engine) RejectProblem(id string, by string) error {
	return e.st.Mutate(func(s *State) error {
		p := s.problem(id)
		if p == nil {
			return errors.New("问题不存在")
		}
		if p.State != PNew {
			return fmt.Errorf("仅待确认问题可驳回（当前 %s）", p.State)
		}
		now := time.Now()
		p.State, p.DecidedBy, p.DecidedAt = PRejected, by, &now
		return nil
	})
}

// CancelProblem 取消问题：运行中 → 取消当前修复；排队/待确认 → 直接取消。
func (e *Engine) CancelProblem(id string) error {
	var running bool
	var runID string
	e.st.View(func(s *State) {
		if p := s.problem(id); p != nil && p.State == PRunning {
			running = true
			runID = p.RunID
		}
	})
	if running {
		if !e.CancelRun(runID) {
			return errors.New("修复刚已结束，刷新查看结果")
		}
		return nil
	}
	return e.st.Mutate(func(s *State) error {
		p := s.problem(id)
		if p == nil {
			return errors.New("问题不存在")
		}
		switch p.State {
		case PNew, PConfirmed:
			p.State = PCanceled
			return nil
		}
		return fmt.Errorf("当前状态（%s）不可取消", p.State)
	})
}
