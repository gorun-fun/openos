// stewardapi.go — 「代码管家」REST 面的领域 handler（连接/上报/总览/仓库/
// 问题/修复记录/设置/转制）。路由注册见 Register，装配见 engine.go。
package stewardapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"openos/server/internal/auth"
	"openos/server/internal/steward"
)

// Register 注册 /api/steward/* 全部路由：管理面挂 authed（登录 mux，逐端点
// RequireAdmin——admin 专属，与 grf-os 宿主同语义），对外上报入口挂公开 mux
//（X-Report-Key 共享密钥自鉴权，与本项目机对机鉴权惯例一致）。引擎停用时
// 仍注册：管理面返回明确错误（App 内提示），上报入口无密钥可验自然 401。
func (h *Handler) Register(authed, public *http.ServeMux) {
	ad := func(pattern string, fn func(w http.ResponseWriter, r *http.Request) error) {
		authed.Handle(pattern, auth.RequireAdmin(wrap(fn)))
	}

	// 公开路由（上报密钥自鉴权）
	public.HandleFunc("POST /api/steward/report", wrap(h.handleStewardReport))
	public.HandleFunc("GET /api/steward/report/{id}", wrap(h.handleStewardReportStatus))

	ad("GET /api/steward/overview", h.handleStewardOverview)
	ad("GET /api/steward/repos", h.handleStewardReposList)
	ad("POST /api/steward/repos", h.handleStewardRepoCreate)
	ad("PUT /api/steward/repos/{id}", h.handleStewardRepoUpdate)
	ad("DELETE /api/steward/repos/{id}", h.handleStewardRepoDelete)
	ad("POST /api/steward/repos/{id}/sync-issues", h.handleStewardRepoSync)
	ad("POST /api/steward/sync-issues", h.handleStewardSyncAll)
	ad("GET /api/steward/problems", h.handleStewardProblems)
	ad("POST /api/steward/problems", h.handleStewardProblemCreate)
	ad("POST /api/steward/problems/{id}/confirm", h.handleStewardProblemConfirm)
	ad("POST /api/steward/problems/{id}/reject", h.handleStewardProblemReject)
	ad("POST /api/steward/problems/{id}/cancel", h.handleStewardProblemCancel)
	ad("GET /api/steward/runs", h.handleStewardRuns)
	ad("GET /api/steward/runs/{id}", h.handleStewardRunGet)
	ad("POST /api/steward/runs/{id}/cancel", h.handleStewardRunCancel)
	ad("POST /api/steward/runs/{id}/retry", h.handleStewardRunRetry)
	ad("GET /api/steward/settings", h.handleStewardSettingsGet)
	ad("PUT /api/steward/settings", h.handleStewardSettingsPut)

	// 平台账号连接（登录方式之二：GitLab 账密/PAT、GitHub PAT；凭据掩码输出）
	ad("GET /api/steward/connections", h.handleStewardConnsList)
	ad("POST /api/steward/connections", h.handleStewardConnAdd)
	ad("DELETE /api/steward/connections/{id}", h.handleStewardConnDelete)
	ad("POST /api/steward/connections/{id}/test", h.handleStewardConnTest)
	ad("POST /api/steward/connections/{id}/activate", h.handleStewardConnActivate)
	ad("GET /api/steward/connections/{id}/repos", h.handleStewardConnRepos)
	ad("POST /api/steward/connections/{id}/projects", h.handleStewardConnProject)

	// AI 转制上架（实验特色功能）：公开项目 → openos 应用 → 货架
	ad("GET /api/steward/conversions", h.handleStewardConversions)
	ad("POST /api/steward/conversions", h.handleStewardConversionCreate)
	ad("GET /api/steward/conversions/{id}", h.handleStewardConversionGet)
	ad("POST /api/steward/conversions/{id}/cancel", h.handleStewardConversionCancel)
	ad("GET /api/steward/apps", h.handleStewardMarketApps)

	// 流水线工作面（GitLab 版本 × CI + 转修桥 + job 运行控制）
	h.registerPipe(authed) // 见 pipe_types.go
}

// ---- 平台账号连接 ----

