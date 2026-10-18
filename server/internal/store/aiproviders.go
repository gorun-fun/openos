// aiproviders.go — AI 配置：系统级单例行（系统设置 → AI 服务）与宿主声明的
// 每用户 AI 供应商（goder 联动，v0.0.42）。
//
// 业务宿主（grf-os / grf-hotel 等）经 PUT /api/ai/user-providers 以服务身份
// 声明式全量写入：每个用户可有一条或多条外部模型供应商（如 token-hub 套餐
// 派生的 per-user 网关接入），goder 渲染 settings.json 时与平台托管条目
// （OpenOS）一并写入。names 表登记历史用过的供应商名，供渲染时清理用户
// settings.json 里已不在声明中的残留条目（宿主是唯一写者，声明外名字不碰）。
package store

import "encoding/json"

// AIUserProvider 宿主声明的单条每用户供应商（写入用户 goder settings.json
// 的普通条目；Name 不得与平台托管名 OpenOS 相同）。
type AIUserProvider struct {
	UID       string
	Name      string
	BaseURL   string
	APIKey    string
	Models    []string
	IsDefault bool
}

// ReplaceAIUserProviders 声明式全量替换（单事务）：条目表全删全插。names 表
// 为历史声明出现过的名字全集（只增不删）——渲染清理 settings.json 残留靠它
// 识别「受管但本轮未声明」的名字（空声明/更换供应商后旧条目仍可被清除）。
func (db *DB) ReplaceAIUserProviders(items []AIUserProvider) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM ai_user_providers`); err != nil {
		return err
	}
	for _, it := range items {
		models := it.Models
		if models == nil {
			models = []string{}
		}
		raw, err := json.Marshal(models)
		if err != nil {
			return err
		}
		def := 0
		if it.IsDefault {
			def = 1
		}
		if _, err := tx.Exec(`INSERT INTO ai_user_providers(uid,name,base_url,api_key,models,is_default,updated_at)
			VALUES(?,?,?,?,?,?,datetime('now'))`, it.UID, it.Name, it.BaseURL, it.APIKey, string(raw), def); err != nil {
			return err
		}
		if it.Name != "" {
			if _, err := tx.Exec(`INSERT INTO ai_user_provider_names(name) VALUES(?) ON CONFLICT(name) DO NOTHING`, it.Name); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// AIUserProvidersByUID 某用户声明的全部供应商。
func (db *DB) AIUserProvidersByUID(uid string) ([]AIUserProvider, error) {
	rows, err := db.Query(`SELECT uid,name,base_url,api_key,models,is_default FROM ai_user_providers WHERE uid=? ORDER BY name`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AIUserProvider{}
	for rows.Next() {
		var it AIUserProvider
		var raw string
		var def int
		if err := rows.Scan(&it.UID, &it.Name, &it.BaseURL, &it.APIKey, &raw, &def); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(raw), &it.Models)
		it.IsDefault = def == 1
		out = append(out, it)
	}
	return out, rows.Err()
}

// AIUserProviderNames 历史声明出现过的供应商名全集（渲染清理残留用）。
func (db *DB) AIUserProviderNames() ([]string, error) {
	rows, err := db.Query(`SELECT name FROM ai_user_provider_names ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// CountAIUserProviders 声明的供应商条目总数（状态展示用）。
func (db *DB) CountAIUserProviders() int {
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ai_user_providers`).Scan(&n)
	return n
}

// ---- 系统级 AI 配置（单例行，系统设置 → AI 服务） ----

// AIConfig AI 服务配置（单例行）。
type AIConfig struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}

// GetAIConfig 读取 AI 配置；空配置返回 ok=false。
func (db *DB) GetAIConfig() (AIConfig, bool, error) {
	var c AIConfig
	var has int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_config WHERE id=1`).Scan(&has); err != nil {
		return c, false, err
	}
	if has == 0 {
		return c, false, nil
	}
	err := db.QueryRow(`SELECT base_url,api_key,model FROM ai_config WHERE id=1`).Scan(&c.BaseURL, &c.APIKey, &c.Model)
	return c, err == nil, err
}

// SetAIConfig 保存 AI 配置。
func (db *DB) SetAIConfig(c AIConfig) error {
	_, err := db.Exec(`INSERT INTO ai_config(id,base_url,api_key,model,updated_at) VALUES(1,?,?,?,datetime('now'))
		ON CONFLICT(id) DO UPDATE SET base_url=excluded.base_url, api_key=excluded.api_key, model=excluded.model, updated_at=datetime('now')`,
		c.BaseURL, c.APIKey, c.Model)
	return err
}
