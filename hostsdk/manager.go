// manager.go — openos-server 子进程生命周期托管（自 grf-hotel/grf-os 同构实现提炼）。
package hostsdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Config 子进程托管配置。
type Config struct {
	Bin            string   // openos-server 可执行文件路径（空则 FindBin 兜底）
	Addr           string   // 子进程环回监听地址（默认 127.0.0.1:8421）
	DataDir        string   // OPENOS_DATA（SQLite/沙箱/goder 数据；默认 ./openos-data）
	ExchangeSecret string   // 与 openos 共享的交换密钥（必填——exchange/declare/publish 全靠它）
	ExchangeRoles  string   // 透传 OPENOS_EXCHANGE_ROLES（强烈建议配置：密钥泄露防铸 admin）
	GoderAddr      string   // 透传 OPENOS_GODER_ADDR（同机多实例错开 goder-serve 端口）
	MarketURL      string   // 透传 OPENOS_MARKET_URL（宿主货架 catalog.json；空则用 openos 默认）
	ExtraEnv       []string // 附加环境（如 OPENOS_VFS=off、OPENOS_MARKET=off）
	ServiceUser    string   // 服务身份用户名（管理 API 调用时换取 admin 令牌；默认 "host-service"）
	// ExternalAuth 身份唯一来源为宿主 SSO（默认 true）：OPENOS_AUTH_MODE=external
	//（openos 本地账密/改密 403、演示账号不种子）+ OPENOS_SEED_DEMO=0。
	// 独立双进程形态（宿主无 SSO）才置 false。
	ExternalAuth bool
	// Dist OPENOS_DIST；默认空目录（静态资源由宿主前端托管，子进程仅提供 API/WS）
	Dist string
}

// DefaultConfig 常用缺省。
func DefaultConfig() Config {
	return Config{
		Addr:         "127.0.0.1:8421",
		DataDir:      "./openos-data",
		ServiceUser:  "host-service",
		ExternalAuth: true,
	}
}

// Manager 子进程托管器。
type Manager struct {
	cfg    Config
	cmd    *exec.Cmd
	mu     sync.Mutex
	client *http.Client
	proxy  http.Handler
}

// New 创建托管器（未启动）。
func New(cfg Config) *Manager {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:8421"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "./openos-data"
	}
	if cfg.ServiceUser == "" {
		cfg.ServiceUser = "host-service"
	}
	m := &Manager{cfg: cfg, client: &http.Client{Timeout: 15 * time.Second}}
	target, _ := url.Parse("http://" + cfg.Addr)
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("[hostsdk] 代理失败 %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, `{"error":"桌面框架暂不可用"}`, http.StatusBadGateway)
	}
	m.proxy = rp
	return m
}

// Addr 子进程监听地址。
func (m *Manager) Addr() string { return m.cfg.Addr }

// Start 拉起子进程并等待健康（15s）。二进制缺失返回错误（桌面壳是必选件，不降级）。
func (m *Manager) Start() error {
	cfg := m.cfg
	bin := cfg.Bin
	if bin == "" {
		bin = FindBin()
	}
	if bin == "" {
		return fmt.Errorf("未找到 openos-server 二进制（配置 Bin 或放到 bin/openos-server）")
	}
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("openos-server 不存在: %s", bin)
	}
	if cfg.ExchangeSecret == "" {
		return fmt.Errorf("ExchangeSecret 未配置（exchange/declare/publish 均不可用）")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}

	env := []string{
		"OPENOS_ADDR=" + cfg.Addr,
		"OPENOS_DATA=" + cfg.DataDir,
		"OPENOS_EXCHANGE_SECRET=" + cfg.ExchangeSecret,
	}
	if cfg.ExchangeRoles != "" {
		env = append(env, "OPENOS_EXCHANGE_ROLES="+cfg.ExchangeRoles)
	}
	if cfg.GoderAddr != "" {
		env = append(env, "OPENOS_GODER_ADDR="+cfg.GoderAddr)
	}
	if cfg.MarketURL != "" {
		env = append(env, "OPENOS_MARKET_URL="+cfg.MarketURL)
	}
	if cfg.ExternalAuth {
		// 身份唯一来源为宿主 SSO：openos 本地账密通道整体下线（登录/自助改密
		// 403、演示账号不种子），用户只能经 exchange 开通
		env = append(env, "OPENOS_AUTH_MODE=external", "OPENOS_SEED_DEMO=0")
	}
	env = append(env, "OPENOS_DIST="+cfg.Dist) // 空 = 静态资源由宿主托管
	env = append(env, cfg.ExtraEnv...)

	log.Printf("[hostsdk] 启动 openos 子进程 %s (addr=%s data=%s)", bin, cfg.Addr, cfg.DataDir)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 openos-server 失败: %w", err)
	}
	m.mu.Lock()
	m.cmd = cmd
	m.mu.Unlock()

	if err := m.waitHealthy(15 * time.Second); err != nil {
		m.Stop()
		return err
	}
	return nil
}

