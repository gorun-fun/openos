// aisync.go — token-hub 套餐 → goder 每用户模型供应商同步（AISyncer）。
//
// goder（AI 助手）的模型供应商不依赖管理员在 openos「设置 → AI」手配一份全局
// Key：宿主周期性把 token-hub 的「用户 → 套餐」派生成 per-user 供应商声明给
// openos（PUT /api/host/ai/user-providers，openos ≥ v0.0.46），openos 渲染进
// 各用户 goder 的 settings.json（条目在 goder 设置页只读展示）：
//
//	base_url = token-hub /v1（OpenAI 兼容端点）
//	api_key  = 该用户名下一把 goder 专用令牌（sk-th-*；限额与模型白名单实时
//	           取自其套餐，换绑/停用即时生效——token-hub 侧完成计量）
//	models   = 该令牌 GET /v1/models 的实际可用清单（套餐白名单过滤后的统一名）
//
// 声明为全量语义：token-hub 侧解绑套餐/停用的用户，其条目随下一轮清除；单用户
// 令牌或模型清单拉取失败仅跳过该用户（不动其余），整轮 users 读取失败则放弃
// 本轮（绝不把空清单写进 openos 清光既有声明）。uid 同源：token-hub 用户 uid
// 与桌面用户同为 Casdoor uid，openos 入库时自行按 goder 目录名规范清洗。
// （自 grf-hotel/grf-os 同构实现提炼上移。）
package hostsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// goderKeyName 同步器为无令牌用户在 token-hub 建立的专用 Key 名（复用优先）。
const goderKeyName = "goder"

// Caller openos 管理 API 调用面（*Manager 天然实现；测试可注入假件）。
type Caller interface {
	Call(method, path string, body, out any) error
}

// AISyncConfig 套餐同步配置。
type AISyncConfig struct {
	THBase string // token-hub 基地址（如 https://th.example.com）
	THKey  string // token-hub 主 API Key（空则同步不启动，Run 报错跳过）
	// ProviderName 声明供应商名（默认 TokenHub；goder 设置页展示用）。
	ProviderName string
	// SetDefault 声明条目设为用户默认模型。
	SetDefault bool
	// Interval 同步间隔（默认 10 分钟）。
	Interval time.Duration
}

// AISyncer token-hub 套餐同步器（周期后台任务，失败仅记日志不影响宿主）。
type AISyncer struct {
	thBase     string
	thKey      string
	os         Caller
	name       string        // 声明供应商名
	setDefault bool          // 声明条目设为用户默认模型
	interval   time.Duration // 同步间隔
	client     *http.Client
	logf       func(format string, args ...any)
}

// NewAISyncer 从配置构造（os 通常传 *Manager）。
func NewAISyncer(cfg AISyncConfig, os Caller) *AISyncer {
	s := &AISyncer{
		thBase:     strings.TrimRight(cfg.THBase, "/"),
		thKey:      cfg.THKey,
		os:         os,
		name:       cfg.ProviderName,
		setDefault: cfg.SetDefault,
		interval:   cfg.Interval,
		client:     &http.Client{Timeout: 30 * time.Second},
		logf:       log.Printf,
	}
	if s.name == "" {
		s.name = "TokenHub"
	}
	if s.interval <= 0 {
		s.interval = 10 * time.Minute
	}
	return s
}

// Run 阻塞循环：启动即同步一次，此后按间隔（宿主 main 以 goroutine 承载）。
func (s *AISyncer) Run(ctx context.Context) {
	s.syncOnceLog(ctx)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.syncOnceLog(ctx)
		}
	}
}

func (s *AISyncer) syncOnceLog(ctx context.Context) {
	n, err := s.SyncOnce(ctx)
	if err != nil {
		s.logf("[ai-sync] token-hub 套餐同步失败（下轮重试）: %v", err)
		return
	}
	s.logf("[ai-sync] token-hub 套餐已声明 %d 个 goder 供应商", n)
}

// ---- token-hub 数据形态（管理 API 子集）----

