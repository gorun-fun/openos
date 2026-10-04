// Package market 应用商店：目录清单与安装逻辑。
//
// 目录源多级：内置清单（catalog.json 随二进制发布，go:embed）为兜底基线；
// 远程清单并发拉取（每 10 分钟 + 保存即拉取）——自定义源（管理员经商店设置添加，
// 持久化 system_kv）排前、官方默认源（OPENOS_MARKET_URL，=off 关闭）垫底，
// 合并去重按 id **首见优先**：自定义源可遮蔽官方同 id 条目。任一源成功即以成功
// 源合并结果为生效目录；全部失败沿用现行目录。更新货架只改远端 JSON，无需发版。
//
// 清单 apps 字段兼容两种形态：数组（官方目录）与 id→条目对象映射（宿主把 App
// 发布货架与商店共用同一 catalog.json 时，如 grf-hotel 的 remoteApps 货架）——
// 对象形态 id 取键名、按 id 排序输出（解析结果须稳定：刷新按序列化字节比较）；
// 缺 name 与 type 的条目视为其他通道的记录，静默跳过不算坏清单。
//
// 目录收录的应用形态：
//   - web   网页应用（config.kind=web）：iframe 直嵌允许被嵌的站点，渲染走 WebApp 容器
//   - proxy 代理网页（config.kind=proxy）：目标站经服务端反代（webproxy），安装即放行其目标主机
//   - schema 声明式应用（config 即 SchemaApp schema）：纯数据描述、无代码执行
//   - bundle 组件应用（config.kind=bundle）：单文件 ES 模块（默认导出 Vue 组件定义），
//     安装时由服务端从目录声明的 https 地址下载落盘（<data>/market/bundles/），
//     运行时经 /api/market/bundle/{id} 提供（按角色授权校验），前端 BundleApp 容器
//     动态 import 渲染——代码只在管理员安装时取一次远端，运行期不依赖外部源。
//     这是唯一的远程代码形态：与内置应用同级信任，宿主应只收录可信来源的目录。
// 安装 = UpsertApp（builtin=0/locked=0，version/source 标记来源）+ 按所选角色授权。
// 宿主可用 OPENOS_MARKET=off 整体下架商店。
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
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
		Version int             `json:"version"`
		Apps    json.RawMessage `json:"apps"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	list, err := parseApps(doc.Apps)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]Entry, 0, len(list))
	for _, e := range list {
		// 缺 name 与 type = 其他发布通道的记录（如 remoteApps 货架条目），不是坏条目
		if e.Name == "" && e.Type == "" {
			continue
		}
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

// parseApps 解析 apps 字段的两种形态：Entry 数组（官方目录）与 id→条目对象映射
// （宿主货架与商店共用同一 catalog.json 时；此时条目内可省 id，键名即 id）。
// 对象形态按 id 排序输出——解析结果必须稳定，RefreshNow 否则会在内容不变时
// 反复交换生效目录。apps 缺失视为空清单（与旧解析行为一致），既非数组也非对象则报错。
func parseApps(raw json.RawMessage) ([]Entry, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var arr []Entry
	if json.Unmarshal(raw, &arr) == nil {
		return arr, nil
	}
	var m map[string]Entry
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Entry, 0, len(ids))
	for _, id := range ids {
		e := m[id]
		e.ID = id
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
	if e.Type != "web" && e.Type != "proxy" && e.Type != "schema" && e.Type != "bundle" {
		return fmt.Errorf("未知形态 %q", e.Type)
	}
	if len(e.Config) == 0 || !json.Valid(e.Config) {
		return fmt.Errorf("config 缺失或非法 JSON")
	}
	if e.Type == "web" || e.Type == "proxy" || e.Type == "bundle" {
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
		// bundle 是要执行的代码：地址强制 https、id/version 限安全字符
		//（二者拼进落盘文件名，先于此挡住路径穿越）
		if e.Type == "bundle" {
			if u.Scheme != "https" {
				return fmt.Errorf("bundle 地址须为 https")
			}
			if !safeNameRe.MatchString(e.ID) || !safeNameRe.MatchString(e.Version) {
				return fmt.Errorf("bundle 条目 id/version 须为字母数字._-")
			}
		}
	}
	return nil
}

// safeNameRe bundle 落盘文件名的组成部分（id/version）允许的字符集。
var safeNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

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
// bundle 形态在入库前先下载组件文件落盘（下载失败即安装失败，不产生装了跑不起来的应用）。
func Install(db *store.DB, id string, roles []string) (Entry, error) {
	e, err := Find(id)
	if err != nil {
		return e, err
	}
	if cur, ok := db.AppByID(e.ID); ok && (cur.Builtin || cur.Locked) {
		return e, ErrProtected
	}
	if e.Type == "bundle" {
		if err := fetchBundle(e); err != nil {
			return e, fmt.Errorf("组件下载失败: %w", err)
		}
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

// ---- bundle 组件文件管理 ----
//
// 安装时从目录声明的 https 地址下载到 <data>/market/bundles/<id>-<version>.js，
// 运行期由服务端本地提供（GET /api/market/bundle/{id}，按角色授权校验）——
// 代码只在管理员安装的那一刻取一次远端，之后版本固定、不再依赖外部源。

var (
	bundleDirOnce sync.Once
	bundleDir     string
)

// SetBundleDir 设置 bundle 落盘目录并确保存在（main.go 启动时注入 <data>/market/bundles）。
func SetBundleDir(dir string) {
	bundleDirOnce.Do(func() {
		bundleDir = dir
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("market: 创建 bundle 目录失败: %v", err)
		}
	})
}

// bundleFile 本地组件文件路径（id/version 已由 validate 限定安全字符）。
func bundleFile(id, version string) string {
	return filepath.Join(bundleDir, fmt.Sprintf("%s-%s.js", id, version))
}

// bundleURL 从条目 config 提取下载地址（validate 已保证形态合法）。
func bundleURL(e Entry) string {
	var cfg struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(e.Config, &cfg)
	return cfg.URL
}

// fetchBundle 下载组件文件并原子落盘（限 2MB / 15s，与目录清单同一套限额），
// 成功后清理同 id 的旧版本文件（升级即换代，不留孤儿）。
func fetchBundle(e Entry) error {
	if bundleDir == "" {
		return fmt.Errorf("bundle 目录未初始化")
	}
	resp, err := httpClient.Get(bundleURL(e))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return fmt.Errorf("组件文件为空")
	}
	if len(raw) >= 2<<20 {
		return fmt.Errorf("组件文件超过 2MB 限制")
	}
	dst := bundleFile(e.ID, e.Version)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	// 清理旧版本文件（忽略错误：残留文件不影响正确性，只占磁盘）
	if matches, _ := filepath.Glob(filepath.Join(bundleDir, e.ID+"-*.js")); len(matches) > 0 {
		for _, m := range matches {
			if m != dst {
				os.Remove(m)
			}
		}
	}
	log.Printf("market: bundle %s@%s 已落盘（%d 字节）", e.ID, e.Version, len(raw))
	return nil
}

// ErrNoBundle 应用非 bundle 形态或组件文件缺失。
var ErrNoBundle = errors.New("market: 组件文件不存在")

// BundlePath 按已安装应用的当前版本解析本地组件文件路径。
// 传入的 AppDef 须为商店安装（source=market）且 config.kind=bundle。
func BundlePath(a store.AppDef) (string, error) {
	if a.Source != SourceMarket || !safeNameRe.MatchString(a.ID) || !safeNameRe.MatchString(a.Version) {
		return "", ErrNoBundle
	}
	var cfg struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(a.Config, &cfg) != nil || cfg.Kind != "bundle" {
		return "", ErrNoBundle
	}
	p := bundleFile(a.ID, a.Version)
	if _, err := os.Stat(p); err != nil {
		return "", ErrNoBundle
	}
	return p, nil
}

// RemoveBundles 卸载应用时清理其全部组件版本文件（幂等；非 bundle 应用为空操作）。
func RemoveBundles(id string) {
	if bundleDir == "" || !safeNameRe.MatchString(id) {
		return
	}
	matches, _ := filepath.Glob(filepath.Join(bundleDir, id+"-*.js"))
	for _, m := range matches {
		os.Remove(m)
	}
}
