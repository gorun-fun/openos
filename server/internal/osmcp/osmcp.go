// Package osmcp 实现 goder（gh 智能体）控制 OpenOS 桌面的 MCP 插件服务。
//
// 接入方式镜像 goder 自带的 goder-console 插件（.goder-src/plugins/console）：
// OpenOS 服务进程内在回环地址宿一个 MCP JSON-RPC over HTTP 服务端，并向每个
// goder 用户的 grf-home/plugins/ 落一个 wrapper（内容烙 openos-server 可执行
// 路径 + 端点 + 令牌 + 用户名），gh 自动发现后经
// `openos-server mcp-shim <endpoint> <token> <user>`（stdio↔HTTP 适配）连入。
//
// 工具分两类：
//   - 服务端直答：应用列表 / 用户设置 / VFS 文件（进程内复用 vfs 的 REST
//     handler，完整保留权限与配额约束）/ 总线发布
//   - 前端桥：窗口/桌面/便签状态在前端，经总线请求-应答往返
//     （下行 hub.PublishTo(user,"sys:agent")，上行 hub.Handle("agent:resp")，
//     goder 应用就开在桌面里，前端桥必然在线）
//
// 安全：端点仅监听回环地址 + Bearer 令牌（<data>/goder/osmcp.json，0600 持久
// 化、跨启动稳定）；wrapper 0700（内嵌令牌）；工具执行前校验用户存在且未禁
// 用，且所有操作只作用于发起用户本人（其桌面/设置/VFS home/通知）。
//
// 环境变量：
//
//	OPENOS_OSMCP       off 时停用（OPENOS_GODER=off 时同样停用）
//	OPENOS_OSMCP_ADDR  监听地址（默认 127.0.0.1:7712；被占则回退随机端口）
package osmcp

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"openos/server/internal/bus"
	"openos/server/internal/store"
	"openos/server/internal/vfs"
)

// Deps 插件服务依赖（由 main 组装；GoderStatus/OnlineUsers 以回调注入避免
// 反向依赖；Goder 为宿主管理面消费口——插件安装经声明式端点完成）。
type Deps struct {
	DB        *store.DB
	Hub       *bus.Hub
	FS        *vfs.FS
	DataDir   string
	Version   string
	StartedAt time.Time
	Goder     GoderAdmin

	GoderStatus func() map[string]any
	OnlineUsers func() []string
}

// Config 持久化配置（<data>/goder/osmcp.json）。令牌**按用户分发**（安全审计
// 2026-10：此前全体用户共享一枚令牌、身份仅由 X-OS-User 声明，任何拿到令牌的
// 本地进程可以任意用户身份调用 os_* 工具）；每用户令牌跨启动稳定：已连着的
// gh 插件进程不至于因令牌轮换而失联。旧版共享 token 字段仅作读取兼容、不再使用。
type Config struct {
	Addr       string            `json:"addr"`
	Token      string            `json:"token,omitempty"`      // 旧版共享令牌（仅兼容读取，不再校验）
	UserTokens map[string]string `json:"user_tokens,omitempty"` // uid → 专属令牌
}

const defaultAddr = "127.0.0.1:7712"

// Manager 桌面控制插件管理器。
type Manager struct {
	deps     Deps
	disabled bool
	cfg      Config
	server   *Server
	srv      *http.Server
	exe      string // openos-server 绝对路径（wrapper 烙写用）

	tokenMu  sync.RWMutex // 护 cfg.UserTokens
	installMu sync.Mutex
	installed map[string]string // uid → 上次成功安装的命令行摘要（幂等跳过用，见 install.go）
}

// New 创建管理器。OPENOS_OSMCP=off 或 OPENOS_GODER=off 时整体停用。
func New(deps Deps) *Manager {
	m := &Manager{
		deps:     deps,
		disabled: os.Getenv("OPENOS_OSMCP") == "off" || os.Getenv("OPENOS_GODER") == "off",
		installed: map[string]string{},
	}
	if m.disabled {
		return m
	}
	// wrapper 烙写当前进程的可执行路径（解析符号链接，dev 的 go run 临时路径
	// 每次启动都变，由 Start 的存量扫描 + EnsureUser 幂等重写自愈）
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		m.exe = exe
	}
	return m
}

// Disabled 是否停用。
func (m *Manager) Disabled() bool { return m.disabled }

// Start 启动回环 MCP 服务端并自愈存量用户的插件安装。
func (m *Manager) Start() {
	if m.disabled {
		return
	}
	os.MkdirAll(filepath.Join(m.deps.DataDir, "goder"), 0o755)
	cfg := loadConfig(m.configPath())
	if v := os.Getenv("OPENOS_OSMCP_ADDR"); v != "" {
		cfg.Addr = v
	}
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		host, port = "127.0.0.1", "7712"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		// 端口被占：回退随机端口并回写（wrapper 每次安装都烙当前端点）
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Printf("osmcp: 启动失败: %v", err)
			return
		}
	}
	cfg.Addr = ln.Addr().String()
	saveConfig(m.configPath(), cfg)
	m.cfg = cfg

	m.server = newServer(m.deps, m.checkUserToken, newBridge(m.deps.Hub))
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", m.server.serveMCP)
	m.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = m.srv.Serve(ln) }()
	log.Printf("osmcp: 桌面控制插件已就绪 %s/mcp（工具 %d 个）", cfg.Addr, len(m.server.tools))

	// 存量用户 wrapper 自愈（可执行路径/端点可能变化；新用户由 /goder-api
	// 首次请求时的 EnsureUser 兜底）
	m.healAll()
}

// Stop 停止 MCP 服务端（优雅停机时调用）。
func (m *Manager) Stop() {
	if m.srv != nil {
		_ = m.srv.Close()
	}
}

// Endpoint 当前 MCP 端点（wrapper 烙写用）。
func (m *Manager) Endpoint() string { return "http://" + m.cfg.Addr + "/mcp" }

func (m *Manager) configPath() string {
	return filepath.Join(m.deps.DataDir, "goder", "osmcp.json")
}

// OnBridgeResp 前端桥应答（挂 hub.Handle("agent:resp")）。
func (m *Manager) OnBridgeResp(env bus.Envelope, reply func(string, any)) {
	if m.server != nil {
		m.server.bridge.onResp(env)
	}
}

func loadConfig(path string) Config {
	cfg := Config{Addr: defaultAddr}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &cfg) // Token 字段仅兼容读取旧文件，校验只认 UserTokens
	}
	if cfg.Addr == "" {
		cfg.Addr = defaultAddr
	}
	return cfg
}

func saveConfig(path string, cfg Config) {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, append(data, '\n'), 0o600) // 令牌等同本地凭据
}

// userToken 取某用户的专属 MCP 令牌（无则生成并持久化，跨启动稳定）。
func (m *Manager) userToken(uid string) string {
	m.tokenMu.Lock()
	defer m.tokenMu.Unlock()
	if m.cfg.UserTokens == nil {
		m.cfg.UserTokens = map[string]string{}
	}
	if t := m.cfg.UserTokens[uid]; t != "" {
		return t
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	t := hex.EncodeToString(b)
	m.cfg.UserTokens[uid] = t
	saveConfig(m.configPath(), m.cfg)
	return t
}

// checkUserToken 校验「令牌 ↔ 用户」绑定：令牌按用户分发，单一令牌泄漏
// 不再波及全体用户（安全审计 2026-10）。
func (m *Manager) checkUserToken(got, uid string) bool {
	m.tokenMu.RLock()
	defer m.tokenMu.RUnlock()
	want := m.cfg.UserTokens[uid]
	return want != "" && got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
