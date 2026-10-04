// userproviders.go — 宿主声明的每用户 AI 供应商（v0.0.42）。
//
// 业务宿主（grf-os / grf-hotel 等）以服务身份（交换密钥 + admin）调
// PUT /api/ai/user-providers 做声明式全量同步：每用户一条或多条外部模型
// 供应商（典型来源 token-hub 套餐：base_url=网关 /v1、api_key=用户令牌、
// models=套餐白名单实际可用的模型清单），openos 负责存储并渲染进该用户
// goder 的 settings.json（见 goder/aisync.go）。宿主是唯一写者：清单外
// 条目清除、names 全集外用户自建供应商不碰。
package ai

import (
	"encoding/json"
	"net/http"
	"strings"

	"openos/server/internal/auth"
	"openos/server/internal/goder"
	"openos/server/internal/store"
)

// UserProviderInput 宿主声明的单条供应商（uid 以 goder 用户目录名规范入库，
// 见 goder.SanUID）。
type UserProviderInput struct {
	UID     string   `json:"uid"`
	Name    string   `json:"name"`
	BaseURL string   `json:"baseUrl"`
	APIKey  string   `json:"apiKey"`
	Models  []string `json:"models"`
	Default bool     `json:"default"`
}

// SaveUserProviders PUT /api/ai/user-providers {providers:[...]}（admin，宿主
// 服务调用）：声明式全量替换，保存后经 AfterUserProvidersSave 联动 goder 渲染。
func (s *Service) SaveUserProviders(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Providers []UserProviderInput `json:"providers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	if len(req.Providers) > 1000 {
		http.Error(w, "声明条目过多（上限 1000）", 400)
		return
	}
	items := make([]store.AIUserProvider, 0, len(req.Providers))
	names := map[string]bool{}
	perUserDefault := map[string]bool{}
	for _, p := range req.Providers {
		p.UID = goder.SanUID(strings.TrimSpace(p.UID))
		p.Name = strings.TrimSpace(p.Name)
		p.BaseURL = strings.TrimSuffix(strings.TrimSpace(p.BaseURL), "/")
		p.APIKey = strings.TrimSpace(p.APIKey)
		if p.UID == "" || p.Name == "" || p.BaseURL == "" || p.APIKey == "" || len(p.Models) == 0 {
			http.Error(w, "每条声明需含 uid/name/baseUrl/apiKey/models", 400)
			return
		}
		if p.Name == goder.AIProviderName {
			http.Error(w, "供应商名不得为平台托管名 "+goder.AIProviderName, 400)
			return
		}
		if len(p.Models) > 100 {
			http.Error(w, "单条供应商模型数过多（上限 100）", 400)
			return
		}
		if p.Default {
			if perUserDefault[p.UID] {
				http.Error(w, "每个 uid 至多一条 default", 400)
				return
			}
			perUserDefault[p.UID] = true
		}
		models := make([]string, 0, len(p.Models))
		for _, m := range p.Models {
			if m = strings.TrimSpace(m); m != "" {
				models = append(models, m)
			}
		}
		if len(models) == 0 {
			http.Error(w, "模型清单不能为空", 400)
			return
		}
		names[p.Name] = true
		items = append(items, store.AIUserProvider{
			UID: p.UID, Name: p.Name, BaseURL: p.BaseURL, APIKey: p.APIKey,
			Models: models, IsDefault: p.Default,
		})
	}
	if err := s.db.ReplaceAIUserProviders(items); err != nil {
		http.Error(w, "保存失败", 500)
		return
	}
	s.db.Audit(auth.FromContext(r).Username, "ai.user-providers",
		strings.Join(mapKeys(names), ","), "")
	writeJSON(w, map[string]any{"ok": "1", "count": len(items)})
	// 联动钩子：刷新声明名缓存并全量重渲染 goder 用户（main 注入）
	if s.AfterUserProvidersSave != nil {
		s.AfterUserProvidersSave()
	}
}

// GetUserProviders GET /api/ai/user-providers（admin，密钥脱敏；对账/调试用）。
func (s *Service) GetUserProviders(w http.ResponseWriter, r *http.Request) {
	uid := goder.SanUID(strings.TrimSpace(r.URL.Query().Get("uid")))
	items, err := s.db.AIUserProvidersByUID(uid)
	if err != nil {
		http.Error(w, "读取失败", 500)
		return
	}
	out := make([]UserProviderInput, 0, len(items))
	for _, it := range items {
		key := it.APIKey
		if n := len(key); n > 6 {
			key = key[:6] + "****"
		}
		out = append(out, UserProviderInput{
			UID: it.UID, Name: it.Name, BaseURL: it.BaseURL, APIKey: key,
			Models: it.Models, Default: it.IsDefault,
		})
	}
	writeJSON(w, map[string]any{"items": out})
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
