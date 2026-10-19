// osmcp_test.go — MCP 分派/鉴权、工具（设置/VFS 进程内调用）、前端桥往返、
// 安装器（经宿主管理面假件：幂等跳过/失败重试/存量自愈）、shim 行协议。
package osmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"openos/server/internal/bus"
	"openos/server/internal/goder"

	"github.com/gorilla/websocket"
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
	fs := vfs.New(t.TempDir(), db, hub)
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
	s := newServer(deps, tokenAuth(testToken), newBridge(deps.Hub))

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
	s := newServer(deps, tokenAuth(testToken), newBridge(deps.Hub))

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
	// ① 与注册表完全一致（单一事实源 staticToolList 即 tools/list 的装配实现：
	//    装配丢工具/错位在此暴露——原硬编码 35 断言的真实意图）
	want := s.staticToolList()
	if len(res.Tools) != len(want) {
		t.Fatalf("tools/list 应与注册表一致：%d 个工具，got %d", len(want), len(res.Tools))
	}
	seen := map[string]bool{}
	for i, tl := range res.Tools {
		if name, _ := want[i]["name"].(string); tl.Name != name {
			t.Errorf("第 %d 个工具应为 %s，got %s", i, name, tl.Name)
		}
		if tl.Description == "" || tl.InputSchema == nil {
			t.Errorf("工具 %s 缺 description 或 inputSchema", tl.Name)
		}
		// ② 全部 os_ 前缀且无重名
		if !strings.HasPrefix(tl.Name, "os_") {
			t.Errorf("工具 %s 缺 os_ 前缀", tl.Name)
		}
		if seen[tl.Name] {
			t.Errorf("工具重名: %s", tl.Name)
		}
		seen[tl.Name] = true
	}
	// ③ CAP 四件必须在场（新应用接入不改服务端的根基）
	for _, name := range []string{"os_caps", "os_app_query", "os_app_call", "os_app_ui"} {
		if !seen[name] {
			t.Errorf("缺少 CAP 工具 %s", name)
		}
	}
	// ④ 宽松下限：防注册组意外大面积丢失（精确账目由①保证）
	if len(res.Tools) <= 30 {
		t.Fatalf("工具总数应大于 30，got %d", len(res.Tools))
	}
}

func TestToolsCallValidation(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	s := newServer(deps, tokenAuth(testToken), newBridge(deps.Hub))

	// 未知工具 → JSON-RPC 错误
	if out, _ := rpc(t, s, "alice", "tools/call", `{"name":"nope"}`); out.Error == nil {
		t.Fatal("未知工具应返回 JSON-RPC 错误")
	}

	// 缺用户身份 → HTTP 层 401（令牌↔用户成对校验，安全审计 2026-10）
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"os_info"}}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.serveMCP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺 X-OS-User 应 401，得到 %d", rec.Code)
	}

	// 用户不存在 → 工具级错误
	out, _ := rpc(t, s, "nobody", "tools/call", `{"name":"os_info"}`)
	var res struct {
		IsError bool `json:"isError"`
	}
	_ = json.Unmarshal(out.Result, &res)
	if !res.IsError {
		t.Fatal("用户 \"nobody\" 应触发工具级错误")
	}
}

