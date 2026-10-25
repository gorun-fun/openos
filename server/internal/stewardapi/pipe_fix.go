// pipe_fix.go — 失败流水线转代码管家桥（自 pipeline.go 按域拆分）。
package stewardapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"openos/server/internal/steward"
)

// ---- 失败流水线转代码管家（进程内桥接 steward 引擎）----

// ensureGitlabRepo 按 CloneURL 匹配现有接管仓库：无则以 GitLab 推送凭据自动
// 注册（手动模式；推送分支与随修发版默认由调用方定，如流水线转修的分支流水线
// 修同分支），有则刷新推送凭据、其余配置不动。凭据形态随数据源：活动 GitLab
// 连接 → 挂 connection_id 动态解析（令牌运行期取，不拷贝短期值）；env →
// oauth2:当前凭证（password-grant 2h 过期故每次转修前刷新；PAT 稳定）。流水线
// 转修与手动录入选 GitLab 项目共用。返回仓库 ID。
func (h *Handler) ensureGitlabRepo(ctx context.Context, path, cloneURL, branch string, autoRelease bool) (string, error) {
	if cred, cerr := h.Eng.ActiveGitLab(ctx); cerr == nil && cred != nil {
		// 连接模式：仓库挂连接，凭据动态解析
		for _, rp := range h.Eng.ListRepos() {
			if rp.CloneURL != cloneURL {
				continue
			}
			if err := h.Eng.ConnectRepo(rp.ID, cred.ConnID, steward.PlatformGitLab); err != nil {
				return "", errBad("仓库挂接连接失败: " + err.Error())
			}
			return rp.ID, nil
		}
		rp, err := h.Eng.UpsertRepo(&steward.Repo{
			Name: path, CloneURL: cloneURL, Branch: branch,
			Mode: steward.ModeManual, AutoRelease: autoRelease,
			ConnectionID: cred.ConnID, Platform: steward.PlatformGitLab,
		})
		if err != nil {
			return "", errBad("仓库自动注册失败: " + err.Error())
		}
		return rp.ID, nil
	}
	tok, err := h.GL.endpoint(ctx)
	if err != nil {
		return "", err
	}
	for _, rp := range h.Eng.ListRepos() {
		if rp.CloneURL != cloneURL {
			continue
		}
		rp.GitUser = "oauth2"
		rp.GitToken = tok.token
		if _, err := h.Eng.UpsertRepo(&rp); err != nil {
			return "", errBad("仓库推送凭据刷新失败: " + err.Error())
		}
		return rp.ID, nil
	}
	rp, err := h.Eng.UpsertRepo(&steward.Repo{
		Name: path, CloneURL: cloneURL, Branch: branch,
		Mode: steward.ModeManual, Platform: steward.PlatformGitLab,
		GitUser: "oauth2", GitToken: tok.token, AutoRelease: autoRelease,
	})
	if err != nil {
		return "", errBad("仓库自动注册失败: " + err.Error())
	}
	return rp.ID, nil
}

type pipeFixIn struct {
	ProjectID  int  `json:"project_id"`
	PipelineID int  `json:"pipeline_id"`
	Confirm    bool `json:"confirm"` // true = 创建/复用后立即确认入队（AI 马上开修）
	Release    bool `json:"release"` // 随修复发版（confirm 时生效；tag 流水线前端默认勾选）
}

