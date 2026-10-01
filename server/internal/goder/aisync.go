// aisync.go — AI 模型配置与 goder 的联动（设置 → AI 服务 的落点）。
//
// OpenOS 唯一的 AI 应用是 goder：管理员在「系统设置 → AI 服务」保存的
// Base URL / API Key / 模型，会以平台托管供应商（名为 AIProviderName）写进
// 每个 goder 用户 grf-home 的 settings.json（gh 的模型配置文件，格式见
// goder frontend agent-dto.ts / 宿主 ~/.grf/settings.json），并设为默认模型。
// goder 设置页经反代注入的 managed 标记将该条目只读展示（嵌入约定，见
// goder docs/embedding.md），用户自建的其他供应商不受影响。
//
// 写盘规则与 osmcp 的插件 upsert 同一套路：map[string]json.RawMessage 保留
// 未知键、内容不变不写、0600。settings.json 尚不存在（goder-serve 还没为该
// 用户播种）时跳过，待 EnsureAIUser 在后续 /goder-api 请求补齐。
package goder

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// aiMark settings.json 的 (mtime, size) 指纹，EnsureAIUser 幂等跳过用。
type aiMark struct{ mtime, size int64 }

// upsertAIProvider 把托管供应商写入（或清出）一个 settings.json。
// forceDefault=true 强制设默认模型；false 仅在现默认为空或已指向托管条目时
// 设置（不打架用户在 goder 里自选的默认模型）。返回是否写盘。
func upsertAIProvider(settingsPath string, cfg ProviderConfig, forceDefault bool) (bool, error) {
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return false, nil // 无文件 = 尚未播种，跳过
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return false, nil // 损坏文件不碰（与 osmcp upsertSettings 同策略）
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
		def := AIProviderName + "/" + cfg.Model
		if forceDefault || curDefault == "" || strings.HasPrefix(curDefault, AIProviderName+"/") {
			all["default"] = mustMarshalString(def)
		}
	} else {
		// 清除托管条目：默认模型随之指向它时删除 default 键（gh 回落内置默认）
		delete(providers, AIProviderName)
		if strings.HasPrefix(curDefault, AIProviderName+"/") {
			delete(all, "default")
		}
	}

	if len(providers) == 0 {
		delete(all, "providers") // 本就无供应商的文件不引入空键
	} else {
		all["providers"] = mustMarshalRaw(providers)
	}
	out, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return false, err
	}
	out = append(out, '\n')
	if string(out) == string(raw) {
		return false, nil // 内容未变不写（不惊动 mtime，osmcp 的指纹也不失效）
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

// SyncAIProvider 把托管供应商同步到所有已存在的 goder 用户（管理员保存
// 配置后与启动时调用）。forceDefault 语义见 upsertAIProvider。返回写盘用户数。
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
		wrote, err := upsertAIProvider(sp, cfg, forceDefault)
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

// EnsureAIUser 幂等确保某用户的托管供应商就位（/goder-api 请求路径调用，
// 先于该用户 gh 的懒启动；settings.json 播种后首次请求即补齐）。
// 保用户自选的默认模型（forceDefault=false）。
func (m *Manager) EnsureAIUser(uid string) {
	cfg, ok := m.AISource()
	if m.disabled || uid == "" || !ok {
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
	if _, err := upsertAIProvider(sp, cfg, false); err != nil {
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
