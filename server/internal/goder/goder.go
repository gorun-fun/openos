// Package goder 将 goder（grf-harness 图形界面）作为内置应用集成进 OpenOS，
// 集成方式与 grf-play 一致（见 ~/AiProjects/goder/docs/embedding.md）：
//
//   - OpenOS 服务端托管 goder-serve 子进程（header 身份模式，每用户独立 gh server）
//   - /goder-api/* 反代到 goder-serve，注入 X-GRF-UID / X-GRF-Name（覆盖式，防外部伪造）
//   - /vendor/goder.js 提供自包含嵌入库（前端动态 import 后 configure + <goder-app-shell>）
//
// 环境变量：
//
//	OPENOS_GODER       off 时整体停用（不启动子进程、store 下架 goder 应用；纯业务宿主建议开启）
//	OPENOS_GODER_BIN   goder-serve 二进制（默认 <data>/bin/goder-serve；不存在则功能关闭）
//	OPENOS_GODER_JS    goder.js 嵌入库路径（默认 <data>/goder/goder.js）
//	OPENOS_GODER_ADDR  goder-serve 监听地址（默认 127.0.0.1:7711）
//	OPENOS_GH_BIN      gh 可执行文件（传给 goder-serve 的 GODER_BIN，默认 PATH 上的 gh）
package goder

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"openos/server/internal/safeenv"
	"openos/server/internal/store"
)

// Manager goder 集成管理器。
type Manager struct {
	binPath string
	jsPath  string
	addr    string
	ghBin   string
	dataDir string
	// disabled：OPENOS_GODER=off 时整体停用（纯业务宿主无需 AI IDE，
	// 同时 store 侧会下架 goder 应用，前端也不再种快捷方式）
	disabled bool

	mu      sync.Mutex
	cmd     *exec.Cmd
	started bool
	ready   bool
	lastErr string

	proxy *httputil.ReverseProxy

	// adminToken / admin：goder-serve 宿主管理面（/goder-api/admin/*，
	// 声明式集成通道——供应商声明/模型解析/插件安装/会话索引/用户目录
	// 生命周期）的 Bearer 令牌与客户端。未配置 GODER_ADMIN_TOKEN 时每次
	// 进程启动生成随机值（子进程只听回环、令牌只在本进程与子进程之间
	// 流转，无须持久化）。全部管理面调用走 adminCall（见 admin.go）。
	adminToken string
	admin      *http.Client

	// AI 联动（见 aisync.go）。锁归属单一：本组全部字段只由 aiMu 保护
	//（历史上曾横跨 mu/aiMu 两把锁，读者无从判断该用哪把，已统一收敛）；
	// mu 只管子进程生命周期（cmd/started/ready）。
	aiMu   sync.Mutex
	aiEnsured map[string]string
	// AI 代理（安全审计 2026-10）：非空时托管供应商写「代理地址 + 每用户代理密钥」
	// 而非直连平台 Key——平台 Key 不再落任何 goder 用户家目录
	aiProxyBase string
	aiProxyKey  func(uid string) string
	// providerDB / managedNames：宿主声明的每用户供应商（见 aisync.go）。
	// managedNames 为名字全集常驻缓存（Replace 后经 RefreshManagedNames 失效）；
	// nil = 未接库（仅平台托管条目）。
	providerDB    *store.DB
	managedNames  []string
	aiSource      func() (ProviderConfig, bool)
	aiSyncedUsers int
}

// New 创建管理器；二进制不存在时 available=false（前端显示引导）。
func New(dataDir string) *Manager {
	m := &Manager{
		binPath:  envOr("OPENOS_GODER_BIN", filepath.Join(dataDir, "bin", "goder-serve")),
		jsPath:   envOr("OPENOS_GODER_JS", filepath.Join(dataDir, "goder", "goder.js")),
		addr:     envOr("OPENOS_GODER_ADDR", "127.0.0.1:7711"),
		ghBin:    envOr("OPENOS_GH_BIN", "gh"),
		dataDir:  dataDir,
		disabled: os.Getenv("OPENOS_GODER") == "off",
		aiEnsured: map[string]string{},
		admin:     &http.Client{Timeout: 30 * time.Second},
	}
	m.adminToken = os.Getenv("GODER_ADMIN_TOKEN")
	if m.adminToken == "" {
		// 未配置则自持随机令牌（launch 时注入子进程）：管理面只服务本进程
		// 的声明式集成，运维仍可用 GODER_ADMIN_TOKEN 显式指定固定值。
		b := make([]byte, 24)
		if _, err := rand.Read(b); err == nil {
			m.adminToken = hex.EncodeToString(b)
		}
	}
	if m.disabled {
		m.lastErr = "goder 已通过 OPENOS_GODER=off 停用"
		return m
	}
	// goder-serve 是无鉴权的 header 身份服务，绝不允许绑定回环之外
	//（安全审计 2026-10：非回环即任意客户端可冒充任意用户）。
	if host, _, err := net.SplitHostPort(m.addr); err == nil {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			log.Printf("goder: 拒绝启动：OPENOS_GODER_ADDR=%s 非回环地址（goder-serve 无自身鉴权，仅允许 127.0.0.1/::1）", m.addr)
			m.disabled = true
			m.lastErr = "OPENOS_GODER_ADDR 必须是回环地址"
		}
	} else {
		log.Printf("goder: 拒绝启动：OPENOS_GODER_ADDR=%s 无法解析（期望 host:port 回环地址）", m.addr)
		m.disabled = true
		m.lastErr = "OPENOS_GODER_ADDR 格式非法"
	}
	return m
}

