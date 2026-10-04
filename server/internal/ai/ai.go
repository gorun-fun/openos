// Package ai 提供 goder 的后端能力：
//   - 配置持久化（SQLite，管理员在系统设置中在线修改并热生效，环境变量作为初始回填）
//   - OpenAI 兼容代理（SSE 流式，逐 delta 转发并在结束时落库）
//   - 对话历史 / 清空 / 连通性测试
//   - 未配置密钥时退化为本地演示模式（保留界面操控指令演示）
package ai

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"openos/server/internal/auth"
	"openos/server/internal/store"
)

// Message 聊天消息。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Service AI 服务。
type Service struct {
	db *store.DB

	mu      sync.RWMutex
	baseURL string
	apiKey  string
	model   string

	startedAt time.Time

	// AfterSave 保存配置后的联动钩子（main 注入：把配置同步为 goder 的
	// 托管模型供应商；参数为解析后的最终值，任一为空 = 已清除配置）
	AfterSave func(base, key, model string)
	// AfterUserProvidersSave 宿主声明每用户供应商替换后的联动钩子（main 注入：
	// 刷新声明名缓存并把声明条目重渲染进各 goder 用户的 settings.json）
	AfterUserProvidersSave func()
}

// New 创建服务：优先读库中配置，其次回退环境变量。
func New(db *store.DB) *Service {
	s := &Service{db: db, startedAt: time.Now()}
	if cfg, ok, err := db.GetAIConfig(); err == nil && ok && cfg.APIKey != "" {
		s.apply(cfg.BaseURL, cfg.APIKey, cfg.Model)
	} else {
		model := os.Getenv("OPENOS_AI_MODEL")
		if model == "" {
			model = "glm-4-flash"
		}
		s.apply(os.Getenv("OPENOS_AI_BASE_URL"), os.Getenv("OPENOS_AI_API_KEY"), model)
	}
	return s
}

func (s *Service) apply(base, key, model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baseURL = strings.TrimSuffix(base, "/")
	s.apiKey = key
	if model == "" {
		model = "glm-4-flash"
	}
	s.model = model
}

// Configured 是否已配置真实 AI。
func (s *Service) Configured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.apiKey != "" && s.baseURL != ""
}

// Model 当前模型名。
func (s *Service) Model() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model
}

// Provider 当前生效的模型配置快照（ok=false 表示未配置，即演示模式）。
func (s *Service) Provider() (base, key, model string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.baseURL, s.apiKey, s.model, s.apiKey != "" && s.baseURL != ""
}

const systemPrompt = `你是「AI 助手」，OpenOS 网页桌面系统的内置 AI 助手（完整编程工作台是另一个应用 goder）。
你可以通过在回复中嵌入指令来操控桌面界面，格式为 [[cmd:动作:参数]]，支持：
  [[cmd:open:terminal]]     打开应用（terminal/files/calculator/settings/docs/sheets/slides/pdf/goder）
  [[cmd:close:terminal]]    关闭应用的所有窗口
  [[cmd:file:/home/用户/文档/xx.md]]  用合适的应用打开用户文件
  [[cmd:wallpaper:2]]       切换桌面壁纸（1-6）
  [[cmd:dark:on]] / [[cmd:dark:off]] 切换深色模式
指令会被系统解析执行并向用户展示结果。请自然地使用它们，例如用户说"帮我打开计算器"时回复中包含 [[cmd:open:calculator]]。
若请求末尾附带了"当前桌面状态"，请基于它了解用户打开的窗口与可用应用，回答可更贴合上下文。`

// Ask 非流式问答（终端 goder 命令使用；同步持久化）。
func (s *Service) Ask(ctx string, prompt string) string {
	if !s.Configured() {
		return demoReply(ctx, prompt)
	}
	msgs := []Message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: prompt}}
	_, out := s.call(msgs)
	s.db.AddChatMsg("__term__", "user", prompt)
	s.db.AddChatMsg("__term__", "assistant", out)
	return out
}

