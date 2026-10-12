// hosttasks.go — 宿主机对机「代用户无头执行 goder 会话」：/api/host/goder/*。
//
// 业务宿主（如 grf-os 的自动修复引擎）以服务端身份为指定 openos 用户创建一个
// gh server 会话并后台执行任务（background 模式），会话落在该用户自己的
// goder 数据目录里——用户打开 goder 即可跟进完整修复过程与续聊。
//
// 执行链：EnsureAIUser（渲染该用户的托管/声明供应商，先于 gh 懒启动）→
// 回环调 goder-serve（X-GRF-UID 身份，与 ProxyHandler 同款注入）→
// POST /goder-api/v1/responses（首请求带 grf:session：workspace 必须已存在，
// 无人值守语义固定 write-workspace + auto_approve，不开放给调用方）→
// background 立回 queued，调用方轮询 GET 取终态 / POST cancel 取消。
//
// 会话归组：goder 的分组索引（项目 → 会话）在前端是本地态，openos 嵌入态已
// 镜像到 user_settings 的 goder.index 键（GoderStudio 的 setPersistence 适配器：
// load 服务端优先、save 防抖回写）。这里在创建成功后把会话条目写进该键并确保
// 「自动修复」项目存在，用户下次打开 goder 会话即出现在该分组下；save 侧的
// 合并逻辑（同文件 GoderStudio.vue）防止陈旧浏览器副本把代建条目冲回未分组。
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
	"sort"
	"strings"
	"time"

	"openos/server/internal/store"
)

// goder.index 镜像键与自动修复分组（字段名与 goder.js persistence.ts 的
// ProjectsIndex 严格一致，未知键（automation 等）原样保真）。
const (
	hostIndexKey     = "goder.index"
	fixerProjectID   = "fixer"
	fixerGroupName   = "自动修复"
	defaultFixModels = 200
)

// HostTasks 宿主机对机 goder 任务端点（main 以 hostOnly 包装挂路由）。
type HostTasks struct {
	m   *Manager
	db  *store.DB
	hc  *http.Client
	log func(string, ...any)
}

// NewHostTasks 构造（db 用于分组索引读写；可为 nil = 不归组，仅执行）。
func NewHostTasks(m *Manager, db *store.DB) *HostTasks {
	return &HostTasks{
		m: m, db: db,
		hc:  &http.Client{Timeout: 60 * time.Second},
		log: log.Printf,
	}
}

// Addr goder-serve 监听地址（回环调用用）。
func (m *Manager) Addr() string { return m.addr }

// HostTaskCreateRequest POST /api/host/goder/tasks 请求体。
type HostTaskCreateRequest struct {
	UID      string `json:"uid"`      // openos 用户名（= exchange 传入的 username）
	Workspace string `json:"workspace"` // 会话工作区（已存在的绝对路径，通常是宿主准备好的仓库工作区）
	Task     string `json:"task"`     // 任务全文（首条用户消息）
	Model    string `json:"model"`    // 模型：裸名（如 glm-5.3-flash）或全限定名（TokenHub/glm-5.3-flash）；空 = 用户默认
	Title    string `json:"title"`    // 会话在分组里的标题；空 = 任务首行截断
	Group    string `json:"group"`    // 分组名；空 = 自动修复
	MaxSteps int    `json:"max_steps"`
	SystemPrompt string `json:"system_prompt"`
}

