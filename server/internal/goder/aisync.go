// aisync.go — AI 模型配置与 goder 的联动（设置 → AI 服务 的落点）。
//
// OpenOS 唯一的 AI 应用是 goder：管理员在「系统设置 → AI 服务」保存的
// Base URL / API Key / 模型，会以平台托管供应商（名为 AIProviderName）写进
// 每个 goder 用户 grf-home 的 settings.json（gh 的模型配置文件，格式见
// goder frontend agent-dto.ts / 宿主 ~/.grf/settings.json），并设为默认模型。
// goder 设置页经反代注入的 managed 标记将该条目只读展示（嵌入约定，见
// goder docs/embedding.md），用户自建的其他供应商不受影响。
//
// v0.0.42 起另有「宿主声明的每用户供应商」（store.AIUserProvider）：业务宿主
// 经 PUT /api/ai/user-providers 声明（如 grf-os 把 token-hub 套餐派生成
// per-user 网关接入），渲染时与托管条目一并写入该用户的 settings.json。
// 声明条目 default 优先于平台托管默认；names 全集内但本次未声明的名字会被
// 从 settings.json 清除（宿主是唯一写者，声明外名字不碰）。
//
// 写盘规则与 osmcp 的插件 upsert 同一套路：map[string]json.RawMessage 保留
// 未知键、内容不变不写、0600。settings.json 尚不存在（用户还没打开过 goder）
// 时以空白配置起播并建目录——必须赶在 goder-serve 播种与 gh 首次启动之前，
// 否则 gh 启动读走无条目配置后不再重读盘（gh 仅启动时读，空闲 30m 才回收）。
package goder

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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

// GrfHomePath 某用户在 goder-serve PerUserHome 布局下的 GRF_HOME。
// 目录名清洗规则须与 goder ghserver 的 uidSan 严格同步（[^a-zA-Z0-9_.-]→_）。
func GrfHomePath(dataDir, uid string) string {
	san := uidSan.ReplaceAllString(uid, "_")
	return filepath.Join(dataDir, "goder", "serve-data", "users", san, "grf-home")
}

// uidSan 用户名 → 目录名清洗规则，与 goder ghserver 的 manager.go 严格一致
// （osmcp 包亦经 GrfHomePath 复用此处，三处不再各自维护副本）。
var uidSan = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// SanUID 用户名 → goder 用户目录名（清洗规则见 uidSan）。宿主声明的每用户
// 供应商以清洗后的 uid 入库，与 serve-data/users/<目录名> 及代理密钥派生对齐。
func SanUID(uid string) string { return uidSan.ReplaceAllString(uid, "_") }

// aiMark settings.json 的 (mtime, size) 指纹，EnsureAIUser 幂等跳过用。
type aiMark struct{ mtime, size int64 }

