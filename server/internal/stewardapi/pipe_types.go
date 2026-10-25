// pipe_types.go — 流水线工作面的 DTO/投影与详情聚合（自 pipeline.go 按域拆分）。
package stewardapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"openos/server/internal/auth"
	"openos/server/internal/steward"
)

// ---- GitLab 原始响应（仅取用到的字段）----

type glProject struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
	DefaultBranch     string `json:"default_branch"`
	Archived          bool   `json:"archived"`
	EmptyRepo         bool   `json:"empty_repo"`
	LastActivityAt    string `json:"last_activity_at"`
}

type glTag struct {
	Name   string `json:"name"`
	Commit struct {
		ID        string `json:"id"`
		ShortID   string `json:"short_id"`
		Title     string `json:"title"`
		CreatedAt string `json:"created_at"`
	} `json:"commit"`
}

type glPipeline struct {
	ID        int    `json:"id"`
	SHA       string `json:"sha"`
	Ref       string `json:"ref"`
	Status    string `json:"status"`
	Source    string `json:"source"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	WebURL    string `json:"web_url"`
	// 以下仅流水线详情端点返回（列表端点零值）
	Tag        bool    `json:"tag"`
	Duration   float64 `json:"duration"`
	Queued     float64 `json:"queued_duration"`
	StartedAt  string  `json:"started_at"`
	FinishedAt string  `json:"finished_at"`
	YAMLErrors string  `json:"yaml_errors"`
}

// glProjRef 单项目引用（详情/修复桥用，full payload 含克隆地址）。
type glProjRef struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
	DefaultBranch     string `json:"default_branch"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
}

// glJob 流水线 job（stage 分组、失败原因、耗时、runner）。
type glJob struct {
	ID            int     `json:"id"`
	Name          string  `json:"name"`
	Stage         string  `json:"stage"`
	Status        string  `json:"status"`
	FailureReason string  `json:"failure_reason"`
	AllowFailure  bool    `json:"allow_failure"`
	Duration      float64 `json:"duration"`
	Queued        float64 `json:"queued_duration"`
	CreatedAt     string  `json:"created_at"`
	StartedAt     string  `json:"started_at"`
	FinishedAt    string  `json:"finished_at"`
	WebURL        string  `json:"web_url"`
	Runner        struct {
		Description string `json:"description"`
	} `json:"runner"`
}

// ---- 对前端的投影（字段精简，无凭证类内容）----

