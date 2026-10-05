package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T, opts ...Option) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(dir, opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestSeedFeatureTrimming 验证功能裁剪：VFS/goder 关闭时对应内置应用不注册。
func TestSeedFeatureTrimming(t *testing.T) {
	full := openTestDB(t)
	ids := func(db *DB) map[string]bool {
		apps, err := db.ListApps()
		if err != nil {
			t.Fatalf("ListApps: %v", err)
		}
		m := map[string]bool{}
		for _, a := range apps {
			m[a.ID] = true
		}
		return m
	}
	m := ids(full)
	for _, id := range []string{"terminal", "files", "docs", "sheets", "slides", "pdf", "goder", "calculator", "settings"} {
		if !m[id] {
			t.Errorf("默认模式应注册 %s", id)
		}
	}

	trimmed := openTestDB(t, WithoutVFS(), WithoutGoder())
	m = ids(trimmed)
	for _, id := range []string{"terminal", "files", "docs", "sheets", "slides", "pdf", "goder"} {
		if m[id] {
			t.Errorf("裁剪模式不应注册 %s", id)
		}
	}
	for _, id := range []string{"calculator", "settings", "notes", "welcome"} {
		if !m[id] {
			t.Errorf("裁剪模式仍应注册 %s", id)
		}
	}
	// 首次建库的默认授权也不应包含被裁剪的应用
	access, _ := trimmed.RoleAccessMap()
	for role, apps := range access {
		for _, id := range apps {
			if vfsAppIDs[id] || id == goderAppID {
				t.Errorf("角色 %s 不应授权被裁剪的应用 %s", role, id)
			}
		}
	}
}

// TestPruneBefore 验证保留期清理（审计 / 聊天，含时间边界）。
func TestPruneBefore(t *testing.T) {
	db := openTestDB(t)
	db.Audit("alice", "login.ok", "", "")
	db.Audit("bob", "login.fail", "", "")
	// 手工回填两条 100 天前的旧记录
	old := time.Now().AddDate(0, 0, -100).UTC().Format("2006-01-02 15:04:05")
	db.Exec(`INSERT INTO audit_log(username,action,detail,ip,created_at) VALUES('old','','','','` + old + `')`)
	db.Exec(`INSERT INTO chat_messages(username,role,content,created_at) VALUES('__term__','user','x','` + old + `')`)

	n, err := db.PruneAuditBefore(time.Now().AddDate(0, 0, -90))
	if err != nil || n != 1 {
		t.Fatalf("PruneAuditBefore = %d, %v; want 1", n, err)
	}
	n, err = db.PruneChatBefore(time.Now().AddDate(0, 0, -90))
	if err != nil || n != 1 {
		t.Fatalf("PruneChatBefore = %d, %v; want 1（__term__ 孤儿也应被清理）", n, err)
	}
	// 新记录保留
	entries, _ := db.ListAudit(10)
	if len(entries) != 2 {
		t.Fatalf("清理后应保留 2 条新审计，实际 %d", len(entries))
	}
}

// TestSystemKVMigration 验证 jwt_secret 从 user_settings 的 __system 迁入 system_kv。
func TestSystemKVMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "openos.db")
	// 先用「旧版布局」建库：jwt_secret 挂在 user_settings 的 __system 名下
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.SetUserSetting("__system", "jwt_secret", "legacy-secret")
	db.SetUserSetting("alice", "wallpaper", "2") // 用户设置不受迁移影响
	db.Close()

	os.Remove(path + "-wal")
	os.Remove(path + "-shm")
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf(" reopen: %v", err)
	}
	defer db2.Close()
	if v, _ := db2.GetSystemKV("jwt_secret"); v != "legacy-secret" {
		t.Fatalf("jwt_secret 应已迁入 system_kv，实际 %q", v)
	}
	if v, _ := db2.GetUserSetting("__system", "jwt_secret"); v != "" {
		t.Fatalf("user_settings 中的旧值应已删除，实际 %q", v)
	}
	if v, _ := db2.GetUserSetting("alice", "wallpaper"); v != "2" {
		t.Fatalf("用户设置不应被迁移波及，实际 %q", v)
	}
}

// TestAppByIDAndRoleAccess 应用代理路由逐请求用到的两个查询。
func TestAppByIDAndRoleAccess(t *testing.T) {
	db := openTestDB(t)
	if err := db.UpsertApp(AppDef{ID: "px1", Name: "代理示例", Config: []byte(`{"kind":"proxy","url":"https://x.example/"}`)}); err != nil {
		t.Fatalf("UpsertApp: %v", err)
	}
	a, ok := db.AppByID("px1")
	if !ok || a.Name != "代理示例" || string(a.Config) != `{"kind":"proxy","url":"https://x.example/"}` {
		t.Fatalf("AppByID 结果不符：%+v ok=%v", a, ok)
	}
	if _, ok := db.AppByID("missing"); ok {
		t.Fatal("不存在的应用应返回 false")
	}
	if err := db.SetRoleApps("user", []string{"px1"}); err != nil {
		t.Fatalf("SetRoleApps: %v", err)
	}
	if !db.RoleCanAccessApp("user", "px1") {
		t.Fatal("授权角色应可访问")
	}
	if db.RoleCanAccessApp("admin", "px1") {
		t.Fatal("未授权角色不应可访问")
	}
}

