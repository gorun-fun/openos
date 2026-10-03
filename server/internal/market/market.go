// Package market 应用商店：内置目录清单（catalog.json 随二进制发布）与安装逻辑。
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
	"log"
	"net/url"
	"sync"

	"openos/server/internal/store"
)

//go:embed catalog.json
var catalogFS embed.FS

// SourceMarket 应用安装来源标记（apps.source 列）：商店安装的应用才允许商店升级比较版本。
const SourceMarket = "market"

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
	catOnce sync.Once
	list    []Entry
)

// Catalog 返回内置目录（embed 清单，进程内解析一次；坏条目跳过并记日志，不阻塞启动）。
func Catalog() []Entry {
	catOnce.Do(loadCatalog)
	return list
}

func loadCatalog() {
	raw, err := catalogFS.ReadFile("catalog.json")
	if err != nil {
		log.Printf("market: 读取内置目录失败: %v", err)
		return
	}
	var doc struct {
		Version int     `json:"version"`
		Apps    []Entry `json:"apps"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		log.Printf("market: 内置目录解析失败: %v", err)
		return
	}
	seen := map[string]bool{}
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
		list = append(list, e)
	}
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