type stConnIn struct {
	Platform string `json:"platform"` // gitlab | github
	BaseURL  string `json:"base_url"` // GitLab 实例根（github 忽略）
	PAT      string `json:"pat"`
	User     string `json:"user"`
	Password string `json:"password"`
}

func (h *Handler) handleStewardConnsList(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	h.ok(w, f.ListConnections())
	return nil
}

func (h *Handler) handleStewardConnAdd(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	in, err := readJSON[stConnIn](r)
	if err != nil {
		return err
	}
	if in.BaseURL == "" {
		in.BaseURL = h.GitLabURL // GitLab 缺省同 env（GitHub 固定 github.com）
	}
	v, err := f.AddConnection(r.Context(), steward.ConnInput{
		Platform: in.Platform, BaseURL: in.BaseURL,
		PAT: in.PAT, User: in.User, Password: in.Password,
	})
	if err != nil {
		return errBad(err.Error())
	}
	h.ok(w, v)
	return nil
}

func (h *Handler) handleStewardConnDelete(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	if err := f.DeleteConnection(r.PathValue("id")); err != nil {
		if errors.Is(err, steward.ErrConnNotFound) {
			return errNotFound(err.Error())
		}
		return errBad(err.Error())
	}
	h.GL.invalidateAll() // 数据源可能变化：流水线聚合缓存全失效
	h.ok(w, nil)
	return nil
}

func (h *Handler) handleStewardConnTest(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	v, err := f.TestConnection(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, steward.ErrConnNotFound) {
			return errNotFound(err.Error())
		}
		return errUpstream(err.Error()) // 验证失败多为平台侧（凭据/网络）
	}
	h.ok(w, v)
	return nil
}

func (h *Handler) handleStewardConnActivate(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	if err := f.ActivateConnection(r.PathValue("id")); err != nil {
		if errors.Is(err, steward.ErrConnNotFound) {
			return errNotFound(err.Error())
		}
		return errBad(err.Error())
	}
	h.GL.invalidateAll() // 切换即生效：下一轮轮询拿到新数据源
	h.ok(w, nil)
	return nil
}

func (h *Handler) handleStewardConnRepos(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	out, err := f.BrowseConnectionRepos(r.Context(), r.PathValue("id"), q.Get("q"), page)
	if err != nil {
		if errors.Is(err, steward.ErrConnNotFound) {
			return errNotFound(err.Error())
		}
		return errUpstream(err.Error())
	}
	h.ok(w, out)
	return nil
}

type stConnProjectIn struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"` // private | internal | public
	AutoInit    bool   `json:"auto_init"`
}

func (h *Handler) handleStewardConnProject(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	in, err := readJSON[stConnProjectIn](r)
	if err != nil {
		return err
	}
	out, err := f.CreateConnectionProject(r.Context(), r.PathValue("id"), steward.CreateProjectInput{
		Name: in.Name, Description: in.Description, Visibility: in.Visibility, AutoInit: in.AutoInit,
	})
	if err != nil {
		if errors.Is(err, steward.ErrConnNotFound) {
			return errNotFound(err.Error())
		}
		if errors.Is(err, steward.ErrProjectNameRequired) {
			return errBad(err.Error())
		}
		return errUpstream(err.Error())
	}
	h.ok(w, out)
	return nil
}

// requireStewardReportKey 上报密钥校验（常数时间比较）。
func (h *Handler) requireStewardReportKey(r *http.Request) error {
	if h.Eng == nil {
		return errUpstream("代码管家引擎未启用")
	}
	key := r.Header.Get("X-Report-Key")
	if key == "" || subtle.ConstantTimeCompare([]byte(key), []byte(h.Eng.EffectiveReportKey())) != 1 {
		return errUnauthenticated("上报密钥无效（请求头 X-Report-Key）")
	}
	return nil
}

// ---- 对外上报（公开）----

