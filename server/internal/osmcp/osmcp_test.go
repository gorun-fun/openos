// osmcp_test.go — MCP 分派/鉴权、工具（设置/VFS 进程内调用）、前端桥往返、
// 安装器（wrapper/settings 两种模式/幂等/自愈）、shim 行协议。
package osmcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openos/server/internal/bus"
	"openos/server/internal/store"
	"openos/server/internal/vfs"
)

const testToken = "test-token"

// newTestDeps 组装带真实 SQLite/VFS/总线的依赖，并种入测试用户 alice。
func newTestDeps(t *testing.T) (Deps, *store.DB, *bus.Hub) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(username,password_hash,salt,role,display_name) VALUES('alice','x','y','user','Alice')`); err != nil {
		t.Fatal(err)
	}
	hub := bus.New()
	fs := vfs.New(t.TempDir(), db, hub, nil)
	fs.EnsureHome("alice")
	return Deps{
		DB: db, Hub: hub, FS: fs, DataDir: t.TempDir(),
		Version: "test", StartedAt: time.Now(),
		OnlineUsers: func() []string { return []string{"alice"} },
	}, db, hub
}

// rpcTestOut 应答解析（Result 保留原文，便于断言）。
type rpcTestOut struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcErrorBody   `json:"error"`
}

// rpc 向 serveMCP 发一条 JSON-RPC 并解析应答。
func rpc(t *testing.T, s *Server, user, method, params string) (rpcTestOut, int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	if user != "" {
		req.Header.Set("X-OS-User", user)
	}
	rec := httptest.NewRecorder()
	s.serveMCP(rec, req)
	var out rpcTestOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("应答不是 JSON: %s", rec.Body.String())
	}
	return out, rec.Code
}

func TestServeMCPAuth(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	s := newServer(deps, testToken, newBridge(deps.Hub))

	// 无令牌 → 401
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.serveMCP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401，got %d", rec.Code)
	}
	// 非 POST → 405
	req = httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec = httptest.NewRecorder()
	s.serveMCP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应 405，got %d", rec.Code)
	}
}

func TestToolsList(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	s := newServer(deps, testToken, newBridge(deps.Hub))

	out, code := rpc(t, s, "alice", "tools/list", `{}`)
	if code != 200 || out.Error != nil {
		t.Fatalf("tools/list 失败: %+v", out)
	}
	var res struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 20 {
		t.Fatalf("应有 20 个工具，got %d", len(res.Tools))
	}
	want := map[string]bool{"os_info": false, "os_apps_list": false, "os_fs_write": false,
		"os_window_open": false, "os_desktop_state": false, "os_notes_do": false, "os_bus_publish": false}
	for _, tl := range res.Tools {
		if _, ok := want[tl.Name]; ok {
			want[tl.Name] = true
		}
		if tl.Description == "" || tl.InputSchema == nil {
			t.Errorf("工具 %s 缺 description 或 inputSchema", tl.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("缺少工具 %s", name)
		}
	}
}

func TestToolsCallValidation(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	s := newServer(deps, testToken, newBridge(deps.Hub))

	// 未知工具 → JSON-RPC 错误
	if out, _ := rpc(t, s, "alice", "tools/call", `{"name":"nope"}`); out.Error == nil {
		t.Fatal("未知工具应返回 JSON-RPC 错误")
	}

	// 缺用户身份 / 用户不存在 → 工具级错误
	for _, user := range []string{"", "nobody"} {
		out, _ := rpc(t, s, user, "tools/call", `{"name":"os_info"}`)
		var res struct {
			IsError bool `json:"isError"`
		}
		_ = json.Unmarshal(out.Result, &res)
		if !res.IsError {
			t.Fatalf("用户 %q 应触发工具级错误", user)
		}
	}
}

func TestSettingsTools(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	s := newServer(deps, testToken, newBridge(deps.Hub))

	if out, _ := rpc(t, s, "alice", "tools/call", `{"name":"os_settings_put","arguments":{"key":"foo","value":"bar"}}`); out.Error != nil {
		t.Fatalf("settings_put 失败: %+v", out)
	}
	out, _ := rpc(t, s, "alice", "tools/call", `{"name":"os_settings_get","arguments":{"key":"foo"}}`)
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out.Result, &res); err != nil || len(res.Content) == 0 || res.Content[0].Text != "bar" {
		t.Fatalf("settings_get 应返回 bar: %s", out.Result)
	}
}

func TestFSTools(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	s := newServer(deps, testToken, newBridge(deps.Hub))

	call := func(name, args string) (string, bool) {
		out, _ := rpc(t, s, "alice", "tools/call", `{"name":"`+name+`","arguments":`+args+`}`)
		var res struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		if err := json.Unmarshal(out.Result, &res); err != nil {
			t.Fatalf("%s 应答异常: %s", name, out.Result)
		}
		return res.Content[0].Text, res.IsError
	}

	// 写入 → 读回 → 列目录
	if _, isErr := call("os_fs_write", `{"path":"/home/alice/笔记/计划.md","content":"hello openos"}`); isErr {
		t.Fatal("fs_write 应成功")
	}
	text, isErr := call("os_fs_read", `{"path":"/home/alice/笔记/计划.md"}`)
	if isErr || !strings.Contains(text, "hello openos") {
		t.Fatalf("fs_read 应读回内容: %s err=%v", text, isErr)
	}
	list, isErr := call("os_fs_list", `{"path":"/home/alice"}`)
	if isErr || !strings.Contains(list, "笔记") {
		t.Fatalf("fs_list 应包含新建目录: %s", list)
	}

	// 越权路径（他人 home）→ 失败
	if _, isErr := call("os_fs_read", `{"path":"/home/bob/secret"}`); !isErr {
		t.Fatal("越权读取应失败")
	}
}

func TestBridgeRoundTrip(t *testing.T) {
	_, _, hub := newTestDeps(t)
	b := newBridge(hub)
	b.timeout = 2 * time.Second

	type result struct {
		data string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		data, err := b.call("alice", "state", nil)
		ch <- result{string(data), err}
	}()

	// 等待请求登记后模拟前端应答
	var id string
	for i := 0; i < 100; i++ {
		b.mu.Lock()
		for k := range b.pending {
			id = k
		}
		b.mu.Unlock()
		if id != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("请求未登记")
	}

	// 他人应答无效
	b.onResp(bus.Envelope{From: "bob", Payload: json.RawMessage(`{"id":"` + id + `","ok":true,"data":{"x":1}}`)})
	select {
	case r := <-ch:
		t.Fatalf("他人应答不应解决请求: %+v", r)
	default:
	}
	// 正确用户应答 → 拿到 data
	b.onResp(bus.Envelope{From: "alice", Payload: json.RawMessage(`{"id":"` + id + `","ok":true,"data":{"x":1}}`)})
	r := <-ch
	if r.err != nil || r.data != `{"x":1}` {
		t.Fatalf("应答数据不符: %+v", r)
	}
}

func TestBridgeErrorAndTimeout(t *testing.T) {
	_, _, hub := newTestDeps(t)
	b := newBridge(hub)
	b.timeout = 50 * time.Millisecond

	// 超时
	if _, err := b.call("alice", "state", nil); err == nil || !strings.Contains(err.Error(), "未应答") {
		t.Fatalf("应超时报错，got %v", err)
	}

	// 前端返回失败
	b2 := newBridge(hub)
	b2.timeout = 2 * time.Second
	ch := make(chan error, 1)
	go func() {
		_, err := b2.call("alice", "window_open", map[string]any{"appId": "nope"})
		ch <- err
	}()
	var id string
	for i := 0; i < 100; i++ {
		b2.mu.Lock()
		for k := range b2.pending {
			id = k
		}
		b2.mu.Unlock()
		if id != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	b2.onResp(bus.Envelope{From: "alice", Payload: json.RawMessage(`{"id":"` + id + `","ok":false,"error":"应用不存在"}`)})
	if err := <-ch; err == nil || err.Error() != "应用不存在" {
		t.Fatalf("应透传前端错误，got %v", err)
	}
}

func TestInstallPlugin(t *testing.T) {
	grfHome := t.TempDir()
	endpoint := "http://127.0.0.1:7712/mcp"

	if err := installPlugin("/opt/openos-server", grfHome, endpoint, "tok", "alice"); err != nil {
		t.Fatal(err)
	}
	wrapPath := filepath.Join(grfHome, "plugins", wrapperFileName())
	raw, err := os.ReadFile(wrapPath)
	if err != nil {
		t.Fatalf("wrapper 未落盘: %v", err)
	}
	want := wrapperContent("/opt/openos-server", endpoint, "tok", "alice")
	if string(raw) != want {
		t.Fatalf("wrapper 内容不符:\n got %q\nwant %q", raw, want)
	}
	st, _ := os.Stat(wrapPath)
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("wrapper 权限应为 0700，got %v", st.Mode().Perm())
	}
	// 自动发现模式：不应创建 settings.json
	if _, err := os.Stat(filepath.Join(grfHome, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("自动发现模式不应写 settings.json")
	}
	// readWrapperUID 能从 wrapper 抠回用户名（healAll 依赖）
	if uid := readWrapperUID(wrapPath); uid != "alice" {
		t.Fatalf("readWrapperUID 应得 alice，got %q", uid)
	}

	// 显式列表模式：settings.json 有 plugins 键 → upsert 命令项且保留他人条目
	settings := filepath.Join(grfHome, "settings.json")
	seed := `{"model": "gpt", "plugins": [{"name": "other", "command": ["x"]}]}` + "\n"
	if err := os.WriteFile(settings, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installPlugin("/opt/openos-server", grfHome, endpoint, "tok", "alice"); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Plugins []struct {
			Name    string   `json:"name"`
			Command []string `json:"command"`
		} `json:"plugins"`
	}
	data, _ := os.ReadFile(settings)
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins) != 2 {
		t.Fatalf("显式模式应有两个插件条目，got %d: %s", len(cfg.Plugins), data)
	}
	var openos *struct {
		Name    string   `json:"name"`
		Command []string `json:"command"`
	}
	for i := range cfg.Plugins {
		if cfg.Plugins[i].Name == pluginName {
			openos = &cfg.Plugins[i]
		}
	}
	if openos == nil || len(openos.Command) != 5 || openos.Command[3] != "tok" || openos.Command[4] != "alice" {
		t.Fatalf("openos 命令项不符: %+v", openos)
	}
	// 再装一次 → upsert 不重复
	if err := installPlugin("/opt/openos-server", grfHome, endpoint, "tok", "alice"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(settings)
	_ = json.Unmarshal(data, &cfg)
	if len(cfg.Plugins) != 2 {
		t.Fatalf("重复安装不应新增条目，got %d", len(cfg.Plugins))
	}
}

func TestManagerEnsureAndHeal(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	m := &Manager{deps: deps, marks: map[string]ensMark{}, exe: "/opt/openos-server", cfg: Config{Addr: "127.0.0.1:7712", Token: "tok"}}

	// 用户名含特殊字符 → 清洗后目录
	m.EnsureUser("we ird!")
	wrap := filepath.Join(userGrfHome(deps.DataDir, "we ird!"), "plugins", wrapperFileName())
	raw, err := os.ReadFile(wrap)
	if err != nil {
		t.Fatalf("EnsureUser 未落 wrapper: %v", err)
	}
	if !strings.Contains(string(raw), `"we ird!" "$@"`) && !strings.Contains(string(raw), "we ird!") {
		t.Fatalf("wrapper 应烙原始用户名: %q", raw)
	}
	if uid := readWrapperUID(wrap); uid != "we ird!" {
		t.Fatalf("wrapper 用户名往返不符: %q", uid)
	}

	// 自愈：可执行路径变化后 healAll 按旧 wrapper 抠回用户名重写
	m.exe = "/new/path/openos-server"
	m.healAll()
	raw, err = os.ReadFile(wrap)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "/new/path/openos-server") {
		t.Fatalf("healAll 应重写 wrapper: %q", raw)
	}
}

func TestRunShim(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	s := newServer(deps, testToken, newBridge(deps.Hub))
	ts := httptest.NewServer(http.HandlerFunc(s.serveMCP))
	defer ts.Close()

	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"os_info","arguments":{}}}` + "\n")
	var out bytes.Buffer
	if code := runShim(in, &out, ts.Client(), ts.URL+"/mcp", testToken, "alice"); code != 0 {
		t.Fatalf("shim 退出码 %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("应有三行应答，got %d: %s", len(lines), out.String())
	}
	if !strings.Contains(lines[0], `"openos"`) || !strings.Contains(lines[0], "2025-06-18") {
		t.Fatalf("initialize 应答异常: %s", lines[0])
	}
	if !strings.Contains(lines[1], "os_info") {
		t.Fatalf("tools/list 应答异常: %s", lines[1])
	}
	if !strings.Contains(lines[2], "alice") {
		t.Fatalf("tools/call os_info 应含用户名: %s", lines[2])
	}

	// 服务不可达 → JSON-RPC 错误行（非 initialize 不重试，立即返回）
	var out2 bytes.Buffer
	runShim(strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/list","params":{}}`+"\n"),
		&out2, ts.Client(), "http://127.0.0.1:1/mcp", testToken, "alice")
	if !strings.Contains(out2.String(), "OpenOS 服务不可达") || !strings.Contains(out2.String(), `"id":9`) {
		t.Fatalf("不可达应答异常: %s", out2.String())
	}
}
