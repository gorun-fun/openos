// Package steward — 「代码管家」引擎：接管 git 仓库，接收问题上报（外部系统
// POST /api/steward/report / GitLab·GitHub issue 周期同步），确认（或自动模式）后用
// 镜像内置 gh（grf-harness 单任务模式）在仓库工作区内修复，提交并直推配置
// 分支，可选打 tag 发版（末位+1，交由各仓 CI 接手构建）。
//
// 数据落 <OPENOS_DATA>/steward/：state.json（JSON 状态文件，原子写）+ runs/<id>.log
// （修复日志）+ grf-home/（gh 配置，宿主按 token-hub 套餐渲染 settings.json）；
// 仓库工作区落操作者 openos home「代码管家」目录（用户文件 App 可见可下载，
// 见 workspaceFor；无操作者退回本目录 ws/<repoID>/）。宿主保持无数据库——此量级
// JSON 足够，引 SQLite 过重。
package steward

import "time"

// 问题来源
const (
	SrcReport   = "report"   // 外部系统经上报 API 推送
	SrcGitHub   = "github"   // GitHub issue 同步
	SrcGitLab   = "gitlab"   // GitLab issue 同步
	SrcManual   = "manual"   // 管理员在 App 内手动录入
	SrcPipeline = "pipeline" // 流水线 App 转报（GitLab CI/CD 失败，宿主桥录入）
)

// 仓库处理模式：收到新问题后是否需要人工确认
const (
	ModeManual = "manual" // 手动模式：新问题停在 new，等管理员确认
	ModeAuto   = "auto"   // 自动模式：新问题直接入队修复
)

// 问题状态机：new →(确认/自动入队) confirmed → running → fixed | failed；
// new → rejected；new/confirmed → canceled；failed →(重试) confirmed。
const (
	PNew       = "new"
	PConfirmed = "confirmed"
	PRejected  = "rejected"
	PRunning   = "running"
	PFixed     = "fixed"
	PFailed    = "failed"
	PCanceled  = "canceled"
)

// 修复记录状态机：prepare(clone/pull) → fixing(gh) → committing → pushing →
// releasing(可选 tag) → done；模型未改动 → no_changes；任一步失败 → failed；
// 人工取消/停机中断 → canceled。
const (
	RPrepare    = "prepare"
	RFixing     = "fixing"
	RCommitting = "committing"
	RPushing    = "pushing"
	RReleasing  = "releasing"
	RDone       = "done"
	RNoChanges  = "no_changes"
	RFailed     = "failed"
	RCanceled   = "canceled"
)

