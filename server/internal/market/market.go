// Package market 应用商店：目录清单与安装逻辑。
//
// 目录源多级：内置清单（catalog.json 随二进制发布，go:embed）为兜底基线；
// 远程清单并发拉取（每 10 分钟 + 保存即拉取）——自定义源（管理员经商店设置添加，
// 持久化 system_kv）排前、官方默认源（OPENOS_MARKET_URL 逗号分隔多个，=off 全关）
// 垫底，合并去重按 id **首见优先**：自定义源可遮蔽默认源同 id 条目、默认源按声明
// 顺序前者遮蔽后者。任一源成功即以成功源合并结果为生效目录；全部失败沿用现行
// 目录。更新货架只改远端 JSON，无需发版。
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
//   - host  宿主预装应用：宿主把自家业务应用列进目录（如 grf-hotel 的房态/订单），
//     本机已由宿主注册安装——商店仅展示+可打开，不可经商店安装/卸载/升级；无 config
//   - bundle 组件应用（config.kind=bundle）：单文件 ES 模块（默认导出 Vue 组件定义），
//     安装时由服务端从目录声明的 https 地址下载落盘（<data>/market/bundles/），
//     运行时经 /api/market/bundle/{id} 提供（按角色授权校验），前端 BundleApp 容器
//     动态 import 渲染——代码只在管理员安装时取一次远端，运行期不依赖外部源。
//     这是唯一的远程代码形态：与内置应用同级信任，宿主应只收录可信来源的目录。
//
// 安装 = UpsertApp（builtin=0/locked=0，version/source 标记来源）+ 按所选角色授权。
// bundle 应用支持按版本切换（SwitchVersion：目录 versions 表/本地缓存取件、授权不变），
// 升级保留历史版本文件作为回滚根基；自动升级（AutoUpgrade，商店全局开关）在每次
// 目录刷新后把商店安装的应用对齐到目录版本。宿主可用 OPENOS_MARKET=off 整体下架商店。
package market

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"openos/server/internal/atomicfile"
	"openos/server/internal/store"
)

//go:embed catalog.json
var catalogFS embed.FS

// SourceMarket 应用安装来源标记（apps.source 列）：商店安装的应用才允许商店升级比较版本。
const SourceMarket = "market"

// 自定义源上限与单条地址长度上限（管理员经商店设置维护）。
const (
	MaxCustomSources = 10
	MaxSourceURLLen  = 500
)

// Entry 目录条目。
type Entry struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Category    string          `json:"category"`
	Icon        string          `json:"icon"`
	Type        string          `json:"type"`    // web | proxy | schema | bundle | host（宿主预装，仅展示/打开）
	Config      json.RawMessage `json:"config"`  // web/proxy 为 {kind,url[,headers]}；schema 为 SchemaApp schema
	Version     string          `json:"version"` // 应用版本（与本机安装版本比较出「可升级」）
	Changelog   string          `json:"changelog,omitempty"`
	Perms       []string        `json:"perms,omitempty"`    // 权限声明（安装确认页展示）
	Versions    []EntryVersion  `json:"versions,omitempty"` // bundle 历史版本（新到旧，供按版本切换/回滚）
	Width       int             `json:"width,omitempty"`
	Height      int             `json:"height,omitempty"`
}

// EntryVersion bundle 历史版本条目：版本号 + 该版本组件文件的下载地址。
// 约定 versions[0] 与顶层 version/config.url 一致（顶层指针仍是唯一权威最新版，
// versions 仅作查找表，旧服务端忽略该字段不受影响）。
type EntryVersion struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

// State 目录条目 + 本机安装状态（GET /api/market/apps 的响应单元）。
type State struct {
	Entry
	Installed        bool   `json:"installed"`
	InstalledVersion string `json:"installedVersion,omitempty"`
	Upgradable       bool   `json:"upgradable"`          // 商店安装且目录版本更新
	Managed          bool   `json:"managed"`             // 本机由商店安装（source=market）
	Conflict         bool   `json:"conflict,omitempty"`  // 同 id 应用为管理员手工注册（重装会覆盖其配置）
	Protected        bool   `json:"protected,omitempty"` // 同 id 应用为内置/宿主锁定（不可安装）
	// Versions 可切换版本列表（bundle 专属）：目录声明的历史版本在前（新到旧），
	// 本地已落盘但目录未列的版本垫底（离线仍可回滚）；非 bundle 形态为空。
	Versions []string `json:"versions,omitempty"`
}