// hostTaskContext 无人值守宿主任务的会话环境简报：与用户在桌面打开
// goder 时的 host_context（GoderStudio 注入）同源语义，但无桌面工具面
//（不装 osmcp，见文件头）。核心是交互约束——无头会话里 ask_user 类
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
		hostErr(w, 400, "请求体解析失败: "+err.Error())
		return
	}
	req.UID = strings.TrimSpace(req.UID)
	req.Workspace = strings.TrimSpace(req.Workspace)
	if req.UID == "" || req.Workspace == "" || strings.TrimSpace(req.Task) == "" {
		hostErr(w, 400, "uid / workspace / task 必填")
		return
	}
	if !filepath.IsAbs(req.Workspace) {
		hostErr(w, 400, "workspace 必须是绝对路径")
		return
	}
	if st, err := os.Stat(req.Workspace); err != nil || !st.IsDir() {
		hostErr(w, 400, "workspace 目录不存在: "+req.Workspace)
		return
	}
	if !h.ready() {
		hostErr(w, 503, "goder 服务未就绪（未部署或未启用，见 GET /api/goder/status）")
		return
	}
	// 供应商就位（幂等，先于 gh 懒启动；不装 osmcp 插件，见文件头说明）
	h.m.EnsureAIUser(req.UID)

	model, err := h.resolveModel(req.UID, req.Model)
	if err != nil {
		hostErr(w, 400, err.Error())
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
	maxSteps := req.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultFixModels
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
	if err := h.loopback(r.Context(), http.MethodPost, "/goder-api/v1/responses", body, req.UID, &created); err != nil {
		hostErr(w, 502, "创建 goder 会话失败: "+err.Error())
		return
	}
	if created.SessionID == "" || created.ID == "" {
		hostErr(w, 502, "goder 会话创建响应异常（缺 grf:session_id / id）")
		return
	}
	// 归组（best-effort：失败不影响任务执行，会话回落「未分组」仍可见）
	if h.db != nil {
		if err := h.indexSession(req.UID, created.SessionID, title, group, model, created.ID); err != nil {
			h.log("[goder] host 任务归组失败(%s/%s): %v", req.UID, created.SessionID, err)
		}
	}
	writeHostJSON(w, 200, map[string]any{
		"session_id":  created.SessionID,
		"response_id": created.ID,
		"model":       model,
		"status":      created.Status,
		"uid":         req.UID,
	})
}