// Available goder-serve 二进制是否就绪。
func (m *Manager) Available() bool {
	st, err := os.Stat(m.binPath)
	return err == nil && !st.IsDir()
}

// JSAvailable 嵌入库是否存在。
func (m *Manager) JSAvailable() bool {
	st, err := os.Stat(m.jsPath)
	return err == nil && !st.IsDir()
}

// Start 拉起 goder-serve 子进程（幂等；崩溃自动重启）。OPENOS_GODER=off 时为空操作。
func (m *Manager) Start() {
	if m.disabled {
		return
	}
	if !m.Available() {
		m.lastErr = "未找到 goder-serve 二进制：" + m.binPath
		return
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()
	m.launch()
}

func (m *Manager) launch() {
	goderData := filepath.Join(m.dataDir, "goder")
	os.MkdirAll(goderData, 0o755)
	// 静态目录给空目录：OpenOS 仅消费 /goder-api，前端由 OpenOS 自己托管
	static := filepath.Join(goderData, "static-empty")
	os.MkdirAll(static, 0o755)

	cmd := exec.Command(m.binPath)
	// 环境白名单化：goder-serve 及其拉起的 gh 子进程不继承服务进程秘密
	//（OPENOS_* 密钥等，安全审计 2026-10），所需变量逐个显式注入。
	// GODER_GH_ENV_*（宿主透传给 gh 子进程的能力开关，典型如回合事件
	// webhook GRF_TURN_WEBHOOK_URL/_SECRET）经 GhEnvOverrides 单独收集，
	// 不进共享白名单（真实终端子进程不应看到）。
	overrides := append([]string{
		"ADDR=" + m.addr,
		"GODER_DATA=" + filepath.Join(goderData, "serve-data"),
		"GODER_STATIC_DIR=" + static,
		"GODER_IDENTITY=header",
		"GODER_CORS=0",          // 同源反代，无需 CORS
		"GODER_SETTINGS_SEED=1", // 新用户种子宿主 ~/.grf 模型配置，开箱即用
		"GODER_BIN=" + m.ghBin,
		"GODER_ACCESS_LOG=0",
	}, safeenv.GhEnvOverrides()...)
	// goder-serve 网关自身的可调项（存在才透传）：空闲回收 TTL、进程生命
	// 周期事件回调（GODER_WEBHOOK_URL/_SECRET，回调带 X-Goder-Signature
	// HMAC 签名头）。值来自部署配置（与白名单同信任级）、不含 OPENOS_* 秘密。
	for _, name := range []string{
		"GODER_IDLE_TTL", "GODER_WEBHOOK_URL", "GODER_WEBHOOK_SECRET",
	} {
		if v := os.Getenv(name); v != "" {
			overrides = append(overrides, name+"="+v)
		}
	}
	// 管理面令牌恒注入（New 里已确定：配置值或自持随机值）——声明式集成
	// （供应商/插件/索引/删号级联）全走 /goder-api/admin/*，不再依赖部署
	// 侧显式配置才可用。
	overrides = append(overrides, "GODER_ADMIN_TOKEN="+m.adminToken)
	cmd.Env = safeenv.Env(overrides...)
	cmd.Stdout = logWriter{prefix: "[goder-serve] "}
	cmd.Stderr = logWriter{prefix: "[goder-serve] "}
	if err := cmd.Start(); err != nil {
		m.mu.Lock()
		m.ready = false
		m.lastErr = err.Error()
		m.mu.Unlock()
		log.Printf("goder: 启动失败: %v", err)
		return
	}
	m.mu.Lock()
	m.cmd = cmd
	m.ready = true
	m.lastErr = ""
	m.mu.Unlock()
	log.Printf("goder: goder-serve 已启动 pid=%d addr=%s", cmd.Process.Pid, m.addr)

	// 崩溃自动重启（指数退避，上限 30s）
	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		m.ready = false
		m.lastErr = fmt.Sprintf("进程退出: %v", err)
		m.cmd = nil
		m.started = false
		m.mu.Unlock()
		log.Printf("goder: %v，10s 后重启", m.lastErr)
		time.Sleep(10 * time.Second)
		m.mu.Lock()
		m.started = false
		m.mu.Unlock()
		m.Start()
	}()
}