type stReportIn struct {
	Repo     string   `json:"repo"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Reporter string   `json:"reporter"`
	Labels   []string `json:"labels"`
}

func (h *Handler) handleStewardReport(w http.ResponseWriter, r *http.Request) error {
	if err := h.requireStewardReportKey(r); err != nil {
		return err
	}
	in, err := readJSON[stReportIn](r)
	if err != nil {
		return err
	}
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	p, err := f.CreateReport(steward.ReportInput{
		Repo: in.Repo, Title: in.Title, Body: in.Body, Reporter: in.Reporter, Labels: in.Labels,
	})
	if err != nil {
		return errBad(err.Error())
	}
	h.ok(w, p)
	return nil
}

func (h *Handler) handleStewardReportStatus(w http.ResponseWriter, r *http.Request) error {
	if err := h.requireStewardReportKey(r); err != nil {
		return err
	}
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	p, ok := f.GetProblem(r.PathValue("id"))
	if !ok {
		return errNotFound("问题不存在")
	}
	out := map[string]any{
		"id": p.ID, "state": p.State, "title": p.Title, "error": p.Error, "created_at": p.CreatedAt,
	}
	if p.RunID != "" {
		if run, ok := f.GetRun(p.RunID); ok {
			out["run"] = map[string]any{
				"id": run.ID, "state": run.State, "commit": run.Commit, "tag": run.Tag,
				"error": run.Error, "ended_at": run.EndedAt,
			}
		}
	}
	h.ok(w, out)
	return nil
}

// ---- 总览 ----

func (h *Handler) handleStewardOverview(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	h.ok(w, f.Overview())
	return nil
}

// ---- 仓库 ----

type stRepoView struct {
	steward.Repo
	GitTokenSet bool `json:"git_token_set"`
}

func (h *Handler) stRepoViews(f *steward.Engine) []stRepoView {
	repos := f.ListRepos()
	out := make([]stRepoView, 0, len(repos))
	for _, rp := range repos {
		out = append(out, stRepoView{Repo: rp, GitTokenSet: f.HasGitToken(rp.ID)})
	}
	return out
}

type stRepoIn struct {
	Name         string   `json:"name"`
	CloneURL     string   `json:"clone_url"`
	Branch       string   `json:"branch"`
	Mode         string   `json:"mode"`
	GitUser      string   `json:"git_user"`
	GitToken     string   `json:"git_token"`
	ConnectionID string   `json:"connection_id"` // 平台账号连接（账号浏览一键接管：凭据动态解析）
	Platform     string   `json:"platform"`      // gitlab | github（随 connection_id）
	GitHubOwner  string   `json:"github_owner"`
	GitHubRepo   string   `json:"github_repo"`
	IssueLabels  []string `json:"issue_labels"`
	AutoRelease  bool     `json:"auto_release"`
}

func (in stRepoIn) toRepo(id string) *steward.Repo {
	return &steward.Repo{
		ID: id, Name: in.Name, CloneURL: in.CloneURL, Branch: in.Branch, Mode: in.Mode,
		GitUser: in.GitUser, GitToken: in.GitToken,
		ConnectionID: in.ConnectionID, Platform: in.Platform,
		GHOwner: in.GitHubOwner, GHRepo: in.GitHubRepo, GHLabels: in.IssueLabels,
		AutoRelease: in.AutoRelease,
	}
}

func (h *Handler) handleStewardReposList(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	h.ok(w, h.stRepoViews(f))
	return nil
}

func (h *Handler) handleStewardRepoCreate(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	in, err := readJSON[stRepoIn](r)
	if err != nil {
		return err
	}
	// 按 CloneURL 去重：账号浏览「接管」重复点击 / 转修桥重复注册都归并为更新
	id := ""
	for _, rp := range f.ListRepos() {
		if rp.CloneURL == strings.TrimSpace(in.CloneURL) {
			id = rp.ID
			break
		}
	}
	rp, err := f.UpsertRepo(in.toRepo(id))
	if err != nil {
		return errBad(err.Error())
	}
	h.ok(w, stRepoView{Repo: rp, GitTokenSet: f.HasGitToken(rp.ID)})
	return nil
}

func (h *Handler) handleStewardRepoUpdate(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	in, err := readJSON[stRepoIn](r)
	if err != nil {
		return err
	}
	rp, err := f.UpsertRepo(in.toRepo(r.PathValue("id")))
	if err != nil {
		if errors.Is(err, steward.ErrRepoNotFound) {
			return errNotFound(err.Error())
		}
		return errBad(err.Error())
	}
	h.ok(w, stRepoView{Repo: rp, GitTokenSet: f.HasGitToken(rp.ID)})
	return nil
}

func (h *Handler) handleStewardRepoDelete(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	if err := f.DeleteRepo(r.PathValue("id")); err != nil {
		if errors.Is(err, steward.ErrRepoNotFound) {
			return errNotFound(err.Error())
		}
		return errBad(err.Error())
	}
	h.ok(w, nil)
	return nil
}

func (h *Handler) handleStewardRepoSync(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	n, err := f.SyncIssuesNow(r.Context(), r.PathValue("id"))
	if err != nil {
		return errBad(err.Error())
	}
	h.ok(w, map[string]int{"added": n})
	return nil
}

// handleStewardSyncAll 全部仓库手动同步一轮（问题页「同步 issue」按钮；单仓
// 失败不阻断其余，失败清单随响应回显）。
func (h *Handler) handleStewardSyncAll(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	added, errs := f.SyncAllIssuesNow(r.Context())
	h.ok(w, map[string]any{"added": added, "errors": errs})
	return nil
}

// ---- 问题 ----

type stProblemView struct {
	steward.Problem
	RepoName string `json:"repo_name"`
}

func (h *Handler) handleStewardProblems(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	q := r.URL.Query()
	names := f.RepoNames()
	list := f.ListProblems(q.Get("state"), q.Get("repo"), q.Get("q"))
	out := make([]stProblemView, 0, len(list))
	for _, p := range list {
		name := names[p.RepoID]
		if name == "" {
			name = "（仓库已删除）"
		}
		out = append(out, stProblemView{Problem: p, RepoName: name})
	}
	h.ok(w, out)
	return nil
}

type stProblemIn struct {
	RepoID        string `json:"repo_id"`
	GitLabProject int    `json:"gitlab_project_id"` // repo_id 为空时生效：GitLab 项目 ID（手动录入选「GitLab 项目」），未接管则自动注册仓库（同流水线转修）
	Title         string `json:"title"`
	Body          string `json:"body"`
}

func (h *Handler) handleStewardProblemCreate(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	in, err := readJSON[stProblemIn](r)
	if err != nil {
		return err
	}
	repoID := in.RepoID
	if repoID == "" && in.GitLabProject > 0 {
		// 选的是 GitLab 项目：按项目 ID 解析克隆地址并接管（已接管仅刷新推送凭据）
		if !h.GL.configured() {
			return errUpstream("GitLab 未配置访问凭证——在代码管家「仓库」页连接 GitLab 账号，或配置 OPENOS_GITLAB_TOKEN / OPENOS_GITLAB_USERNAME/OPENOS_GITLAB_PASSWORD")
		}
		pj, err := h.GL.projectRef(r.Context(), in.GitLabProject)
		if err != nil {
			return err
		}
		if repoID, err = h.ensureGitlabRepo(r.Context(), pj.PathWithNamespace, pj.HTTPURLToRepo, "", false); err != nil {
			return err
		}
	}
	p, err := f.CreateProblem(steward.ProblemInput{
		RepoID: repoID, Source: steward.SrcManual, Title: in.Title, Body: in.Body,
		Reporter: stewardID(r).Name,
	})
	if err != nil {
		return errBad(err.Error())
	}
	h.ok(w, p)
	return nil
}

func (h *Handler) handleStewardProblemConfirm(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	// body 可选：{release: bool} 覆盖本次是否发版；空体 = 沿用默认
	var release *bool
	if raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<10)); len(raw) > 0 {
		var in struct {
			Release *bool `json:"release"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return errBad("请求体解析失败: " + err.Error())
		}
		release = in.Release
	}
	if err := f.ConfirmProblem(r.PathValue("id"), release, stewardID(r).Name, stewardID(r).UID); err != nil {
		return errBad(err.Error())
	}
	h.ok(w, nil)
	return nil
}