// TestChatRoomHistory 聊天室历史：插入 → 最新一页 → before 分页 → 附件快照回读。
func TestChatRoomHistory(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	for i := 1; i <= 7; i++ {
		m := ChatRoomMessage{From: "alice", Text: fmt.Sprintf("m%d", i), TS: int64(1000 + i)}
		if i == 3 {
			m.Attach = &AttachmentRef{ID: "abc123", Name: "图.png", MIME: "image/png", Size: 88}
		}
		if err := db.InsertChatRoomMessage(m); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// 最新一页（limit 5）：正序 m3..m7，m3 带附件快照
	page, err := db.ChatRoomHistory(0, 5)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(page) != 5 || page[0].Text != "m3" || page[4].Text != "m7" {
		t.Fatalf("最新页顺序错误: %+v", page)
	}
	if page[0].Attach == nil || page[0].Attach.Name != "图.png" {
		t.Fatalf("附件快照回读错误: %+v", page[0].Attach)
	}

	// 上一页：before=m3.id → m1..m2
	page2, err := db.ChatRoomHistory(page[0].ID, 5)
	if err != nil {
		t.Fatalf("history2: %v", err)
	}
	if len(page2) != 2 || page2[0].Text != "m1" || page2[1].Text != "m2" {
		t.Fatalf("翻页错误: %+v", page2)
	}

	// 无附件消息 attach 字段为 nil
	if page[4].Attach != nil {
		t.Fatalf("无附件消息 Attach 应为 nil: %+v", page[4])
	}
}

// TestExternalIdentityRoleFiltering 外部身份模式（OPENOS_AUTH_MODE=external）下
// user/guest 是宿主机制角色，不进角色目录与角色清单；独立部署模式保持内置三角色。
func TestExternalIdentityRoleFiltering(t *testing.T) {
	// 模拟宿主集成库：guest 用户存在（访客兜底桌面）且持有应用授权
	seedGuest := func(db *DB) {
		if _, err := db.Exec(`INSERT INTO users(username,password_hash,salt,role,display_name,token_version)
			VALUES('g1','','','guest','访客甲',0)`); err != nil {
			t.Fatalf("seed guest user: %v", err)
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO role_apps(role,app_id) VALUES('guest','calculator')`); err != nil {
			t.Fatalf("seed guest access: %v", err)
		}
	}
	has := func(list []string, k string) bool {
		for _, s := range list {
			if s == k {
				return true
			}
		}
		return false
	}

	standalone := openTestDB(t)
	seedGuest(standalone)
	if roles, err := standalone.ListRoles(); err != nil {
		t.Fatalf("ListRoles: %v", err)
	} else if !has(roles, "admin") || !has(roles, "user") || !has(roles, "guest") {
		t.Fatalf("独立部署应含内置三角色: %v", roles)
	}
	if defs, err := standalone.ListRoleDefs(); err != nil {
		t.Fatalf("ListRoleDefs: %v", err)
	} else {
		keys := map[string]bool{}
		for _, d := range defs {
			keys[d.Key] = true
		}
		if !keys["user"] || !keys["guest"] {
			t.Fatalf("独立部署角色目录应含 user/guest: %v", keys)
		}
	}

	external := openTestDB(t, ExternalIdentity())
	seedGuest(external)
	if roles, err := external.ListRoles(); err != nil {
		t.Fatalf("ListRoles: %v", err)
	} else if has(roles, "user") || has(roles, "guest") {
		t.Fatalf("外部身份模式角色清单不应含 user/guest: %v", roles)
	} else if !has(roles, "admin") {
		t.Fatalf("外部身份模式应保留 admin: %v", roles)
	}
	if defs, err := external.ListRoleDefs(); err != nil {
		t.Fatalf("ListRoleDefs: %v", err)
	} else {
		for _, d := range defs {
			if d.Key == "user" || d.Key == "guest" {
				t.Fatalf("外部身份模式角色目录不应含 %s（访客机制角色只留数据不进目录）", d.Key)
			}
		}
	}
	// 存储不动：guest 的授权仍在（访客桌面 Dock 靠它）
	m, err := external.RoleAccessMap()
	if err != nil {
		t.Fatalf("RoleAccessMap: %v", err)
	}
	if len(m["guest"]) == 0 {
		t.Fatalf("guest 应用授权应保留: %v", m)
	}
}
