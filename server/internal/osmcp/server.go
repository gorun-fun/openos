// server.go — MCP JSON-RPC over HTTP：与 gh mcp 客户端严格兼容（单 POST 单
// application/json 应答，通知回空体）。鉴权为「Bearer 令牌 + X-OS-User 用户」
// 成对校验（令牌按用户分发，安装期生成、随 wrapper 参数下发——安全审计
// 2026-10 之前全体用户共享一枚令牌，任何拿到令牌的本地进程可冒充任意用户）。
package osmcp

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"openos/server/internal/auth"
)

// protocolVersion 与 gh mcp 客户端声明一致（knownVersions 全集内的最新版；
// 客户端对未知版本会拒绝握手）。
const protocolVersion = "2025-06-18"

// Tool 一个 MCP 工具定义与处理器。
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	ReadOnly    bool
	// Handler 执行工具。返回 content 与 isError（工具自身失败，协议层正常）；
	// 参数问题也按 isError 返回，让模型能看到可修正的提示。
	Handler func(c *ToolCall) ([]Content, bool)
}

// Content 工具返回的内容项（osmcp 只产出 text）。
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolCall 一次工具调用的上下文（用户已校验存在且未禁用）。
type ToolCall struct {
	User        string
	Role        string
	DisplayName string
	Args        json.RawMessage
}

// Server MCP 服务端：工具注册表 + JSON-RPC 分派。
type Server struct {
	deps   Deps
	authz  func(gotToken, user string) bool // 令牌↔用户 绑定校验（Manager 注入）
	bridge *bridge
	tools  []*Tool
	byName map[string]*Tool
}

// tokenAuth 固定令牌校验器（单测用：忽略用户绑定语义）。
func tokenAuth(token string) func(gotToken, user string) bool {
	return func(got, _ string) bool {
		return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
	}
}

func newServer(deps Deps, authz func(gotToken, user string) bool, b *bridge) *Server {
	s := &Server{deps: deps, authz: authz, bridge: b, byName: map[string]*Tool{}}
	s.registerTools()
	return s
}

func (s *Server) add(t Tool) {
	s.tools = append(s.tools, &t)
	s.byName[t.Name] = &t
}

// rpcIn JSON-RPC 请求（gh 客户端只发 POST 单请求：id 为数字或缺失）。
type rpcIn struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcOut struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      *int64        `json:"id"`
	Result  any           `json:"result,omitempty"`
	Error   *rpcErrorBody `json:"error,omitempty"`
}

func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	user := strings.TrimSpace(r.Header.Get("X-OS-User"))
	// 令牌与用户成对校验：没有用户身份（shim 必带）或令牌不属于该用户一律拒绝
	if user == "" || !s.authz(got, user) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	var req rpcIn
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	// 通知（无 id）：200 空体即可。
	if req.ID == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	out := rpcOut{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		out.Result = map[string]any{
			"protocolVersion": negotiateVersion(req.Params),
			"capabilities":    map[string]any{},
			"serverInfo":      map[string]any{"name": pluginName, "version": s.deps.Version},
		}
	case "ping":
		out.Result = map[string]any{}
	case "tools/list":
		tools := make([]map[string]any, 0, len(s.tools))
		for _, t := range s.tools {
			schema := t.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": schema,
				"annotations": map[string]any{"readOnlyHint": t.ReadOnly},
			})
		}
		out.Result = map[string]any{"tools": tools}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			out.Error = &rpcErrorBody{Code: -32602, Message: "params 解析失败: " + err.Error()}
			break
		}
		t, ok := s.byName[p.Name]
		if !ok {
			out.Error = &rpcErrorBody{Code: -32602, Message: "未知工具: " + p.Name}
			break
		}
		c, terr := s.newCall(r, p.Arguments)
		if terr != nil {
			out.Result = toolErr("%s", terr.Error())
			break
		}
		content, isErr := t.Handler(c)
		if !t.ReadOnly {
			s.deps.DB.Audit(c.User, "goder.tool", t.Name, "") // 写操作入审计
		}
		out.Result = map[string]any{"content": content, "isError": isErr}
	default:
		out.Error = &rpcErrorBody{Code: -32601, Message: "未知方法: " + req.Method}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// newCall 从请求构造工具调用上下文：校验 shim 携带的用户存在且未禁用。
func (s *Server) newCall(r *http.Request, args json.RawMessage) (*ToolCall, error) {
	user := strings.TrimSpace(r.Header.Get("X-OS-User"))
	if user == "" {
		return nil, fmt.Errorf("缺少用户身份（X-OS-User）")
	}
	info, err := s.deps.DB.GetUserAuth(user)
	if err != nil || !info.Exists {
		return nil, fmt.Errorf("用户不存在：%s", user)
	}
	if info.Disabled {
		return nil, fmt.Errorf("用户已禁用：%s", user)
	}
	return &ToolCall{User: user, Role: info.Role, DisplayName: info.DisplayName, Args: args}, nil
}

// negotiateVersion 回显客户端请求的已知版本（未知/缺省回落到当前版）。
func negotiateVersion(params json.RawMessage) string {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	switch p.ProtocolVersion {
	case "2024-11-05", "2025-03-26", "2025-06-18":
		return p.ProtocolVersion
	}
	return protocolVersion
}

// ---- 工具输出助手 ----

func toolErr(format string, args ...any) map[string]any {
	return map[string]any{
		"content": []Content{{Type: "text", Text: fmt.Sprintf(format, args...)}},
		"isError": true,
	}
}

func textErr(format string, args ...any) ([]Content, bool) {
	return []Content{{Type: "text", Text: fmt.Sprintf(format, args...)}}, true
}

func textJSON(v any) ([]Content, bool) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return textErr("序列化结果失败: %v", err)
	}
	return []Content{{Type: "text", Text: string(data)}}, false
}

// decodeArgs 把工具参数解码到结构体（缺省字段为零值，由各工具自行校验）。
func decodeArgs(c *ToolCall, v any) error {
	if len(c.Args) == 0 {
		return nil
	}
	if err := json.Unmarshal(c.Args, v); err != nil {
		return fmt.Errorf("参数解析失败: %w", err)
	}
	return nil
}

// ---- 进程内调用 vfs REST handler ----

// fsCall 以发起用户身份进程内调用 vfs 的 REST handler：完整复用 clean 权限
// （限定 /home/<用户>）、8MB 单文件与配额限制、fs:changed 总线广播。
// 返回 (响应体, 是否失败)。
func (s *Server) fsCall(c *ToolCall, h http.HandlerFunc, method, urlPath string, body any) (string, bool) {
	var payload string
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err.Error(), true
		}
		payload = string(data)
	}
	req, err := http.NewRequest(method, urlPath, strings.NewReader(payload))
	if err != nil {
		return err.Error(), true
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req = auth.WithIdentity(req, &auth.Identity{Username: c.User, Role: c.Role})
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code >= 400 {
		return strings.TrimSpace(rec.Body.String()), true
	}
	return strings.TrimSpace(rec.Body.String()), false
}
