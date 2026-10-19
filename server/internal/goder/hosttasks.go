// hosttasks.go — 宿主机对机「代用户无头执行 goder 会话」：/api/host/goder/*。
//
// 业务宿主（如 grf-os 的自动修复引擎）以服务端身份为指定 openos 用户创建一个
// gh server 会话并后台执行任务（background 模式），会话落在该用户自己的
// goder 数据目录里——用户打开 goder 即可跟进完整修复过程与续聊。
//
// 执行链：EnsureAIUser（经管理面声明该用户的托管/声明供应商，先于 gh 懒
// 启动）→ 管理面解析模型（fail-fast：歧义/未含给可读错误，择序由 goder-serve
// 与 gh ResolveModel 对齐维护，openos 不再复刻）→ 回环调 goder-serve
//（X-GRF-UID 身份，与 ProxyHandler 同款注入）→ POST /goder-api/v1/responses
//（首请求带 grf:session：workspace 必须已存在，无人值守语义固定
// write-workspace + auto_approve，不开放给调用方）→ background 立回 queued，
// 调用方轮询 GET 取终态 / POST cancel 取消。
//
// 会话归组：经 goder-serve 管理面把会话追加进服务端会话索引
//（POST /admin/users/{uid}/index/sessions，读改写在服务端一把锁内完成）；
// 前端 GoderStudio 的 setPersistence 适配器同样以 goder-serve 索引为权威
//（load/save 走 /goder-api/index）。历史实现经 user_settings 的 goder.index
// 镜像三方耦合（服务端直写 + 浏览器读改写合并）已收敛为单一存储。归组
// best-effort：失败不影响任务执行，会话回落「未分组」仍可见。
//
// 安全边界：X-Exchange-Secret 全信任（宿主级）；不装 osmcp 桌面控制插件——
// 无头修复会话不需要桌面工具面，也不给模型多余的攻击面（用户自己打开 goder
// 时 /goder-api 路径会 EnsureUser 补装，gh 进程空闲回收后自然带上）。
package goder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"openos/server/internal/webres"
)

// goder.index 归组的默认值见 index.go（分组编排整体拆出）；
// 管理面客户端见 admin.go。

// HostTasks 宿主机对机 goder 任务端点（main 以 hostOnly 包装挂路由）。
type HostTasks struct {
	m   *Manager
	hc  *http.Client
	log func(string, ...any)
}

// NewHostTasks 构造（会话归组走 goder-serve 管理面，不再需要 openos 库）。
func NewHostTasks(m *Manager) *HostTasks {
	return &HostTasks{
		m:   m,
		hc:  &http.Client{Timeout: 60 * time.Second},
		log: log.Printf,
	}
}

// Addr goder-serve 监听地址（回环调用用）。
func (m *Manager) Addr() string { return m.addr }

// HostTaskCreateRequest POST /api/host/goder/tasks 请求体。
type HostTaskCreateRequest struct {
	UID       string `json:"uid"`        // openos 用户名（= exchange 传入的 username）
	Workspace string `json:"workspace"`  // 会话工作区（已存在的绝对路径，通常是宿主准备好的仓库工作区）
	Task      string `json:"task"`       // 任务全文（首条用户消息）
	Model     string `json:"model"`      // 模型：裸名（如 glm-5.3-flash）或全限定名（TokenHub/glm-5.3-flash）；空 = 用户默认
	Title     string `json:"title"`      // 会话在分组里的标题；空 = 任务首行截断
	Group     string `json:"group"`      // 分组名；空 = 自动修复
	ProjectID string `json:"project_id"` // 分组项目 id；空 = fixer（同一业务复用稳定 id，改名不重建分组）
	Actor     string `json:"actor"`      // 身份显示名（透传 X-GRF-Name）；空 = 自动修复
	MaxSteps     int    `json:"max_steps"`
	SystemPrompt string `json:"system_prompt"`
}

// hostTaskContext 无人值守宿主任务的会话环境简报：与用户在桌面打开
// goder 时的 host_context（GoderStudio 注入）同源语义，但无桌面工具面
// （不装 osmcp，见文件头）。核心是交互约束——无头会话里 ask_user 类
// 交互会挂起等待永不到来的用户。
func hostTaskContext(uid string) string {
	return fmt.Sprintf(
		"本会话由 OpenOS 平台以服务端身份无人值守启动（代用户 %s），没有用户在旁边："+
			"不要调用 ask_user 类交互工具等待回答，工具审批已自动放行；"+
			"工作区是宿主准备好的目录，自主推进任务，完成或受阻时在最终回复中明确说明结果与遗留问题；"+
			"用户之后会在自己的 goder 界面里看到完整过程并可能续聊。",
		uid)
}