// Repo 被接管的 git 仓库（工作区在操作者 home「代码管家」目录下，凭据仅命令行
// 注入不落 .git/config）。
type Repo struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	CloneURL     string     `json:"clone_url"`
	Branch       string     `json:"branch,omitempty"`        // 推送目标分支；空 = 远端默认分支
	DefBranch    string     `json:"def_branch,omitempty"`    // 首次 clone 后缓存的远端默认分支
	Workspace    string     `json:"workspace,omitempty"`     // 最近一次工作区绝对路径（操作者 home 下，删除仓库时清理）
	Mode         string     `json:"mode"`                    // manual | auto
	GitUser      string     `json:"git_user,omitempty"`      // 推送凭据用户名；空 = token 直接作用户名
	GitToken     string     `json:"git_token,omitempty"`     // 仅服务端保存，API 输出掩码
	ConnectionID string     `json:"connection_id,omitempty"` // 平台账号连接（登录方式二）：凭据动态解析，优先于仓级凭据
	Platform     string     `json:"platform,omitempty"`      // gitlab | github | 空（手动注册）
	GHOwner      string     `json:"github_owner,omitempty"`  // GitHub issue 同步（开源仓库）
	GHRepo       string     `json:"github_repo,omitempty"`
	GHLabels     []string   `json:"issue_labels,omitempty"` // 仅同步命中这些 label 的 issue；空 = 全部
	AutoRelease  bool       `json:"auto_release"`           // 确认修复时「发版」勾选的默认值
	LastSync     *time.Time `json:"last_issue_sync,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// Problem 待修复问题（上报 / GitHub 同步 / 手动录入 / 流水线转修）。
type Problem struct {
	ID         string     `json:"id"`
	RepoID     string     `json:"repo_id"`
	Source     string     `json:"source"`
	ExtKey     string     `json:"ext_key,omitempty"` // 去重键：gh:<owner>/<repo>#<n> / gl:<项目路径>/issue/<iid> / gl:<项目ID>/pipe/<流水线ID>
	ExtURL     string     `json:"ext_url,omitempty"`
	Title      string     `json:"title"`
	Body       string     `json:"body,omitempty"`
	Reporter   string     `json:"reporter,omitempty"`
	Labels     []string   `json:"labels,omitempty"`
	State      string     `json:"state"`
	Release    bool       `json:"release"` // 本次修复是否发版（确认时定格）
	Error      string     `json:"error,omitempty"`
	RunID      string     `json:"run_id,omitempty"` // 最近一次修复记录
	DecidedBy  string     `json:"decided_by,omitempty"`
	DecidedUID string     `json:"decided_uid,omitempty"` // 确认人 openos 用户名（goder 执行的操作者归属）
	DecidedAt  *time.Time `json:"decided_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Run 一次修复执行记录。goder 执行（v0.0.26 起默认）只做状态跟踪：会话本体在
// 操作者的 Goder 里（SessionID 深链跟进），runs/<ID>.log 仅记引擎阶段行，不再
// 录制修复会话转录。
type Run struct {
	ID        string     `json:"id"`
	ProblemID string     `json:"problem_id"`
	RepoID    string     `json:"repo_id"`
	State     string     `json:"state"`
	Model     string     `json:"model,omitempty"`
	Executor  string     `json:"executor,omitempty"` // goder | gh（空 = 旧记录，按 gh 展示日志）
	Operator  string     `json:"operator,omitempty"` // goder 会话归属的 openos 用户名
	SessionID string     `json:"session_id,omitempty"`
	RespID    string     `json:"resp_id,omitempty"`
	Summary   string     `json:"summary,omitempty"` // 修复完成摘要（模型总结，非会话全文）
	Release   bool       `json:"release"`
	Commit    string     `json:"commit,omitempty"`
	Tag       string     `json:"tag,omitempty"`
	Files     int        `json:"files,omitempty"`
	Ins       int        `json:"ins,omitempty"`
	Dels      int        `json:"dels,omitempty"`
	Error     string     `json:"error,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// Settings 可在 App 内修改的引擎设置（环境变量覆盖优先：上报 Key / GitHub Token）。
type Settings struct {
	ReportKey   string `json:"report_key,omitempty"`    // 上报共享密钥；首启自动生成
	GitHubToken string `json:"github_token,omitempty"`  // GitHub API Token（提限额/私仓；连接 PAT 优先）
	Model       string `json:"model,omitempty"`         // 默认模型；空 = 模型清单首个
	EnvSeedDone bool   `json:"env_seed_done,omitempty"` // env→连接 首次种子已完成（防删除后重启复活）
}

// State 全量持久化状态（state.json）。
type State struct {
	Repos       []*Repo       `json:"repos"`
	Problems    []*Problem    `json:"problems"`
	Runs        []*Run        `json:"runs"`
	Conversions []*Conversion `json:"conversions"`
	Connections []*Connection `json:"connections"`
	Settings    *Settings     `json:"settings"`
}

func (s *State) conn(id string) *Connection {
	for _, c := range s.Connections {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (s *State) repo(id string) *Repo {
	for _, r := range s.Repos {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (s *State) problem(id string) *Problem {
	for _, p := range s.Problems {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (s *State) run(id string) *Run {
	for _, r := range s.Runs {
		if r.ID == id {
			return r
		}
	}
	return nil
}
