// steward.go — 引擎总装：生命周期（worker + GitHub 同步循环）与领域操作
// （仓库/问题/记录/设置），供 api 层薄封装调用。
package steward

import (
	"context"
	"fmt"
	"openos/server/internal/cache"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Options 引擎配置（api 层从 config 组装，测试可直造）。
type Options struct {
	DataDir  string // <OPENOS_DATA>/steward
	HomesDir string // openos 统一 home 根 <OPENOS_DATA>/homes；工作区落操作者 home「代码管家」目录（用户文件 App 可见可下载）；空 = 退回内部 ws/
	GhBin    string // grf-harness gh 二进制（gh 执行器用）
	GitBin   string // 默认 git
	LLMBase  string // OpenAI 兼容根（gh 执行器默认 token-hub 地址）
	LLMKey   string
	DefModel string // 环境默认模型（OPENOS_STEWARD_MODEL，默认 glm-5.3-flash）

	// Goder 执行器（v0.0.26 起默认）：Executor="goder" 且 Goder 非 nil 时生效，
	// 修复会话经 openos 宿主 API 落到操作者 Goder 的「代码管家」分组。
	Executor        string      // goder | gh
	Goder           GoderTasker // openos /api/host/goder/* 客户端
	DefaultOperator string      // 无确认人问题（report/auto）的操作者兜底（openos 用户名）

	ReportKeyEnv   string // OPENOS_STEWARD_REPORT_KEY，覆盖存储内 Key
	GitHubTokenEnv string // OPENOS_STEWARD_GITHUB_TOKEN，覆盖存储内 Token
	MarketDir      string // 货架目录（OPENOS_STEWARD_MARKET_DIR；空 = 转制发布禁用）

	GitName  string // 提交身份，默认 grf-steward
	GitEmail string

	Timeout   time.Duration // 单次修复上限，默认 30 分钟
	SyncEvery time.Duration // GitHub 同步间隔，默认 5 分钟

	Logf func(format string, args ...any)
}

// Engine 代码管家引擎。
type Engine struct {
	st   *Store
	opts Options

	wsDir   string // 仓库工作区根
	runDir  string // 运行日志目录
	grfHome string // gh GRF_HOME（settings.json 由 writeGhSettings 渲染）

	llmBase  string
	llmKey   string
	defModel string

	client *http.Client
	ghAPI  string // GitHub API 根（空 = 公网常量；测试注入假地址）
	logf   func(format string, args ...any)

	mu          sync.Mutex
	modelsCache []string
	modelsAt    time.Time
	cur         struct {
		id     string
		cancel context.CancelFunc
	}

	glTokens      *GlTokenCache                 // password-grant access_token（键见 connTokenKey，共享件见 gitlabauth.go）
	connRepoCache *cache.TTL[string, ReposPage] // 账号仓库浏览（30s，键 connID|q|page）

	wake chan struct{} // 确认/同步后立即唤醒 worker
}

// New 构造引擎（打开/初始化状态文件与目录树）。
func New(opts Options) (*Engine, error) {
	if opts.GitBin == "" {
		opts.GitBin = "git"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Minute
	}
	if opts.SyncEvery <= 0 {
		opts.SyncEvery = 5 * time.Minute
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	migrateLegacyDataDir(opts.DataDir, opts.Logf)
	st, err := Open(opts.DataDir)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		st:            st,
		opts:          opts,
		wsDir:         filepath.Join(opts.DataDir, "ws"),
		runDir:        filepath.Join(opts.DataDir, "runs"),
		grfHome:       filepath.Join(opts.DataDir, "grf-home"),
		llmBase:       strings.TrimRight(opts.LLMBase, "/"),
		llmKey:        opts.LLMKey,
		defModel:      opts.DefModel,
		client:        &http.Client{Timeout: 30 * time.Second},
		logf:          opts.Logf,
		glTokens:      NewGlTokenCache(&http.Client{Timeout: 30 * time.Second}),
		connRepoCache: cache.NewTTL[string, ReposPage](connReposTTL, 32),
		wake:          make(chan struct{}, 1),
	}
	for _, dir := range []string{e.wsDir, e.runDir, e.grfHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建 %s: %w", dir, err)
		}
	}
	return e, nil
}