func TestSettingsTools(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	s := newServer(deps, tokenAuth(testToken), newBridge(deps.Hub))

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
	s := newServer(deps, tokenAuth(testToken), newBridge(deps.Hub))

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

// TestCAPToolsBridge — 能力暴露层四工具的端到端桥往返：真实 WebSocket 模拟前端
// （订阅 sys:agent、按动作应答 agent:resp），验证工具参数到桥动作/args 的接线。
func TestCAPToolsBridge(t *testing.T) {
	deps, _, hub := newTestDeps(t)
	b := newBridge(deps.Hub)
	s := newServer(deps, tokenAuth(testToken), b)
	// 生产由 Manager 挂 hub.Handle("agent:resp") → b.onResp，测试同款接线
	hub.Handle("agent:resp", func(env bus.Envelope, _ func(string, any)) { b.onResp(env) })
	defer hub.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeWS(w, r, "alice", "user", "tok")
	}))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"sub","topics":["sys:agent"]}`)); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env struct {
				Topic   string
				Payload json.RawMessage
			}
			if json.Unmarshal(data, &env) != nil || env.Topic != "sys:agent" {
				continue
			}
			var req struct {
				ID     string
				Action string
				Args   json.RawMessage
			}
			if json.Unmarshal(env.Payload, &req) != nil {
				continue
			}
			var out any
			switch req.Action {
			case "caps_list":
				out = map[string]any{"apps": []any{map[string]any{"appId": "rooms", "queries": []any{"vacant"}}}}
			case "cap_query":
				out = map[string]any{"result": []any{map[string]any{"room": "301"}}}
			case "cap_call":
				out = map[string]any{"result": "blocked"}
			case "app_ui":
				out = map[string]any{"windowId": "w1", "highlighted": true}
			default:
				out = map[string]any{"echo": req.Action}
			}
			resp, _ := json.Marshal(map[string]any{"action": "pub", "topic": "agent:resp",
				"payload": map[string]any{"id": req.ID, "ok": true, "data": out}})
			_ = conn.WriteMessage(websocket.TextMessage, resp)
		}
	}()

	assert := func(tool, args, want string) {
		t.Helper()
		out, _ := rpc(t, s, "alice", "tools/call", `{"name":"`+tool+`","arguments":`+args+`}`)
		if out.Error != nil {
			t.Fatalf("%s 失败: %+v", tool, out.Error)
		}
		if !strings.Contains(string(out.Result), want) {
			t.Fatalf("%s 应答应含 %q: %s", tool, want, out.Result)
		}
	}
	assert("os_caps", `{}`, `rooms`)
	assert("os_app_query", `{"appId":"rooms","query":"vacant","args":{"date":"2026-10-11"}}`, `301`)
	assert("os_app_call", `{"appId":"rooms","capability":"block","args":{"room":"301"}}`, `blocked`)
	assert("os_app_ui", `{"appId":"rooms","view":"grid","anchor":"room-grid"}`, `highlighted`)
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

// fakeGoderAdmin — 宿主管理面假件（记录安装调用；wrapper/settings 双模式
// 落盘语义已上移 goder-serve，见 goder 仓 pkg/ghserver/hostsettings_test.go）。
type fakeGoderAdmin struct {
	mu       sync.Mutex
	installs []fakeInstall
	listed   []goder.AdminPluginItem
	fail     bool
}

type fakeInstall struct {
	uid, name string
	command   []string
}

func (f *fakeGoderAdmin) InstallHostPlugin(_ context.Context, uid, name string, command []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("goder 服务未就绪")
	}
	f.installs = append(f.installs, fakeInstall{uid, name, command})
	return nil
}

func (f *fakeGoderAdmin) ListHostPlugins(context.Context) ([]goder.AdminPluginItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listed, nil
}

func (f *fakeGoderAdmin) EnsureReady(time.Duration) bool { return true }

func (f *fakeGoderAdmin) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.installs)
}

func TestEnsureUserInstallsViaAdmin(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	fake := &fakeGoderAdmin{}
	deps.Goder = fake
	m := &Manager{deps: deps, installed: map[string]string{}, exe: "/opt/openos-server", cfg: Config{Addr: "127.0.0.1:7712"}}

	// 用户名含特殊字符：安装命令按原始 uid 透传（目录清洗是 goder-serve 的事）
	m.EnsureUser("we ird!")
	if fake.count() != 1 {
		t.Fatalf("首调应安装一次,got %d", fake.count())
	}
	ins := fake.installs[0]
	if ins.uid != "we ird!" || ins.name != pluginName || len(ins.command) != 5 {
		t.Fatalf("安装参数不符: %+v", ins)
	}
	if ins.command[0] != "/opt/openos-server" || ins.command[1] != "mcp-shim" ||
		!strings.HasPrefix(ins.command[2], "http://127.0.0.1:7712") || ins.command[4] != "we ird!" {
		t.Fatalf("命令行不符: %v", ins.command)
	}
	// 幂等：命令行未变不重放
	m.EnsureUser("we ird!")
	if fake.count() != 1 {
		t.Fatalf("命令行未变应跳过,got %d", fake.count())
	}
	// 安装失败不记缓存：下次重试
	fake.mu.Lock()
	fake.fail = true
	fake.mu.Unlock()
	m.exe = "/new/path/openos-server" // 命令行变化触发重放（失败）
	m.EnsureUser("we ird!")
	if fake.count() != 1 {
		t.Fatalf("失败调用不应记录,got %d", fake.count())
	}
	fake.mu.Lock()
	fake.fail = false
	fake.mu.Unlock()
	m.EnsureUser("we ird!")
	if fake.count() != 2 {
		t.Fatalf("失败后应重试成功,got %d", fake.count())
	}
	if fake.installs[1].command[0] != "/new/path/openos-server" {
		t.Fatalf("重试应带新 exe: %v", fake.installs[1].command)
	}
}

func TestHealUsers(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	fake := &fakeGoderAdmin{listed: []goder.AdminPluginItem{
		{UID: "张三", San: "张三", Name: pluginName},
		{UID: "u2", San: "u2", Name: pluginName},
		{UID: "u3", San: "u3", Name: "someone-else"}, // 非本插件：不动
	}}
	deps.Goder = fake
	m := &Manager{deps: deps, installed: map[string]string{}, exe: "/opt/openos-server", cfg: Config{Addr: "127.0.0.1:7712"}}

	if n := m.healUsers(); n != 2 {
		t.Fatalf("应自愈两个用户,got %d", n)
	}
	if fake.count() != 2 {
		t.Fatalf("安装计数不符,got %d", fake.count())
	}
	for _, ins := range fake.installs {
		if ins.name != pluginName {
			t.Fatalf("不应安装其它插件: %+v", ins)
		}
	}
}

func TestRunShim(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	s := newServer(deps, tokenAuth(testToken), newBridge(deps.Hub))
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