// Stop 终止子进程（幂等；SIGTERM → 5s → Kill）。
func (m *Manager) Stop() {
	m.mu.Lock()
	cmd := m.cmd
	m.cmd = nil
	m.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	log.Println("[hostsdk] openos 子进程已退出")
}

func (m *Manager) waitHealthy(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := m.client.Get("http://" + m.cfg.Addr + "/api/health")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("openos 子进程健康检查超时（%s）", timeout)
}

// Handler 反向代理到子进程。纵深防御封锁 openos 自带账密登录：子进程本就以
// OPENOS_AUTH_MODE=external 下线了登录端点（403），此处再拦一道（身份唯一来源为宿主 SSO）。
func (m *Manager) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/login" {
			http.Error(w, `{"error":"请使用统一身份登录"}`, http.StatusForbidden)
			return
		}
		m.proxy.ServeHTTP(w, r)
	})
}

// FindBin 按常见位置寻找 openos-server 二进制（run.sh 会先构建到 server/bin/；
// OPENOS_BIN 环境变量优先）。
func FindBin() string {
	for _, p := range []string{
		os.Getenv("OPENOS_BIN"),
		filepath.Join("bin", "openos-server"),
		filepath.Join("server", "bin", "openos-server"),
	} {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// ---- 服务端到服务端 API（Client 面） ----

// Exchange 服务端到服务端换取 openos JWT（用户不存在自动开通；角色变化热吊销）。
// perms 非空时写入用户级权限快照（宿主接管模式：宿主权威库算出的权限并集）。
func (m *Manager) Exchange(username, role, displayName string, perms []string) (string, error) {
	if username == "" {
		return "", fmt.Errorf("空用户名")
	}
	body := map[string]any{"username": username, "role": role, "displayName": displayName}
	if perms != nil {
		body["perms"] = perms
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, "http://"+m.cfg.Addr+"/api/auth/exchange", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exchange-Secret", m.cfg.ExchangeSecret)
	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("exchange status %d: %s", resp.StatusCode, string(out))
	}
	var parsed struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil || parsed.Token == "" {
		return "", fmt.Errorf("exchange 响应异常")
	}
	return parsed.Token, nil
}

// serviceCall 以服务身份（ExchangeSecret + 服务用户令牌）调用 openos 管理 API，
// 2xx 时把响应 JSON 解到 out（可为 nil）。openos 以交换密钥头识别宿主服务调用
// （如应用注册携带 locked 时仅此身份可信）。
func (m *Manager) serviceCall(method, path string, body, out any) error {
	token, err := m.Exchange(m.cfg.ServiceUser, "admin", "Host Service", nil)
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, "http://"+m.cfg.Addr+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Exchange-Secret", m.cfg.ExchangeSecret)
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s → %d: %s", method, path, resp.StatusCode, string(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s 响应解析失败: %w", path, err)
		}
	}
	return nil
}

// Call 逃生通道：直接调 openos 管理 API（SDK 未封装的新端点）。
func (m *Manager) Call(method, path string, body, out any) error {
	return m.serviceCall(method, path, body, out)
}

