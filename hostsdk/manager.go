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
	"strings"
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
	// ServiceUser 已废弃（仅旧 openos 快照的兼容回落通道使用）：服务身份用户名，
	// 新面 /api/host/* 全部经 X-Exchange-Secret 直连、不再铸造 admin 服务账号令牌
	ServiceUser    string
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
	// 旧管理 REST 面回落用的服务账号令牌缓存（20h；/api/host/* 新面不需要）
	svcToken    string
	svcTokenExp time.Time
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
		// 先确认子进程仍存活（绑定失败立即退出）：健康探测若打到同端口的
		// 他人实例（如本机独立 openos/孤儿进程），会把故障误判为健康
		m.mu.Lock()
		cmd := m.cmd
		m.mu.Unlock()
		if cmd != nil && cmd.Process != nil {
			if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
				return fmt.Errorf("openos 子进程已退出（端口被占用？addr=%s）", m.cfg.Addr)
			}
		}
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

// serviceCall 机对机调用 openos 管理 API：X-Exchange-Secret 直连（/api/host/* 组，
// openos ≥ v0.0.46）——不再铸造 admin 服务账号令牌，与 OPENOS_EXCHANGE_ROLES
// 白名单解耦、不再产生假 admin 用户行。2xx 时把响应 JSON 解到 out（可为 nil）。
//
// 兼容回落：宿主业务代码经 Call 逃生通道自行传入的旧管理 REST 路径（如
// /api/ai/user-providers、/api/apps/role-labels）走登录中间件鉴权，密钥直连会被
// 拒——此时自动回落「服务账号 exchange → Bearer」重试一次（需白名单放行 admin
// 或未配白名单），保证宿主随旧调用面升级 openos 快照时不断路。
func (m *Manager) serviceCall(method, path string, body, out any) error {
	raw0, err0 := m.doCall(method, path, body, "")
	if err0 == nil {
		return decodeOut(path, raw0, out)
	}
	if !strings.HasPrefix(path, "/api/host/") {
		if token, err := m.serviceToken(); err == nil {
			raw1, err1 := m.doCall(method, path, body, token)
			if err1 == nil {
				return decodeOut(path, raw1, out)
			}
			return err1
		}
	}
	return err0
}

// doCall 单次 HTTP 调用（bearer 空时仅凭 X-Exchange-Secret）。
func (m *Manager) doCall(method, path string, body any, bearer string) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, "http://"+m.cfg.Addr+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("X-Exchange-Secret", m.cfg.ExchangeSecret)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s → %d: %s", method, path, resp.StatusCode, string(raw))
	}
	return raw, nil
}

func decodeOut(path string, raw []byte, out any) error {
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s 响应解析失败: %w", path, err)
		}
	}
	return nil
}

// serviceToken 兼容回落的旧服务账号令牌（缓存 20h；仅旧 openos 快照需要，
// 新面 /api/host/* 不经过这里）。deprecated：宿主快照全量升级后可移除。
func (m *Manager) serviceToken() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.svcToken != "" && time.Now().Before(m.svcTokenExp) {
		return m.svcToken, nil
	}
	tok, err := m.Exchange(m.cfg.ServiceUser, "admin", "Host Service", nil)
	if err != nil {
		return "", err
	}
	m.svcToken = tok
	m.svcTokenExp = time.Now().Add(20 * time.Hour)
	return tok, nil
}

// Call 逃生通道：直接调 openos 管理 API（SDK 未封装的新端点）。推荐使用
// /api/host/* 组（密钥直连）；旧管理 REST 路径自动走兼容回落。
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