// Get GET /api/host/goder/tasks/{uid}/{id} — 轮询任务终态。
func (h *HostTasks) Get(w http.ResponseWriter, r *http.Request) {
	uid, id := r.PathValue("uid"), r.PathValue("id")
	if uid == "" || id == "" {
		hostErr(w, 400, "uid / id 必填")
		return
	}
	if !h.ready() {
		hostErr(w, 503, "goder 服务未就绪")
		return
	}
	h.m.EnsureAIUser(uid)
	var res ghResponse
	if err := h.loopback(r.Context(), http.MethodGet, "/goder-api/v1/responses/"+id, nil, uid, &res); err != nil {
		if he, ok := err.(*hostHTTPError); ok {
			writeHostJSON(w, he.status, he.body)
			return
		}
		hostErr(w, 502, "查询 goder 任务失败: "+err.Error())
		return
	}
	out := map[string]any{
		"status":    res.Status,
		"model":     res.Model,
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
	writeHostJSON(w, 200, out)
}

// Cancel POST /api/host/goder/tasks/{uid}/{id}/cancel — 取消在跑回合（幂等）。
func (h *HostTasks) Cancel(w http.ResponseWriter, r *http.Request) {
	uid, id := r.PathValue("uid"), r.PathValue("id")
	if uid == "" || id == "" {
		hostErr(w, 400, "uid / id 必填")
		return
	}
	if !h.ready() {
		hostErr(w, 503, "goder 服务未就绪")
		return
	}
	var res json.RawMessage
	if err := h.loopback(r.Context(), http.MethodPost, "/goder-api/v1/responses/"+id+"/cancel", []byte("{}"), uid, &res); err != nil {
		if he, ok := err.(*hostHTTPError); ok {
			writeHostJSON(w, he.status, he.body)
			return
		}
		hostErr(w, 502, "取消 goder 任务失败: "+err.Error())
		return
	}
	writeHostJSON(w, 200, map[string]any{"ok": true})
}

// ---- 内部 ----

// ready 惰性拉起并等待 goder-serve 就绪（与 ProxyHandler 同款 5s 等待）。
func (h *HostTasks) ready() bool {
	if h.m.disabled || !h.m.Available() {
		return false
	}
	if h.m.Ready() {
		return true
	}
	h.m.Start()
	deadline := time.Now().Add(5 * time.Second)
	for !h.m.Ready() && time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
	}
	return h.m.Ready()
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

// loopback 以指定用户身份调 goder-serve（header 身份注入与 ProxyHandler 同源：
// 覆盖式 set，环回直连不受外部伪造影响）。
func (h *HostTasks) loopback(ctx context.Context, method, path string, body []byte, uid string, out any) error {
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
	req.Header.Set("X-GRF-Name", "自动修复")
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
	ID         string `json:"id"`
	Status     string `json:"status"`
	Model      string `json:"model"`
	SessionID  string `json:"grf:session_id"`
	Error      *struct {
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

// resolveModel 把请求模型解析成全限定名（provider/model）。gh 新版已能在
// 服务端解析裸名（默认供应商优先 → 唯一供应商命中 → 歧义忽略），但这里
// 保留本地解析：无头任务要 fail-fast——解析失败给管理员可读的错误（套餐
// 未含/未同步是最常见原因），绝不静默换模型，而 gh 的 best-effort 语义会
// 静默回落默认。空 = 用户默认。
func (h *HostTasks) resolveModel(uid, want string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(GrfHomePath(h.m.dataDir, uid), "settings.json"))
	if err != nil {
		return "", fmt.Errorf("用户 %s 的 goder 模型配置不可读（先登录一次 goder 或检查 AI 供应商同步）: %v", uid, err)
	}
	var cfg struct {
		Providers map[string]struct {
			Models []string `json:"models"`
		} `json:"providers"`
		Default string `json:"default"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("用户 %s 的 settings.json 损坏: %v", uid, err)
	}
	names := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	has := func(provider, model string) bool {
		p, ok := cfg.Providers[provider]
		if !ok {
			return false
		}
		for _, m := range p.Models {
			if m == model {
				return true
			}
		}
		return false
	}
	switch {
	case want == "":
		if cfg.Default == "" {
			return "", fmt.Errorf("用户 %s 未配置默认模型（token-hub 套餐未绑定或未同步）", uid)
		}
		return cfg.Default, nil
	case strings.Contains(want, "/"):
		provider, model, _ := strings.Cut(want, "/")
		if has(provider, model) {
			return want, nil
		}
		return "", fmt.Errorf("模型 %s 不在用户 %s 的可用清单（供应商未配置或套餐未含）", want, uid)
	default:
		for _, n := range names {
			if has(n, want) {
				return n + "/" + want, nil
			}
		}
		return "", fmt.Errorf("模型 %s 不在用户 %s 的可用清单（token-hub 套餐未含或未同步，可在「设置 → AI」或套餐侧确认）", want, uid)
	}
}

// indexSession 把会话写进用户的 goder.index（确保分组项目存在）。整体
// map[string]json.RawMessage 保真未知键（automation / lastActiveSessionId 等）。
func (h *HostTasks) indexSession(uid, sid, title, group, model, respID string) error {
	raw, err := h.db.GetUserSetting(uid, hostIndexKey)
	if err != nil {
		return err
	}
	all := map[string]json.RawMessage{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &all); err != nil {
			// 损坏镜像不整体覆盖（用户其余分组宝贵）：跳过归组，会话回落未分组
			return fmt.Errorf("goder.index 损坏，跳过归组: %w", err)
		}
	}
	var idx struct {
		Projects []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			CreatedAt string `json:"createdAt"`
		} `json:"projects"`
		Sessions map[string]json.RawMessage `json:"sessions"`
	}
	_ = json.Unmarshal(all["projects"], &idx.Projects)
	if idx.Sessions == nil {
		if err := json.Unmarshal(all["sessions"], &idx.Sessions); err != nil {
			idx.Sessions = map[string]json.RawMessage{}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	found := false
	for _, p := range idx.Projects {
		if p.ID == fixerProjectID {
			found = true
			break
		}
	}
	if !found {
		idx.Projects = append(idx.Projects, struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			CreatedAt string `json:"createdAt"`
		}{ID: fixerProjectID, Name: group, CreatedAt: now})
	}
	rec, err := json.Marshal(map[string]any{
		"projectId":      fixerProjectID,
		"title":          title,
		"createdAt":      now,
		"lastResponseId": respID,
		"model":          model,
	})
	if err != nil {
		return err
	}
	if idx.Sessions == nil {
		idx.Sessions = map[string]json.RawMessage{}
	}
	idx.Sessions[sid] = rec

	projRaw, err := json.Marshal(idx.Projects)
	if err != nil {
		return err
	}
	sessRaw, err := json.Marshal(idx.Sessions)
	if err != nil {
		return err
	}
	if _, ok := all["version"]; !ok {
		all["version"] = json.RawMessage("1")
	}
	all["projects"] = projRaw
	all["sessions"] = sessRaw
	out, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return h.db.SetUserSetting(uid, hostIndexKey, string(out))
}

// deriveHostTitle 任务首行截断作会话标题（与 goder.js deriveTitle 的 42 字口径接近）。
func deriveHostTitle(task string) string {
	line := task
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	r := []rune(line)
	if len(r) > 42 {
		r = r[:42]
	}
	return string(r)
}

func hostErr(w http.ResponseWriter, status int, msg string) {
	writeHostJSON(w, status, map[string]any{"error": msg})
}

func writeHostJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
