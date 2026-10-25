// gh.go — 内置 Goder 引擎（grf-harness gh 二进制）的子进程调用。
//
// 单任务模式：`gh "任务描述"` 非交互直出、跑完退出（非 TTY 自动降级纯文本行
// 模式，见 grf-harness docs/integration.md §10）。配置经 GRF_HOME 重定位到
// steward/grf-home/settings.json——宿主按 token-hub 套餐渲染（与 goder 的
// AISyncer 同源：base_url = /v1、api_key = 主 Key、models = 实际可用清单），
// 附带无人值守三件：auto_approve / policy=write-workspace / max_steps。
// gh 在进程启动时读一次盘（新版另在回合边界按 mtime^size 指纹热跟随
// settings.json 变化）；单任务进程跑完即退，运行前重写即首个回合生效。
package steward

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"openos/server/internal/safeenv"
)

// fetchModels 拉模型清单（60s 缓存；OpenAI 兼容 /v1/models）。
func (e *Engine) fetchModels(ctx context.Context) ([]string, error) {
	e.mu.Lock()
	if e.modelsCache != nil && time.Since(e.modelsAt) < time.Minute {
		models := e.modelsCache
		e.mu.Unlock()
		return models, nil
	}
	e.mu.Unlock()

	if e.llmKey == "" {
		return nil, fmt.Errorf("未配置 LLM 密钥（OPENOS_STEWARD_LLM_API_KEY（或平台 AI 配置 OPENOS_AI_API_KEY））")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(e.llmBase, "/")+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+e.llmKey)
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/v1/models → %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("模型清单为空")
	}
	e.mu.Lock()
	e.modelsCache, e.modelsAt = models, time.Now()
	e.mu.Unlock()
	return models, nil
}

// resolveModel 生效模型：App 内设置 > 环境默认 > 清单首个。
func (e *Engine) resolveModel(ctx context.Context) string {
	var stored string
	e.st.View(func(s *State) { stored = s.Settings.Model })
	if m := firstNonEmpty(stored, e.defModel); m != "" {
		return m
	}
	if models, err := e.fetchModels(ctx); err == nil {
		return models[0]
	}
	return ""
}

// writeGhSettings 渲染 grf-home/settings.json（每次运行前重写：模型清单/密钥
// 变化下一次运行即生效——单任务进程短命，无需依赖 gh 的回合边界热跟随）。
func (e *Engine) writeGhSettings(ctx context.Context, model string) error {
	if e.llmKey == "" {
		return fmt.Errorf("未配置 LLM 密钥（OPENOS_STEWARD_LLM_API_KEY（或平台 AI 配置 OPENOS_AI_API_KEY））")
	}
	if model == "" {
		return fmt.Errorf("未确定修复模型（OPENOS_STEWARD_MODEL 或设置页选择）")
	}
	models, err := e.fetchModels(ctx)
	if err != nil || len(models) == 0 {
		models = []string{model} // 清单拉取失败回落单模型，别让修复卡死
	}
	if err := os.MkdirAll(e.grfHome, 0o755); err != nil {
		return err
	}
	settings := map[string]any{
		"providers": map[string]any{
			"TokenHub": map[string]any{
				"base_url": strings.TrimRight(e.llmBase, "/") + "/v1",
				"api_key":  e.llmKey,
				"models":   models,
			},
		},
		// 默认模型用全限定名：裸名解析是 gh 6419b4c（0.0.1-alpha.29+）才在
		// 服务端实装的（此前为死代码），生产镜像里的 gh 版本可能滞后——限定名
		// 在新旧版本上行为一致，勿「简化」成裸名
		"default":      "TokenHub/" + model,
		"auto_approve": true,              // 无人值守：沙箱升级/计划自动批准
		"policy":       "write-workspace", // 写作区沙箱：覆写前须本会话读过
		"max_steps":    200,
	}
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	// 0600：内含 api_key
	return os.WriteFile(ghSettingsPath(e.grfHome), raw, 0o600)
}

func ghSettingsPath(grfHome string) string { return grfHome + "/settings.json" }

// ghRun 起一次 gh 修复任务：stdout/stderr 全量进运行日志；ctx 取消/超时杀整个
// 进程组（gh 的 bash 工具会派生 shell，只杀主进程会留孤儿进程继续写日志）。
func (e *Engine) ghRun(ctx context.Context, workspace, task string, logw io.Writer) error {
	bin := e.ghBin()
	if bin == "" {
		return fmt.Errorf("未找到 gh 二进制（设置 OPENOS_STEWARD_GH_BIN；生产镜像内置 /app/goder/gh）")
	}
	cmd := exec.CommandContext(ctx, bin, task)
	cmd.Dir = workspace
	// safeenv 白名单环境（2026-10 安全审计第二版）：gh 任务文本来自外部 issue/上报，
	// auto_approve 下提示注入可让 agent 执行 env——绝不让 OPENOS_* 秘密进入该链
	cmd.Env = safeenv.Env("GRF_HOME=" + e.grfHome)
	cmd.Stdout = logw
	cmd.Stderr = logw
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 gh: %w", err)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-done:
		}
	}()
	err := cmd.Wait()
	close(done)
	return err
}

// buildTask 拼给 gh 的修复任务描述。
func buildTask(p *Problem, r *Repo) string {
	var b strings.Builder
	b.WriteString("修复当前工作区（git 仓库）里一个已确认的问题，做最小必要的改动。\n\n")
	fmt.Fprintf(&b, "【问题标题】%s\n", p.Title)
	if p.Body != "" {
		fmt.Fprintf(&b, "【问题描述】\n%s\n", p.Body)
	}
	switch p.Source {
	case SrcGitHub:
		fmt.Fprintf(&b, "\n【来源】GitHub issue%s", issueRef(p))
		if p.Reporter != "" {
			fmt.Fprintf(&b, "（作者 %s）", p.Reporter)
		}
		b.WriteString("\n")
	case SrcReport:
		if p.Reporter != "" {
			fmt.Fprintf(&b, "\n【来源】外部系统上报（%s）\n", p.Reporter)
		} else {
			b.WriteString("\n【来源】外部系统上报\n")
		}
	case SrcManual:
		b.WriteString("\n【来源】管理员录入\n")
	}
	b.WriteString(`
【要求】
- 先读仓库的 AGENTS.md / README 与相关代码，遵循既有代码风格、注释语言与项目约定。
- 定位并修复问题本身，不做无关重构，不引入新依赖除非确有必要。
- 不要执行 git commit / push，不要创建 tag——提交由外部系统完成。
- 不要改动或删除 .grf/ 目录（agent 会话数据）。
- 完成后用两三句话总结根因与改动。`)
	if len(p.Labels) > 0 {
		fmt.Fprintf(&b, "\n\n【标签】%s", strings.Join(p.Labels, ", "))
	}
	return b.String()
}

func issueRef(p *Problem) string {
	if p.ExtKey == "" {
		return ""
	}
	// ExtKey 形如 gh:owner/repo#12
	return " " + strings.TrimPrefix(p.ExtKey, "gh:")
}