type pipePipeline struct {
	ID        int    `json:"id"`
	SHA       string `json:"sha"`
	Ref       string `json:"ref"`
	Status    string `json:"status"`
	Source    string `json:"source"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	WebURL    string `json:"web_url"`
	// 以下仅详情视图有值（列表聚合为空/零）
	Tag        bool    `json:"tag,omitempty"`      // ref 是否为 tag（发版流水线）
	Duration   float64 `json:"duration,omitempty"` // 总耗时秒
	Queued     float64 `json:"queued_duration,omitempty"`
	StartedAt  string  `json:"started_at,omitempty"`
	FinishedAt string  `json:"finished_at,omitempty"`
	YAMLErrors string  `json:"yaml_errors,omitempty"` // .gitlab-ci.yml 校验错误
}

// toPipePipeline GitLab → 前端投影（列表聚合与详情共用）。
func toPipePipeline(pl glPipeline) pipePipeline {
	return pipePipeline{
		ID: pl.ID, SHA: pl.SHA, Ref: pl.Ref, Status: pl.Status, Source: pl.Source,
		CreatedAt: pl.CreatedAt, UpdatedAt: pl.UpdatedAt, WebURL: pl.WebURL,
		Tag: pl.Tag, Duration: pl.Duration, Queued: pl.Queued,
		StartedAt: pl.StartedAt, FinishedAt: pl.FinishedAt, YAMLErrors: pl.YAMLErrors,
	}
}

// toPipeJob GitLab job → 前端投影（详情聚合与 job 动作回传共用）。
func toPipeJob(j glJob) pipeJob {
	return pipeJob{
		ID: j.ID, Name: j.Name, Stage: j.Stage, Status: j.Status,
		FailureReason: j.FailureReason, AllowFailure: j.AllowFailure,
		Duration: j.Duration, Queued: j.Queued,
		CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
		WebURL: j.WebURL, Runner: j.Runner.Description,
	}
}

// pipeJob / pipeStage / pipeDetail — 流水线详情视图（阶段聚合 + job 清单）。
type pipeJob struct {
	ID            int     `json:"id"`
	Name          string  `json:"name"`
	Stage         string  `json:"stage"`
	Status        string  `json:"status"`
	FailureReason string  `json:"failure_reason,omitempty"`
	AllowFailure  bool    `json:"allow_failure"`
	Duration      float64 `json:"duration,omitempty"`
	Queued        float64 `json:"queued_duration,omitempty"`
	CreatedAt     string  `json:"created_at,omitempty"`
	StartedAt     string  `json:"started_at,omitempty"`
	FinishedAt    string  `json:"finished_at,omitempty"`
	WebURL        string  `json:"web_url"`
	Runner        string  `json:"runner,omitempty"`
}

type pipeStage struct {
	Name   string `json:"name"`
	Status string `json:"status"` // 阶段聚合状态（worst-rank job）
}

type pipeProjView struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	WebURL        string `json:"web_url"`
	DefaultBranch string `json:"default_branch"`
	GitURL        string `json:"git_url,omitempty"` // http 克隆地址（修复桥注册仓库用）
}

type pipeDetail struct {
	Project  pipeProjView `json:"project"`
	Pipeline pipePipeline `json:"pipeline"`
	Stages   []pipeStage  `json:"stages"`
	Jobs     []pipeJob    `json:"jobs"`
}

type pipeTag struct {
	Name      string        `json:"name"`
	ShortID   string        `json:"short_id"`
	Title     string        `json:"title"`
	CreatedAt string        `json:"created_at"` // 打 tag 提交时间（tag 本身无时间）
	Pipeline  *pipePipeline `json:"pipeline"`   // 该版本对应流水线（最新一条；null = 无 CI）
}

type pipeProject struct {
	ID             int            `json:"id"`
	Name           string         `json:"name"`
	Path           string         `json:"path"` // path_with_namespace
	WebURL         string         `json:"web_url"`
	DefaultBranch  string         `json:"default_branch"`
	Archived       bool           `json:"archived"`
	EmptyRepo      bool           `json:"empty_repo,omitempty"`
	LastActivityAt string         `json:"last_activity_at"`
	LatestTag      *pipeTag       `json:"latest_tag"`      // 版本序最新（语义化比较）
	Tags           []pipeTag      `json:"tags"`            // 版本序降序
	Pipelines      []pipePipeline `json:"pipelines"`       // 最近流水线（时间降序）
	BranchPipeline *pipePipeline  `json:"branch_pipeline"` // 默认分支最新一条
	Error          string         `json:"error,omitempty"` // 明细拉取失败（项目行保留展示）
}

// GET /api/steward/pipe/projects — 全量聚合（admin）。缓存期内直接回放；取数持锁
// 串行（并发请求等待后命中新缓存），失败不清缓存由前端保留上一轮数据。
func (h *Handler) handlePipeProjects(w http.ResponseWriter, r *http.Request) error {
	data, err := h.GL.cachedProjects(r.Context())
	if err != nil {
		return err
	}
	h.ok(w, data)
	return nil
}

// cachedProjects 聚合结果 + TTL 缓存。取数持锁串行是有意的：两个 tab 同为 30s
// 轮询，并发只允许一轮上游取数（详情/日志缓存无此语义，见各自实现）。
func (g *GitLabClient) cachedProjects(ctx context.Context) (any, error) {
	g.fetchMu.Lock()
	defer g.fetchMu.Unlock()
	if v, ok := g.projCache.Get("all"); ok {
		return v, nil
	}
	data, err := g.fetchProjects(ctx)
	if err != nil {
		return nil, err
	}
	g.projCache.Put("all", data)
	return data, nil
}

// fetchProjects 拉全量项目 → 并行取每项目 tags + pipelines → 按 ref 连接。
func (g *GitLabClient) fetchProjects(ctx context.Context) (any, error) {
	if !g.configured() {
		return nil, errUpstream(errGitlabNoCred)
	}
	var projects []glProject
	for page := 1; page <= pipePagesMax; page++ {
		var batch []glProject
		hdr, err := g.Get(ctx, fmt.Sprintf(
			"/projects?simple=true&order_by=last_activity_at&sort=desc&per_page=100&page=%d", page), &batch)
		if err != nil {
			return nil, err
		}
		projects = append(projects, batch...)
		if hdr.Get("X-Next-Page") == "" {
			break
		}
	}
	out := make([]pipeProject, len(projects))
	sem := make(chan struct{}, pipeFetchers)
	var wg sync.WaitGroup
	for i, p := range projects {
		out[i] = pipeProject{
			ID: p.ID, Name: p.Name, Path: p.PathWithNamespace, WebURL: p.WebURL,
			DefaultBranch: p.DefaultBranch, Archived: p.Archived, EmptyRepo: p.EmptyRepo,
			LastActivityAt: p.LastActivityAt,
			Tags:           []pipeTag{}, Pipelines: []pipePipeline{},
		}
		if p.EmptyRepo {
			continue
		}
		wg.Add(1)
		go func(i int, p glProject) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var tags []glTag
			var pipes []glPipeline
			_, errT := g.Get(ctx, fmt.Sprintf("/projects/%d/repository/tags?per_page=%d", p.ID, pipeTagsLimit), &tags)
			_, errP := g.Get(ctx, fmt.Sprintf("/projects/%d/pipelines?per_page=%d&sort=desc&order_by=id", p.ID, pipePipesLimit), &pipes)
			// 部分失败保留：tags 成功即入列（pipelines 缺失时 CI 显示「无 CI」），
			// 错误记入行内提示（如 share/* 项目 pipelines 接口对低权限账户 403）
			if errT == nil {
				joinPipeDetail(&out[i], tags, pipes)
			}
			switch {
			case errT != nil:
				out[i].Error = "明细拉取失败: " + steward.ErrShort(errT)
			case errP != nil:
				out[i].Error = "流水线拉取失败: " + steward.ErrShort(errP)
			}
		}(i, p)
	}
	wg.Wait()
	return map[string]any{"fetched_at": time.Now().Format(time.RFC3339), "projects": out}, nil
}

// joinPipeDetail 版本 ↔ 流水线按 ref 就近连接：pipelines 已按 id 降序，同 ref
// 首条即最新；tag 无对应流水线 = 无 CI（Pipeline 留空）。
func joinPipeDetail(p *pipeProject, tags []glTag, pipes []glPipeline) {
	byRef := map[string]*pipePipeline{}
	pipelineSlice := make([]pipePipeline, len(pipes))
	for i, pl := range pipes {
		pipelineSlice[i] = toPipePipeline(pl)
	}
	for i := range pipelineSlice {
		if _, ok := byRef[pipelineSlice[i].Ref]; !ok {
			byRef[pipelineSlice[i].Ref] = &pipelineSlice[i]
		}
	}
	p.Pipelines = pipelineSlice
	p.BranchPipeline = byRef[p.DefaultBranch]
	p.Tags = make([]pipeTag, len(tags))
	for i, t := range tags {
		p.Tags[i] = pipeTag{
			Name: t.Name, ShortID: t.Commit.ShortID, Title: t.Commit.Title,
			CreatedAt: t.Commit.CreatedAt, Pipeline: byRef[t.Name],
		}
	}
	// 版本序降序（v0.0.10 > v0.0.9；非版本号 tag 回落字符串序）
	stableSortDesc(p.Tags, func(a, b pipeTag) bool { return cmpVersion(a.Name, b.Name) > 0 })
	if len(p.Tags) > 0 {
		p.LatestTag = &p.Tags[0]
	}
}

func stableSortDesc(s []pipeTag, less func(a, b pipeTag) bool) {
	// 插入排序（≤8 元素）：稳定且足够
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && less(s[j], s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// cmpVersion 语义化版本比较（v 前缀可省；数字段逐位数值比；预发布段低于同
// 数字主版本，如 v0.0.1-alpha.1 < v0.0.1；任一侧无法解析回落字符串比较）。
func cmpVersion(a, b string) int {
	va, aok := parseVersion(a)
	vb, bok := parseVersion(b)
	if !aok || !bok {
		return strings.Compare(a, b)
	}
	for i := 0; i < len(va.nums) || i < len(vb.nums); i++ {
		var x, y int
		if i < len(va.nums) {
			x = va.nums[i]
		}
		if i < len(vb.nums) {
			y = vb.nums[i]
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	switch {
	case va.pre == vb.pre:
		return 0
	case va.pre == "":
		return 1
	case vb.pre == "":
		return -1
	}
	pa, pb := strings.Split(va.pre, "."), strings.Split(vb.pre, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x == y {
			continue
		}
		na, e1 := strconv.Atoi(x)
		nb, e2 := strconv.Atoi(y)
		if e1 == nil && e2 == nil {
			if na > nb {
				return 1
			}
			return -1
		}
		return strings.Compare(x, y)
	}
	if len(pa) != len(pb) {
		if len(pa) > len(pb) {
			return 1
		}
		return -1
	}
	return 0
}

type glVersion struct {
	nums []int
	pre  string
}

func parseVersion(s string) (glVersion, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "" {
		return glVersion{}, false
	}
	main, pre := s, ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		main, pre = s[:i], s[i+1:]
	}
	parts := strings.Split(main, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			return glVersion{}, false
		}
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				return glVersion{}, false
			}
		}
		n, _ := strconv.Atoi(p)
		nums = append(nums, n)
	}
	return glVersion{nums: nums, pre: pre}, true
}

// ---- 流水线详情（阶段聚合 + job 清单）----

// GET /api/steward/pipe/pipelines/{projectID}/{pipelineID} — 流水线详情（admin）：
// 项目引用 + 流水线（含 tag/耗时/yaml_errors）+ 阶段聚合 + job 清单。
func (h *Handler) handlePipePipeline(w http.ResponseWriter, r *http.Request) error {
	pid, err := strconv.Atoi(r.PathValue("projectID"))
	if err != nil || pid <= 0 {
		return errBad("项目 ID 无效")
	}
	plid, err := strconv.Atoi(r.PathValue("pipelineID"))
	if err != nil || plid <= 0 {
		return errBad("流水线 ID 无效")
	}
	d, err := h.GL.cachedPipelineDetail(r.Context(), pid, plid)
	if err != nil {
		return err
	}
	h.ok(w, d)
	return nil
}

// cachedPipelineDetail 详情 + TTL 缓存（运行中流水线前端 5s 轮询）。取数不持
// 锁：此前 detMu 罩住整个网络往返，不同流水线/日志的并发轮询会被无谓串行化
// （缓存锁跨网络请求是设计瑕疵——需要串行的是聚合取数，不是这里）。
func (g *GitLabClient) cachedPipelineDetail(ctx context.Context, projectID, pipelineID int) (*pipeDetail, error) {
	key := fmt.Sprintf("%d/%d", projectID, pipelineID)
	if v, ok := g.detCache.Get(key); ok {
		return v, nil
	}
	d, err := g.fetchPipelineDetail(ctx, projectID, pipelineID)
	if err != nil {
		return nil, err
	}
	g.detCache.Put(key, d)
	return d, nil
}

// projectRef 按项目 ID 拉项目引用（含克隆地址；无 http_url_to_repo 时以
// WebURL 兜底拼 .git）。流水线详情与手动录入选 GitLab 项目共用。
func (g *GitLabClient) projectRef(ctx context.Context, projectID int) (glProjRef, error) {
	var pj glProjRef
	if _, err := g.Get(ctx, fmt.Sprintf("/projects/%d", projectID), &pj); err != nil {
		return glProjRef{}, err
	}
	if pj.HTTPURLToRepo == "" && pj.WebURL != "" {
		pj.HTTPURLToRepo = strings.TrimRight(pj.WebURL, "/") + ".git"
	}
	return pj, nil
}

// fetchPipelineDetail 项目引用 + 流水线详情 + jobs 三连拉，聚合阶段状态。
func (g *GitLabClient) fetchPipelineDetail(ctx context.Context, projectID, pipelineID int) (*pipeDetail, error) {
	if !g.configured() {
		return nil, errUpstream(errGitlabNoCred)
	}
	pj, err := g.projectRef(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var pl glPipeline
	if _, err := g.Get(ctx, fmt.Sprintf("/projects/%d/pipelines/%d", projectID, pipelineID), &pl); err != nil {
		return nil, err
	}
	var glJobs []glJob
	if _, err := g.Get(ctx, fmt.Sprintf("/projects/%d/pipelines/%d/jobs?per_page=100", projectID, pipelineID), &glJobs); err != nil {
		return nil, err
	}
	d := &pipeDetail{
		Project: pipeProjView{
			ID: pj.ID, Name: pj.Name, Path: pj.PathWithNamespace, WebURL: pj.WebURL,
			DefaultBranch: pj.DefaultBranch, GitURL: pj.HTTPURLToRepo,
		},
		Pipeline: toPipePipeline(pl),
		Jobs:     make([]pipeJob, len(glJobs)),
	}
	for i, j := range glJobs {
		d.Jobs[i] = toPipeJob(j)
	}
	d.Stages = aggregateStages(d.Jobs)
	return d, nil
}

// aggregateStages 按首次出现顺序聚合各阶段状态。
func aggregateStages(jobs []pipeJob) []pipeStage {
	var order []string
	byName := map[string][]pipeJob{}
	for _, j := range jobs {
		if _, ok := byName[j.Stage]; !ok {
			order = append(order, j.Stage)
		}
		byName[j.Stage] = append(byName[j.Stage], j)
	}
	out := make([]pipeStage, 0, len(order))
	for _, name := range order {
		worstRank, st := jobRank(byName[name][0])
		for _, j := range byName[name][1:] {
			if rk, js := jobRank(j); rk < worstRank {
				worstRank, st = rk, js
			}
		}
		out = append(out, pipeStage{Name: name, Status: st})
	}
	return out
}

// jobRank job 状态 → 严重度序（小者胜）：硬失败 > 运行中 > 未跑 > 手动 >
// 成功（allow_failure 的失败放行）> 取消/跳过。返回（序, 对外状态）。
func jobRank(j pipeJob) (int, string) {
	switch {
	case j.Status == "failed" && !j.AllowFailure:
		return 0, "failed"
	case j.Status == "running":
		return 1, "running"
	case glActive(j.Status):
		return 2, j.Status
	case j.Status == "manual":
		return 3, "manual"
	case j.Status == "success" || (j.Status == "failed" && j.AllowFailure):
		return 4, "success"
	default:
		return 5, j.Status
	}
}

func glActive(s string) bool {
	switch s {
	case "running", "pending", "preparing", "created", "waiting_for_resource", "scheduled":
		return true
	}
	return false
}

// invalidateDetails 失效全部详情缓存（job 运行/取消后调用，前端下一轮轮询立即
// 拿到新状态；条目本就 5s TTL，整清无代价）。
func (g *GitLabClient) invalidateDetails() {
	g.detCache.Clear()
}

// registerPipe 流水线工作面路由（admin）。
func (h *Handler) registerPipe(authed *http.ServeMux) {
	ad := func(pattern string, fn func(w http.ResponseWriter, r *http.Request) error) {
		authed.Handle(pattern, auth.RequireAdmin(wrap(fn)))
	}
	ad("GET /api/steward/pipe/projects", h.handlePipeProjects)
	ad("GET /api/steward/pipe/pipelines/{projectID}/{pipelineID}", h.handlePipePipeline)
	ad("GET /api/steward/pipe/jobs/{projectID}/{jobID}/trace", h.handlePipeTrace)
	ad("POST /api/steward/pipe/jobs/{projectID}/{jobID}/run", h.handlePipeJobRun)
	ad("POST /api/steward/pipe/jobs/{projectID}/{jobID}/cancel", h.handlePipeJobCancel)
	ad("POST /api/steward/pipe/fix", h.handlePipeFix)
}
