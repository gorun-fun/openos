// aisync.go — AI 模型配置与 goder 的联动（设置 → AI 服务 的落点）。
//
// OpenOS 唯一的 AI 应用是 goder：管理员在「系统设置 → AI 服务」保存的
// Base URL / API Key / 模型，以平台托管供应商（名为 AIProviderName）经
// goder-serve 宿主管理面声明（PUT /goder-api/admin/users/{uid}/providers，
// 见 admin.go），写入该用户 grf-home 的 settings.json 并按决策链设默认
// 模型。managed:true 只读展示由 goder-serve 网关在其代理层注入（声明过
// 受管名即生效），openos 不再改写响应。
//
// v0.0.42 起另有「宿主声明的每用户供应商」（store.AIUserProvider）：业务
// 宿主经 PUT /api/ai/user-providers 声明（如 grf-os 把 token-hub 套餐派生成
// per-user 网关接入），渲染时与托管条目一并声明；声明条目 default 优先于
// 平台托管默认；names 全集内但本次未声明的名字会被清除（宿主是唯一写者，
// 声明外名字不碰）。
//
// 写盘的保真/清理/默认决策链/内容不变不写/空白起播语义全部由 goder-serve
// 的声明端点承载（语义用例见 goder pkg/ghserver/hostsettings_test.go，
// 自本仓 upsertAIProviders 时代逐条移植）；openos 侧只负责合成声明与
// 幂等缓存（服务端 settings.json 指纹 + 声明摘要一致即免重放）。
package goder

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"time"

	"openos/server/internal/store"
)

// AIProviderName 平台托管供应商名（goder 侧只读展示，工具栏模型选择可直接用）。
const AIProviderName = "OpenOS"

// ProviderConfig 一次待同步的模型配置；三个字段任一为空视为清除托管条目。
type ProviderConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

func (c ProviderConfig) valid() bool {
	return c.BaseURL != "" && c.APIKey != "" && c.Model != ""
}

// uidSan 用户名 → 键名清洗（[^a-zA-Z0-9_.-]→_）。历史上是与 goder ghserver
// 目录布局的锁步约束（错位即数据目录漂移）；管理面上移后 goder-serve 自持
// 目录知识，本规则只剩两个内部用途：声明表键与代理密钥派生的口径对齐
//（两者必须同键，否则全量同步每次都改写触发指纹抖动）。
var uidSan = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// SanUID 用户名 → 声明表/代理密钥的统一键（清洗规则见 uidSan 注释）。
func SanUID(uid string) string { return uidSan.ReplaceAllString(uid, "_") }

// buildAdminDecl 合成一次声明：平台托管条目 + 宿主按用户声明条目；受管名
// 全集 = 托管名 + 声明名全集（清理残留用）；默认模型决策链与旧
// upsertAIProviders 一致（声明条目 IsDefault 优先，否则平台托管默认）。
func buildAdminDecl(cfg ProviderConfig, extras []store.AIUserProvider, managedNames []string, forceDefault bool) AdminProvidersDecl {
	decl := AdminProvidersDecl{ForceDefault: forceDefault}
	if cfg.valid() {
		decl.Entries = append(decl.Entries, AdminProviderEntry{
			Name: AIProviderName, BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Models: []string{cfg.Model},
		})
	}
	for _, e := range extras {
		if e.Name == "" || e.Name == AIProviderName || e.UID == "" {
			continue
		}
		if e.BaseURL == "" || e.APIKey == "" || len(e.Models) == 0 {
			continue
		}
		decl.Entries = append(decl.Entries, AdminProviderEntry{
			Name: e.Name, BaseURL: e.BaseURL, APIKey: e.APIKey, Models: e.Models,
		})
		if e.IsDefault && decl.Default == "" {
			decl.Default = e.Name + "/" + e.Models[0]
		}
	}
	if decl.Default == "" && cfg.valid() {
		decl.Default = AIProviderName + "/" + cfg.Model
	}
	// 受管名全集（去重有序：摘要稳定性）
	set := map[string]bool{AIProviderName: true}
	for _, n := range managedNames {
		if n != "" {
			set[n] = true
		}
	}
	for n := range set {
		decl.Managed = append(decl.Managed, n)
	}
	sort.Strings(decl.Managed)
	return decl
}

