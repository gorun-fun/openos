// goder.go — Goder 执行器（v0.0.26 起默认）：修复经 openos 宿主 API 代操作者
// 创建 gh server 会话并后台执行，会话落在操作者自己的 Goder 里（「代码管家」
// 分组，可跟进续聊），引擎只做状态跟踪。
//
// 与 gh 执行器（gh.go，本地子进程兜底）的差异：
//   - 模型凭据不归宿主渲染：操作者的 goder 供应商（AISyncer 从 token-hub 套餐
//     派生）即真实执行方，模型按其套餐清单解析全限定名（glm-5.3-flash →
//     TokenHub/glm-5.3-flash），套餐未含给管理员可读错误；
//   - runs/<ID>.log 只记引擎阶段行（工作区就绪/会话已建/提交推送），不再录制
//     修复会话转录——会话本体在操作者 Goder，Run.SessionID 供深链跟进；
//   - 取消/超时经 API（POST cancel）而非杀进程组。
package steward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// gh server 响应状态（ORS 六值）；incomplete 视为需人工跟进的失败。
const (
	goderQueued     = "queued"
	goderRunning    = "in_progress"
	goderCompleted  = "completed"
	goderFailed     = "failed"
	goderCancelled  = "cancelled"
	goderIncomplete = "incomplete"
)

// GoderTasker openos 宿主 goder 任务 API 的最小面（api 层注入 hostsdk 实现，
// 测试可换假实现）。
type GoderTasker interface {
	// CreateTask 代用户创建后台会话任务，返回会话 ID、回合 ID、实际解析的模型。
	CreateTask(ctx context.Context, t GoderTask) (sessionID, respID, model string, err error)
	// PollTask 查回合状态（status/error/output_text）。
	PollTask(ctx context.Context, uid, respID string) (GoderStatus, error)
	// CancelTask 取消在跑回合（幂等）。
	CancelTask(ctx context.Context, uid, respID string) error
}

// GoderTask 一次无头修复任务的入参。
type GoderTask struct {
	UID       string // 操作者 openos 用户名（会话归属）
	Workspace string // 仓库工作区绝对路径
	Task      string // 任务全文（buildTask 产物）
	Model     string // 期望模型（裸名；空 = 操作者默认）
	Title     string // 分组里的会话标题
	Group     string // goder 分组名；空 = openos 默认分组（「自动修复」）
}

// goderGroup 会话在操作者 goder.index 里的分组名（v0.0.32 App 改名「代码管家」
// 起，新会话落新分组；openos 侧默认分组仍叫「自动修复」，故显式传入）。
const goderGroup = "代码管家"

// GoderStatus 回合状态快照。
type GoderStatus struct {
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	Incomplete string `json:"incomplete_reason,omitempty"`
	OutputText string `json:"output_text,omitempty"`
}

// useGoder 当前引擎是否走 Goder 执行器。
func (e *Engine) useGoder() bool {
	return e.opts.Executor != "gh" && e.opts.Goder != nil
}

// goderFix 执行修复的 goder 路径：建会话 → 轮询终态 → 返回摘要。
// ctx 取消/超时先 API 取消再返回 ctx.Err。完成返回 (摘要, nil)；失败返回
// 已格式化的错误信息（调用方据此 finish）。
func (e *Engine) goderFix(ctx context.Context, runID string, prob *Problem, repo *Repo, workspace, model string, lg *runLog) (string, error) {
	op := e.operatorOf(prob)
	if op == "" {
		return "", fmt.Errorf("未指定修复操作者：该问题无确认人（外部上报/自动入队），且未配置 OPENOS_STEWARD_GODER_UID")
	}
	// openos 侧要求工作区为已存在的绝对路径（本地 OPENOS_DATA 可能是相对路径）
	ws, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("工作区路径解析失败: %w", err)
	}
	task := buildTask(prob, repo)
	title := fmt.Sprintf("%s · %s", repo.Name, truncateRunesFor(firstLine(prob.Title), 24))
	sid, rid, usedModel, err := e.opts.Goder.CreateTask(ctx, GoderTask{
		UID: op, Workspace: ws, Task: task, Model: model, Title: title, Group: goderGroup,
	})
	if err != nil {
		return "", fmt.Errorf("创建 Goder 会话失败: %s", cleanHostErr(err))
	}
	e.setRunFields(runID, func(r *Run) {
		r.Executor, r.Operator, r.SessionID, r.RespID = "goder", op, sid, rid
		if usedModel != "" {
			r.Model = usedModel
		}
	})
	if lg != nil {
		lg.Printf("Goder 会话已建（操作者 %s，模型 %s）：%s", op, usedModel, sid)
	}
	return e.goderAwait(ctx, op, rid, sid, "修复", lg)
}

// goderAwait 轮询 Goder 回合到终态（修复与转制共用同一状态机，此前两份
// 逐行相同的「2s ticker → poll → 六状态 switch」，仅日志标签不同）。
// ctx 取消/超时先 API 取消（幂等，会话保留供跟进）再返回 ctx.Err；完成返回
// 回合摘要；失败返回已格式化的错误（label 进日志与失败前缀）。
func (e *Engine) goderAwait(ctx context.Context, op, respID, sessionID, label string, lg *runLog) (string, error) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = e.opts.Goder.CancelTask(cctx, op, respID)
			cancel()
			return "", ctx.Err()
		case <-tick.C:
		}
		st, err := e.opts.Goder.PollTask(ctx, op, respID)
		if err != nil {
			// 瞬时查询失败容忍重试（gh server 回收/重启窗口）；连续失败由外层超时兜底
			if lg != nil {
				lg.Printf("Goder 状态查询失败（将继续重试）: %s", cleanHostErr(err))
			}
			continue
		}
		switch st.Status {
		case goderQueued, goderRunning:
			continue
		case goderCompleted:
			if lg != nil {
				lg.Printf("Goder %s完成（会话 %s）", label, sessionID)
			}
			return st.OutputText, nil
		case goderFailed:
			msg := firstNonEmpty(st.Error, "模型执行失败（详见 Goder 会话）")
			return "", fmt.Errorf("Goder %s失败: %s", label, msg)
		case goderCancelled:
			return "", errCanceled
		case goderIncomplete:
			reason := firstNonEmpty(st.Incomplete, "等待交互")
			return "", fmt.Errorf("Goder 会话未完成（%s）：可在 Goder「代码管家」分组中跟进后续", reason)
		default:
			return "", fmt.Errorf("Goder 会话状态异常: %s", st.Status)
		}
	}
}

// errCanceled 轮询发现回合被取消（区分引擎侧 ctx 取消；两者都归 canceled 终态）。
var errCanceled = errors.New("会话已被取消")

// setRunFields 按 ID 更新 Run 字段（execute 内 setRun 的具名别名）。
func (e *Engine) setRunFields(runID string, f func(r *Run)) {
	_ = e.st.Mutate(func(s *State) error {
		if r := s.run(runID); r != nil {
			f(r)
		}
		return nil
	})
}

// cleanHostErr 从 hostsdk/openos 的错误串里剥出人类可读信息
// （hostsdk 报错形如 `POST /api/host/... → 400: {"error":"..."}`）。
func cleanHostErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if i := strings.Index(msg, `{"error":`); i >= 0 {
		var b struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(msg[i:]), &b) == nil && b.Error != "" {
			return b.Error
		}
	}
	return msg
}

func truncateRunesFor(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