type aiSyncUser struct {
	UID         string      `json:"uid"`
	PlanID      string      `json:"plan_id"`
	PlanEnabled bool        `json:"plan_enabled"`
	Enabled     bool        `json:"enabled"`
	Keys        []aiSyncKey `json:"keys"`
}

type aiSyncKey struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

// providerDecl 声明给 openos 的单条供应商（openos /api/host/ai/user-providers 请求体）。
type providerDecl struct {
	UID     string   `json:"uid"`
	Name    string   `json:"name"`
	BaseURL string   `json:"baseUrl"`
	APIKey  string   `json:"apiKey"`
	Models  []string `json:"models"`
	Default bool     `json:"default"`
}

// SyncOnce 执行一轮同步，返回本轮声明条数。
func (s *AISyncer) SyncOnce(ctx context.Context) (int, error) {
	if s.thKey == "" {
		return 0, errors.New("未配置 token-hub 主 API Key（AISyncConfig.THKey）")
	}
	var out struct {
		Items []aiSyncUser `json:"items"`
	}
	if err := s.thCall(ctx, http.MethodGet, "/api/admin/users", nil, &out); err != nil {
		return 0, fmt.Errorf("读取 token-hub 用户列表: %w", err)
	}
	decls := make([]providerDecl, 0, len(out.Items))
	for _, u := range out.Items {
		// 只同步启用中且已绑定启用套餐的用户（解绑/停用随全量声明清除）
		if !u.Enabled || u.PlanID == "" || !u.PlanEnabled {
			continue
		}
		key, err := s.ensureKey(ctx, u)
		if err != nil {
			s.logf("[ai-sync] 用户 %s 令牌准备失败，跳过: %v", u.UID, err)
			continue
		}
		models, err := s.listModels(ctx, key)
		if err != nil || len(models) == 0 {
			s.logf("[ai-sync] 用户 %s 模型清单拉取失败（key %s…），跳过: %v", u.UID, keyPrefix(key), err)
			continue
		}
		decls = append(decls, providerDecl{
			UID: u.UID, Name: s.name,
			BaseURL: s.thBase + "/v1", APIKey: key, Models: models,
			Default: s.setDefault,
		})
	}
	if err := s.os.Call(http.MethodPut, "/api/host/ai/user-providers",
		map[string]any{"providers": decls}, nil); err != nil {
		return 0, fmt.Errorf("声明 openos 每用户供应商: %w", err)
	}
	return len(decls), nil
}

// ensureKey 取该用户用于 goder 的令牌：现成启用令牌优先（goder 专用名 > 任一），
// 没有则以专用名创建一把（归属用户，限额/白名单实时取自套餐）。
func (s *AISyncer) ensureKey(ctx context.Context, u aiSyncUser) (string, error) {
	var fallback string
	for _, k := range u.Keys {
		if !k.Enabled || k.Key == "" {
			continue
		}
		if k.Name == goderKeyName {
			return k.Key, nil
		}
		if fallback == "" {
			fallback = k.Key
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	var created aiSyncKey
	if err := s.thCall(ctx, http.MethodPost, "/api/admin/keys",
		map[string]any{"name": goderKeyName, "owner_uid": u.UID, "enabled": true, "window_hours": 5},
		&created); err != nil {
		return "", fmt.Errorf("创建 goder 专用令牌: %w", err)
	}
	if created.Key == "" {
		return "", errors.New("创建令牌响应缺少 key")
	}
	return created.Key, nil
}

// listModels 拉该令牌实际可用的模型清单（token-hub /v1/models，套餐白名单过滤）。
func (s *AISyncer) listModels(ctx context.Context, key string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.thBase+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncateStr(string(raw), 120))
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	return models, nil
}

// thCall 调 token-hub 管理 API（主 API Key）。
func (s *AISyncer) thCall(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.thBase+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", s.thKey)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s → %d: %s", method, path, resp.StatusCode, truncateStr(string(raw), 160))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func keyPrefix(key string) string {
	if len(key) > 10 {
		return key[:10]
	}
	return key
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