func (h *Handler) handleStewardProblemReject(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	if err := f.RejectProblem(r.PathValue("id"), stewardID(r).Name); err != nil {
		return errBad(err.Error())
	}
	h.ok(w, nil)
	return nil
}

func (h *Handler) handleStewardProblemCancel(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	if err := f.CancelProblem(r.PathValue("id")); err != nil {
		return errBad(err.Error())
	}
	h.ok(w, nil)
	return nil
}

// ---- 修复记录 ----

type stRunView struct {
	steward.Run
	RepoName     string `json:"repo_name"`
	ProblemTitle string `json:"problem_title"`
}

func (h *Handler) handleStewardRuns(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	q := r.URL.Query()
	names := f.RepoNames()
	list := f.ListRuns(q.Get("problem"), q.Get("repo"))
	out := make([]stRunView, 0, len(list))
	for _, run := range list {
		name := names[run.RepoID]
		if name == "" {
			name = "（仓库已删除）"
		}
		title := ""
		if p, ok := f.GetProblem(run.ProblemID); ok {
			title = p.Title
		}
		out = append(out, stRunView{Run: run, RepoName: name, ProblemTitle: title})
	}
	h.ok(w, out)
	return nil
}

func (h *Handler) handleStewardRunGet(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	run, ok := f.GetRun(r.PathValue("id"))
	if !ok {
		return errNotFound("记录不存在")
	}
	offset, _ := strconv.ParseInt(r.URL.Query().Get("log"), 10, 64)
	chunk, next, err := f.ReadRunLog(run.ID, offset)
	if err != nil {
		return errBad(err.Error())
	}
	names := f.RepoNames()
	title := ""
	if p, ok := f.GetProblem(run.ProblemID); ok {
		title = p.Title
	}
	h.ok(w, map[string]any{
		"run":        stRunView{Run: run, RepoName: names[run.RepoID], ProblemTitle: title},
		"log":        string(chunk),
		"log_offset": next,
	})
	return nil
}