// upsertAIProviders 把托管供应商与宿主声明的每用户供应商写入（或清出）一个
// settings.json。managedNames 为宿主历史声明过的名字全集（清理残留用）。
// 默认模型决策链：声明条目 IsDefault 优先，否则平台托管默认；写入时机 =
// forceDefault（admin 保存/宿主声明后）或现默认为空或已指向受管条目
// （OpenOS 或声明名）——不打架用户在 goder 里自选的默认模型。返回是否写盘。
func upsertAIProviders(settingsPath string, cfg ProviderConfig, extras []store.AIUserProvider, managedNames []string, forceDefault bool) (bool, error) {
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		// 无文件 = 该用户尚未打开过 goder：以空白配置起播而非跳过——否则首请求
		// 时 goder-serve 先播种空 settings.json、gh 随即启动读走无托管/声明条目的
		// 配置（gh 只在启动时读盘），第二轮 Ensure 补写也追不上。先写则 goder-serve
		// 的 GODER_SETTINGS_SEED（仅文件不存在时复制）跳过，gh 首启即带条目
		raw = nil
	}
	var all map[string]json.RawMessage
	if raw != nil {
		if err := json.Unmarshal(raw, &all); err != nil {
			return false, nil // 损坏文件不碰（与 osmcp upsertSettings 同策略）
		}
	}
	if all == nil {
		all = map[string]json.RawMessage{}
	}

	provs := all["providers"]
	var providers map[string]json.RawMessage
	if len(provs) > 0 && string(provs) != "null" {
		if err := json.Unmarshal(provs, &providers); err != nil {
			return false, err
		}
	}
	if providers == nil {
		providers = map[string]json.RawMessage{}
	}

	curDefault := ""
	if d, ok := all["default"]; ok {
		_ = json.Unmarshal(d, &curDefault)
	}

	// 受管名字集合 = 平台托管名 + 宿主声明名全集
	managedSet := map[string]bool{AIProviderName: true}
	for _, n := range managedNames {
		if n != "" && n != AIProviderName {
			managedSet[n] = true
		}
	}

	// 平台托管条目
	if cfg.valid() {
		entry, err := json.Marshal(map[string]any{
			"base_url": cfg.BaseURL,
			"api_key":  cfg.APIKey,
			"models":   []string{cfg.Model},
		})
		if err != nil {
			return false, err
		}
		providers[AIProviderName] = entry
	} else {
		delete(providers, AIProviderName)
	}

	// 宿主声明的每用户条目（要素不全的跳过）
	declared := map[string]bool{}
	for _, e := range extras {
		if e.Name == "" || e.Name == AIProviderName || e.UID == "" {
			continue
		}
		if e.BaseURL == "" || e.APIKey == "" || len(e.Models) == 0 {
			continue
		}
		models := e.Models
		entry, err := json.Marshal(map[string]any{
			"base_url": e.BaseURL,
			"api_key":  e.APIKey,
			"models":   models,
		})
		if err != nil {
			return false, err
		}
		providers[e.Name] = entry
		declared[e.Name] = true
	}
	// names 全集内但本次未声明的 → 清理残留（声明外名字不碰）
	for name := range providers {
		if name != AIProviderName && managedSet[name] && !declared[name] {
			delete(providers, name)
		}
	}

	// 默认模型决策链
	wantDefault := ""
	for _, e := range extras {
		if e.IsDefault && len(e.Models) > 0 {
			wantDefault = e.Name + "/" + e.Models[0]
			break
		}
	}
	if wantDefault == "" && cfg.valid() {
		wantDefault = AIProviderName + "/" + cfg.Model
	}
	curManaged := managedSet[strings.SplitN(curDefault, "/", 2)[0]]
	if wantDefault != "" {
		if forceDefault || curDefault == "" || curManaged {
			all["default"] = mustMarshalString(wantDefault)
		}
	} else if curManaged {
		// 无候选默认：指向受管条目的 default 随条目清除（gh 回落内置默认）
		delete(all, "default")
	}

	if len(providers) == 0 {
		delete(all, "providers") // 本就无供应商的文件不引入空键
	} else {
		all["providers"] = mustMarshalRaw(providers)
	}
	// 无原文件且渲染后既无供应商也无默认模型：不值得起播（避免为空白配置建文件）
	if raw == nil && len(providers) == 0 {
		if d, ok := all["default"]; !ok || string(d) == `""` {
			return false, nil
		}
	}
	out, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return false, err
	}
	out = append(out, '\n')
	if raw != nil && string(out) == string(raw) {
		return false, nil // 内容未变不写（不惊动 mtime，osmcp 的指纹也不失效）
	}
	if raw == nil { // 起播：用户目录可能尚未建立（从未打开过 goder）
		if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
			return false, err
		}
	}
	return true, os.WriteFile(settingsPath, out, 0o600)
}

func mustMarshalString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func mustMarshalRaw(m map[string]json.RawMessage) json.RawMessage {
	b, _ := json.Marshal(m)
	return b
}

// SetAIProviderSource 注入当前 AI 配置的读取回调（托管标记注入与逐用户
// 确保用；由 main 在 ai/goder 两个管理器都就绪后接线）。
func (m *Manager) SetAIProviderSource(src func() (ProviderConfig, bool)) {
	m.mu.Lock()
	m.aiSource = src
	m.mu.Unlock()
}

// AISource 当前 AI 配置（未注入回调或未配置时 ok=false）。
func (m *Manager) AISource() (ProviderConfig, bool) {
	m.mu.Lock()
	src := m.aiSource
	m.mu.Unlock()
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
	m.mu.Lock()
	m.aiProxyBase, m.aiProxyKey = base, keyFor
	m.mu.Unlock()
}

// SetProviderDB 注入宿主声明供应商的存储（main 启动时调用；未注入则只渲染
// 平台托管条目）。
func (m *Manager) SetProviderDB(db *store.DB) {
	m.aiMu.Lock()
	m.providerDB = db
	m.managedNames = nil // 重建缓存
	m.aiMu.Unlock()
}