// Chat POST /api/ai/chat {messages, stream, context}
// context 由前端注入桌面状态（打开的窗口/可用应用），帮助 AI 贴合场景。
func (s *Service) Chat(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r)
	var req struct {
		Messages []Message `json:"messages"`
		Stream   bool      `json:"stream"`
		Context  string    `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
		http.Error(w, "参数错误", 400)
		return
	}
	sys := systemPrompt
	if req.Context != "" {
		sys += "\n\n当前桌面状态：" + req.Context
	}
	all := append([]Message{{Role: "system", Content: sys}}, req.Messages...)
	last := req.Messages[len(req.Messages)-1]
	s.db.AddChatMsg(id.Username, "user", last.Content)

	if !s.Configured() {
		reply := demoReply("chat", last.Content)
		s.db.AddChatMsg(id.Username, "assistant", reply)
		if req.Stream {
			s.streamDemo(w, reply)
		} else {
			writeJSON(w, map[string]any{"content": reply})
		}
		return
	}

	if req.Stream {
		s.streamReal(w, r, id.Username, all)
		return
	}
	_, out := s.call(all)
	s.db.AddChatMsg(id.Username, "assistant", out)
	writeJSON(w, map[string]any{"content": out})
}

// History GET /api/ai/history
func (s *Service) History(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r)
	msgs, _ := s.db.ListChatMsgs(id.Username, 200)
	writeJSON(w, msgs)
}

// ClearHistory DELETE /api/ai/history
func (s *Service) ClearHistory(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r)
	s.db.ClearChatMsgs(id.Username)
	writeJSON(w, map[string]string{"ok": "1"})
}

// GetConfig GET /api/ai/config （admin，密钥脱敏）
func (s *Service) GetConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	masked := ""
	if s.apiKey != "" {
		n := len(s.apiKey)
		show := 3
		if n < show {
			show = n
		}
		masked = s.apiKey[:show] + strings.Repeat("*", 8)
	}
	writeJSON(w, map[string]any{
		"baseUrl": s.baseURL, "apiKey": masked, "model": s.model,
		"configured": s.apiKey != "" && s.baseURL != "",
		"demo":       s.apiKey == "" || s.baseURL == "",
	})
}

// SaveConfig PUT /api/ai/config {baseUrl, apiKey?, model}（admin；apiKey 留空表示不变）
func (s *Service) SaveConfig(w http.ResponseWriter, r *http.Request) {
	var req struct{ BaseURL, APIKey, Model string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	s.mu.RLock()
	oldKey := s.apiKey
	s.mu.RUnlock()
	// 约定："__clear__" 显式清空（恢复演示模式）；空串或掩码串表示"不修改密钥"
	if req.APIKey == "__clear__" {
		req.APIKey = ""
	} else if req.APIKey == "" || strings.Contains(req.APIKey, "*") {
		req.APIKey = oldKey
	}
	if err := s.db.SetAIConfig(store.AIConfig{BaseURL: strings.TrimSuffix(req.BaseURL, "/"), APIKey: req.APIKey, Model: req.Model}); err != nil {
		http.Error(w, "保存失败", 500)
		return
	}
	s.apply(req.BaseURL, req.APIKey, req.Model)
	s.db.Audit(auth.FromContext(r).Username, "ai.config", "model="+req.Model, "")
	writeJSON(w, map[string]string{"ok": "1"})
	// 联动钩子：同步为 goder 各用户的托管模型供应商（OpenOS 唯一的 AI 应用）
	if s.AfterSave != nil {
		base, key, model, _ := s.Provider()
		s.AfterSave(base, key, model)
	}
}

// Test POST /api/ai/test （admin）连通性测试。
func (s *Service) Test(w http.ResponseWriter, r *http.Request) {
	if !s.Configured() {
		writeJSON(w, map[string]any{"ok": false, "error": "尚未配置 AI 服务"})
		return
	}
	start := time.Now()
	msgs := []Message{{Role: "system", Content: "You are a health probe. Reply with the single word: pong."}, {Role: "user", Content: "ping"}}
	client := &http.Client{Timeout: 20 * time.Second}
	body, _ := json.Marshal(map[string]any{"model": s.Model(), "messages": msgs, "max_tokens": 8})
	s.mu.RLock()
	url := s.baseURL + "/chat/completions"
	key := s.apiKey
	s.mu.RUnlock()
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "连接失败：" + err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		writeJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("服务返回 %d：%s", resp.StatusCode, truncate(buf.String(), 200))})
		return
	}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	writeJSON(w, map[string]any{
		"ok": true, "latencyMs": time.Since(start).Milliseconds(),
		"model": s.Model(), "reply": out.Choices[0].Message.Content,
	})
}

// ---- 上游调用 ----

// ProxyChatCompletions POST /api/ai/v1/chat/completions —— OpenAI 兼容代理端点，
// 供 goder 托管供应商（名为 AIProviderName）接入：平台 API Key 只存服务端，
// 不再随 settings.json 下发到每个 goder 用户（安全审计 2026-10 之前明文 Key
// 落所有用户家目录，人人可取）。鉴权在路由层（每用户 AI 代理密钥，auth.AIProxyKey）。
//   - 模型恒以平台配置为准（忽略请求 model，防向供应商请求任意高价模型）；
//   - 消息条数 ≤200、请求体 ≤4MB；
//   - SSE 与 JSON 应答原样透传（gh 侧自带协议解析与历史管理，此处不落库）。
func (s *Service) ProxyChatCompletions(w http.ResponseWriter, r *http.Request) {
	if !s.Configured() {
		http.Error(w, `{"error":{"message":"AI 服务未配置"}}`, http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil || len(body) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req struct {
		Model    string    `json:"model"`
		Messages []Message `json:"messages"`
		Stream   bool      `json:"stream"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.Messages) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(req.Messages) > 200 {
		http.Error(w, "消息数超过上限（200）", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	url := s.baseURL + "/chat/completions"
	key := s.apiKey
	model := s.model
	s.mu.RUnlock()

	payload, _ := json.Marshal(map[string]any{"model": model, "messages": req.Messages, "stream": req.Stream})
	up, err := http.NewRequestWithContext(r.Context(), "POST", url, bytes.NewReader(payload))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	up.Header.Set("Content-Type", "application/json")
	up.Header.Set("Authorization", "Bearer "+key)
	if req.Stream {
		up.Header.Set("Accept", "text/event-stream")
	}
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(up)
	if err != nil {
		http.Error(w, "AI 服务不可达: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var buf bytes.Buffer
		buf.ReadFrom(io.LimitReader(resp.Body, 4<<20))
		http.Error(w, fmt.Sprintf("AI 服务返回 %d: %s", resp.StatusCode, truncate(buf.String(), 300)), http.StatusBadGateway)
		return
	}
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

func (s *Service) call(msgs []Message) (int, string) {
	s.mu.RLock()
	url := s.baseURL + "/chat/completions"
	key := s.apiKey
	model := s.model
	s.mu.RUnlock()

	body, _ := json.Marshal(map[string]any{"model": model, "messages": msgs})
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return 500, "请求构建失败: " + err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return 502, "AI 服务不可达: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		return resp.StatusCode, fmt.Sprintf("AI 服务返回 %d: %s", resp.StatusCode, truncate(buf.String(), 300))
	}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Choices) > 0 {
		return 200, out.Choices[0].Message.Content
	}
	return 200, "（AI 未返回内容）"
}

// streamReal 流式转发上游 SSE，结束后累积落库。
func (s *Service) streamReal(w http.ResponseWriter, r *http.Request, username string, msgs []Message) {
	s.mu.RLock()
	url := s.baseURL + "/chat/completions"
	key := s.apiKey
	model := s.model
	s.mu.RUnlock()

	body, _ := json.Marshal(map[string]any{"model": model, "messages": msgs, "stream": true})
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		http.Error(w, "AI 服务不可达: "+err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	flusher, ok := w.(http.Flusher)
	if !ok || resp.StatusCode != 200 {
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		http.Error(w, fmt.Sprintf("AI 服务返回 %d: %s", resp.StatusCode, truncate(buf.String(), 300)), 502)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	var full strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) == nil && len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			delta := chunk.Choices[0].Delta.Content
			full.WriteString(delta)
			fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]string{"delta": delta}))
			flusher.Flush()
		}
	}
	if full.Len() == 0 {
		full.WriteString("（AI 未返回内容）")
	}
	s.db.AddChatMsg(username, "assistant", full.String())
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// ---- 演示模式 ----