// 错误：路由层据此映射 404 / 403 / 400。
var (
	ErrNotFound        = errors.New("market: 目录中不存在该应用")
	ErrProtected       = errors.New("market: 应用 ID 与内置或宿主锁定应用冲突")
	ErrNotInstallable  = errors.New("market: 宿主预装应用不可经商店安装")
	ErrVersionNotFound = errors.New("market: 目录与本地均没有该版本")
	ErrNotBundle       = errors.New("market: 仅 bundle 形态支持按版本切换")
	ErrNotInstalled    = errors.New("market: 应用未安装，无法切换版本")
	ErrBadVersion      = errors.New("market: 非法版本号")
)

var (
	embeddedOnce sync.Once
	embeddedList []Entry

	mu          sync.RWMutex
	active      []Entry  // 当前生效目录：全部远程源合并结果，或内置兜底
	defaultURLs []string // 官方默认源列表（OPENOS_MARKET_URL 逗号分隔；空 = off 只剩自定义源）
	customSrcs  []string // 管理员自定义源（有序，首见优先遮蔽默认源同 id 条目）

	refreshMu       sync.Mutex // 串行化刷新（周期循环与保存即拉取互斥）
	refreshInterval = 10 * time.Minute
)

// marketGet 市场专用拉取（安全审计 2026-10）：仅 https；域名单次解析后按 IP 直连
// （堵 DNS rebinding TOCTOU），非配置源主机的域名解析含内网 IP 一律拒绝——
// 配置源（管理员设置的默认/自定义源）主机视同 webproxy 的配置目标显式放行，
// 供内网镜像货架使用；bundle 下载地址来自目录内容，仅当主机恰为某配置源主机
// 时才允许内网，其余必须公网。
func marketGet(rawURL string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("market: 仅允许 https 源（%s）", u.Host)
	}
	trusted := isSourceHost(u.Hostname())
	c := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil || len(ips) == 0 {
				return nil, fmt.Errorf("market: 域名解析失败 %s: %v", host, err)
			}
			if !trusted {
				for _, ip := range ips {
					if !isPublicIP(ip.IP) {
						return nil, fmt.Errorf("market: 拒绝拨号 %s（解析含内网地址，非配置源主机）", host)
					}
				}
			}
			var d net.Dialer
			var lastErr error
			for _, ip := range ips {
				conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}}
	return c.Get(rawURL)
}

// isSourceHost 主机名是否为当前某配置源（默认源或自定义源）的主机。
func isSourceHost(host string) bool {
	for _, s := range Sources() {
		if su, err := url.Parse(s); err == nil && strings.EqualFold(su.Hostname(), host) {
			return true
		}
	}
	return false
}

// isLoopbackHost 回环主机例外（本地开发/单测的 httptest 服务；
// 生产自定义源经 validSourceURL 已强制 https，走不到此分支）。
func isLoopbackHost(h string) bool {
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "[::1]"
}

func isPublicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified())
}

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
func StartRemote(defs []string) {
	mu.Lock()
	defaultURLs = defs
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

// ParseDefaultSources 解析 OPENOS_MARKET_URL：整体 "off"/空 = 关闭默认源（nil）；
// 否则逗号分隔多个 catalog 地址，逐项 trim、按 validSourceURL 过滤（非法项记日志
// 丢弃，不拖垮整个列表——宿主笔误时其余源仍可用）、去重保序。
func ParseDefaultSources(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "off" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		u := strings.TrimSpace(part)
		if u == "" || seen[u] {
			continue
		}
		if !validSourceURL(u) {
			log.Printf("market: 默认源地址非法，已忽略 %q", u)
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

// DefaultURLs 官方默认源列表（空 = 已由 OPENOS_MARKET_URL=off 关闭）。
func DefaultURLs() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, len(defaultURLs))
	copy(out, defaultURLs)
	return out
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
	// https 强制（安全审计 2026-10）：http 源可被中间人投毒目录（bundle/web 条目）；
	// 回环 http 例外——与 marketGet 的拉取例外对齐（本地开发自建货架不出网卡，
	// 无 MITM 面），否则配置层直接拒掉拉取层声称支持的形态
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return false
	}
	return p.Scheme == "https" || (p.Scheme == "http" && isLoopbackHost(p.Hostname()))
}