// declDigest 声明摘要（幂等缓存键的一半）：取影响写盘结果的全部面。
func declDigest(decl AdminProvidersDecl) string {
	return fmt.Sprintf("%d/%v/%s/%v/%v", len(decl.Entries), decl.Entries, decl.Default, decl.Managed, decl.ForceDefault)
}

// SetAIProviderSource 注入当前 AI 配置的读取回调（声明合成用；由 main 在
// ai/goder 两个管理器都就绪后接线）。
func (m *Manager) SetAIProviderSource(src func() (ProviderConfig, bool)) {
	m.aiMu.Lock()
	m.aiSource = src
	m.aiMu.Unlock()
}

// AISource 当前 AI 配置（未注入回调或未配置时 ok=false）。
func (m *Manager) AISource() (ProviderConfig, bool) {
	m.aiMu.Lock()
	src := m.aiSource
	m.aiMu.Unlock()
	if src == nil {
		return ProviderConfig{}, false
	}
	return src()
}

// SetAIProxy 注入 AI 代理端点与每用户代理密钥构造器（main 在启动时调用）。
// 非空时托管供应商条目写「base=代理地址、api_key=该用户代理密钥」——
// 平台 API Key 只留服务端（安全审计 2026-10：此前明文 Key 写进每个 goder
// 用户的 settings.json，用户家目录人人可读自己的 Key）。
func (m *Manager) SetAIProxy(base string, keyFor func(uid string) string) {
	m.aiMu.Lock()
	m.aiProxyBase, m.aiProxyKey = base, keyFor
	m.aiMu.Unlock()
}

// SetProviderDB 注入宿主声明供应商的存储（main 启动时调用；未注入则只声明
// 平台托管条目）。
func (m *Manager) SetProviderDB(db *store.DB) {
	m.aiMu.Lock()
	m.providerDB = db
	m.managedNames = nil // 重建缓存
	m.aiEnsured = map[string]string{}
	m.aiMu.Unlock()
}

// RefreshManagedNames 立即失效声明名缓存（宿主 Replace 声明后由钩子调用，
// 让本轮同步立即看到新增/移除的名字）。
func (m *Manager) RefreshManagedNames() {
	m.aiMu.Lock()
	m.managedNames = nil
	m.aiMu.Unlock()
}

// managedNamesSnapshot 声明名全集（缓存，Replace 声明后经 RefreshManagedNames
// 失效重建；声明合成用）。
func (m *Manager) managedNamesSnapshot() []string {
	m.aiMu.Lock()
	defer m.aiMu.Unlock()
	if m.managedNames != nil || m.providerDB == nil {
		return m.managedNames
	}
	names, err := m.providerDB.AIUserProviderNames()
	if err != nil {
		names = nil
	}
	m.managedNames = names
	return names
}

// managedProvider 把平台配置转换为写进声明的托管条目：代理注入时指向
// /api/ai/v1/* 并换每用户密钥；未注入（兼容形态）或平台未配置（三要素
// 不全——启动自愈/声明渲染会带 invalid cfg 进来）时保持原样，让声明走
// 清除分支，绝不凭代理地址把「未配置」写成有效条目。密钥派生统一用清洗
// 后的 uid（SyncAIProvider 只能拿到目录名口径，两路派生必须一致）。
func (m *Manager) managedProvider(cfg ProviderConfig, uid string) ProviderConfig {
	m.aiMu.Lock()
	base, kf := m.aiProxyBase, m.aiProxyKey
	m.aiMu.Unlock()
	if base == "" || kf == nil || !cfg.valid() {
		return cfg
	}
	return ProviderConfig{BaseURL: base, APIKey: kf(SanUID(uid)), Model: cfg.Model}
}