// Stop 停止子进程（优雅停机时调用）。
func (m *Manager) Stop() {
	m.mu.Lock()
	cmd := m.cmd
	m.started = false
	m.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
	}
}

// Ready 子进程是否在运行。
func (m *Manager) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ready
}

// ensureReady 惰性拉起并等待 goder-serve 就绪（ProxyHandler 首请求与
// hosttasks 的 ready 共用同一份等待循环）：未运行则 Start 并以 150ms 轮询，
// 至多等 wait；disabled 或二进制缺失时直接 false，不开等待循环。
func (m *Manager) ensureReady(wait time.Duration) bool {
	if m.disabled || !m.Available() {
		return false
	}
	if m.Ready() {
		return true
	}
	m.Start()
	deadline := time.Now().Add(wait)
	for !m.Ready() && time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
	}
	return m.Ready()
}

// Status 状态汇总（前端引导用）。
func (m *Manager) Status() map[string]any {
	return map[string]any{
		"available":     !m.disabled && m.Available() && m.JSAvailable(),
		"running":       m.Ready(),
		"disabled":      m.disabled,
		"addr":          m.addr,
		"error":         m.lastErr,
		"aiProvider":    AIProviderName,
		"aiSyncedUsers": m.LastSyncUsers(),
	}
}

// DeleteUserData 删除某用户的 goder 全部数据（停其 gh 进程 + 删用户目录，
// 幂等）。删用户时级联清理；经 goder-serve 管理面完成（含 PerUserHome 目录
// 清洗规则），openos 不再触碰 serve-data 布局。
func (m *Manager) DeleteUserData(username string) {
	if username == "" || m.disabled {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.adminCall(ctx, http.MethodDelete, "/goder-api/admin/users/"+url.PathEscape(username), nil, nil); err != nil {
		// goder 未部署/未就绪时目录本就不存在（或无进程），按幂等空操作处理
		log.Printf("goder: 删号级联清理失败(%s): %v", username, err)
	}
}

// ---- HTTP ----

// ProxyHandler 反代 /goder-api/* → goder-serve，注入身份头。
// 调用方负责已通过 OpenOS 鉴权（identity != nil 时注入，否则 401）。
// 平台托管供应商的 managed:true 注入由 goder-serve 网关在其代理层完成
// （声明过受管名即生效，见 admin.go 的供应商声明），本端不再改写响应。
func (m *Manager) ProxyHandler(identity func(r *http.Request) (uid, name string, ok bool)) http.Handler {
	target, _ := url.Parse("http://" + m.addr)
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.FlushInterval = -1 // SSE 流式透传
	orig := rp.Director
	rp.Director = func(req *http.Request) {
		orig(req)
		// 覆盖式设置身份头：先剥离外部伪造值，再注入 OpenOS 登录态
		req.Header.Del("X-GRF-UID")
		req.Header.Del("X-GRF-Name")
		req.Header.Del("X-Forwarded-User")
		if uid, name, ok := identity(req); ok {
			req.Header.Set("X-GRF-UID", uid)
			req.Header.Set("X-GRF-Name", name)
		}
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, `{"error":"goder 服务不可达（可能正在启动）"}`, http.StatusBadGateway)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 惰性拉起：首个请求到达时若未运行则启动（等待至多 5s）
		if !m.ensureReady(5 * time.Second) {
			http.Error(w, `{"error":"goder 服务未就绪，请稍后重试或检查部署"}`, http.StatusServiceUnavailable)
			return
		}
		// identity 在此仅做 401 判定（身份头由 Director 里的 identity 注入）
		if _, _, ok := identity(r); !ok {
			http.Error(w, `{"error":"未认证"}`, http.StatusUnauthorized)
			return
		}
		rp.ServeHTTP(w, r)
	})
}

// ServeJS GET /vendor/goder.js —— 自包含 ESM 嵌入库。
func (m *Manager) ServeJS(w http.ResponseWriter, r *http.Request) {
	if !m.JSAvailable() {
		http.Error(w, "// goder.js 未部署：见 OPENOS_GODER_JS 配置", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache") // 开发迭代期禁长缓存
	http.ServeFile(w, r, m.jsPath)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type logWriter struct{ prefix string }

func (l logWriter) Write(p []byte) (int, error) {
	log.Print(l.prefix + strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