func demoReply(ctx, prompt string) string {
	p := strings.ToLower(prompt)
	switch {
	case strings.Contains(p, "计算") || strings.Contains(p, "算"):
		return "好的，我来帮你打开计算器：[[cmd:open:calculator]]\n\n（当前为演示模式：管理员可在「系统设置 → AI 服务」配置任意 OpenAI 兼容接口接入真实大模型）"
	case strings.Contains(p, "终端") || strings.Contains(p, "命令"):
		return "终端在这里：[[cmd:open:terminal]] 输入 help 可查看全部命令。"
	case strings.Contains(p, "表格") || strings.Contains(p, "excel") || strings.Contains(p, "sheet"):
		return "已为你打开表格应用：[[cmd:open:sheets]]，支持公式 =SUM(A1:A5)。"
	case strings.Contains(p, "文档") || strings.Contains(p, "笔记"):
		return "文档应用来了：[[cmd:open:docs]]，支持自动保存与导出。"
	case strings.Contains(p, "幻灯") || strings.Contains(p, "演示"):
		return "幻灯片应用：[[cmd:open:slides]]，支持全屏演示模式。"
	case strings.Contains(p, "文件"):
		return "文件管理器：[[cmd:open:files]]，支持上传下载与搜索。"
	case strings.Contains(p, "深色") || strings.Contains(p, "夜间"):
		return "已切换到深色模式：[[cmd:dark:on]]"
	case strings.Contains(p, "壁纸"):
		return "换一张壁纸：[[cmd:wallpaper:" + fmt.Sprint(time.Now().Unix()%6+1) + "]]"
	case strings.Contains(p, "你好") || strings.Contains(p, "hello") || strings.Contains(p, "hi") || strings.Contains(p, "你是谁"):
		return "你好！我是 OpenOS 的 AI 助手。我可以帮你打开应用、切换深色模式或壁纸、打开文件。\n\n当前为**演示模式**——管理员在「系统设置 → AI 服务」填入任意 OpenAI 兼容接口（如智谱、DeepSeek、OpenAI）的 Base URL 与 API Key 即可接入真实大模型，配置即时生效。"
	default:
		return "收到：「" + prompt + "」\n\n当前运行在演示模式（未配置 AI 服务）。你可以让我「打开计算器」「切换深色模式」「换张壁纸」，或在「系统设置 → AI 服务」接入真实大模型后畅所欲言。"
	}
}

func (s *Service) streamDemo(w http.ResponseWriter, reply string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, map[string]any{"content": reply})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	for _, r := range []rune(reply) {
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]string{"delta": string(r)}))
		flusher.Flush()
		time.Sleep(10 * time.Millisecond)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

var _ = subtle.ConstantTimeCompare // 保留提示：密钥比较请用常数时间比较
