// admin.go — goder-serve 宿主管理面（/goder-api/admin/*）的客户端面。
// 声明式集成通道：托管供应商声明、裸名模型解析、宿主插件安装、会话索引、
// 用户目录生命周期全部经 API 表达意图（goder ≥ 宿主管理面版本起提供，
// 见 goder docs/embedding.md「宿主管理面」）；此前 openos 直写 goder 私有
// 文件（serve-data 布局 / settings.json 多写者协议 / wrapper 反抠）的
// 全部路径知识由此收敛进 goder-serve，openos 侧只剩本文件的薄客户端。
package goder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// adminCall 调 goder-serve 管理面（Bearer 管理令牌，见 Manager.adminToken）。
// path 以 /goder-api/admin 开头；body 非 nil 时 JSON 编码发送；out 非 nil 时
// 解析响应。非 200 返回带状态码与响应体摘录的错误。冷启动竞态兜底：ensureReady
// 在子进程 exec 成功即返回，goder-serve 绑定端口尚需百毫秒级——连接类错误
//（connection refused）等 400ms 重试一次，覆盖启动自愈/首个请求撞空窗。
func (m *Manager) adminCall(ctx context.Context, method, path string, body, out any) error {
	err := m.adminCallOnce(ctx, method, path, body, out)
	if err == nil || !isConnError(err) {
		return err
	}
	select {
	case <-ctx.Done():
		return err
	case <-time.After(400 * time.Millisecond):
	}
	return m.adminCallOnce(ctx, method, path, body, out)
}

func (m *Manager) adminCallOnce(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+m.addr+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+m.adminToken)
	resp, err := m.admin.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("goder-serve 管理面 HTTP %d: %s", resp.StatusCode, clipStr(string(raw), 300))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("管理面响应解析失败: %w", err)
		}
	}
	return nil
}

// isConnError 连接建立类错误（冷启动窗口的 connection refused 等）。
func isConnError(err error) bool {
	var ce *net.OpError
	if errors.As(err, &ce) {
		return ce.Op == "dial" || ce.Op == "read"
	}
	return false
}

func clipStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// adminUserEscape 管理面路径里的 uid 段转义（用户名无字符集限制）。
func adminUserEscape(uid string) string { return url.PathEscape(uid) }

// ---- 端点面（DTO 与 goder pkg/gateway/adminhost.go 对齐） ----

// AdminProvidersDecl PUT /admin/users/{uid}/providers 请求体。
type AdminProvidersDecl struct {
	Entries      []AdminProviderEntry `json:"entries"`
	Managed      []string             `json:"managed"`
	Default      string               `json:"default"`
	ForceDefault bool                 `json:"force_default"`
}

// AdminProviderEntry 一条受管供应商声明（键形状与 gh settings.json 的
// providers.<name> 一致）。
type AdminProviderEntry struct {
	Name    string   `json:"name"`
	BaseURL string   `json:"base_url"`
	APIKey  string   `json:"api_key"`
	Models  []string `json:"models"`
}

// AdminProvidersMark GET /admin/users/{uid}/providers 响应：settings.json
// 指纹与最近声明的受管名（EnsureAIUser 幂等缓存判定用）。
type AdminProvidersMark struct {
	Mark    string   `json:"mark"`
	Managed []string `json:"managed"`
}

// AdminUserEntry GET /admin/users 条目（San 即清洗后的目录名，与声明表/
// 代理密钥派生口径一致）。
type AdminUserEntry struct {
	San     string `json:"san"`
	Running bool   `json:"running"`
	Addr    string `json:"addr"`
}

// AdminResolveResult GET /admin/users/{uid}/resolve-model 响应。
type AdminResolveResult struct {
	Status     string   `json:"status"` // ok|no_default|not_found|ambiguous|unreadable
	Resolved   string   `json:"resolved"`
	Candidates []string `json:"candidates"`
}

// AdminPluginItem GET /admin/plugins 条目（UID 为原始用户名——目录名清洗
// 不可逆，规格存档是唯一来源）。
type AdminPluginItem struct {
	UID  string `json:"uid"`
	San  string `json:"san"`
	Name string `json:"name"`
}