// Create 代用户创建并后台启动一个 goder 会话任务。
func (h *HostTasks) Create(w http.ResponseWriter, r *http.Request) {
	var req HostTaskCreateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
		webres.Error(w, 400, "请求体解析失败: "+err.Error())
		return
	}
	req.UID = strings.TrimSpace(req.UID)
	req.Workspace = strings.TrimSpace(req.Workspace)
	if req.UID == "" || req.Workspace == "" || strings.TrimSpace(req.Task) == "" {
		webres.Error(w, 400, "uid / workspace / task 必填")
		return
	}
	if !filepath.IsAbs(req.Workspace) {
		webres.Error(w, 400, "workspace 必须是绝对路径")
		return
	}
	if st, err := os.Stat(req.Workspace); err != nil || !st.IsDir() {
		webres.Error(w, 400, "workspace 目录不存在: "+req.Workspace)
		return
	}
	if !h.ready() {
		webres.Error(w, 503, "goder 服务未就绪（未部署或未启用，见 GET /api/goder/status）")
		return
	}
	// 供应商就位（幂等，先于 gh 懒启动；不装 osmcp 插件，见文件头说明）
	h.m.EnsureAIUser(req.UID)

	model, err := h.resolveModel(r.Context(), req.UID, req.Model)
	if err != nil {
		webres.Error(w, 400, err.Error())
		return
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = deriveHostTitle(req.Task)
	}
	group := req.Group
	if group == "" {
		group = fixerGroupName
	}
	projectID := req.ProjectID
	if projectID == "" {
		projectID = fixerProjectID
	}
	actor := req.Actor
	if actor == "" {
		actor = fixerGroupName
	}
	maxSteps := req.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultFixSteps
	}
	sessCfg := map[string]any{
		"workspace":    req.Workspace,
		"policy":       "write-workspace",
		"auto_approve": true,
		"max_steps":    maxSteps,
	}
	// 宿主环境简报（gh ≥ host_context 版本独立拼段，与 system_prompt 叠加；
	// 旧 gh 忽略未知字段不受影响）：无人值守语义是环境事实而非任务指令，
	// 放这里而不是塞进调用方的 system_prompt——调用方（如 grf-os fixer）
	// 的提示词保持纯业务。
	sessCfg["host_context"] = hostTaskContext(req.UID)
	if sp := strings.TrimSpace(req.SystemPrompt); sp != "" {
		sessCfg["system_prompt"] = sp
	}
	body, _ := json.Marshal(map[string]any{
		"model":       model,
		"input":       req.Task,
		"background":  true,
		"grf:session": sessCfg,
	})
	var created ghResponse
	if err := h.loopback(r.Context(), http.MethodPost, "/goder-api/v1/responses", body, req.UID, actor, &created); err != nil {
		webres.Error(w, 502, "创建 goder 会话失败: "+err.Error())
		return
	}
	if created.SessionID == "" || created.ID == "" {
		webres.Error(w, 502, "goder 会话创建响应异常（缺 grf:session_id / id）")
		return
	}
	// 归组（best-effort：失败不影响任务执行，会话回落「未分组」仍可见）
	if err := h.indexSession(r.Context(), req.UID, created.SessionID, projectID, group, title, model, created.ID); err != nil {
		h.log("[goder] host 任务归组失败(%s/%s): %v", req.UID, created.SessionID, err)
	}
	webres.WriteJSON(w, map[string]any{
		"session_id":  created.SessionID,
		"response_id": created.ID,
		"model":       model,
		"status":      created.Status,
		"uid":         req.UID,
	})
}

// Get GET /api/host/goder/tasks/{uid}/{id} — 轮询任务终态。
func (h *HostTasks) Get(w http.ResponseWriter, r *http.Request) {
	h.withTask(w, r, "查询 goder 任务失败", func(uid, id string) error {
		h.m.EnsureAIUser(uid)
		var res ghResponse
		if err := h.loopback(r.Context(), http.MethodGet, "/goder-api/v1/responses/"+id, nil, uid, fixerGroupName, &res); err != nil {
			return err
		}
		out := map[string]any{
			"status":     res.Status,
			"model":      res.Model,
			"session_id": res.SessionID,
		}
		if res.Error != nil {
			out["error"] = res.Error.Message
		}
		if res.IncompleteDetails != nil {
			out["incomplete_reason"] = res.IncompleteDetails.Reason
		}
		if txt := res.assistantText(); txt != "" {
			out["output_text"] = txt
		}
		if res.Usage != nil {
			out["usage"] = map[string]int{
				"input_tokens":  res.Usage.InputTokens,
				"output_tokens": res.Usage.OutputTokens,
			}
		}
		webres.WriteJSON(w, out)
		return nil
	})
}

// Cancel POST /api/host/goder/tasks/{uid}/{id}/cancel — 取消在跑回合（幂等）。
func (h *HostTasks) Cancel(w http.ResponseWriter, r *http.Request) {
	h.withTask(w, r, "取消 goder 任务失败", func(uid, id string) error {
		var res json.RawMessage
		if err := h.loopback(r.Context(), http.MethodPost, "/goder-api/v1/responses/"+id+"/cancel", []byte("{}"), uid, fixerGroupName, &res); err != nil {
			return err
		}
		webres.WriteJSON(w, map[string]any{"ok": true})
		return nil
	})
}

