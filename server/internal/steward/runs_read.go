// runs_read.go — 修复记录读模型（自 steward.go 按域拆分）。
package steward

import (
	"errors"
	"io"
	"os"
)

// ---- 修复记录 ----

// ListRuns 记录列表（时间倒序；problem/repo 过滤）。
func (e *Engine) ListRuns(problemID, repoID string) []Run {
	var out []Run
	e.st.View(func(s *State) {
		for i := len(s.Runs) - 1; i >= 0; i-- {
			r := *s.Runs[i]
			if problemID != "" && r.ProblemID != problemID {
				continue
			}
			if repoID != "" && r.RepoID != repoID {
				continue
			}
			out = append(out, r)
		}
	})
	return out
}

func (e *Engine) GetRun(id string) (Run, bool) {
	var out Run
	var ok bool
	e.st.View(func(s *State) {
		if r := s.run(id); r != nil {
			out, ok = *r, true
		}
	})
	return out, ok
}

// ReadRunLog 读运行日志（修复记录与转制任务共用 runs/<ID>.log；从 offset 起，
// 最多 64KB），返回内容与新 offset。存在性守卫必须保留：id 直接拼日志路径，
// 不校验即成任意 .log 路径穿越读。此前只认 Run，转制详情（cv-*）永远 404 化。
func (e *Engine) ReadRunLog(id string, offset int64) ([]byte, int64, error) {
	if _, ok := e.GetRun(id); !ok {
		if _, ok := e.GetConversion(id); !ok {
			return nil, 0, errors.New("记录不存在")
		}
	}
	f, err := os.Open(e.runLogPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	buf := make([]byte, 64<<10)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return nil, offset, nil
	}
	return buf[:n], offset + int64(n), nil
}

// RetryRun 重试失败的修复（问题回 confirmed，复用上次的发版选择）。
func (e *Engine) RetryRun(runID string) error {
	var probID string
	err := e.st.Mutate(func(s *State) error {
		r := s.run(runID)
		if r == nil {
			return errors.New("记录不存在")
		}
		probID = r.ProblemID
		p := s.problem(probID)
		if p == nil {
			return errors.New("关联问题不存在")
		}
		if p.State == PFixed {
			return errors.New("问题已修复")
		}
		if p.State == PRunning || p.State == PConfirmed {
			return errors.New("该问题已在修复中/排队中")
		}
		p.State, p.Error = PConfirmed, ""
		return nil
	})
	if err == nil {
		e.Notify()
	}
	return err
}