// adminListUsers GET /admin/users。
func (m *Manager) adminListUsers(ctx context.Context) ([]AdminUserEntry, error) {
	var out struct {
		Users []AdminUserEntry `json:"users"`
	}
	if err := m.adminCall(ctx, http.MethodGet, "/goder-api/admin/users", nil, &out); err != nil {
		return nil, err
	}
	return out.Users, nil
}

// adminPutProviders PUT /admin/users/{uid}/providers（返回是否实际写盘）。
func (m *Manager) adminPutProviders(ctx context.Context, uid string, decl AdminProvidersDecl) (bool, error) {
	var out struct {
		Applied bool `json:"applied"`
	}
	err := m.adminCall(ctx, http.MethodPut,
		"/goder-api/admin/users/"+adminUserEscape(uid)+"/providers", decl, &out)
	return out.Applied, err
}

// adminGetProvidersMark GET /admin/users/{uid}/providers（指纹查询）。
func (m *Manager) adminGetProvidersMark(ctx context.Context, uid string) (AdminProvidersMark, error) {
	var out AdminProvidersMark
	err := m.adminCall(ctx, http.MethodGet,
		"/goder-api/admin/users/"+adminUserEscape(uid)+"/providers", nil, &out)
	return out, err
}

// adminResolveModel GET /admin/users/{uid}/resolve-model?name=。
func (m *Manager) adminResolveModel(ctx context.Context, uid, name string) (AdminResolveResult, error) {
	var out AdminResolveResult
	err := m.adminCall(ctx, http.MethodGet,
		"/goder-api/admin/users/"+adminUserEscape(uid)+"/resolve-model?name="+url.QueryEscape(name), nil, &out)
	return out, err
}

// adminInstallPlugin POST /admin/plugins/install。
func (m *Manager) adminInstallPlugin(ctx context.Context, uid, name string, command []string) error {
	return m.adminCall(ctx, http.MethodPost, "/goder-api/admin/plugins/install", map[string]any{
		"uid": uid, "name": name, "command": command,
	}, nil)
}

// adminListPlugins GET /admin/plugins。
func (m *Manager) adminListPlugins(ctx context.Context) ([]AdminPluginItem, error) {
	var out struct {
		Plugins []AdminPluginItem `json:"plugins"`
	}
	if err := m.adminCall(ctx, http.MethodGet, "/goder-api/admin/plugins", nil, &out); err != nil {
		return nil, err
	}
	return out.Plugins, nil
}

// adminAppendIndexSession POST /admin/users/{uid}/index/sessions（无头任务
// 代建会话的归组落点；读改写在 goder-serve 一把锁内完成）。
func (m *Manager) adminAppendIndexSession(ctx context.Context, uid string, body map[string]any) error {
	return m.adminCall(ctx, http.MethodPost,
		"/goder-api/admin/users/"+adminUserEscape(uid)+"/index/sessions", body, nil)
}

// ---- 导出面（osmcp 等包消费） ----

// EnsureReady 惰性拉起并等待 goder-serve 就绪（跨包消费口）。
func (m *Manager) EnsureReady(wait time.Duration) bool { return m.ensureReady(wait) }

// InstallHostPlugin 安装一个宿主 stdio 插件到某用户的 gh（osmcp 桌面控制
// 插件的安装通道；命令行 = 完整 argv，goder-serve 负责 wrapper/settings
// 双模式落盘与规格存档）。
func (m *Manager) InstallHostPlugin(ctx context.Context, uid, name string, command []string) error {
	if !m.ensureReady(5 * time.Second) {
		return fmt.Errorf("goder 服务未就绪")
	}
	return m.adminInstallPlugin(ctx, uid, name, command)
}

// ListHostPlugins 全部已安装宿主插件的清单（含原始 uid——存量自愈的
// 遍历面）。
func (m *Manager) ListHostPlugins(ctx context.Context) ([]AdminPluginItem, error) {
	if !m.ensureReady(5 * time.Second) {
		return nil, fmt.Errorf("goder 服务未就绪")
	}
	return m.adminListPlugins(ctx)
}
