package market

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"openos/server/internal/store"
)

// TestCatalogValid 内置目录随二进制发布：全部条目须通过静态校验（坏条目在启动时
// 只会被跳过，测试兜底防手改 catalog.json 引入无效条目而不自知）。
func TestCatalogValid(t *testing.T) {
	entries := Catalog()
	if len(entries) == 0 {
		t.Fatal("内置目录为空")
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.ID] {
			t.Errorf("目录条目 %q 重复", e.ID)
			continue
		}
		seen[e.ID] = true
		if err := e.validate(); err != nil {
			t.Errorf("条目 %q 无效: %v", e.ID, err)
		}
		if e.Version == "" {
			t.Errorf("条目 %q 缺少 version", e.ID)
		}
	}
}

// TestInstallLifecycle 安装 → 已装状态 → 授权覆盖 → 升级版本比较 的完整链路。
func TestInstallLifecycle(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	entries := Catalog()
	if len(entries) == 0 {
		t.Skip("内置目录为空")
	}
	// 挑非 bundle 条目：bundle 安装会真实下载组件文件，生命周期测试用零网络形态
	e := entries[0]
	for _, cand := range entries {
		if cand.Type != "bundle" {
			e = cand
			break
		}
	}
	if e.Type == "bundle" {
		t.Skip("内置目录全是 bundle 条目")
	}

	// 1. 安装：落库 + version/source 标记 + admin 恒在授权
	if _, err := Install(db, e.ID, []string{"user"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, ok := db.AppByID(e.ID)
	if !ok {
		t.Fatal("安装后 AppByID 未命中")
	}
	if got.Version != e.Version || got.Source != SourceMarket {
		t.Errorf("version/source 标记错误: got %q/%q", got.Version, got.Source)
	}
	if got.Builtin || got.Locked {
		t.Errorf("商店应用不应 builtin/locked: %+v", got)
	}
	if !db.RoleCanAccessApp("admin", e.ID) || !db.RoleCanAccessApp("user", e.ID) {
		t.Error("admin/user 角色应已授权")
	}
	if db.RoleCanAccessApp("guest", e.ID) {
		t.Error("未选择的角色不应有授权")
	}

	// 2. 重复安装换角色：授权整组覆盖（guest 进、user 出）
	if _, err := Install(db, e.ID, []string{"guest"}); err != nil {
		t.Fatalf("重装: %v", err)
	}
	if db.RoleCanAccessApp("user", e.ID) {
		t.Error("重装换角色后旧授权应被移除")
	}
	if !db.RoleCanAccessApp("guest", e.ID) || !db.RoleCanAccessApp("admin", e.ID) {
		t.Error("重装后 guest/admin 应有授权")
	}

	// 3. 幽灵角色不落 role_apps
	if _, err := Install(db, e.ID, []string{"nobody-role"}); err != nil {
		t.Fatalf("重装(幽灵角色): %v", err)
	}
	if db.RoleCanAccessApp("nobody-role", e.ID) {
		t.Error("未知角色不应被写入授权")
	}

	// 4. List 聚合状态
	states := List(db)
	st := findState(states, e.ID)
	if st == nil || !st.Installed || !st.Managed || st.Upgradable {
		t.Errorf("已装状态聚合错误: %+v", st)
	}

	// 5. 目录版本升级 → Upgradable 翻转（直接改库模拟旧版本）
	db.UpsertApp(store.AppDef{ID: e.ID, Name: e.Name, Config: e.Config, Version: "0.0.1", Source: SourceMarket})
	st = findState(List(db), e.ID)
	if st == nil || !st.Upgradable {
		t.Errorf("目录版本较新时应提示可升级: %+v", st)
	}

	// 6. 手工注册的同 id 应用 → Conflict 标记（重装会覆盖）。
	//    注意 UpsertApp 的空值不覆盖语义：管理员编辑商店应用不清 source（保持升级链），
	//    因此先卸载再手工注册，模拟「先有手工应用、目录后收录同 id」的真实场景
	if err := db.DeleteApp(e.ID); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	db.UpsertApp(store.AppDef{ID: e.ID, Name: e.Name, Config: e.Config})
	st = findState(List(db), e.ID)
	if st == nil || !st.Conflict || st.Managed {
		t.Errorf("手工注册的同 id 应用应标记 Conflict: %+v", st)
	}

	// 7. 卸载走 store.DeleteApp：行与授权一并回收
	if err := db.DeleteApp(e.ID); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if _, ok := db.AppByID(e.ID); ok {
		t.Error("卸载后应用不应存在")
	}
	if db.RoleCanAccessApp("admin", e.ID) {
		t.Error("卸载后授权应回收")
	}
}

// TestInstallProtected 目录 id 撞内置/宿主锁定应用时拒绝安装。
func TestInstallProtected(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// 造一个与内置应用同 id 的目录条目场景：直接验证 Install 对 builtin 的守卫
	// （内置目录正常不会收录 "settings"，这里用 UpsertApp 把目标 id 锁成 builtin）
	target := Catalog()[0].ID
	db.UpsertApp(store.AppDef{ID: target, Name: "占位", Builtin: true})
	if _, err := Install(db, target, nil); err == nil {
		t.Fatal("撞 builtin 应用应拒绝安装")
	} else if err != ErrProtected {
		t.Fatalf("错误类型应为 ErrProtected: %v", err)
	}

	st := findState(List(db), target)
	if st == nil || !st.Protected {
		t.Errorf("撞内置应用的状态应标记 Protected: %+v", st)
	}
}

// TestInstallNotFound 未知 id 返回 ErrNotFound（路由层映射 404）。
func TestInstallNotFound(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if _, err := Install(db, "no-such-app", nil); err != ErrNotFound {
		t.Fatalf("错误类型应为 ErrNotFound: %v", err)
	}
}

// TestParseCatalogRemoteDoc 远程清单解析：顶层非法报错（调用方沿用现行目录），
// 个别坏条目跳过不拖垮整份清单——与内置目录共用同一套校验。
func TestParseCatalogRemoteDoc(t *testing.T) {
	if _, err := parseCatalog([]byte("not-json")); err == nil {
		t.Error("顶层非法 JSON 应报错")
	}
	doc := []byte(`{"version":1,"apps":[
		{"id":"bad","name":"坏条目","type":"web","config":{},"version":"1"},
		{"id":"good","name":"好条目","type":"web","config":{"kind":"web","url":"https://example.com/"},"version":"1"},
		{"id":"good","name":"重复 id","type":"web","config":{"kind":"web","url":"https://example.com/"},"version":"2"}
	]}`)
	es, err := parseCatalog(doc)
	if err != nil {
		t.Fatalf("parseCatalog: %v", err)
	}
	if len(es) != 1 || es[0].ID != "good" {
		t.Errorf("应只保留 1 个去重后的好条目: %+v", es)
	}
}

// TestParseCatalogObjectShape apps 对象映射形态（宿主把发布货架与商店共用同一
// catalog.json，如 grf-hotel remoteApps 货架）：id 取键名、按 id 稳定排序、
// 跨通道记录（缺 name 与 type）静默跳过；apps 既非数组也非对象仍报错。
func TestParseCatalogObjectShape(t *testing.T) {
	doc := []byte(`{"version":1,"updatedAt":"2026-10-04T00:33:55.685Z","apps":{
		"rooms":{"version":"v0.0.23","entry":"files/rooms/v0.0.23/rooms.js","minHostVersion":"v0.0.23"},
		"wiki":{"name":"维基百科","type":"web","config":{"kind":"web","url":"https://www.wikipedia.org/"},"version":"1.0.0"},
		"amap":{"name":"高德地图","type":"web","config":{"kind":"web","url":"https://www.amap.com/"},"version":"1.0.0"}
	}}`)
	es, err := parseCatalog(doc)
	if err != nil {
		t.Fatalf("parseCatalog: %v", err)
	}
	if len(es) != 2 || es[0].ID != "amap" || es[1].ID != "wiki" {
		t.Fatalf("应跳过跨通道记录并按 id 排序: %+v", es)
	}
	if es[1].Name != "维基百科" {
		t.Errorf("条目字段应完整解析: %+v", es[1])
	}
	// 解析结果稳定（内容不变时 RefreshNow 不应反复交换生效目录）
	again, _ := parseCatalog(doc)
	ab, _ := json.Marshal(es)
	bb, _ := json.Marshal(again)
	if string(ab) != string(bb) {
		t.Error("重复解析结果应稳定")
	}
	// apps 非数组/对象 → 整份清单非法
	if _, err := parseCatalog([]byte(`{"version":1,"apps":"nope"}`)); err == nil {
		t.Error("apps 非数组/对象应报错")
	}
	// 缺 apps = 空清单不报错（与数组形态行为一致）
	if es, err := parseCatalog([]byte(`{"version":1}`)); err != nil || len(es) != 0 {
		t.Errorf("缺 apps 应为空清单: %v %+v", err, es)
	}
}

// TestRemoteSwap 远程清单交换生效：active 被远程结果替换后 Catalog/Find 即反映
// 远程条目（Install 的目录查询同源），恢复后回落。refreshRemote 的网络部分不在
// 单测范围（fetchRemote 需真实 HTTP）。
func TestRemoteSwap(t *testing.T) {
	remote, err := parseCatalog([]byte(`{"version":1,"apps":[
		{"id":"remote-only","name":"远程独占应用","type":"web",
		 "config":{"kind":"web","url":"https://example.com/"},"version":"1.0.0"}]}`))
	if err != nil {
		t.Fatalf("parseCatalog: %v", err)
	}
	mu.Lock()
	prev := active
	active = remote
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); active = prev; mu.Unlock() })

	if _, err := Find("remote-only"); err != nil {
		t.Error("交换后远程条目应可被 Find 命中")
	}
	for _, e := range Catalog() {
		if e.ID != "remote-only" {
			t.Errorf("生效目录应只剩远程条目: %+v", e)
		}
	}
}