// userProviderView 某用户的渲染视图：声明条目 + 名字全集（未接库为零值）。
func (m *Manager) userProviderView(uid string) ([]store.AIUserProvider, []string) {
	m.aiMu.Lock()
	db := m.providerDB
	m.aiMu.Unlock()
	if db == nil {
		return nil, nil
	}
	extras, _ := db.AIUserProvidersByUID(uid)
	return extras, m.managedNamesSnapshot()
}

// SyncAIProvider 把托管供应商与宿主声明条目同步到所有已存在的 goder 用户
// （管理员保存配置后、宿主声明后与启动时调用）。forceDefault 语义见
// buildAdminDecl。返回写盘用户数（服务端 applied 聚合）。
func (m *Manager) SyncAIProvider(cfg ProviderConfig, forceDefault bool) int {
	if m.disabled {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if !m.ensureReady(5 * time.Second) {
		log.Printf("goder: 全量同步跳过（goder-serve 未就绪，靠请求路径 EnsureAIUser 兜底）")
		return 0
	}
	users, err := m.adminListUsers(ctx)
	if err != nil {
		log.Printf("goder: 同步 AI 供应商失败（列用户）: %v", err)
		return 0
	}
	n := 0
	for _, u := range users {
		// 目录名即清洗后的 uid，与声明表（原 uid）与代理密钥派生对齐
		extras, names := m.userProviderView(u.San)
		decl := buildAdminDecl(m.managedProvider(cfg, u.San), extras, names, forceDefault)
		applied, err := m.adminPutProviders(ctx, u.San, decl)
		if err != nil {
			log.Printf("goder: 同步 AI 供应商失败(%s): %v", u.San, err)
			continue
		}
		if applied {
			n++
		}
	}
	m.aiMu.Lock()
	m.aiSyncedUsers = n
	m.aiMu.Unlock()
	return n
}

// EnsureAIUser 幂等确保某用户的托管供应商与声明条目就位（/goder-api 请求
// 路径调用，先于该用户 gh 的懒启动——中间件先于反代执行，声明完成时 gh
// 尚未首启，首启即读到正确配置）。热路径幂等：服务端 settings.json 指纹
// 与声明摘要同上次成功写入一致即免重放。保用户自选的默认模型
//（forceDefault=false）。
func (m *Manager) EnsureAIUser(uid string) {
	if m.disabled || uid == "" {
		return
	}
	san := SanUID(uid)
	cfg, ok := m.AISource()
	extras, names := m.userProviderView(san)
	if !ok && len(extras) == 0 && len(names) == 0 {
		return
	}
	if !m.ensureReady(5 * time.Second) {
		return // 未部署/未就绪：由反代层的 503/启动流兜底，下个请求重试
	}
	decl := buildAdminDecl(m.managedProvider(cfg, uid), extras, names, false)
	want := declDigest(decl)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if mark, err := m.adminGetProvidersMark(ctx, san); err == nil && mark.Mark != "" {
		m.aiMu.Lock()
		prev, seen := m.aiEnsured[uid]
		m.aiMu.Unlock()
		if seen && prev == mark.Mark+"|"+want {
			return // 文件与声明都未变化
		}
	}
	if _, err := m.adminPutProviders(ctx, san, decl); err != nil {
		log.Printf("goder: 确保 AI 供应商失败(%s): %v", uid, err)
		return
	}
	if mark, err := m.adminGetProvidersMark(ctx, san); err == nil {
		m.aiMu.Lock()
		m.aiEnsured[uid] = mark.Mark + "|" + want
		m.aiMu.Unlock()
	}
}

// LastSyncUsers 最近一次全量同步写盘的用户数（状态展示用）。
func (m *Manager) LastSyncUsers() int {
	m.aiMu.Lock()
	defer m.aiMu.Unlock()
	return m.aiSyncedUsers
}