// RefreshManagedNames 立即失效声明名缓存（宿主 Replace 声明后由钩子调用，
// 让本轮同步立即看到新增/移除的名字）。
func (m *Manager) RefreshManagedNames() {
	m.aiMu.Lock()
	m.managedNames = nil
	m.aiMu.Unlock()
}

// managedNamesSnapshot 声明名全集（常驻缓存，Replace 声明后经 RefreshManagedNames
// 失效重建；markManagedProvider 高频路径用）。
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

// managedProvider 把平台配置转换为写进 settings.json 的托管条目：
// 代理注入时指向 /api/ai/v1/* 并换每用户密钥；未注入（兼容形态）或平台未配置
// （三要素不全——启动自愈/声明渲染会带 invalid cfg 进来）时保持原样，让 upsert
// 走清除分支，绝不凭代理地址把「未配置」写成有效条目。
// 密钥派生统一用清洗后的 uid（SyncAIProvider 只能拿到目录名，两路派生必须一致，
// 否则每次全量同步都会改写 settings.json 触发指纹抖动）。
func (m *Manager) managedProvider(cfg ProviderConfig, uid string) ProviderConfig {
	m.mu.Lock()
	base, kf := m.aiProxyBase, m.aiProxyKey
	m.mu.Unlock()
	if base == "" || kf == nil || !cfg.valid() {
		return cfg
	}
	san := uidSan.ReplaceAllString(uid, "_")
	return ProviderConfig{BaseURL: base, APIKey: kf(san), Model: cfg.Model}
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
// upsertAIProviders。返回写盘用户数。
func (m *Manager) SyncAIProvider(cfg ProviderConfig, forceDefault bool) int {
	if m.disabled {
		return 0
	}
	usersDir := filepath.Join(m.dataDir, "goder", "serve-data", "users")
	entries, err := os.ReadDir(usersDir)
	if err != nil {
		return 0 // 还没有任何 goder 用户
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		grfHome := filepath.Join(usersDir, e.Name(), "grf-home")
		if st, err := os.Stat(grfHome); err != nil || !st.IsDir() {
			continue
		}
		sp := filepath.Join(grfHome, "settings.json")
		// 目录名即清洗后的 uid，与声明表（原 uid）与代理密钥派生对齐
		extras, names := m.userProviderView(e.Name())
		wrote, err := upsertAIProviders(sp, m.managedProvider(cfg, e.Name()), extras, names, forceDefault)
		if err != nil {
			log.Printf("goder: 同步 AI 供应商失败(%s): %v", e.Name(), err)
			continue
		}
		if wrote {
			n++
		}
		m.rememberAIMark(e.Name(), sp)
	}
	m.mu.Lock()
	m.aiSyncedUsers = n
	m.mu.Unlock()
	return n
}

// EnsureAIUser 幂等确保某用户的托管供应商与声明条目就位（/goder-api 请求路径
// 调用，先于该用户 gh 的懒启动；settings.json 播种后首次请求即补齐）。
// 保用户自选的默认模型（forceDefault=false）。
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
	sp := filepath.Join(GrfHomePath(m.dataDir, uid), "settings.json")
	mt, sz := statAIMark(sp)
	m.aiMu.Lock()
	mark, seen := m.aiMarks[uid]
	m.aiMu.Unlock()
	if seen && mark == (aiMark{mt, sz}) {
		return // 文件未变化
	}
	if _, err := upsertAIProviders(sp, m.managedProvider(cfg, uid), extras, names, false); err != nil {
		log.Printf("goder: 确保 AI 供应商失败(%s): %v", uid, err)
		return
	}
	m.rememberAIMark(uid, sp)
}

func (m *Manager) rememberAIMark(uid, settingsPath string) {
	mt, sz := statAIMark(settingsPath)
	m.aiMu.Lock()
	m.aiMarks[uid] = aiMark{mt, sz}
	m.aiMu.Unlock()
}

func statAIMark(p string) (int64, int64) {
	if st, err := os.Stat(p); err == nil {
		return st.ModTime().UnixNano(), st.Size()
	}
	return 0, 0
}

// LastSyncUsers 最近一次全量同步写盘的用户数（状态展示用）。
func (m *Manager) LastSyncUsers() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.aiSyncedUsers
}
