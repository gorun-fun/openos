// engine.go — 引擎装配：环境变量 → steward.Options；goder 执行器直调
// hosttasks 内部面（进程内，不再绕 HTTP 回环）；env 凭证首启种子。
//
// 环境变量命名：OPENOS_STEWARD_* 为主（上游化后的正名），GRF_OS_STEWARD_*
// 兜底（grf-os 宿主部署存量无感——子进程全量继承宿主环境，compose 不改也
// 能跑）。GitLab 凭证同理 OPENOS_GITLAB_* → GRF_OS_GITLAB_* 兜底。
package stewardapi

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"openos/server/internal/goder"
	"openos/server/internal/steward"
)

// getenv 环境变量解析（名次序取首个非空；主名在前、宿主兼容名兜底）。
func getenv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// GlEnv GitLab 流水线数据源的 env 兜底凭证（活动 GitLab 连接优先，见
// pipeline.go endpoint）。
type GlEnv struct {
	URL, Token, User, Password string
}

// ReadGlEnv 从环境解析 GitLab 兜底凭证。
func ReadGlEnv(dataDir string) GlEnv {
	return GlEnv{
		URL:      getenv("OPENOS_GITLAB_URL", "GRF_OS_GITLAB_URL", "https://git.gorun.fun"),
		Token:    getenv("OPENOS_GITLAB_TOKEN", "GRF_OS_GITLAB_TOKEN"),
		User:     getenv("OPENOS_GITLAB_USERNAME", "GRF_OS_GITLAB_USERNAME"),
		Password: getenv("OPENOS_GITLAB_PASSWORD", "GRF_OS_GITLAB_PASSWORD"),
	}
}

// GoderTaskerHost goder 执行器的进程内实现：直调 hosttasks 内部面
//（HTTP 端点 /api/host/goder/tasks 的同流程，供宿主机对机与引擎共用）。
type GoderTaskerHost struct{ HT *goder.HostTasks }

func (t GoderTaskerHost) CreateTask(ctx context.Context, task steward.GoderTask) (string, string, string, error) {
	return t.HT.CreateTask(ctx, goder.HostTaskCreateRequest{
		UID: task.UID, Workspace: task.Workspace, Task: task.Task,
		Model: task.Model, Title: task.Title, Group: task.Group,
	})
}

func (t GoderTaskerHost) PollTask(ctx context.Context, uid, respID string) (steward.GoderStatus, error) {
	st, err := t.HT.PollTask(ctx, uid, respID)
	if err != nil {
		return steward.GoderStatus{}, err
	}
	return steward.GoderStatus{
		Status: st.Status, Error: st.Error, Incomplete: st.Incomplete, OutputText: st.OutputText,
	}, nil
}

func (t GoderTaskerHost) CancelTask(ctx context.Context, uid, respID string) error {
	return t.HT.CancelTask(ctx, uid, respID)
}

