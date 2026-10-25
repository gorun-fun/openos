// pipe_jobs.go — job 运行控制与日志（自 pipeline.go 按域拆分）。
package stewardapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"openos/server/internal/steward"
)

// ---- job 运行控制（manual 播放 / 失败重试 / 取消；v0.0.25 起，取代
// v0.0.18「不做 GitLab 重试/取消等直接写」原则中的 job 操作部分——发应用 =
// 播放 app-release，回滚 = 重放历史发布 pipeline 的 job（携带其原变量），重新
// 部署某版本 = 重试 tag 流水线的 deploy job；转修桥不变）----

// POST /api/steward/pipe/jobs/{projectID}/{jobID}/run — 运行 job（admin）：
// manual → play（沿用流水线既有变量）；success/failed/canceled → retry
// （按原 commit 与变量重跑）；运行/排队中拒绝。
func (h *Handler) handlePipeJobRun(w http.ResponseWriter, r *http.Request) error {
	pid, jid, err := pipeJobIDs(r)
	if err != nil {
		return err
	}
	var jb glJob
	if _, err := h.GL.Get(r.Context(), fmt.Sprintf("/projects/%d/jobs/%d", pid, jid), &jb); err != nil {
		return err
	}
	var act string
	switch jb.Status {
	case "manual":
		act = "play"
	case "running", "pending", "created", "preparing", "waiting_for_resource", "scheduled":
		return errBad("job 正在运行或排队中，无需运行")
	default:
		act = "retry"
	}
	return h.pipeJobAct(w, r, pid, jid, act)
}

// POST /api/steward/pipe/jobs/{projectID}/{jobID}/cancel — 取消运行/排队中的 job
// （admin；非运行态 GitLab 自会报错，原文透传）。
func (h *Handler) handlePipeJobCancel(w http.ResponseWriter, r *http.Request) error {
	pid, jid, err := pipeJobIDs(r)
	if err != nil {
		return err
	}
	return h.pipeJobAct(w, r, pid, jid, "cancel")
}

// pipeJobAct 执行 GitLab job 动作（play/retry/cancel），回传 GitLab 返回的
// 最新 job 视图并失效详情缓存。
func (h *Handler) pipeJobAct(w http.ResponseWriter, r *http.Request, pid, jid int, act string) error {
	if !h.GL.configured() {
		return errUpstream(errGitlabNoCred)
	}
	path := fmt.Sprintf("/projects/%d/jobs/%d/%s", pid, jid, act)
	st, raw, err := h.GL.post(r.Context(), path)
	if err != nil {
		return err
	}
	if st >= 300 {
		return errUpstream(fmt.Sprintf("GitLab %s → %d: %s", path, st, steward.ErrMessage(raw)))
	}
	var jb glJob
	_ = json.Unmarshal(raw, &jb)
	h.GL.invalidateDetails()
	h.ok(w, map[string]any{"job": toPipeJob(jb)})
	return nil
}

func pipeJobIDs(r *http.Request) (int, int, error) {
	pid, err := strconv.Atoi(r.PathValue("projectID"))
	if err != nil || pid <= 0 {
		return 0, 0, errBad("项目 ID 无效")
	}
	jid, err := strconv.Atoi(r.PathValue("jobID"))
	if err != nil || jid <= 0 {
		return 0, 0, errBad("job ID 无效")
	}
	return pid, jid, nil
}

// ---- job 日志（trace）----

// GET /api/steward/pipe/jobs/{projectID}/{jobID}/trace?offset=N — job 日志增量
// （admin）。GitLab trace 为纯文本且满是 ANSI 颜色码，服务端全量拉取、剥离
// 转义后按 offset 切增量返回；job 尚无日志（404）返回空。
func (h *Handler) handlePipeTrace(w http.ResponseWriter, r *http.Request) error {
	pid, err := strconv.Atoi(r.PathValue("projectID"))
	if err != nil || pid <= 0 {
		return errBad("项目 ID 无效")
	}
	jid, err := strconv.Atoi(r.PathValue("jobID"))
	if err != nil || jid <= 0 {
		return errBad("job ID 无效")
	}
	off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if off < 0 {
		off = 0
	}
	text, err := h.GL.cachedTrace(r.Context(), pid, jid)
	if err != nil {
		return err
	}
	if off > len(text) {
		off = 0 // job 重跑日志重置，前端 offset 越界 → 从头重发
	}
	tail := text[off:]
	for len(tail) > 0 && tail[0]&0xC0 == 0x80 {
		tail = tail[1:] // 避开切在多字节字符中间
	}
	h.ok(w, map[string]any{"log": tail, "offset": len(text), "size": len(text)})
	return nil
}

// cachedTrace 剥离 ANSI 后的完整日志 + TTL 缓存（运行中 job 前端 2s 跟随）。
// 取数不持锁（同 cachedPipelineDetail）。
func (g *GitLabClient) cachedTrace(ctx context.Context, projectID, jobID int) (string, error) {
	key := fmt.Sprintf("%d/%d", projectID, jobID)
	if v, ok := g.traceCache.Get(key); ok {
		return v, nil
	}
	st, raw, err := g.GetRaw(ctx, fmt.Sprintf("/projects/%d/jobs/%d/trace", projectID, jobID))
	if err != nil {
		return "", err
	}
	text := ""
	switch st {
	case http.StatusOK:
		text = stripANSI(string(raw))
	case http.StatusNotFound:
		// job 尚未产生日志（排队/未启动）
	default:
		return "", errUpstream(fmt.Sprintf("GitLab trace → %d: %s", st, steward.ErrMessage(raw)))
	}
	g.traceCache.Put(key, text)
	return text, nil
}

// ansiRe CSI / OSC / 单字符转义序列（GitLab trace 的颜色码与光标控制）。
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?<=>]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b[@-Z\\^_]")

func stripANSI(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n") // 进度行重绘的裸 \r 转行
}