// POST /api/steward/pipe/fix — 失败流水线一键转修（admin）。服务端组装失败上下文
// （首个硬失败 job + 日志尾部 + yaml_errors），自动注册/刷新 steward 仓库（推送
// 凭据 oauth2:当前 GitLab 凭证，password-grant 模式 2h 过期故每次转修前刷新），
// 按 ExtKey 幂等录入问题后（可选）立即确认入队。全程进程内直调引擎，不走 HTTP。
func (h *Handler) handlePipeFix(w http.ResponseWriter, r *http.Request) error {
	in, err := readJSON[pipeFixIn](r)
	if err != nil {
		return err
	}
	if in.ProjectID <= 0 || in.PipelineID <= 0 {
		return errBad("project_id / pipeline_id 必填")
	}
	if !h.GL.configured() {
		return errUpstream(errGitlabNoCred)
	}
	if h.Eng == nil {
		return errBad("代码管家引擎未启用（OPENOS_STEWARD=off 或初始化失败），无法转修")
	}
	ctx := r.Context()
	d, err := h.GL.cachedPipelineDetail(ctx, in.ProjectID, in.PipelineID)
	if err != nil {
		return err
	}
	pl := d.Pipeline
	if pl.Status != "failed" {
		return errBad(fmt.Sprintf("流水线 #%d 状态为 %s，仅失败流水线可转修", in.PipelineID, pl.Status))
	}

	// 首个硬失败 job（failed 且非 allow_failure）及其日志尾部
	var failJob *pipeJob
	for i := range d.Jobs {
		if d.Jobs[i].Status == "failed" && !d.Jobs[i].AllowFailure {
			failJob = &d.Jobs[i]
			break
		}
	}
	traceTail := ""
	if failJob != nil {
		text, err := h.GL.cachedTrace(ctx, in.ProjectID, failJob.ID)
		if err != nil {
			return err
		}
		if len(text) > pipeTraceTail {
			text = text[len(text)-pipeTraceTail:]
		}
		traceTail = text
	}

	// 仓库：按克隆地址匹配现有接管仓库；无则自动注册（分支流水线修同分支，
	// tag 流水线修默认分支 + 默认随修发版）
	branch := ""
	if !pl.Tag {
		branch = pl.Ref
	}
	repoID, err := h.ensureGitlabRepo(ctx, d.Project.Path, d.Project.GitURL, branch, pl.Tag)
	if err != nil {
		return err
	}

	// 问题：ExtKey 幂等（进行中/待确认/修复失败可复用，失败态确认即重入队）
	extKey := fmt.Sprintf("gl:%d/pipe/%d", in.ProjectID, in.PipelineID)
	by := stewardID(r).Name
	byUID := stewardID(r).UID
	var prob steward.Problem
	created := true
	if prev, ok := h.Eng.ProblemByExtKey(extKey); ok {
		switch prev.State {
		case steward.PNew, steward.PConfirmed, steward.PRunning, steward.PFailed:
			prob, created = prev, false
		}
	}
	if created {
		title := fmt.Sprintf("[CI] %s %s 流水线 #%d 失败", d.Project.Path, pl.Ref, in.PipelineID)
		if failJob != nil {
			title += fmt.Sprintf("（%s/%s）", failJob.Stage, failJob.Name)
		}
		p, err := h.Eng.CreateProblem(steward.ProblemInput{
			RepoID: repoID, Source: steward.SrcPipeline, ExtKey: extKey, ExtURL: pl.WebURL,
			Title: title, Body: pipeFixBody(d, pl, failJob, traceTail), Reporter: by,
		})
		if err != nil {
			return errBad("问题录入失败: " + err.Error())
		}
		prob = p
	}
	if in.Confirm && (prob.State == steward.PNew || prob.State == steward.PFailed) {
		release := in.Release
		if err := h.Eng.ConfirmProblem(prob.ID, &release, by, byUID); err != nil {
			return errBad("确认入队失败: " + err.Error())
		}
		prob.State, prob.Release = steward.PConfirmed, release
	}
	h.ok(w, map[string]any{
		"problem_id": prob.ID, "repo_id": prob.RepoID,
		"state": prob.State, "release": prob.Release, "created": created,
	})
	return nil
}

// pipeFixBody 组装转修问题正文：失败上下文 + 日志尾部 + 修复要求。
func pipeFixBody(d *pipeDetail, pl pipePipeline, failJob *pipeJob, traceTail string) string {
	refKind := "分支"
	if pl.Tag {
		refKind = "tag"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "GitLab CI/CD 流水线失败（流水线 App 转修）。\n\n")
	fmt.Fprintf(&b, "仓库：%s（%s）\n", d.Project.Path, d.Project.WebURL)
	fmt.Fprintf(&b, "ref：%s（%s）\n", pl.Ref, refKind)
	fmt.Fprintf(&b, "提交：%s\n", pl.SHA)
	fmt.Fprintf(&b, "流水线：#%d（%s）状态 %s\n", pl.ID, pl.WebURL, pl.Status)
	if pl.YAMLErrors != "" {
		fmt.Fprintf(&b, "\n流水线 YAML 错误（.gitlab-ci.yml）：\n%s\n", pl.YAMLErrors)
	}
	if failJob != nil {
		reason := failJob.FailureReason
		if reason == "" {
			reason = "unknown"
		}
		fmt.Fprintf(&b, "失败任务：%s / %s（%s）\n", failJob.Stage, failJob.Name, reason)
		if traceTail != "" {
			fmt.Fprintf(&b, "\n失败任务日志（尾部）：\n%s\n", traceTail)
		}
	} else {
		b.WriteString("\n（无失败 job 明细——多为 .gitlab-ci.yml 配置/语法错误）\n")
	}
	b.WriteString("\n修复要求：定位失败原因并修复——可能是代码编译错误，也可能是 .gitlab-ci.yml 配置错误。")
	if pl.Tag {
		b.WriteString("修复提交将推送至默认分支并打 tag（末位+1）发版，由 CI 重跑验证。")
	} else {
		fmt.Fprintf(&b, "修复提交将推送至 %s 分支，由 CI 重跑验证。", pl.Ref)
	}
	return b.String()
}