// Sources 当前全部远程源（去重副本）：自定义源在前（遮蔽默认源同 id 条目），默认源垫底。
func Sources() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(customSrcs)+len(defaultURLs))
	seen := map[string]bool{}
	for _, u := range customSrcs {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	for _, u := range defaultURLs {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// RefreshNow 立即并发拉取全部远程源并按 Sources 顺序合并（id 首见优先）：
// 任一源成功即以成功源的合并结果为生效目录（失败源的条目本轮缺席，记日志）；
// 全部失败沿用现行目录。返回成功源数与生效条目数（保存接口回显用）。
func RefreshNow() (okCount, total int) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	// 刷新尾部跑自动升级（defer 后注册先执行：仍在 refreshMu 持有期间，与手动安装串行）；
	// 源全部失败/零源也照跑——目录指针未变时对齐的是现行目录，语义一致
	defer runAutoUpgrade()
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

// fetchLimited 限长拉取共用内核（目录清单与 bundle 组件同一套限额）：仅 https、
// 15s 超时、非 200 视为失败、限 2MB 且恰在限额处截断即拒绝——半截 JSON/JS
// 静默入库比显式报错更糟（此前仅 bundle 通道查截断，目录通道吞成解析失败）。
// 拨号防线见 marketGet（SSRF：非配置源主机拒绝内网 IP，安全审计 2026-10）。
func fetchLimited(u string) ([]byte, error) {
	resp, err := marketGet(u)
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
	if len(raw) >= 2<<20 {
		return nil, fmt.Errorf("响应超过 2MB 限制")
	}
	return raw, nil
}

// fetchRemote 拉取并解析远程清单（限额与拨号防线见 fetchLimited）。
func fetchRemote(u string) ([]Entry, error) {
	raw, err := fetchLimited(u)
	if err != nil {
		return nil, err
	}
	return parseCatalog(raw)
}

// ---- 自动升级（商店全局开关，system_kv 持久化，**默认开**） ----
//
// 每次 RefreshNow 尾部执行：本机商店安装（source=market）的应用与目录版本
// 不一致即自动重装到目录版本，**保留现有授权**。目录最新版指针被货架回退时
// 也会跟随回退——货架本身就是全局回滚杠杆。
//
// 跳过问题版本：管理员手动切换到旧版本（SwitchVersion）= 明确避开当时的目录
// 最新版（KVSkipVersions 记下该版本）。自动升级发现目录版本 == 被避开版本时
// 跳过该应用，直到目录出现不同的（更新的）版本才恢复自动升级并清除记录；
// 手动点「更新」或切回最新版同样清除。

// KVAutoUpgrade 自动升级开关的 system_kv 键（"0"=显式关，缺省/其它=开）。
const KVAutoUpgrade = "market_autoupgrade"

// KVSkipVersions 被避开的应用版本表（system_kv，JSON {appID: 目录版本}）：
// 手动切旧版时记录，自动升级据此跳过该版本。
const KVSkipVersions = "market_skip_versions"

// AutoUpgraded 单个应用的自动升级结果（main 注入的回调据此审计与推送）。
type AutoUpgraded struct {
	ID      string
	Version string // 升级后的版本
}

// autoUpgradeFn 刷新尾部回调（main 注入：跑 AutoUpgrade + 审计 + sys:apps-changed；
// market 包不依赖 hub，发布收在 main 侧）。
var autoUpgradeFn func()

// SetAutoUpgrader 注册/注销自动升级回调（main 启动时注入 db 闭包）。
func SetAutoUpgrader(fn func()) { autoUpgradeFn = fn }

// AutoUpgradeOn 自动升级开关是否开启（默认开：仅显式存 "0" 为关，
// 未写过开关的新部署/升级部署直接生效）。
func AutoUpgradeOn(db *store.DB) bool {
	v, _ := db.GetSystemKV(KVAutoUpgrade)
	return v != "0"
}

// SetAutoUpgrade 开关持久化（PUT /api/market/autoupgrade 调用）。
func SetAutoUpgrade(db *store.DB, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return db.SetSystemKV(KVAutoUpgrade, v)
}

// AutoUpgrade 自动升级一遍：开关关时空操作；逐应用比对目录版本，不一致即重装
// 到目录版本并保留授权。目录版本 == 被避开版本（手动切旧版记录，见 KVSkipVersions）
// 时跳过该应用——那是管理员明确避开的疑似问题版本，等目录发布不同的版本再恢复。
// 单个应用失败只记日志跳过（bundle 下载失败等），不拖垮其余应用与刷新本身。
// 目录已下架的应用保留现状不动作。
func AutoUpgrade(db *store.DB) []AutoUpgraded {
	if !AutoUpgradeOn(db) {
		return nil
	}
	apps, err := db.ListApps()
	if err != nil {
		log.Printf("market: 自动升级读取应用列表失败: %v", err)
		return nil
	}
	skips := skipVersions(db)
	var changed []AutoUpgraded
	for _, a := range apps {
		e, err := Find(a.ID)
		if err != nil {
			continue
		}
		// 商店安装（managed）与宿主预装（host，登记版本对齐货架）都参与自动升级
		if a.Source != SourceMarket && e.Type != "host" {
			continue
		}
		if a.Version == e.Version {
			continue
		}
		if skips[a.ID] == e.Version {
			log.Printf("market: 自动升级跳过 %s@%s（管理员切换版本时避开）", a.ID, e.Version)
			continue
		}
		if _, err := Install(db, a.ID, appRoles(db, a.ID)); err != nil {
			log.Printf("market: 自动升级 %s 失败: %v", a.ID, err)
			continue
		}
		log.Printf("market: 自动升级 %s %s → %s", a.ID, a.Version, e.Version)
		changed = append(changed, AutoUpgraded{ID: a.ID, Version: e.Version})
	}
	return changed
}

// skipVersions 读被避开的版本表（{appID: 目录版本}，坏 JSON 视为空）。
func skipVersions(db *store.DB) map[string]string {
	raw, _ := db.GetSystemKV(KVSkipVersions)
	m := map[string]string{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	return m
}

// setSkipVersion 记录/清除某应用被避开的版本（ver 为空 = 清除，即恢复正常
// 自动跟踪；写失败只记日志——skip 表是尽力而为的元数据，不影响安装主流程）。
func setSkipVersion(db *store.DB, id, ver string) {
	m := skipVersions(db)
	if ver == "" {
		if _, ok := m[id]; !ok {
			return
		}
		delete(m, id)
	} else {
		if m[id] == ver {
			return
		}
		m[id] = ver
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	if err := db.SetSystemKV(KVSkipVersions, string(b)); err != nil {
		log.Printf("market: 记录跳过版本失败 %s@%s: %v", id, ver, err)
	}
}

// runAutoUpgrade RefreshNow 尾部执行（refreshMu 持有期间）。回调异常只记日志，
// 绝不影响刷新主流程。
func runAutoUpgrade() {
	if autoUpgradeFn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("market: 自动升级异常: %v", r)
		}
	}()
	autoUpgradeFn()
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
	if e.Type != "web" && e.Type != "proxy" && e.Type != "schema" && e.Type != "bundle" && e.Type != "host" {
		return fmt.Errorf("未知形态 %q", e.Type)
	}
	// host = 宿主预装应用条目（宿主把自家业务应用列进目录供展示/打开）：
	// 不经商店安装，无需 config，本机不存在时也不可装
	if e.Type == "host" {
		return nil
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
			// 历史版本表：版本号同样限安全字符、地址强制 https（与最新版同一套防线）
			seen := map[string]bool{}
			for _, v := range e.Versions {
				if !safeNameRe.MatchString(v.Version) {
					return fmt.Errorf("bundle 历史版本号 %q 须为字母数字._-", v.Version)
				}
				vu, err := url.Parse(v.URL)
				if err != nil || vu.Scheme != "https" || vu.Host == "" {
					return fmt.Errorf("bundle 历史版本 %s 地址须为合法 https", v.Version)
				}
				if seen[v.Version] {
					return fmt.Errorf("bundle 历史版本 %s 重复", v.Version)
				}
				seen[v.Version] = true
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
			// host 与商店安装（managed）同样参与版本对齐（货架热更新登记）
			st.Upgradable = (st.Managed || e.Type == "host") && cur.Version != e.Version
			if cur.Builtin || cur.Locked {
				st.Protected = true
			} else if !st.Managed {
				st.Conflict = true
			}
		}
		// 可切换版本列表（bundle 专属）：目录声明的历史版本在前（新到旧），
		// 本地已落盘但目录未列的版本垫底（货架清过历史仍可离线回滚）
		if e.Type == "bundle" {
			seen := map[string]bool{}
			var vs []string
			for _, v := range e.Versions {
				if !seen[v.Version] {
					seen[v.Version] = true
					vs = append(vs, v.Version)
				}
			}
			for _, v := range localBundleVersions(e.ID) {
				if !seen[v] {
					seen[v] = true
					vs = append(vs, v)
				}
			}
			st.Versions = vs
		}
		out = append(out, st)
	}
	return out
}

// Install 安装/升级/覆盖安装目录条目（最新版），并按所选角色整组授权（admin 恒在授权内）。
// 角色只认系统出现过的（ListRoles），防 role_apps 写入幽灵角色。
// bundle 形态在入库前先下载组件文件落盘（下载失败即安装失败，不产生装了跑不起来的应用）。
// 按指定版本安装（历史版本切换/回滚）走 SwitchVersion。
func Install(db *store.DB, id string, roles []string) (Entry, error) {
	e, err := Find(id)
	if err != nil {
		return e, err
	}
	// host = 宿主预装应用：仅当本机已注册时允许"更新"——对齐登记版本（保留
	// locked/授权/配置，组件实际加载走宿主货架热更新通道）；本机没有则不可装
	//（宿主 runtimeComponents 在其他部署无处渲染）
	if e.Type == "host" {
		cur, ok := db.AppByID(e.ID)
		if !ok {
			return e, ErrNotInstallable
		}
		cur.Version = e.Version
		if err := db.UpsertApp(cur); err != nil {
			return e, err
		}
		setSkipVersion(db, e.ID, "")
		return e, nil
	}
	ver := e.Version
	url := bundleURL(e)
	if err := installEntry(db, e, roles, ver, url); err != nil {
		return e, err
	}
	// 到达目标版本（手动安装/更新/自动升级均经此）= 恢复正常自动跟踪
	setSkipVersion(db, e.ID, "")
	e.Version = ver
	return e, nil
}

// SwitchVersion 把已安装的 bundle 应用切换到指定版本（历史版本回滚的入口）：
// 版本地址取目录 versions 表；本地已有该版本文件则直接复用（离线可回滚），否则下载。
// **保留现有授权不变**（区别于 Install 的整组覆盖）；仅 bundle 形态支持——
// web/schema 等形态的"版本"就是条目本身，没有历史快照可切。
func SwitchVersion(db *store.DB, id, version string) (Entry, error) {
	e, err := Find(id)
	if err != nil {
		return e, err
	}
	// 保护检查（内置/宿主锁定）在 installEntry 开头统一做（Install 同源）
	if _, ok := db.AppByID(e.ID); !ok {
		return e, ErrNotInstalled
	}
	if e.Type != "bundle" {
		return e, ErrNotBundle
	}
	if !safeNameRe.MatchString(version) {
		return e, ErrBadVersion
	}
	url := ""
	if version == e.Version {
		url = bundleURL(e)
	} else {
		for _, v := range e.Versions {
			if v.Version == version {
				url = v.URL
				break
			}
		}
	}
	if url == "" && !bundleFileExists(e.ID, version) {
		return e, ErrVersionNotFound
	}
	roles := appRoles(db, e.ID) // 现有授权原样带回（installEntry 校验后整组写回）
	// 此刻 e.Version 仍是目录最新版：切到它 = 恢复自动跟踪；切到旧版 = 避开它
	latest := e.Version
	if err := installEntry(db, e, roles, version, url); err != nil {
		return e, err
	}
	if version != latest {
		setSkipVersion(db, e.ID, latest)
		log.Printf("market: %s 切换到 %s，自动升级将跳过目录版本 %s（下一个新版恢复）", e.ID, version, latest)
	} else {
		setSkipVersion(db, e.ID, "")
	}
	e.Version = version
	return e, nil
}

// installEntry 安装内核（Install/SwitchVersion 共用）：内置/宿主锁定应用保护检查、
// 按目标版本下载组件（bundle）、角色校验过滤、UpsertApp 落库（version 标记目标
// 版本）+ 整组写授权。
func installEntry(db *store.DB, e Entry, roles []string, version, bundleAddr string) error {
	// 保护检查：内置与宿主锁定应用不可经商店覆盖（此前 Install/SwitchVersion
	// 各写一份，收敛于此）
	if cur, ok := db.AppByID(e.ID); ok && (cur.Builtin || cur.Locked) {
		return ErrProtected
	}
	if e.Type == "bundle" {
		if err := fetchBundle(e.ID, version, bundleAddr); err != nil {
			return fmt.Errorf("组件下载失败: %w", err)
		}
	}
	valid, err := db.ListRoles()
	if err != nil {
		return err
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
		Config: e.Config, Version: version, Source: SourceMarket,
		Width: e.Width, Height: e.Height,
	}); err != nil {
		return err
	}
	return db.SetAppRoles(e.ID, granted)
}

// appRoles 查应用当前已授权角色（RoleAccessMap 按角色聚合，倒排回来），
// 自动升级/版本切换用它保持授权不变。
func appRoles(db *store.DB, appID string) []string {
	m, err := db.RoleAccessMap()
	if err != nil {
		return nil
	}
	var out []string
	for role, ids := range m {
		if contains(ids, appID) {
			out = append(out, role)
		}
	}
	sort.Strings(out)
	return out
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
// 安装/切换版本时从目录声明的 https 地址下载到 <data>/market/bundles/<id>-<version>.js
// （本地已有同名版本文件则复用——版本号即内容不可变），运行期由服务端本地提供
// （GET /api/market/bundle/{id}，按角色授权校验）。升级换代**保留**历史版本文件
// （按版本回滚的本地根基），卸载经 RemoveBundles 全清。

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

// fetchBundle 下载指定版本的组件文件并原子落盘（限 2MB / 15s，与目录清单同一套限额）。
// **本地已有同名版本文件则直接复用不再下载**：版本号即内容不可变（与运行期
// /api/market/bundle 的版本化长缓存语义一致），离线也能切换/回滚到曾装过的版本。
// 旧版本文件不再清理（升级换代保留历史文件，作为按版本回滚的本地根基；
// 卸载时经 RemoveBundles 全清）。
func fetchBundle(id, version, addr string) error {
	if bundleDir == "" {
		return fmt.Errorf("bundle 目录未初始化")
	}
	dst := bundleFile(id, version)
	if _, err := os.Stat(dst); err == nil {
		log.Printf("market: bundle %s@%s 复用本地文件", id, version)
		return nil
	}
	raw, err := fetchLimited(addr)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return fmt.Errorf("组件文件为空")
	}
	if err := atomicfile.Write(dst, raw, 0o644); err != nil {
		return err
	}
	log.Printf("market: bundle %s@%s 已落盘（%d 字节）", id, version, len(raw))
	return nil
}

// bundleFileExists 本地是否已有该版本的组件文件。
func bundleFileExists(id, version string) bool {
	_, err := os.Stat(bundleFile(id, version))
	return err == nil
}

// localBundleVersions 扫描本地已落盘的该 id 全部版本号（文件名 <id>-<version>.js）。
func localBundleVersions(id string) []string {
	if bundleDir == "" || !safeNameRe.MatchString(id) {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(bundleDir, id+"-*.js"))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		base := strings.TrimSuffix(filepath.Base(m), ".js")
		v := strings.TrimPrefix(base, id+"-")
		if v != "" && safeNameRe.MatchString(v) {
			out = append(out, v)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
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