// Run 阻塞主循环（main 以 goroutine 承载）：恢复中断 → issue 同步循环
// （GitHub/GitLab）+ 修复 worker。worker 单并发。空闲 tick 经 hasWork 粗判
// 后再进 workOne——workOne 无条件 Mutate（即无条件全量落盘 state.json），
// 不判空会导致空闲时每 2 秒重写两次状态文件。
func (e *Engine) Run(ctx context.Context) {
	e.recoverInterrupted()
	go e.syncLoop(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
		case <-time.After(2 * time.Second):
		}
		for e.hasWork() && (e.workOne(ctx) || e.workOneConvert(ctx)) {
		}
	}
}

// hasWork 队列粗判（共享锁一遍扫描；取队仍由 workOne 的 Mutate 复核，粗判与
// 领取之间被取消属正常竞态，无副作用）。
func (e *Engine) hasWork() bool {
	var ok bool
	e.st.View(func(s *State) {
		for _, p := range s.Problems {
			if p.State == PConfirmed {
				ok = true
				return
			}
		}
		for _, c := range s.Conversions {
			if c.State == CQueued {
				ok = true
				return
			}
		}
	})
	return ok
}

// syncLoop issue 周期同步（GitHub/GitLab；启动稍等 15s 避开启动高峰；单仓失败跳过）。
func (e *Engine) syncLoop(ctx context.Context) {
	select {
	case <-time.After(15 * time.Second):
	case <-ctx.Done():
		return
	}
	e.syncAll(ctx)
	t := time.NewTicker(e.opts.SyncEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.syncAll(ctx)
		}
	}
}

// syncEligibleIDs 可同步仓库：GitHub 仓配了 owner/repo，或 GitLab 仓
// （platform=gitlab，项目路径取 clone 地址）。
func (e *Engine) syncEligibleIDs() []string {
	var ids []string
	e.st.View(func(s *State) {
		for _, r := range s.Repos {
			if (r.GHOwner != "" && r.GHRepo != "") || r.Platform == PlatformGitLab {
				ids = append(ids, r.ID)
			}
		}
	})
	return ids
}

func (e *Engine) syncAll(ctx context.Context) {
	for _, id := range e.syncEligibleIDs() {
		if ctx.Err() != nil {
			return
		}
		n, err := e.syncRepo(ctx, id)
		if err != nil {
			e.logf("[steward] issue 同步失败（%s）: %v", id, err)
			continue
		}
		if n > 0 {
			e.logf("[steward] issue 同步：%d 个新问题（%s）", n, id)
		}
	}
}

// syncRepo 按仓库平台分发一次 issue 同步：gitlab → GitLab 项目路径同步；
// 其余（含空 platform 的 GitHub 配置仓）→ GitHub owner/repo 同步。
func (e *Engine) syncRepo(ctx context.Context, repoID string) (int, error) {
	var platform string
	e.st.View(func(s *State) {
		if r := s.repo(repoID); r != nil {
			platform = r.Platform
		}
	})
	if platform == PlatformGitLab {
		return e.syncGitLabIssues(ctx, repoID)
	}
	return e.syncRepoIssues(ctx, repoID)
}

