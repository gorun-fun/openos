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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

	// AI 联动（见 aisync.go）：配置来源回调 + 逐用户写盘指纹 + 最近同步计数
	aiSource      func() (ProviderConfig, bool)
	aiMu          sync.Mutex
	aiMarks       map[string]aiMark
	aiSyncedUsers int
	// AI 代理（安全审计 2026-10）：非空时托管供应商写「代理地址 + 每用户代理密钥」
	// 而非直连平台 Key——平台 Key 不再落任何 goder 用户家目录
	aiProxyBase string
	aiProxyKey  func(uid string) string
	// providerDB / managedNames：宿主声明的每用户供应商（见 aisync.go）。
	// managedNames 为名字全集常驻缓存（Replace 后经 RefreshManagedNames 失效）；
	// nil = 未接库（仅平台托管条目）。
	providerDB   *store.DB
	managedNames []string
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
		aiMarks:  map[string]aiMark{},
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
		"ADDR="+m.addr,
		"GODER_DATA="+filepath.Join(goderData, "serve-data"),
		"GODER_STATIC_DIR="+static,
		"GODER_IDENTITY=header",
		"GODER_CORS=0",          // 同源反代，无需 CORS
		"GODER_SETTINGS_SEED=1", // 新用户种子宿主 ~/.grf 模型配置，开箱即用
		"GODER_BIN="+m.ghBin,
		"GODER_ACCESS_LOG=0",
	}, safeenv.GhEnvOverrides()...)
	// goder-serve 网关自身的可调项（存在才透传）：空闲回收 TTL、进程生命
	// 周期事件回调（GODER_WEBHOOK_URL/_SECRET，回调带 X-Goder-Signature
	// HMAC 签名头）、运维面令牌（GODER_ADMIN_TOKEN，/goder-api/admin/*）。
	// 值均来自部署配置（与白名单同信任级）、不含 OPENOS_* 秘密。
	for _, name := range []string{
		"GODER_IDLE_TTL", "GODER_WEBHOOK_URL", "GODER_WEBHOOK_SECRET", "GODER_ADMIN_TOKEN",
	} {
		if v := os.Getenv(name); v != "" {
			overrides = append(overrides, name+"="+v)
		}
	}
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

// DeleteUserData 删除某用户的 goder 会话数据目录（header 身份模式下按 UID 分目录；
// 删用户时级联清理，目录不存在时为空操作）。
func (m *Manager) DeleteUserData(username string) {
	if username == "" {
		return
	}
	// serve-data 实际布局为 users/<清洗后 uid>（PerUserHome），历史实现少了 users
	// 段且未做 uid 清洗，删号级联从未真正清掉 goder 数据
	_ = os.RemoveAll(filepath.Join(m.dataDir, "goder", "serve-data", "users", SanUID(username)))
}

// ---- HTTP ----

// ProxyHandler 反代 /goder-api/* → goder-serve，注入身份头。
// 调用方负责已通过 OpenOS 鉴权（identity != nil 时注入，否则 401）。
func (m *Manager) ProxyHandler(identity func(r *http.Request) (uid, name string, ok bool)) http.Handler {
	target, _ := url.Parse("http://" + m.addr)
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.FlushInterval = -1 // SSE 流式透传
	rp.ModifyResponse = m.markManagedProvider
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
		if !m.Ready() {
			// 惰性拉起：首个请求到达时若未运行则启动（等待至多 5s）
			m.Start()
			deadline := time.Now().Add(5 * time.Second)
			for !m.Ready() && time.Now().Before(deadline) {
				time.Sleep(150 * time.Millisecond)
			}
			if !m.Ready() {
				http.Error(w, `{"error":"goder 服务未就绪，请稍后重试或检查部署"}`, http.StatusServiceUnavailable)
				return
			}
		}
		uid, name, ok := identity(r)
		if !ok {
			http.Error(w, `{"error":"未认证"}`, http.StatusUnauthorized)
			return
		}
		_ = uid
		_ = name
		rp.ServeHTTP(w, r)
	})
}

// markManagedProvider GET /goder-api/v1/agent/models（goder 设置页模型表的
// 数据源）响应里，为平台托管供应商注入 managed:true —— goder 嵌入约定的
// 只读展示语义（见 goder docs/embedding.md 与 agent-dto.ts ProviderDTO.managed），
// 用户在 goder 设置页看到的 OpenOS 条目与宿主声明条目均不可改（前者来源
// 「系统设置 → AI 服务」，后者来源宿主业务系统如 token-hub 套餐）。
// 用 map[string]json.RawMessage 改写，default/path/maxSteps 等其余键原样保真
// （goder 前端 PUT 时整表回传，丢键会破坏用户配置）。
func (m *Manager) markManagedProvider(resp *http.Response) error {
	if resp.Request == nil || resp.Request.Method != http.MethodGet ||
		resp.Request.URL.Path != "/goder-api/v1/agent/models" ||
		resp.Header.Get("Content-Encoding") != "" {
		return nil
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "json") {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil // 读到多少放回多少，不因改写失败断掉响应
	}
	var all map[string]json.RawMessage
	if json.Unmarshal(body, &all) != nil {
		return restoreBody(resp, body)
	}
	var provs []map[string]json.RawMessage
	if json.Unmarshal(all["providers"], &provs) != nil || len(provs) == 0 {
		return restoreBody(resp, body)
	}
	managed, _ := json.Marshal(true)
	touched := false
	for _, p := range provs {
		var name string
		_ = json.Unmarshal(p["name"], &name)
		if m.isManagedProviderName(name) {
			p["managed"] = managed
			touched = true
		}
	}
	if !touched {
		return restoreBody(resp, body)
	}
	nb, err := json.Marshal(provs)
	if err != nil {
		return restoreBody(resp, body)
	}
	all["providers"] = nb
	out, err := json.Marshal(all)
	if err != nil {
		return restoreBody(resp, body)
	}
	return restoreBody(resp, out)
}

// isManagedProviderName 供应商名是否为平台托管名或宿主声明名（只读展示语义）。
func (m *Manager) isManagedProviderName(name string) bool {
	if name == AIProviderName {
		return true
	}
	for _, n := range m.managedNamesSnapshot() {
		if n == name {
			return true
		}
	}
	return false
}

// restoreBody 把（可能改写过的）响应体放回并校准 Content-Length。
func restoreBody(resp *http.Response, body []byte) error {
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
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
