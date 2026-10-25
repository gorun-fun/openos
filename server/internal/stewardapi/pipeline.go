// pipeline.go — 流水线工作面：GitLab（默认 git.gorun.fun，活动连接可换）项目
// 版本（tag）与对应 CI/CD 流水线状态的聚合，admin 专属。三层只读 + 一个修复
// 写桥：
//   - GET /api/steward/pipe/projects            全量聚合（版本 × CI，30s 缓存）
//   - GET /api/steward/pipe/pipelines/{p}/{pl}  流水线详情（阶段聚合 + job 清单，5s 缓存）
//   - GET /api/steward/pipe/jobs/{p}/{j}/trace  job 日志增量（全量拉取服务端切增量，2s 缓存，
//     剥 ANSI 转义——GitLab trace 满屏颜色码）
//   - POST /api/steward/pipe/fix                失败流水线转代码管家（进程内直调 steward 引擎：
//     自动注册 GitLab 仓库 + 组装失败上下文，见 handlePipeFix）
//
// GitLab 凭证只在服务端：数据源 = 活动 GitLab 连接（steward 引擎的账号登录）
// 优先，无连接回落 env（GlEnv：PAT 直用，或账密走 OAuth password grant 兑换
// 2h access_token 缓存临期重取；git credential 的 Basic auth 对 API 无效）。
// 凭证既服务 API 聚合，也经修复桥注入 steward 仓库的推送凭据（oauth2:token，
// PAT 稳定 / access_token 每次转修前刷新保运行期有效）。
package stewardapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"openos/server/internal/cache"
	"openos/server/internal/steward"
)

const (
	pipeCacheTTL    = 30 * time.Second // 聚合结果缓存（前端两 tab 30s 轮询共享）
	pipePagesMax    = 5                // 项目列表分页上限（100/页，500 项目足够）
	pipeTagsLimit   = 8                // 每项目取的版本数
	pipePipesLimit  = 10               // 每项目取的流水线数
	pipeFetchers    = 8                // 项目明细并发
	pipeTokenMargin = 2 * time.Minute  // password grant token 临期重取余量
	pipeDetTTL      = 5 * time.Second  // 流水线详情缓存（前端 5s 轮询不打爆上游）
	pipeTraceTTL    = 2 * time.Second  // job 日志缓存（运行中跟随 2s 轮询）
	pipeTraceTail   = 16 << 10         // 转修问题附带的失败日志尾部上限
)

// errGitlabNoCred 未配置凭证的可读提示（多处使用，收为常量防文案漂移）。
const errGitlabNoCred = "GitLab 未配置访问凭证——在代码管家「仓库」页连接 GitLab 账号，或配置 OPENOS_GITLAB_TOKEN / OPENOS_GITLAB_USERNAME/OPENOS_GITLAB_PASSWORD"

// GitLabClient GitLab API v4 客户端（凭证兑换与缓存在此，绝不外泄到浏览器）。
// 数据源解析（v0.0.32 起）：活动 GitLab 连接（steward 引擎的账号登录）优先，
// 无连接回落 env（OPENOS_GITLAB_*（GRF_OS_GITLAB_* 宿主兼容兜底））——请求期实时读，切换连接即生效。
type GitLabClient struct {
	env    GlEnv
	client  *http.Client
	Steward *steward.Engine // 活动 GitLab 连接来源（NewApp 装配；nil = 纯 env 模式）

	envTokens *steward.GlTokenCache // env 账密模式的 password-grant 兑换缓存（共享件，连接模式的缓存在引擎侧）

	fetchMu    sync.Mutex                      // 聚合取数串行（两 tab 30s 轮询并发只发一轮上游；详情/日志不持此锁）
	projCache  *cache.TTL[string, any]         // 聚合结果缓存（pipeCacheTTL，单条目）
	detCache   *cache.TTL[string, *pipeDetail] // 流水线详情缓存，键 "<projectID>/<pipelineID>"
	traceCache *cache.TTL[string, string]      // job 日志缓存，键 "<projectID>/<jobID>"
}

// NewGitLabClient 创建客户端（未验证连通性，首次调用时才兑换 token）。
func NewGitLabClient(env GlEnv) *GitLabClient {
	return &GitLabClient{
		env: env, client: &http.Client{Timeout: 20 * time.Second},
		envTokens:  steward.NewGlTokenCache(&http.Client{Timeout: 20 * time.Second}),
		projCache:  cache.NewTTL[string, any](pipeCacheTTL, 4),
		detCache:   cache.NewTTL[string, *pipeDetail](pipeDetTTL, 64),
		traceCache: cache.NewTTL[string, string](pipeTraceTTL, 64),
	}
}

// configured 是否具备访问凭证（活动 GitLab 连接，或 env PAT/账密二要素）。
func (g *GitLabClient) configured() bool {
	if g.Steward != nil && g.Steward.HasActiveGitLab() {
		return true
	}
	return g.env.Token != "" || (g.env.User != "" && g.env.Password != "")
}