// TestSetCustomSources 自定义源清洗：trim/空项剔除/非法地址报错/去重/超限报错。
func TestSetCustomSources(t *testing.T) {
	t.Cleanup(func() { SetCustomSources(nil) })
	got, err := SetCustomSources([]string{" https://a/x.json ", "", "https://a/x.json"})
	if err != nil || len(got) != 1 || got[0] != "https://a/x.json" {
		t.Errorf("清洗结果错误: %v %v", got, err)
	}
	if _, err := SetCustomSources([]string{"ftp://bad/x"}); err == nil {
		t.Error("非法 scheme 应报错")
	}
	if _, err := SetCustomSources([]string{"not-a-url"}); err == nil {
		t.Error("裸字符串应报错")
	}
	var many []string
	for i := 0; i < MaxCustomSources+1; i++ {
		many = append(many, fmt.Sprintf("https://host%d/x.json", i))
	}
	if _, err := SetCustomSources(many); err == nil {
		t.Errorf("超过 %d 个应报错", MaxCustomSources)
	}
}

// TestSourcesOrder 源序：自定义在前、默认垫底（多默认源按声明序）、跨级重复去重。
func TestSourcesOrder(t *testing.T) {
	mu.Lock()
	prevCustom, prevDefault := customSrcs, defaultURLs
	customSrcs = []string{"https://mine/x.json", "https://default/x.json"}
	defaultURLs = []string{"https://default/x.json", "https://alt/x.json"}
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); customSrcs, defaultURLs = prevCustom, prevDefault; mu.Unlock() })

	got := Sources()
	want := []string{"https://mine/x.json", "https://default/x.json", "https://alt/x.json"}
	if len(got) != len(want) {
		t.Fatalf("源序错误: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("源序错误: %v", got)
			break
		}
	}
}