// kickFirstSync 接管/挂连接/新建仓库后异步触发一次 issue 同步（仅可同步仓；
// 独立超时 ctx 不随请求取消；失败仅日志，不影响接管结果）。
func (e *Engine) kickFirstSync(repoID string) {
	go func() {
		var eligible bool
		e.st.View(func(s *State) {
			if r := s.repo(repoID); r != nil {
				eligible = (r.GHOwner != "" && r.GHRepo != "") || r.Platform == PlatformGitLab
			}
		})
		if !eligible {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if n, err := e.syncRepo(ctx, repoID); err != nil {
			e.logf("[steward] 首次 issue 同步失败（%s）: %v", repoID, err)
		} else if n > 0 {
			e.logf("[steward] 首次 issue 同步：%d 个新问题（%s）", n, repoID)
		}
	}()
}

// Notify 唤醒 worker（新 confirmed 问题入队后调用；缓冲 1，已唤醒则忽略）。
func (e *Engine) Notify() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// ---- 路径与二进制 ----

func (e *Engine) workspace(repoID string) string { return filepath.Join(e.wsDir, repoID) }

// workspaceFor 本次修复的工作区：有操作者且配了 homes 根时落其 openos home
// 「代码管家」目录（文件 App 可见可下载，goder 会话 workspace 同址）；否则退回
// 内部 ws/<ID>（无操作者兜底，行为同旧版）。
func (e *Engine) workspaceFor(r *Repo, operator string) string {
	if operator == "" || e.opts.HomesDir == "" {
		return e.workspace(r.ID)
	}
	return filepath.Join(e.opts.HomesDir, sanitizeDirName(operator, "user"), "代码管家", sanitizeDirName(r.Name, r.ID))
}

// operatorOf 修复操作者（goder 会话与工作区归属）：确认人优先，无确认人回落
// 引擎级兜底（OPENOS_STEWARD_GODER_UID）。
func (e *Engine) operatorOf(prob *Problem) string {
	return firstNonEmpty(prob.DecidedUID, e.opts.DefaultOperator)
}

// sanitizeDirName 目录名清洗：去掉路径分隔符/控制字符等（防注入与逃逸），
// 清洗后为空或仍是 "."/".." 时用 fallback。
func sanitizeDirName(name, fallback string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ':':
			return '_'
		case r < 0x20 || r == 0x7f:
			return -1
		default:
			return r
		}
	}, strings.TrimSpace(name))
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return fallback
	}
	return cleaned
}

func (e *Engine) runLogPath(runID string) string { return filepath.Join(e.runDir, runID+".log") }
func (e *Engine) gitBin() string                 { return e.opts.GitBin }
func (e *Engine) ghBin() string                  { return e.opts.GhBin }
func (e *Engine) gitName() string                { return firstNonEmpty(e.opts.GitName, "grf-steward") }
func (e *Engine) gitEmail() string               { return firstNonEmpty(e.opts.GitEmail, "steward@gorun.fun") }

// githubToken issue 同步令牌：GitHub 连接 PAT → 环境变量 → 设置页。
func (e *Engine) githubToken() string {
	if pat := e.GitHubPAT(); pat != "" {
		return pat
	}
	if e.opts.GitHubTokenEnv != "" {
		return e.opts.GitHubTokenEnv
	}
	var tok string
	e.st.View(func(s *State) { tok = s.Settings.GitHubToken })
	return tok
}

// EffectiveReportKey 生效上报密钥：环境变量覆盖存储内值。
func (e *Engine) EffectiveReportKey() string {
	if e.opts.ReportKeyEnv != "" {
		return e.opts.ReportKeyEnv
	}
	var key string
	e.st.View(func(s *State) { key = s.Settings.ReportKey })
	return key
}

// ---- ID 生成（前缀 + 随机 8 hex，state 内查重）----

func newRepoID(s *State) string {
	return uniqID("r", func(id string) bool { return s.repo(id) != nil })
}
func newProblemID(s *State) string {
	return uniqID("p", func(id string) bool { return s.problem(id) != nil })
}
func newRunID(s *State) string {
	return uniqID("run", func(id string) bool { return s.run(id) != nil })
}

func uniqID(prefix string, exists func(string) bool) string {
	for i := 0; i < 8; i++ {
		id := prefix + "-" + randHex(4)
		if !exists(id) {
			return id
		}
	}
	// 撞车兜底：加宽随机继续查重（兜底不再验重则可能撞库内既有 ID）
	for i := 0; i < 8; i++ {
		id := prefix + "-" + randHex(8)
		if !exists(id) {
			return id
		}
	}
	return prefix + "-" + randHex(16) // 16 字节熵下撞库内 ID 物理不可能，不再验
}

func repoNameTaken(s *State, name string) bool {
	for _, r := range s.Repos {
		if r.Name == name {
			return true
		}
	}
	return false
}