// NotifyPerm 向「持某权限点的全部用户」推送桌面通知（P4：openos 按角色权限
// 矩阵/用户级快照展开收件人，取代宿主侧 notifyPermHolders 类 SQL）。users
// 非空时与权限展开取并集（如「全局 admin + 持 perm 的员工」一并通知）。
func (m *Manager) NotifyPerm(perm string, users []string, title, body, appID string) {
	if perm == "" {
		m.NotifyUsers(users, title, body, appID)
		return
	}
	if title == "" {
		return
	}
	payload := map[string]any{"title": title}
	if body != "" {
		payload["body"] = body
	}
	if appID != "" {
		payload["appId"] = appID
	}
	raw, _ := json.Marshal(map[string]any{"users": users, "perm": perm, "topic": "sys:notify", "payload": payload})
	req, err := http.NewRequest(http.MethodPost, "http://"+m.cfg.Addr+"/api/hub/publish", bytes.NewReader(raw))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exchange-Secret", m.cfg.ExchangeSecret)
	resp, err := m.client.Do(req)
	if err != nil {
		log.Printf("[hostsdk] 权限通知推送失败（%s）: %v", title, err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Printf("[hostsdk] 权限通知推送被拒（%s）: status %d", title, resp.StatusCode)
	}
}

// HostUser GET /api/host/users 返回的用户条目（全字段，含权限快照与 token 版本）。
type HostUser struct {
	ID           int64    `json:"id"`
	Username     string   `json:"username"`
	DisplayName  string   `json:"displayName"`
	Role         string   `json:"role"`
	Disabled     bool     `json:"disabled"`
	Perms        []string `json:"perms"`
	TokenVersion int64    `json:"tokenVersion"`
	CreatedAt    string   `json:"createdAt"`
}

// UserSync 批量同步的单个用户条目（与 openos /api/host/users/sync 入参同构；
// 指针/nil 语义：Disabled nil=不变；Perms nil=不变、非 nil 含空数组=覆盖）。
type UserSync struct {
	Username    string   `json:"username"`
	Role        string   `json:"role"`
	DisplayName string   `json:"displayName"`
	Disabled    *bool    `json:"disabled"`
	Perms       []string `json:"perms"`
}

// SyncUser 同步单个 openos 用户的角色/显示名/禁用态/权限快照（PUT /api/host/users/{name}，
// 变化即时生效、不吊销该用户其它设备的令牌）。perms 传 nil 表示不变。
func (m *Manager) SyncUser(username, role, displayName string, disabled bool, perms []string) error {
	body := map[string]any{"role": role, "displayName": displayName, "disabled": disabled}
	if perms != nil {
		body["perms"] = perms
	}
	return m.serviceCall(http.MethodPut, "/api/host/users/"+url.PathEscape(username), body, nil)
}

// SyncUsers 批量幂等同步用户（POST /api/host/users/sync：不存在开通、存在按字段
// 更新；不在名单内的用户不动）。返回 created/updated/skipped 统计。
func (m *Manager) SyncUsers(users []UserSync) (created, updated int, skipped []string, err error) {
	var out struct {
		Created  int      `json:"created"`
		Updated  int      `json:"updated"`
		Skipped  []string `json:"skipped"`
	}
	if err := m.serviceCall(http.MethodPost, "/api/host/users/sync", map[string]any{"users": users}, &out); err != nil {
		return 0, 0, nil, err
	}
	return out.Created, out.Updated, out.Skipped, nil
}

// ListUsers 全量用户列表（含权限快照/token 版本/禁用态——宿主对账用）。
func (m *Manager) ListUsers() ([]HostUser, error) {
	var out []HostUser
	if err := m.serviceCall(http.MethodGet, "/api/host/users", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateUser 显式创建用户（宿主权威通道；密码留空 → 随机值，登录只能经 exchange）。
func (m *Manager) CreateUser(username, password, role, displayName string, perms []string) error {
	body := map[string]any{"username": username, "password": password, "role": role, "displayName": displayName}
	if perms != nil {
		body["perms"] = perms
	}
	return m.serviceCall(http.MethodPost, "/api/host/users", body, nil)
}

// DeleteUser 删除 openos 用户（openos 侧级联清其设置/文件/聊天/分享/反馈/AI 供应商/
// 附件与磁盘数据）。失败由调用方旁路记日志（员工档案已删，孤儿桌面用户无法再经
// exchange 换令牌）。
func (m *Manager) DeleteUser(username string) error {
	return m.serviceCall(http.MethodDelete, "/api/host/users/"+url.PathEscape(username), nil, nil)
}

// RoleDecl declare 声明的单个角色（权威在宿主库，openos 侧只读投影）。
// Apps 为 nil 表示该角色的应用授权不在宿主权威内（openos role_apps 运行期
// 唯一存储的场景，如 grf-hotel），声明不动应用授权；非 nil（含空数组）才全量覆盖。
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

// RoleAppMap 当前角色 → 应用授权映射（GET /api/host/apps/access）。
func (m *Manager) RoleAppMap() (map[string][]string, error) {
	var out struct {
		Access map[string][]string `json:"access"`
	}
	if err := m.serviceCall(http.MethodGet, "/api/host/apps/access", nil, &out); err != nil {
		return nil, err
	}
	return out.Access, nil
}

// SetRoleApps 覆盖某角色的应用授权（PUT /api/host/apps/access；Declare 已含此语义，
// 仅存量宿主渐进迁移用）。
func (m *Manager) SetRoleApps(role string, apps []string) error {
	return m.serviceCall(http.MethodPut, "/api/host/apps/access", map[string]any{"role": role, "apps": apps}, nil)
}

// SetRoleLabels 覆盖角色显示名映射（PUT /api/host/apps/role-labels）。
func (m *Manager) SetRoleLabels(labels map[string]string) error {
	return m.serviceCall(http.MethodPut, "/api/host/apps/role-labels", map[string]any{"labels": labels}, nil)
}

// HostApp GET /api/host/apps 返回的应用条目（与 openos store.AppDef 同构的
// 常用字段子集——宿主对账/清退旧应用用）。
type HostApp struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Builtin bool   `json:"builtin"`
	Locked  bool   `json:"locked"`
	Hidden  bool   `json:"hidden"`
	Source  string `json:"source"`
	Version string `json:"version"`
}

// ListApps 全量应用列表（GET /api/host/apps；含 builtin/locked 标记，宿主清理
// 旧版应用的对账依据）。
func (m *Manager) ListApps() ([]HostApp, error) {
	var out []HostApp
	if err := m.serviceCall(http.MethodGet, "/api/host/apps", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteApp 强制删除应用（DELETE /api/host/apps/{id}；宿主清退自己的 locked 应用）。
func (m *Manager) DeleteApp(id string) error {
	return m.serviceCall(http.MethodDelete, "/api/host/apps/"+url.PathEscape(id), nil, nil)
}

// UpsertApp 注册/更新应用（POST /api/host/apps；宿主通道恒信任 locked——
// 设置页禁编辑删除，宿主业务应用标配）。
func (m *Manager) UpsertApp(app map[string]any) error {
	return m.serviceCall(http.MethodPost, "/api/host/apps", app, nil)
}