// TestParseDefaultSources OPENOS_MARKET_URL 多默认源解析：off/空 = 关闭；逗号分隔
// 逐项清洗；非法地址（非 https/超长）丢弃不拖垮列表；去重保序。
func TestParseDefaultSources(t *testing.T) {
	cases := []struct {
		name, raw string
		want      []string
	}{
		{"空串关闭", "", nil},
		{"off 关闭", " off ", nil},
		{"单项", "https://a/x.json", []string{"https://a/x.json"}},
		{"多项保序", "https://a/x.json, https://b/y.json", []string{"https://a/x.json", "https://b/y.json"}},
		{"去重", "https://a/x.json,https://a/x.json", []string{"https://a/x.json"}},
		{"非法丢弃", "http://insecure/x.json,https://a/x.json", []string{"https://a/x.json"}},
		{"全非法为空表", "http://a/x.json,not-a-url", nil},
	}
	for _, c := range cases {
		got := ParseDefaultSources(c.raw)
		if len(got) != len(c.want) {
			t.Errorf("%s: %v（期望 %v）", c.name, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("%s: %v（期望 %v）", c.name, got, c.want)
				break
			}
		}
	}
}

// TestRefreshNowMultiSource 多源合并（httptest 真实 HTTP）：自定义源遮蔽默认源同 id
// 条目（首见优先）；部分源失败以成功源合并为准；全部失败沿用现行目录。
func TestRefreshNowMultiSource(t *testing.T) {
	entry := func(id, name string) string {
		return fmt.Sprintf(`{"id":%q,"name":%q,"type":"web","config":{"kind":"web","url":"https://example.com/"},"version":"1.0.0"}`, id, name)
	}
	mine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"version":1,"apps":[%s,%s]}`, entry("shared", "自定义版"), entry("mine-only", "自定义独占"))
	}))
	off := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	def := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"version":1,"apps":[%s,%s]}`, entry("shared", "官方版"), entry("def-only", "官方独占"))
	}))
	alt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"version":1,"apps":[%s,%s]}`, entry("shared", "官方alt版"), entry("alt-only", "第二默认源独占"))
	}))
	defer mine.Close()
	defer off.Close()
	defer def.Close()
	defer alt.Close()

	restore := func() {
		mu.Lock()
		customSrcs, defaultURLs = nil, nil
		active = embeddedList
		mu.Unlock()
	}
	t.Cleanup(restore)

	// 1) 自定义(含失败源) + 多默认源：失败源缺席，shared 取自定义版（首见优先）、
	// 两个默认源独占条目都在、默认源间同 id 以前者为准
	mu.Lock()
	customSrcs = []string{mine.URL, off.URL}
	defaultURLs = []string{def.URL, alt.URL}
	active = embeddedList
	mu.Unlock()
	okN, total := RefreshNow()
	if okN != 3 || total != 4 {
		t.Errorf("合并计数错误: ok=%d total=%d（期望 3/4）", okN, total)
	}
	shared, _ := Find("shared")
	if shared.Name != "自定义版" {
		t.Errorf("shared 应被自定义源遮蔽: %+v", shared)
	}
	if _, err := Find("mine-only"); err != nil {
		t.Error("自定义独占条目应存在")
	}
	if _, err := Find("def-only"); err != nil {
		t.Error("默认源独占条目应存在")
	}
	if _, err := Find("alt-only"); err != nil {
		t.Error("第二默认源独占条目应存在")
	}

	// 2) 全部源失败：沿用现行（上一步的合并结果仍在）
	mu.Lock()
	customSrcs = []string{off.URL}
	defaultURLs = []string{off.URL}
	mu.Unlock()
	okN, total = RefreshNow()
	if okN != 0 {
		t.Errorf("全部失败时 ok 应为 0: %d", okN)
	}
	if _, err := Find("mine-only"); err != nil {
		t.Error("全部失败后应沿用现行目录")
	}
	if total != 4 {
		t.Errorf("沿用现行时 total 应为现行条目数 4: %d", total)
	}
}

// TestBundleValidate bundle 形态静态校验：地址强制 https、id/version 限安全字符（拼文件名）；
// versions 历史版本表同套防线（安全字符/https/去重）。
func TestBundleValidate(t *testing.T) {
	mk := func(id, ver, rawURL string) Entry {
		return Entry{ID: id, Name: "组件应用", Type: "bundle", Version: ver,
			Config: []byte(fmt.Sprintf(`{"kind":"bundle","url":%q}`, rawURL))}
	}
	if err := mk("chat", "1.0.0", "https://example.com/chat.js").validate(); err != nil {
		t.Errorf("合法 bundle 条目不应报错: %v", err)
	}
	if err := mk("chat", "1.0.0", "http://example.com/chat.js").validate(); err == nil {
		t.Error("http 地址的 bundle 条目应被拒（可执行代码强制 https）")
	}
	if err := mk("chat", "", "https://example.com/chat.js").validate(); err == nil {
		t.Error("缺少 version 应被拒")
	}
	if err := mk("../evil", "1.0.0", "https://example.com/chat.js").validate(); err == nil {
		t.Error("id 含路径穿越字符应被拒")
	}
	if err := mk("chat", "a/b", "https://example.com/chat.js").validate(); err == nil {
		t.Error("version 含路径穿越字符应被拒")
	}
	// versions 历史版本表
	ok := mk("chat", "1.0.0", "https://example.com/chat.js")
	ok.Versions = []EntryVersion{{Version: "0.9.0", URL: "https://example.com/chat-0.9.0.js"}}
	if err := ok.validate(); err != nil {
		t.Errorf("合法 versions 不应报错: %v", err)
	}
	badChars := ok
	badChars.Versions = []EntryVersion{{Version: "a/b", URL: "https://example.com/x.js"}}
	if err := badChars.validate(); err == nil {
		t.Error("历史版本号含穿越字符应被拒")
	}
	badURL := ok
	badURL.Versions = []EntryVersion{{Version: "0.9.0", URL: "http://example.com/x.js"}}
	if err := badURL.validate(); err == nil {
		t.Error("历史版本 http 地址应被拒")
	}
	dup := ok
	dup.Versions = []EntryVersion{{Version: "0.9.0", URL: "https://example.com/a.js"}, {Version: "0.9.0", URL: "https://example.com/b.js"}}
	if err := dup.validate(); err == nil {
		t.Error("历史版本重复应被拒")
	}
}

// TestBundleInstallLifecycle bundle 安装全链路（httptest 真实 HTTP）：下载落盘 →
// BundlePath 按已装版本解析 → 升级换代清理旧文件 → RemoveBundles 清理 → 下载失败不入库。
func TestBundleInstallLifecycle(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const body = "export default { template: '<div>hi</div>' }\n"
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if strings.HasSuffix(r.URL.Path, "broken.js") {
			http.Error(w, "boom", 500)
			return
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	resetBundleDir(t, filepath.Join(t.TempDir(), "bundles"))

	// bundle 下载走拨号防线（marketGet）：把测试服务主机登记为配置源，否则回环
	// 地址会被 SSRF 防线拒绝（生产自定义源已强制 https，回环例外仅限本地）
	mu.Lock()
	prevSrcs := customSrcs
	customSrcs = append([]string{srv.URL}, customSrcs...)
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); customSrcs = prevSrcs; mu.Unlock() })

	entry := Entry{ID: "demo-bundle", Name: "演示组件", Type: "bundle", Version: "1.0.0",
		Config: []byte(fmt.Sprintf(`{"kind":"bundle","url":%q}`, srv.URL+"/demo.js"))}
	broken := Entry{ID: "broken-bundle", Name: "坏源组件", Type: "bundle", Version: "1.0.0",
		Config: []byte(fmt.Sprintf(`{"kind":"bundle","url":%q}`, srv.URL+"/broken.js"))}
	mu.Lock()
	prev := active
	active = []Entry{entry, broken}
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); active = prev; mu.Unlock() })

	// 1) 下载失败：Install 报错且不入库（不产生装了跑不起来的应用）
	if _, err := Install(db, "broken-bundle", nil); err == nil {
		t.Fatal("下载失败应导致安装失败")
	}
	if _, ok := db.AppByID("broken-bundle"); ok {
		t.Error("下载失败的应用不应入库")
	}

	// 2) 正常安装：入库 + 文件落盘 + BundlePath 可解析
	if _, err := Install(db, "demo-bundle", []string{"user"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cur, ok := db.AppByID("demo-bundle")
	if !ok {
		t.Fatal("安装后未入库")
	}
	p, err := BundlePath(cur)
	if err != nil {
		t.Fatalf("BundlePath: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil || string(raw) != body {
		t.Errorf("落盘内容不一致: %v", err)
	}

	// 3) 非 bundle 应用 / 非 market 来源 → BundlePath 拒绝
	if _, err := BundlePath(store.AppDef{ID: "demo-bundle", Version: "1.0.0", Source: SourceMarket, Config: []byte(`{"kind":"web","url":"https://x/"}`)}); err != ErrNoBundle {
		t.Errorf("web 形态应返回 ErrNoBundle: %v", err)
	}
	if _, err := BundlePath(store.AppDef{ID: "demo-bundle", Version: "1.0.0", Config: entry.Config}); err != ErrNoBundle {
		t.Errorf("非 market 来源应返回 ErrNoBundle: %v", err)
	}

	// 4) 升级换代：目录版本更新重装 → 新文件落盘、**旧版本文件保留**（按版本回滚的根基）
	up := entry
	up.Version = "1.1.0"
	up.Config = []byte(fmt.Sprintf(`{"kind":"bundle","url":%q}`, srv.URL+"/v2.js"))
	up.Versions = []EntryVersion{
		{Version: "1.1.0", URL: srv.URL + "/v2.js"},
		{Version: "1.0.0", URL: srv.URL + "/demo.js"},
	}
	mu.Lock()
	active = []Entry{up}
	mu.Unlock()
	if _, err := Install(db, "demo-bundle", []string{"user"}); err != nil {
		t.Fatalf("升级安装: %v", err)
	}
	cur, _ = db.AppByID("demo-bundle")
	if cur.Version != "1.1.0" {
		t.Fatalf("升级后版本应为 1.1.0: %s", cur.Version)
	}
	if _, err := BundlePath(cur); err != nil {
		t.Fatalf("升级后 BundlePath: %v", err)
	}
	if _, err := os.Stat(bundleFile("demo-bundle", "1.0.0")); err != nil {
		t.Error("旧版本文件应保留（回滚根基）")
	}
	if !db.RoleCanAccessApp("user", "demo-bundle") {
		t.Error("升级时显式带角色应保持授权")
	}

	// 5) 切换回旧版本（回滚）：版本回退、授权不变、本地文件直接复用（零下载）
	hitsBefore := hits
	if _, err := SwitchVersion(db, "demo-bundle", "1.0.0"); err != nil {
		t.Fatalf("SwitchVersion: %v", err)
	}
	cur, _ = db.AppByID("demo-bundle")
	if cur.Version != "1.0.0" {
		t.Errorf("切换后版本应为 1.0.0: %s", cur.Version)
	}
	if !db.RoleCanAccessApp("user", "demo-bundle") || !db.RoleCanAccessApp("admin", "demo-bundle") {
		t.Error("版本切换应保留现有授权")
	}
	if hits != hitsBefore {
		t.Errorf("本地已有该版本文件应复用不再下载: hits %d → %d", hitsBefore, hits)
	}
	if p, err := BundlePath(cur); err != nil {
		t.Fatalf("回滚后 BundlePath: %v", err)
	} else if raw, _ := os.ReadFile(p); string(raw) != body {
		t.Error("回滚后组件内容应为旧版本")
	}

	// 6) 卸载清理（含全部历史版本文件）
	RemoveBundles("demo-bundle")
	if _, err := os.Stat(bundleFile("demo-bundle", "1.1.0")); !os.IsNotExist(err) {
		t.Error("卸载后组件文件应被清理")
	}
	if _, err := os.Stat(bundleFile("demo-bundle", "1.0.0")); !os.IsNotExist(err) {
		t.Error("卸载后历史版本文件应一并清理")
	}
}

// resetBundleDir 测试助手：SetBundleDir 的 sync.Once 全局只生效一次，跨测试重置
// bundleDir（前一个测试的临时目录随测试结束已被清理）。
func resetBundleDir(t *testing.T, dir string) {
	t.Helper()
	bundleDirOnce = sync.Once{}
	bundleDir = ""
	SetBundleDir(dir)
}

// TestSwitchVersion 按版本切换的边界：未安装/非 bundle/未知版本/非法版本号报错；
// 目录 versions 表未列但本地有文件的版本仍可切换（货架清过历史也能离线回滚）。
func TestSwitchVersion(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const v1Body, v2Body = "export default { template: '<div>v1</div>' }\n", "export default { template: '<div>v2</div>' }\n"
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if strings.HasSuffix(r.URL.Path, "/v1.js") {
			fmt.Fprint(w, v1Body)
			return
		}
		fmt.Fprint(w, v2Body)
	}))
	defer srv.Close()
	resetBundleDir(t, filepath.Join(t.TempDir(), "bundles"))
	mu.Lock()
	prevSrcs := customSrcs
	customSrcs = append([]string{srv.URL}, customSrcs...)
	prev := active
	active = []Entry{{
		ID: "sw-app", Name: "切换演示", Type: "bundle", Version: "2.0.0",
		Config:   []byte(fmt.Sprintf(`{"kind":"bundle","url":%q}`, srv.URL+"/v2.js")),
		Versions: []EntryVersion{{Version: "2.0.0", URL: srv.URL + "/v2.js"}, {Version: "1.0.0", URL: srv.URL + "/v1.js"}},
	}}
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		customSrcs = prevSrcs
		active = prev
		mu.Unlock()
	})

	// 未安装 → ErrNotInstalled
	if _, err := SwitchVersion(db, "sw-app", "1.0.0"); err != ErrNotInstalled {
		t.Fatalf("未安装应报 ErrNotInstalled: %v", err)
	}
	// 安装最新版（下载 v2）
	if _, err := Install(db, "sw-app", []string{"user"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// 未知版本 / 非法版本号
	if _, err := SwitchVersion(db, "sw-app", "9.9.9"); err != ErrVersionNotFound {
		t.Fatalf("未知版本应报 ErrVersionNotFound: %v", err)
	}
	if _, err := SwitchVersion(db, "sw-app", "../evil"); err != ErrBadVersion {
		t.Fatalf("非法版本号应报 ErrBadVersion: %v", err)
	}
	// 切到目录声明的历史版本：从货架下载 v1、授权保持
	if _, err := SwitchVersion(db, "sw-app", "1.0.0"); err != nil {
		t.Fatalf("SwitchVersion: %v", err)
	}
	cur, _ := db.AppByID("sw-app")
	if cur.Version != "1.0.0" || !db.RoleCanAccessApp("user", "sw-app") {
		t.Errorf("切换后版本/授权错误: v=%s user=%v", cur.Version, db.RoleCanAccessApp("user", "sw-app"))
	}
	// 目录清掉 1.0.0 历史（货架回收）→ 本地文件仍在，仍可切回
	mu.Lock()
	e := active[0]
	e.Versions = []EntryVersion{{Version: "2.0.0", URL: srv.URL + "/v2.js"}}
	active[0] = e
	mu.Unlock()
	if _, err := SwitchVersion(db, "sw-app", "1.0.0"); err != nil {
		t.Fatalf("本地已有文件的版本应可离线切换: %v", err)
	}
	// 目录与本地都没有 → ErrVersionNotFound
	if _, err := SwitchVersion(db, "sw-app", "0.5.0"); err != ErrVersionNotFound {
		t.Fatalf("目录与本地均无应报 ErrVersionNotFound: %v", err)
	}
	// 非 bundle 条目 → ErrNotBundle
	mu.Lock()
	active = append(active, Entry{ID: "sw-web", Name: "网页演示", Type: "web", Version: "1.0.0",
		Config: []byte(`{"kind":"web","url":"https://example.com/"}`)})
	mu.Unlock()
	if _, err := Install(db, "sw-web", nil); err != nil {
		t.Fatalf("Install web: %v", err)
	}
	if _, err := SwitchVersion(db, "sw-web", "1.0.0"); err != ErrNotBundle {
		t.Fatalf("web 形态应报 ErrNotBundle: %v", err)
	}
}

// TestAutoUpgrade 自动升级：开关**默认开**；商店安装的应用对齐目录版本并**保留授权**；
// 显式关闭后不动作；目录最新版指针回退时跟随回退；非商店来源的应用不动。
// 切旧版避开问题版本（skip）语义见 TestAutoUpgradeSkipVersion。
func TestAutoUpgrade(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// 用 web 条目驱动（Install 不走网络），目录版本直接改 active 模拟货架发布/回退
	setActive := func(ver string) {
		mu.Lock()
		defer mu.Unlock()
		active = []Entry{{
			ID: "au-app", Name: "自动升级演示", Type: "web", Version: ver,
			Config: []byte(`{"kind":"web","url":"https://example.com/"}`),
		}}
	}
	mu.Lock()
	prev := active
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); active = prev; mu.Unlock() })

	setActive("1.0.0")
	if _, err := Install(db, "au-app", []string{"user"}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// 默认开：目录发新版即对齐、授权保留（user 不掉）
	if !AutoUpgradeOn(db) {
		t.Fatal("自动升级默认应为开")
	}
	setActive("2.0.0")
	got := AutoUpgrade(db)
	if len(got) != 1 || got[0].ID != "au-app" || got[0].Version != "2.0.0" {
		t.Fatalf("自动升级结果错误: %+v", got)
	}
	cur, _ := db.AppByID("au-app")
	if cur.Version != "2.0.0" || !db.RoleCanAccessApp("user", "au-app") {
		t.Errorf("升级后版本/授权错误: v=%s user=%v", cur.Version, db.RoleCanAccessApp("user", "au-app"))
	}
	// 版本已一致 → 幂等空转
	if got := AutoUpgrade(db); got != nil {
		t.Errorf("版本一致时不应有变更: %+v", got)
	}

	// 显式关闭 → 不动作
	if err := SetAutoUpgrade(db, false); err != nil {
		t.Fatalf("SetAutoUpgrade: %v", err)
	}
	setActive("3.0.0")
	if got := AutoUpgrade(db); got != nil {
		t.Errorf("开关关闭时不应有变更: %+v", got)
	}
	if cur, _ := db.AppByID("au-app"); cur.Version != "2.0.0" {
		t.Errorf("开关关闭时版本不应变化: %s", cur.Version)
	}
	if err := SetAutoUpgrade(db, true); err != nil {
		t.Fatalf("SetAutoUpgrade: %v", err)
	}

	// 目录指针回退（货架全局回滚）→ 跟随回退
	setActive("1.0.0")
	got = AutoUpgrade(db)
	if len(got) != 1 || got[0].Version != "1.0.0" {
		t.Fatalf("目录回退应跟随回退: %+v", got)
	}
	if cur, _ = db.AppByID("au-app"); cur.Version != "1.0.0" {
		t.Errorf("跟随回退后版本应为 1.0.0: %s", cur.Version)
	}

	// 非商店来源（管理员手工注册）不受自动升级影响
	db.UpsertApp(store.AppDef{ID: "au-manual", Name: "手工应用", Config: []byte(`{"kind":"web","url":"https://example.com/"}`), Version: "0.1.0"})
	mu.Lock()
	active = append(active, Entry{ID: "au-manual", Name: "手工应用", Type: "web", Version: "9.9.9",
		Config: []byte(`{"kind":"web","url":"https://example.com/"}`)})
	mu.Unlock()
	AutoUpgrade(db)
	if cur, ok := db.AppByID("au-manual"); !ok || cur.Version != "0.1.0" {
		t.Errorf("非商店来源不应被自动升级: %+v", cur)
	}
}

// TestAutoUpgradeSkipVersion 跳过问题版本：手动切旧版 = 避开当时的目录最新版，
// 自动升级跳过该版本；目录发布不同（更新）版本后恢复自动升级并清除记录；
// 手动 Install 到目录最新版也清除记录。
func TestAutoUpgradeSkipVersion(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const body = "export default { template: '<div>hi</div>' }\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	resetBundleDir(t, filepath.Join(t.TempDir(), "bundles"))
	mkEntry := func(ver string) Entry {
		return Entry{ID: "skip-app", Name: "跳过演示", Type: "bundle", Version: ver,
			Config:   []byte(fmt.Sprintf(`{"kind":"bundle","url":%q}`, srv.URL+"/"+ver+".js")),
			Versions: []EntryVersion{{Version: ver, URL: srv.URL + "/" + ver + ".js"}, {Version: "1.0.0", URL: srv.URL + "/1.0.0.js"}}}
	}
	setActive := func(ver string) {
		mu.Lock()
		defer mu.Unlock()
		active = []Entry{mkEntry(ver)}
	}
	mu.Lock()
	prevSrcs, prev := customSrcs, active
	customSrcs = append([]string{srv.URL}, customSrcs...)
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); customSrcs, active = prevSrcs, prev; mu.Unlock() })

	// 装 1.0.0 → 目录发 2.0.0（疑似问题版本）→ 手动切回 1.0.0 = 避开 2.0.0
	setActive("1.0.0")
	if _, err := Install(db, "skip-app", []string{"user"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	setActive("2.0.0")
	if _, err := SwitchVersion(db, "skip-app", "1.0.0"); err != nil {
		t.Fatalf("SwitchVersion: %v", err)
	}
	if v := skipVersions(db)["skip-app"]; v != "2.0.0" {
		t.Fatalf("切旧版应记录被避开的目录版本 2.0.0: %q", v)
	}

	// 目录仍是 2.0.0：自动升级跳过（版本停在 1.0.0）
	if got := AutoUpgrade(db); got != nil {
		t.Errorf("被避开的版本应跳过自动升级: %+v", got)
	}
	if cur, _ := db.AppByID("skip-app"); cur.Version != "1.0.0" {
		t.Fatalf("跳过期间版本不应变化: %s", cur.Version)
	}

	// 目录发布 2.1.0（≠被避开的 2.0.0）→ 恢复自动升级并清除记录
	setActive("2.1.0")
	got := AutoUpgrade(db)
	if len(got) != 1 || got[0].Version != "2.1.0" {
		t.Fatalf("新版本发布后应恢复自动升级: %+v", got)
	}
	if v := skipVersions(db)["skip-app"]; v != "" {
		t.Errorf("恢复升级后应清除跳过记录: %q", v)
	}
	if cur, _ := db.AppByID("skip-app"); cur.Version != "2.1.0" || !db.RoleCanAccessApp("user", "skip-app") {
		t.Errorf("恢复升级后版本/授权错误: v=%s", cur.Version)
	}

	// 再次避开 → 手动点「更新」（Install 到目录最新版）= 主动接受，清除记录
	setActive("3.0.0")
	if _, err := SwitchVersion(db, "skip-app", "2.1.0"); err != nil {
		t.Fatalf("SwitchVersion: %v", err)
	}
	if v := skipVersions(db)["skip-app"]; v != "3.0.0" {
		t.Fatalf("应记录避开 3.0.0: %q", v)
	}
	if _, err := Install(db, "skip-app", appRoles(db, "skip-app")); err != nil {
		t.Fatalf("手动更新: %v", err)
	}
	if v := skipVersions(db)["skip-app"]; v != "" {
		t.Errorf("手动更新到目录最新版应清除跳过记录: %q", v)
	}
}

func findState(states []State, id string) *State {
	for i := range states {
		if states[i].ID == id {
			return &states[i]
		}
	}
	return nil
}
