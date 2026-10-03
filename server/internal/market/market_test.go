package market

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
	e := entries[0]

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

// TestSourcesOrder 源序：自定义在前、默认垫底、与默认重复的自定义去重。
func TestSourcesOrder(t *testing.T) {
	mu.Lock()
	prevCustom, prevDefault := customSrcs, defaultURL
	customSrcs = []string{"https://mine/x.json", "https://default/x.json"}
	defaultURL = "https://default/x.json"
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); customSrcs, defaultURL = prevCustom, prevDefault; mu.Unlock() })

	got := Sources()
	want := []string{"https://mine/x.json", "https://default/x.json"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("源序错误: %v", got)
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
	defer mine.Close()
	defer off.Close()
	defer def.Close()

	restore := func() {
		mu.Lock()
		customSrcs, defaultURL = nil, ""
		active = embeddedList
		mu.Unlock()
	}
	t.Cleanup(restore)

	// 1) 自定义(含失败源) + 默认：失败源缺席，shared 取自定义版（首见优先）
	mu.Lock()
	customSrcs = []string{mine.URL, off.URL}
	defaultURL = def.URL
	active = embeddedList
	mu.Unlock()
	okN, total := RefreshNow()
	if okN != 2 || total != 3 {
		t.Errorf("合并计数错误: ok=%d total=%d（期望 2/3）", okN, total)
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

	// 2) 全部源失败：沿用现行（上一步的合并结果仍在）
	mu.Lock()
	customSrcs = []string{off.URL}
	defaultURL = off.URL
	mu.Unlock()
	okN, total = RefreshNow()
	if okN != 0 {
		t.Errorf("全部失败时 ok 应为 0: %d", okN)
	}
	if _, err := Find("mine-only"); err != nil {
		t.Error("全部失败后应沿用现行目录")
	}
	if total != 3 {
		t.Errorf("沿用现行时 total 应为现行条目数 3: %d", total)
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