// withTask Get/Cancel 的公共前言：uid/id 必填校验 + ready 检查后以 uid/id
// 执行回调；回调返回的 error 统一按 hostHTTPError 解包——goder-serve 的
// 非 200 原状态原体透传，其余错误 502 + failMsg。回调写完各自的 200 响应后
// 返回 nil。
func (h *HostTasks) withTask(w http.ResponseWriter, r *http.Request, failMsg string, fn func(uid, id string) error) {
	uid, id := r.PathValue("uid"), r.PathValue("id")
	if uid == "" || id == "" {
		webres.Error(w, 400, "uid / id 必填")
		return
	}
	if !h.ready() {
		webres.Error(w, 503, "goder 服务未就绪")
		return
	}
	if err := fn(uid, id); err != nil {
		if he, ok := err.(*hostHTTPError); ok {
			webres.WriteJSONStatus(w, he.status, he.body)
			return
		}
		webres.Error(w, 502, failMsg+": "+err.Error())
		return
	}
}

// ---- 内部 ----

// ready 惰性拉起并等待 goder-serve 就绪（与 ProxyHandler 同款 5s 等待，
// 收敛到 Manager.ensureReady）。
func (h *HostTasks) ready() bool {
	return h.m.ensureReady(5 * time.Second)
}

type hostHTTPError struct {
	status int
	body   map[string]any
}

func (e *hostHTTPError) Error() string {
	if ee, ok := e.body["error"]; ok {
		return fmt.Sprint(ee)
	}
	return fmt.Sprintf("goder-serve HTTP %d", e.status)
}

// loopback 以指定用户身份调 goder-serve（header 身份注入与 ProxyHandler
// 同源：覆盖式 set，环回直连不受外部伪造影响；name 为身份显示名）。
func (h *HostTasks) loopback(ctx context.Context, method, path string, body []byte, uid, name string, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+h.m.Addr()+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GRF-UID", uid)
	req.Header.Set("X-GRF-Name", name)
	resp, err := h.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		be := map[string]any{}
		_ = json.Unmarshal(raw, &be)
		if _, ok := be["error"]; !ok {
			be["error"] = string(raw)
		}
		return &hostHTTPError{status: resp.StatusCode, body: be}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("响应解析失败: %w", err)
		}
	}
	return nil
}

// ghResponse gh server Responses 资源的精简视图（轮询够用）。
type ghResponse struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Model     string `json:"model"`
	SessionID string `json:"grf:session_id"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Output []ghItem `json:"output"`
}

type ghItem struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// assistantText 拼最后一条 assistant 消息的文本（修复总结摘要用）。
func (r *ghResponse) assistantText() string {
	for i := len(r.Output) - 1; i >= 0; i-- {
		it := r.Output[i]
		if it.Type != "message" || it.Role != "assistant" {
			continue
		}
		var sb strings.Builder
		for _, p := range it.Content {
			if p.Type == "output_text" || p.Type == "text" {
				sb.WriteString(p.Text)
			}
		}
		return sb.String()
	}
	return ""
}

// resolveModel 把请求模型解析成全限定名（provider/model）。解析择序（默认
// 供应商优先 → 恰一家命中；多家歧义如实上报，不武断挑选）由 goder-serve
// 管理面承载（与 gh config.ResolveModel 对齐维护，openos 不再复刻规则）；
// 这里只做 fail-fast 的错误文案合成——无头任务绝不静默换模型，套餐未含/
// 未同步给管理员可读的错误是最常见诉求。空 = 用户默认。
func (h *HostTasks) resolveModel(ctx context.Context, uid, want string) (string, error) {
	res, err := h.m.adminResolveModel(ctx, uid, want)
	if err != nil {
		return "", fmt.Errorf("用户 %s 的模型配置查询失败（goder 管理面不可达）: %v", uid, err)
	}
	switch res.Status {
	case "ok":
		return res.Resolved, nil
	case "no_default":
		return "", fmt.Errorf("用户 %s 未配置默认模型（token-hub 套餐未绑定或未同步）", uid)
	case "ambiguous":
		return "", fmt.Errorf("模型 %s 同时在用户 %s 的多家供应商清单（%s），请用全限定名 供应商/模型 指定", want, uid, strings.Join(res.Candidates, "、"))
	case "not_found":
		if strings.Contains(want, "/") {
			return "", fmt.Errorf("模型 %s 不在用户 %s 的可用清单（供应商未配置或套餐未含）", want, uid)
		}
		return "", fmt.Errorf("模型 %s 不在用户 %s 的可用清单（token-hub 套餐未含或未同步，可在「设置 → AI」或套餐侧确认）", want, uid)
	default: // unreadable
		return "", fmt.Errorf("用户 %s 的 goder 模型配置不可读（先登录一次 goder 或检查 AI 供应商同步）", uid)
	}
}

// indexSession / deriveHostTitle（会话归组编排）见 index.go。
