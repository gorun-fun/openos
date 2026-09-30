package store

import (
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