// credential 一轮请求的已解析数据源。
type gitlabEndpoint struct {
	base         string // API 根（实例地址）
	token        string // Bearer
	passwordMode bool   // password-grant 短期令牌（401 时重兑重试一次；PAT 稳定不重试）
}

// endpoint 解析数据源与凭证：活动 GitLab 连接优先（令牌在引擎侧缓存/临期重兑），
// 无连接回落 env（PAT 直用；账密 password grant 经共享 GlTokenCache 兑换缓存）。
// 无凭证给可读错误（提示连接账号或配 env）。
func (g *GitLabClient) endpoint(ctx context.Context) (gitlabEndpoint, error) {
	if g.Steward != nil {
		if cred, err := g.Steward.ActiveGitLab(ctx); err == nil && cred != nil {
			return gitlabEndpoint{base: cred.BaseURL, token: cred.Token, passwordMode: !cred.PAT}, nil
		}
	}
	if g.env.Token != "" {
		return gitlabEndpoint{base: g.env.URL, token: g.env.Token}, nil
	}
	if g.env.User == "" || g.env.Password == "" {
		return gitlabEndpoint{}, errUpstream(errGitlabNoCred)
	}
	tok, err := g.envTokens.Exchange(ctx, "env", g.env.URL, g.env.User, g.env.Password)
	if err != nil {
		return gitlabEndpoint{}, err
	}
	return gitlabEndpoint{base: g.env.URL, token: tok, passwordMode: true}, nil
}

// invalidate 作废 password-grant 令牌缓存（401 强制重兑一次；连接模式的缓存在
// 引擎侧，一并作废）。
func (g *GitLabClient) invalidate() {
	g.envTokens.Invalidate("env")
	if g.Steward != nil {
		g.Steward.InvalidateActiveGitLabToken()
	}
}

// invalidateAll 数据源切换/删除时全量失效（令牌 + 聚合/详情/日志缓存——
// 项目 ID 是实例内的，换实例后旧缓存无意义）。
func (g *GitLabClient) invalidateAll() {
	g.invalidate()
	g.projCache.Clear()
	g.detCache.Clear()
	g.traceCache.Clear()
}

// do 请求核心（get/post 共用）：凭证注入 + 401（password-grant 模式）作废重兑
// 重试一次。不因 HTTP 状态报错（调用方自判），返回 状态码 / 响应头（分页用）/
// 原始响应体。原先 get 与 post 各写一份同款重试循环。
func (g *GitLabClient) do(ctx context.Context, method, path string, bodyLimit int64) (int, http.Header, []byte, error) {
	ep, err := g.endpoint(ctx)
	if err != nil {
		return 0, nil, nil, err
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method,
			strings.TrimRight(ep.base, "/")+"/api/v4"+path, nil)
		if err != nil {
			return 0, nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+ep.token)
		resp, err := g.client.Do(req)
		if err != nil {
			return 0, nil, nil, errUpstream("GitLab 不可达: " + steward.ErrShort(err))
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 && ep.passwordMode {
			g.invalidate()
			if ep, err = g.endpoint(ctx); err != nil {
				return 0, nil, nil, err
			}
			continue
		}
		return resp.StatusCode, resp.Header, raw, nil
	}
}

func (g *GitLabClient) get(ctx context.Context, path string) (int, http.Header, []byte, error) {
	return g.do(ctx, http.MethodGet, path, 8<<20)
}

// Get 调 GitLab API v4（path 以 / 起头）解析到 out，返回响应头（分页用）。
// 非 2xx 一律报错（含可读的 GitLab 错误信息）。
func (g *GitLabClient) Get(ctx context.Context, path string, out any) (http.Header, error) {
	st, hdr, raw, err := g.get(ctx, path)
	if err != nil {
		return nil, err
	}
	if st >= 300 {
		return nil, errUpstream(fmt.Sprintf("GitLab %s → %d: %s", path, st, steward.ErrMessage(raw)))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, errUpstream("GitLab 响应解析失败: " + err.Error())
		}
	}
	return hdr, nil
}

// GetRaw 调 GitLab API v4 取原始响应体与状态码（trace 等纯文本端点；404 等
// 状态由调用方自判，如 job 尚无日志）。
func (g *GitLabClient) GetRaw(ctx context.Context, path string) (int, []byte, error) {
	st, _, raw, err := g.get(ctx, path)
	return st, raw, err
}

// post 写操作核心（job play/retry/cancel）：与 get 同一 do 实现（401 重兑重试）。
func (g *GitLabClient) post(ctx context.Context, path string) (int, []byte, error) {
	st, _, raw, err := g.do(ctx, http.MethodPost, path, 4<<20)
	return st, raw, err
}
