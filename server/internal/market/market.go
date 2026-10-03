// Package market 应用商店：目录清单与安装逻辑。
//
// 目录源多级：内置清单（catalog.json 随二进制发布，go:embed）为兜底基线；
// 远程清单并发拉取（每 10 分钟 + 保存即拉取）——自定义源（管理员经商店设置添加，
// 持久化 system_kv）排前、官方默认源（OPENOS_MARKET_URL，=off 关闭）垫底，
// 合并去重按 id **首见优先**：自定义源可遮蔽官方同 id 条目。任一源成功即以成功
// 源合并结果为生效目录；全部失败沿用现行目录。更新货架只改远端 JSON，无需发版。
//
// MVP 目录只收录两档零攻击面的应用形态：
//   - web   网页应用（config.kind=web）：iframe 直嵌允许被嵌的站点，渲染走 WebApp 容器
//   - proxy 代理网页（config.kind=proxy）：目标站经服务端反代（webproxy），安装即放行其目标主机
//   - schema 声明式应用（config 即 SchemaApp schema）：纯数据描述、无代码执行
// 安装 = UpsertApp（builtin=0/locked=0，version/source 标记来源）+ 按所选角色授权，
// 不引入任何远程代码加载。宿主可用 OPENOS_MARKET=off 整体下架商店。
package market

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"openos/server/internal/store"
)

//go:embed catalog.json
var catalogFS embed.FS

// SourceMarket 应用安装来源标记（apps.source 列）：商店安装的应用才允许商店升级比较版本。
const SourceMarket = "market"

// 自定义源上限与单条地址长度上限（管理员经商店设置维护）。
const (
	MaxCustomSources  = 10
	MaxSourceURLLen   = 500
)

// Entry 目录条目。
type Entry struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Category    string          `json:"category"`
	Icon        string          `json:"icon"`
	Type        string          `json:"type"`               // web | proxy | schema
	Config      json.RawMessage `json:"config"`             // web/proxy 为 {kind,url[,headers]}；schema 为 SchemaApp schema
	Version     string          `json:"version"`            // 应用版本（与本机安装版本比较出「可升级」）
	Changelog   string          `json:"changelog,omitempty"`
	Perms       []string        `json:"perms,omitempty"`    // 权限声明（安装确认页展示）
	Width       int             `json:"width,omitempty"`
	Height      int             `json:"height,omitempty"`
}

// State 目录条目 + 本机安装状态（GET /api/market/apps 的响应单元）。
type State struct {
	Entry
	Installed        bool   `json:"installed"`
	InstalledVersion string `json:"installedVersion,omitempty"`
	Upgradable       bool   `json:"upgradable"`             // 商店安装且目录版本更新
	Managed          bool   `json:"managed"`                // 本机由商店安装（source=market）
	Conflict         bool   `json:"conflict,omitempty"`     // 同 id 应用为管理员手工注册（重装会覆盖其配置）
	Protected        bool   `json:"protected,omitempty"`    // 同 id 应用为内置/宿主锁定（不可安装）
}

// 错误：路由层据此映射 404 / 403。
var (
	ErrNotFound = errors.New("market: 目录中不存在该应用")
	ErrProtected = errors.New("market: 应用 ID 与内置或宿主锁定应用冲突")
)

var (
	embeddedOnce sync.Once
	embeddedList []Entry

	mu           sync.RWMutex
	active       []Entry  // 当前生效目录：全部远程源合并结果，或内置兜底
	defaultURL   string   // 官方默认源（OPENOS_MARKET_URL；=off 时为空 = 只剩自定义源）
	customSrcs   []string // 管理员自定义源（有序，首见优先遮蔽默认源同 id 条目）

	refreshMu       sync.Mutex // 串行化刷新（周期循环与保存即拉取互斥）
	refreshInterval = 10 * time.Minute
	httpClient      = &http.Client{Timeout: 15 * time.Second}
)