func (h *Handler) handleStewardRunCancel(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	if !f.CancelRun(r.PathValue("id")) {
		return errBad("该记录不在运行中（可能刚结束，刷新查看）")
	}
	h.ok(w, nil)
	return nil
}

func (h *Handler) handleStewardRunRetry(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	if err := f.RetryRun(r.PathValue("id")); err != nil {
		return errBad(err.Error())
	}
	h.ok(w, nil)
	return nil
}

// ---- 设置 ----

func (h *Handler) handleStewardSettingsGet(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	h.ok(w, f.GetSettings(r.Context()))
	return nil
}

func (h *Handler) handleStewardSettingsPut(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	in, err := readJSON[struct {
		Model       string `json:"model"`
		GitHubToken string `json:"github_token"`
	}](r)
	if err != nil {
		return err
	}
	if err := f.SetSettings(in.Model, in.GitHubToken); err != nil {
		return errBad(err.Error())
	}
	h.ok(w, nil)
	return nil
}

// ---- AI 转制上架（实验特色功能） ----

func (h *Handler) handleStewardConversions(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	h.ok(w, f.ListConversions())
	return nil
}

func (h *Handler) handleStewardConversionCreate(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	in, err := readJSON[struct {
		SourceURL string `json:"source_url"`
	}](r)
	if err != nil {
		return err
	}
	cv, err := f.CreateConversion(steward.ConvInput{
		SourceURL: in.SourceURL,
		Operator:  stewardID(r).UID,
		By:        stewardID(r).Name,
	})
	if err != nil {
		return errBad(err.Error())
	}
	h.ok(w, cv)
	return nil
}

func (h *Handler) handleStewardConversionGet(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	cv, ok := f.GetConversion(r.PathValue("id"))
	if !ok {
		return errNotFound("任务不存在")
	}
	offset, _ := strconv.ParseInt(r.URL.Query().Get("log"), 10, 64)
	chunk, next, err := f.ReadRunLog(cv.ID, offset)
	if err != nil {
		return errBad(err.Error())
	}
	h.ok(w, map[string]any{
		"conversion": cv,
		"log":        string(chunk),
		"log_offset": next,
	})
	return nil
}

func (h *Handler) handleStewardConversionCancel(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	if !f.CancelConversion(r.PathValue("id")) {
		return errBad("该任务不在进行中（可能刚结束，刷新查看）")
	}
	h.ok(w, nil)
	return nil
}

func (h *Handler) handleStewardMarketApps(w http.ResponseWriter, r *http.Request) error {
	f, err := h.requireSteward()
	if err != nil {
		return err
	}
	out, err := f.MarketApps()
	if err != nil {
		return errBad(err.Error())
	}
	h.ok(w, out)
	return nil
}