// NewEngine 按环境装配引擎（OPENOS_STEWARD=off 或初始化失败返回 nil，服务
// 不受影响；停用时 App 面提示未启用）。
func NewEngine(dataDir string, ht *goder.HostTasks) *steward.Engine {
	if getenv("OPENOS_STEWARD", "GRF_OS_STEWARD_OFF") == "off" { // GRF_OS_STEWARD_OFF=1 宿主兼容形态
		return nil
	}
	ghBin := getenv("OPENOS_STEWARD_GH_BIN", "OPENOS_STEWARD_GH_BIN", "OPENOS_GH_BIN")
	if ghBin == "" {
		if p, err := exec.LookPath("gh"); err == nil {
			ghBin = p
		}
	}
	// LLM 默认链（仅 gh 兜底执行器用；goder 执行器的模型凭据在操作者 goder 侧）：
	// 专属覆盖 → 平台 AI 配置 → grf-os 宿主兼容（token-hub 主 Key）
	llmBase := getenv("OPENOS_STEWARD_LLM_BASE_URL", "GRF_OS_STEWARD_LLM_BASE_URL", "OPENOS_AI_BASE_URL", "GRF_OS_TH_URL")
	llmKey := getenv("OPENOS_STEWARD_LLM_API_KEY", "GRF_OS_STEWARD_LLM_API_KEY", "OPENOS_AI_API_KEY", "GRF_OS_TH_API_KEY")
	executor := getenv("OPENOS_STEWARD_EXECUTOR", "OPENOS_STEWARD_EXECUTOR")
	if executor != "gh" {
		executor = "goder"
	}
	timeoutMin, _ := strconv.Atoi(getenv("OPENOS_STEWARD_TIMEOUT_MIN", "OPENOS_STEWARD_TIMEOUT_MIN"))
	if timeoutMin <= 0 {
		timeoutMin = 30
	}
	syncMin, _ := strconv.Atoi(getenv("OPENOS_STEWARD_SYNC_MIN", "OPENOS_STEWARD_SYNC_MIN"))
	if syncMin <= 0 {
		syncMin = 5
	}
	eng, err := steward.New(steward.Options{
		DataDir:  filepath.Join(dataDir, "steward"),
		HomesDir: filepath.Join(dataDir, "homes"), // 工作区落操作者 home「代码管家」目录（文件 App 可见）
		GhBin:    ghBin,
		LLMBase:  llmBase,
		LLMKey:   llmKey,
		DefModel: getenv("OPENOS_STEWARD_MODEL", "OPENOS_STEWARD_MODEL", "glm-5.3-flash"),

		Executor:        executor,
		Goder:           GoderTaskerHost{HT: ht},
		DefaultOperator: getenv("OPENOS_STEWARD_GODER_UID", "OPENOS_STEWARD_GODER_UID"),

		ReportKeyEnv:   getenv("OPENOS_STEWARD_REPORT_KEY", "OPENOS_STEWARD_REPORT_KEY"),
		GitHubTokenEnv: getenv("OPENOS_STEWARD_GITHUB_TOKEN", "OPENOS_STEWARD_GITHUB_TOKEN"),
		MarketDir:      getenv("OPENOS_STEWARD_MARKET_DIR", "OPENOS_STEWARD_MARKET_DIR"),
		GitName:        getenv("OPENOS_STEWARD_GIT_NAME", "OPENOS_STEWARD_GIT_NAME"),
		GitEmail:       getenv("OPENOS_STEWARD_GIT_EMAIL", "OPENOS_STEWARD_GIT_EMAIL"),
		Timeout:        time.Duration(timeoutMin) * time.Minute,
		SyncEvery:      time.Duration(syncMin) * time.Minute,
	})
	if err != nil {
		log.Printf("[steward] 引擎初始化失败（App 将提示未启用）: %v", err)
		return nil
	}
	if executor == "gh" && ghBin == "" {
		log.Printf("[steward] 未找到 gh 二进制（OPENOS_STEWARD_GH_BIN / OPENOS_GH_BIN / PATH），修复任务将失败于启动 gh")
	}
	return eng
}

// SeedEnvConnections env→连接 首次种子：无任何连接且未种过时，把 GitLab env
// 凭证（PAT 或账密）自动建为平台连接（置活动）——存量部署无感升级到「登录
// 方式二」。用户删除种子连接后不复活（EnvSeedDone 标记）；种子失败（平台暂
// 不可达）下轮启动重试。
func SeedEnvConnections(eng *steward.Engine, env GlEnv, ghToken string) {
	if eng == nil || eng.EnvSeedDone() {
		return
	}
	if len(eng.ListConnections()) == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if env.Token != "" || (env.User != "" && env.Password != "") {
			if _, err := eng.AddConnection(ctx, steward.ConnInput{
				Platform: steward.PlatformGitLab, BaseURL: env.URL,
				PAT: env.Token, User: env.User, Password: env.Password,
			}); err != nil {
				log.Printf("[steward] env GitLab 凭证种子为连接失败（下次启动重试）: %v", err)
				return
			}
			log.Printf("[steward] 已把 env GitLab 凭证种子为平台连接（活动数据源）")
		}
		if ghToken != "" {
			if _, err := eng.AddConnection(ctx, steward.ConnInput{
				Platform: steward.PlatformGitHub, PAT: ghToken,
			}); err != nil {
				log.Printf("[steward] env GitHub Token 种子为连接失败（下次启动重试）: %v", err)
				return
			}
			log.Printf("[steward] 已把 env GitHub Token 种子为平台连接")
		}
	}
	eng.MarkEnvSeedDone()
}
