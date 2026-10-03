package market

import (
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

func findState(states []State, id string) *State {
	for i := range states {
		if states[i].ID == id {
			return &states[i]
		}
	}
	return nil
}
