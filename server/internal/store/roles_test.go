// roles_test.go — declare 收敛保护：清单外 host 角色仍被用户绑定时保留并报告，
// 用户迁走后重声明幂等收敛。
package store

import "testing"

func TestDeclareHostRolesUnresolved(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES('u1','h','s','rb','',1)`); err != nil {
		t.Fatal(err)
	}
	decl := func(entries ...HostRoleEntry) []StaleRole {
		t.Helper()
		unresolved, err := db.DeclareHostRoles(entries, nil)
		if err != nil {
			t.Fatalf("declare: %v", err)
		}
		return unresolved
	}

	// 初次声明 ra+rb
	if u := decl(HostRoleEntry{Key: "ra", Label: "A"}, HostRoleEntry{Key: "rb", Label: "B"}); len(u) != 0 {
		t.Fatalf("first declare should have no unresolved: %v", u)
	}

	// rb 已被用户绑定：重声明清单只留 ra 时 rb 保留并报告，不悬空删除
	u := decl(HostRoleEntry{Key: "ra", Label: "A"})
	if len(u) != 1 || u[0].Key != "rb" || u[0].BoundUsers != 1 {
		t.Fatalf("bound stale role should be reported: %+v", u)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM roles WHERE key='rb' AND managed_by='host'`).Scan(&n)
	if n != 1 {
		t.Fatal("bound stale role must be retained")
	}

	// 用户迁走后重声明：rb 被收敛移除，unresolved 为空
	if _, err := db.Exec(`UPDATE users SET role='ra' WHERE username='u1'`); err != nil {
		t.Fatal(err)
	}
	if u := decl(HostRoleEntry{Key: "ra", Label: "A"}); len(u) != 0 {
		t.Fatalf("resolved role should be removed: %+v", u)
	}
	db.QueryRow(`SELECT COUNT(*) FROM roles WHERE key='rb'`).Scan(&n)
	if n != 0 {
		t.Fatal("unbound stale role must be removed after re-declare")
	}
}