// NotifyUsers 向一批 openos 用户推送桌面通知（sys:notify：右上角横幅 + 通知中心，
// 点击可打开 appID 应用）。旁路能力：推送失败仅记日志绝不影响调用方业务事务；
// 不在线的用户无投递，由前端列表/轮询兜底。
func (m *Manager) NotifyUsers(users []string, title, body, appID string) {
	if len(users) == 0 || title == "" {
		return
	}
	payload := map[string]any{"title": title}
	if body != "" {
		payload["body"] = body
	}
	if appID != "" {
		payload["appId"] = appID
	}
	raw, _ := json.Marshal(map[string]any{"users": users, "topic": "sys:notify", "payload": payload})
	req, err := http.NewRequest(http.MethodPost, "http://"+m.cfg.Addr+"/api/hub/publish", bytes.NewReader(raw))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exchange-Secret", m.cfg.ExchangeSecret)
	resp, err := m.client.Do(req)
	if err != nil {
		log.Printf("[hostsdk] 桌面通知推送失败（%s）: %v", title, err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Printf("[hostsdk] 桌面通知推送被拒（%s）: status %d", title, resp.StatusCode)
	}
}

// SyncUser 同步单个 openos 用户的角色/显示名/禁用态/权限快照（PUT /api/users/{name}，
// 变化自动吊销其全部旧令牌）。perms 传 nil 表示不变。
func (m *Manager) SyncUser(username, role, displayName string, disabled bool, perms []string) error {
	body := map[string]any{"role": role, "displayName": displayName, "disabled": disabled}
	if perms != nil {
		body["perms"] = perms
	}
	return m.serviceCall(http.MethodPut, "/api/users/"+url.PathEscape(username), body, nil)
}

// DeleteUser 删除 openos 用户（openos 侧级联清其设置/文件/磁盘数据）。
// 失败由调用方旁路记日志（员工档案已删，孤儿桌面用户无法再经 exchange 换令牌）。
func (m *Manager) DeleteUser(username string) error {
	return m.serviceCall(http.MethodDelete, "/api/users/"+url.PathEscape(username), nil, nil)
}

// RoleDecl declare 声明的单个角色（权威在宿主库，openos 侧只读投影）。
type RoleDecl struct {
	Key   string   `json:"key"`
	Label string   `json:"label"`
	Perms []string `json:"perms"`
	Apps  []string `json:"apps"`
}

// PermDecl 权限点目录声明。
type PermDecl struct {
	Code  string `json:"code"`
	Group string `json:"group"`
	Label string `json:"label"`
}

// Declare 声明式全量同步角色目录/权限点/应用授权到 openos（POST /api/host/declare，
// 幂等收敛：清单外 host 角色移除、system 角色不动）。取代宿主侧 os_role_seed
// 标记表 / 「access 含 gateway」这类种子 workaround。保存即广播 sys:apps-changed。
func (m *Manager) Declare(roles []RoleDecl, perms []PermDecl) error {
	body := map[string]any{"roles": roles, "perms": perms}
	return m.serviceCall(http.MethodPost, "/api/host/declare", body, nil)
}

// RoleAppMap 当前角色 → 应用授权映射（GET /api/apps/access）。
func (m *Manager) RoleAppMap() (map[string][]string, error) {
	var out struct {
		Access map[string][]string `json:"access"`
	}
	if err := m.serviceCall(http.MethodGet, "/api/apps/access", nil, &out); err != nil {
		return nil, err
	}
	return out.Access, nil
}

// SetRoleApps 覆盖某角色的应用授权（PUT /api/apps/access；Declare 已含此语义，
// 仅存量宿主渐进迁移用）。
func (m *Manager) SetRoleApps(role string, apps []string) error {
	return m.serviceCall(http.MethodPut, "/api/apps/access", map[string]any{"role": role, "apps": apps}, nil)
}

// UpsertApp 注册/更新应用（POST /api/apps；带 locked=true 时仅宿主服务身份可信，
// 设置页禁编辑删除——宿主业务应用标配）。
func (m *Manager) UpsertApp(app map[string]any) error {
	return m.serviceCall(http.MethodPost, "/api/apps", app, nil)
}