// Catalog 返回当前生效目录：远程源合并结果（任一源成功即整体以成功源为准），
// 全部失败沿用现行（不回退内置——现行可能已是更早的成功结果）；初始 = 内置清单。
func Catalog() []Entry {
	embeddedOnce.Do(func() {
		raw, err := catalogFS.ReadFile("catalog.json")
		if err != nil {
			log.Printf("market: 读取内置目录失败: %v", err)
			return
		}
		embeddedList, err = parseCatalog(raw)
		if err != nil {
			log.Printf("market: 内置目录解析失败: %v", err)
			embeddedList = nil
		}
		mu.Lock()
		if active == nil {
			active = embeddedList
		}
		mu.Unlock()
	})
	mu.RLock()
	defer mu.RUnlock()
	return active
}

// parseCatalog 解析清单文档：顶层结构非法返回错误（调用方沿用现行目录）；
// 个别条目无效只跳过并记日志，不拖垮整份清单。内置与远程共用同一套校验。
func parseCatalog(raw []byte) ([]Entry, error) {
	var doc struct {
		Version int     `json:"version"`
		Apps    []Entry `json:"apps"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]Entry, 0, len(doc.Apps))
	for _, e := range doc.Apps {
		if err := e.validate(); err != nil {
			log.Printf("market: 目录条目 %q 无效已跳过: %v", e.ID, err)
			continue
		}
		if seen[e.ID] {
			log.Printf("market: 目录条目 %q 重复已跳过", e.ID)
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	return out, nil
}

// StartRemote 启动远程目录同步循环（market 启用时恒起；默认源为空则只等自定义源）：
// 启动即拉取一次，此后按固定间隔刷新。
func StartRemote(def string) {
	mu.Lock()
	defaultURL = def
	mu.Unlock()
	go func() {
		RefreshNow()
		t := time.NewTicker(refreshInterval)
		defer t.Stop()
		for range t.C {
			RefreshNow()
		}
	}()
}

// DefaultURL 官方默认源地址（空 = 已由 OPENOS_MARKET_URL=off 关闭）。
func DefaultURL() string {
	mu.RLock()
	defer mu.RUnlock()
	return defaultURL
}

// CustomSources 当前自定义源列表（副本）。
func CustomSources() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, len(customSrcs))
	copy(out, customSrcs)
	return out
}

// SetCustomSources 全量覆盖自定义源：trim、剔除空项与非法 http(s) 地址、去重、
// 限量校验（>MaxCustomSources 报错），返回清洗后的列表。
func SetCustomSources(urls []string) ([]string, error) {
	clean := make([]string, 0, len(urls))
	seen := map[string]bool{}
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if !validSourceURL(u) {
			return nil, fmt.Errorf("非法目录地址: %s", u)
		}
		if seen[u] {
			continue
		}
		seen[u] = true
		clean = append(clean, u)
	}
	if len(clean) > MaxCustomSources {
		return nil, fmt.Errorf("自定义源最多 %d 个", MaxCustomSources)
	}
	mu.Lock()
	customSrcs = clean
	mu.Unlock()
	return append([]string{}, clean...), nil
}

func validSourceURL(u string) bool {
	if len(u) > MaxSourceURLLen {
		return false
	}
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != ""
}

// Sources 当前全部远程源（去重副本）：自定义源在前（遮蔽默认源同 id 条目），默认源垫底。
func Sources() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(customSrcs)+1)
	seen := map[string]bool{}
	for _, u := range customSrcs {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	if defaultURL != "" && !seen[defaultURL] {
		out = append(out, defaultURL)
	}
	return out
}

// RefreshNow 立即并发拉取全部远程源并按 Sources 顺序合并（id 首见优先）：
// 任一源成功即以成功源的合并结果为生效目录（失败源的条目本轮缺席，记日志）；
// 全部失败沿用现行目录。返回成功源数与生效条目数（保存接口回显用）。
func RefreshNow() (okCount, total int) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	srcs := Sources()
	if len(srcs) == 0 {
		return 0, len(Catalog())
	}
	results := make([][]Entry, len(srcs))
	errs := make([]error, len(srcs))
	var wg sync.WaitGroup
	for i, s := range srcs {
		wg.Add(1)
		go func(i int, s string) {
			defer wg.Done()
			results[i], errs[i] = fetchRemote(s)
		}(i, s)
	}
	wg.Wait()
	merged := make([]Entry, 0)
	seen := map[string]bool{}
	for i, es := range results {
		if errs[i] != nil {
			log.Printf("market: 目录源拉取失败 %s: %v", srcs[i], errs[i])
			continue
		}
		okCount++
		for _, e := range es {
			if !seen[e.ID] {
				seen[e.ID] = true
				merged = append(merged, e)
			}
		}
	}
	if okCount == 0 {
		mu.RLock()
		n := len(active)
		mu.RUnlock()
		return 0, n
	}
	mu.Lock()
	if !entriesEqual(active, merged) {
		active = merged
		log.Printf("market: 远程目录已生效，%d/%d 个源成功，共 %d 个条目", okCount, len(srcs), len(merged))
	}
	total = len(active)
	mu.Unlock()
	return okCount, total
}

// fetchRemote 拉取并解析远程清单：仅 http(s)、限 2MB、15s 超时，非 200 视为失败。
func fetchRemote(u string) ([]Entry, error) {
	resp, err := httpClient.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	return parseCatalog(raw)
}

// entriesEqual 内容级比较（清单条目量小，直接比序列化字节），内容不变不交换不打日志。
func entriesEqual(a, b []Entry) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

// validate 目录条目静态校验：必填字段、形态合法、config 与形态一致、web/proxy 目标须为 http(s)。
func (e Entry) validate() error {
	if e.ID == "" || e.Name == "" {
		return fmt.Errorf("id 与 name 必填")
	}
	if e.Type != "web" && e.Type != "proxy" && e.Type != "schema" {
		return fmt.Errorf("未知形态 %q", e.Type)
	}
	if len(e.Config) == 0 || !json.Valid(e.Config) {
		return fmt.Errorf("config 缺失或非法 JSON")
	}
	if e.Type == "web" || e.Type == "proxy" {
		var cfg struct {
			Kind string `json:"kind"`
			URL  string `json:"url"`
		}
		if json.Unmarshal(e.Config, &cfg) != nil || cfg.Kind != e.Type {
			return fmt.Errorf("config.kind 须等于 type=%s", e.Type)
		}
		u, err := url.Parse(cfg.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("config.url 须为合法 http(s) 地址")
		}
	}
	return nil
}

// Find 按 ID 查目录条目。
func Find(id string) (Entry, error) {
	for _, e := range Catalog() {
		if e.ID == id {
			return e, nil
		}
	}
	return Entry{}, ErrNotFound
}

// List 目录 + 本机安装状态聚合（普通登录用户可浏览，安装按钮仅管理员）。
func List(db *store.DB) []State {
	entries := Catalog()
	out := make([]State, 0, len(entries))
	for _, e := range entries {
		st := State{Entry: e}
		if cur, ok := db.AppByID(e.ID); ok {
			st.Installed = true
			st.InstalledVersion = cur.Version
			st.Managed = cur.Source == SourceMarket
			st.Upgradable = st.Managed && cur.Version != e.Version
			if cur.Builtin || cur.Locked {
				st.Protected = true
			} else if !st.Managed {
				st.Conflict = true
			}
		}
		out = append(out, st)
	}
	return out
}

// Install 安装/升级/覆盖安装目录条目，并按所选角色整组授权（admin 恒在授权内）。
// 角色只认系统出现过的（ListRoles），防 role_apps 写入幽灵角色。
func Install(db *store.DB, id string, roles []string) (Entry, error) {
	e, err := Find(id)
	if err != nil {
		return e, err
	}
	if cur, ok := db.AppByID(e.ID); ok && (cur.Builtin || cur.Locked) {
		return e, ErrProtected
	}
	valid, err := db.ListRoles()
	if err != nil {
		return e, err
	}
	vm := make(map[string]bool, len(valid))
	for _, r := range valid {
		vm[r] = true
	}
	granted := []string{"admin"}
	for _, r := range roles {
		if r != "admin" && vm[r] && !contains(granted, r) {
			granted = append(granted, r)
		}
	}
	if err := db.UpsertApp(store.AppDef{
		ID: e.ID, Name: e.Name, Icon: e.Icon, Category: e.Category,
		Config: e.Config, Version: e.Version, Source: SourceMarket,
		Width: e.Width, Height: e.Height,
	}); err != nil {
		return e, err
	}
	if err := db.SetAppRoles(e.ID, granted); err != nil {
		return e, err
	}
	return e, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
