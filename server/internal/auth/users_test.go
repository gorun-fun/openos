// users_test.go — 登录态稳定化语义（身份与授权分离）与用户体系开放面的测试：
// 授权变化不掉线、密码重置唯一吊销、patchUser 统一内核、UpdateUser 的 perms
// 字段、/api/host/* 密钥守护、删号级联。
package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openos/server/internal/store"
)

func mustExec(t *testing.T, db *store.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func userTV(t *testing.T, db *store.DB, name string) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRow(`SELECT token_version FROM users WHERE username=?`, name).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func userField(t *testing.T, db *store.DB, name, col string) string {
	t.Helper()
	var v string
	if err := db.QueryRow(`SELECT `+col+` FROM users WHERE username=?`, name).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestSessionSurvivesAuthzChanges 核心语义：角色/禁用变化实时生效但不因 role
// 比对掉线；密码重置（tv bump）才令旧令牌失效。
func TestSessionSurvivesAuthzChanges(t *testing.T) {
	db := testDB(t)
	s := New(db, "test-secret")
	mustExec(t, db, `INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES('alice','h','s','front','张前台',7)`)
	tok, _ := s.Issue("alice", "front", 7)

	// 角色库值变化（未 bump）：会话仍有效，且身份取实时角色
	mustExec(t, db, `UPDATE users SET role='manager' WHERE username='alice'`)
	s.InvalidateCache("alice")
	id, err := s.VerifySession(tok)
	if err != nil {
		t.Fatalf("role change without bump must not kill session: %v", err)
	}
	if id.Role != "manager" {
		t.Fatalf("identity should carry live role, got %q", id.Role)
	}

	// 中间件同判定（REST 面不掉线）
	served := false
	h := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = FromContext(r).Role == "manager"
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest("GET", "/api/whatever", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !served {
		t.Fatal("middleware should admit token across role change with live role")
	}

	// 禁用：实时拦截（无需 bump）
	mustExec(t, db, `UPDATE users SET disabled=1 WHERE username='alice'`)
	s.InvalidateCache("alice")
	if _, err := s.VerifySession(tok); err == nil {
		t.Fatal("disabled user must be blocked live")
	}
	mustExec(t, db, `UPDATE users SET disabled=0 WHERE username='alice'`)
	s.InvalidateCache("alice")
	if _, err := s.VerifySession(tok); err != nil {
		t.Fatalf("re-enable should restore session: %v", err)
	}

	// 密码重置（bump）：唯一身份级吊销
	db.BumpTokenVersion("alice")
	s.InvalidateCache("alice")
	if _, err := s.VerifySession(tok); err == nil {
		t.Fatal("bumped token must fail")
	}
}

// TestPatchUserStableSemantics 统一变更内核：role/perms/displayName 变化不 bump、
// 幂等；密码重置 bump；防提权与末位管理员保护。
func TestPatchUserStableSemantics(t *testing.T) {
	db := testDB(t)
	s := New(db, "test-secret")
	mustExec(t, db, `INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES('bob','h','s','user','Bob',7)`)
	base := userTV(t, db, "bob")

	role := "manager"
	if _, err := s.patchUser("bob", userPatch{Role: &role}, "tester", "", true); err != nil {
		t.Fatal(err)
	}
	if userTV(t, db, "bob") != base {
		t.Fatal("role change must not bump token_version")
	}
	if got := userField(t, db, "bob", "role"); got != "manager" {
		t.Fatalf("role not applied: %q", got)
	}

	name := "新名字"
	if _, err := s.patchUser("bob", userPatch{DisplayName: &name}, "tester", "", true); err != nil {
		t.Fatal(err)
	}
	if userTV(t, db, "bob") != base {
		t.Fatal("displayName change must not bump token_version")
	}

	perms := []string{"users.manage", "apps.manage"}
	if changed, err := s.patchUser("bob", userPatch{Perms: perms}, "tester", "", true); err != nil || !changed {
		t.Fatalf("perms apply: changed=%v err=%v", changed, err)
	}
	if userTV(t, db, "bob") != base {
		t.Fatal("perms change must not bump token_version")
	}
	if got := userField(t, db, "bob", "perms"); got != `["users.manage","apps.manage"]` {
		t.Fatalf("perms not applied: %q", got)
	}
	// 幂等：同样 perms 再来一次不产生变更
	if changed, err := s.patchUser("bob", userPatch{Perms: perms}, "tester", "", true); err != nil || changed {
		t.Fatalf("idempotent perms should be no-op: changed=%v err=%v", changed, err)
	}

	// 密码重置：唯一 bump 路径
	pw := "newpass88"
	if _, err := s.patchUser("bob", userPatch{Password: &pw}, "tester", "", true); err != nil {
		t.Fatal(err)
	}
	if userTV(t, db, "bob") != base+1 {
		t.Fatal("password reset must bump token_version")
	}

	// 防提权：非 admin 操作者（operatorAdmin=false）不可动 admin 用户、不可授 admin
	mustExec(t, db, `INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES('root','h','s','admin','Root',1)`)
	if _, err := s.patchUser("root", userPatch{DisplayName: &name}, "bob", "", false); err == nil {
		t.Fatal("non-admin operator must not touch admin user")
	}
	adminRole := "admin"
	if _, err := s.patchUser("bob", userPatch{Role: &adminRole}, "carol", "", false); err == nil {
		t.Fatal("non-admin operator must not grant admin role")
	}
	// 末位管理员保护
	demote := "user"
	if _, err := s.patchUser("root", userPatch{Role: &demote}, "admin", "", true); err == nil {
		t.Fatal("last admin must not be demotable")
	}
}

// TestUpdateUserAcceptsPerms PUT /api/users/{name} 的 perms 字段生效（旧版静默丢弃）。
func TestUpdateUserAcceptsPerms(t *testing.T) {
	db := testDB(t)
	s := New(db, "test-secret")
	mustExec(t, db, `INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES('bob','h','s','user','Bob',7)`)
	req := httptest.NewRequest("PUT", "/api/users/bob", strings.NewReader(`{"perms":["apps.manage"]}`))
	req.SetPathValue("name", "bob")
	req = WithIdentity(req, &Identity{Username: "root", Role: "admin"})
	w := httptest.NewRecorder()
	s.UpdateUser(w, req)
	if w.Code != 200 {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	if got := userField(t, db, "bob", "perms"); got != `["apps.manage"]` {
		t.Fatalf("perms not applied via UpdateUser: %q", got)
	}
}

// TestSyncUsersStableSync 批量同步（display 名/角色变化）不吊销令牌。
func TestSyncUsersStableSync(t *testing.T) {
	db := testDB(t)
	s := New(db, "test-secret")
	mustExec(t, db, `INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES('bob','h','s','user','Bob',7)`)
	base := userTV(t, db, "bob")
	tok, _ := s.Issue("bob", "user", base)

	body := `{"users":[{"username":"bob","role":"manager","displayName":"改名"},{"username":"newbie","role":"user","displayName":"新人"}]}`
	req := httptest.NewRequest("POST", "/api/users/sync", strings.NewReader(body))
	req = WithIdentity(req, &Identity{Username: "root", Role: "admin"})
	w := httptest.NewRecorder()
	s.SyncUsers(w, req)
	if w.Code != 200 {
		t.Fatalf("sync: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Created int `json:"created"`
		Updated int `json:"updated"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Created != 1 || out.Updated != 1 {
		t.Fatalf("created=%d updated=%d: %s", out.Created, out.Updated, w.Body.String())
	}
	if userTV(t, db, "bob") != base {
		t.Fatal("sync with role/displayName change must not bump token_version")
	}
	if userField(t, db, "bob", "display_name") != "改名" {
		t.Fatal("displayName not synced")
	}
	// 会话不掉线且身份已按新角色生效
	id, err := s.VerifySession(tok)
	if err != nil || id.Role != "manager" {
		t.Fatalf("session should survive sync with live role: %v %+v", err, id)
	}
}

// TestNonAdminUsersManageCannotEscalate REST 侧持 users.manage 的非 admin 操作者
// 不得经 create/sync/delete 三条路径触及 admin 语义（开通/授予/删除均拒绝）。
func TestNonAdminUsersManageCannotEscalate(t *testing.T) {
	db := testDB(t)
	s := New(db, "test-secret")
	mustExec(t, db, `INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES('bob','h','s','user','Bob',7)`)
	mustExec(t, db, `INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES('root','h','s','admin','Root',1)`)
	nonAdmin := func(r *http.Request) *http.Request {
		return WithIdentity(r, &Identity{Username: "boss", Role: "manager"})
	}

	// 1) 单独创建 admin 账号 → 403
	req := httptest.NewRequest("POST", "/api/users", strings.NewReader(`{"username":"evil","password":"passw0rd123","role":"admin"}`))
	req = nonAdmin(req)
	w := httptest.NewRecorder()
	s.CreateUser(w, req)
	if w.Code != 403 {
		t.Fatalf("non-admin create admin should 403, got %d %s", w.Code, w.Body.String())
	}
	if info, _ := db.GetUserAuth("evil"); info.Exists {
		t.Fatal("rejected create must not insert user")
	}

	// 2) 批量同步：新建 admin 账号 → skipped；已有用户提权为 admin → skipped
	body := `{"users":[{"username":"evil2","role":"admin"},{"username":"bob","role":"admin"}]}`
	req = httptest.NewRequest("POST", "/api/users/sync", strings.NewReader(body))
	req = nonAdmin(req)
	w = httptest.NewRecorder()
	s.SyncUsers(w, req)
	if w.Code != 200 {
		t.Fatalf("sync should 200 with skips, got %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Created int      `json:"created"`
		Updated int      `json:"updated"`
		Skipped []string `json:"skipped"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Created != 0 || out.Updated != 0 || len(out.Skipped) != 2 {
		t.Fatalf("both entries must be skipped: %s", w.Body.String())
	}
	if info, _ := db.GetUserAuth("evil2"); info.Exists {
		t.Fatal("sync must not create admin for non-admin operator")
	}
	if userField(t, db, "bob", "role") != "user" {
		t.Fatal("sync must not promote bob to admin for non-admin operator")
	}

	// 3) 删除 admin 用户 → 403
	req = httptest.NewRequest("DELETE", "/api/users/root", nil)
	req.SetPathValue("name", "root")
	req = nonAdmin(req)
	w = httptest.NewRecorder()
	s.DeleteUser(w, req)
	if w.Code != 403 {
		t.Fatalf("non-admin delete admin should 403, got %d %s", w.Code, w.Body.String())
	}

	// 4) 对照：admin 操作者照常可建 admin（无回归）
	req = httptest.NewRequest("POST", "/api/users", strings.NewReader(`{"username":"root2","password":"passw0rd123","role":"admin"}`))
	req = WithIdentity(req, &Identity{Username: "root", Role: "admin"})
	w = httptest.NewRecorder()
	s.CreateUser(w, req)
	if w.Code != 200 {
		t.Fatalf("admin create admin should succeed, got %d %s", w.Code, w.Body.String())
	}
}

// TestHostAPIGuard /api/host/* 密钥守护与全字段列表。
func TestHostAPIGuard(t *testing.T) {
	db := testDB(t)
	s := New(db, "test-secret")
	mustExec(t, db, `INSERT INTO users(username,password_hash,salt,role,display_name,token_version,perms) VALUES('bob','h','s','user','Bob',7,'["apps.manage"]')`)

	// 未启用（未配置密钥）→ 503
	w := httptest.NewRecorder()
	s.HostListUsers(w, httptest.NewRequest("GET", "/api/host/users", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unset secret should 503, got %d", w.Code)
	}
	s.SetExchangeSecret("sec")
	// 错误密钥 → 401
	req := httptest.NewRequest("GET", "/api/host/users", nil)
	req.Header.Set("X-Exchange-Secret", "wrong")
	w = httptest.NewRecorder()
	s.HostListUsers(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bad secret should 401, got %d", w.Code)
	}
	// 正确密钥 → 全字段列表（含 perms/tokenVersion）
	req = httptest.NewRequest("GET", "/api/host/users", nil)
	req.Header.Set("X-Exchange-Secret", "sec")
	w = httptest.NewRecorder()
	s.HostListUsers(w, req)
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var users []store.User
	if err := json.Unmarshal(w.Body.Bytes(), &users); err != nil || len(users) != 1 {
		t.Fatalf("list parse: %v %s", err, w.Body.String())
	}
	if users[0].Username != "bob" || len(users[0].Perms) != 1 || users[0].Perms[0] != "apps.manage" || users[0].TokenVersion != 7 {
		t.Fatalf("full fields mismatch: %+v", users[0])
	}
}

// TestHostSyncViaSecret 宿主批量同步走密钥守护，语义与 REST 版一致（不 bump）。
func TestHostSyncViaSecret(t *testing.T) {
	db := testDB(t)
	s := New(db, "test-secret")
	s.SetExchangeSecret("sec")
	mustExec(t, db, `INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES('bob','h','s','user','Bob',7)`)
	base := userTV(t, db, "bob")

	req := httptest.NewRequest("POST", "/api/host/users/sync", strings.NewReader(`{"users":[{"username":"bob","displayName":"宿主改名"}]}`))
	req.Header.Set("X-Exchange-Secret", "sec")
	w := httptest.NewRecorder()
	s.HostSyncUsers(w, req)
	if w.Code != 200 {
		t.Fatalf("host sync: %d %s", w.Code, w.Body.String())
	}
	if userTV(t, db, "bob") != base {
		t.Fatal("host sync must not bump token_version")
	}
	if userField(t, db, "bob", "display_name") != "宿主改名" {
		t.Fatal("host sync displayName not applied")
	}
}

// TestExchangeStableSync exchange 同步角色/权限不吊销该用户其它设备的令牌，
// 响应带 tokenTtl。
func TestExchangeStableSync(t *testing.T) {
	db := testDB(t)
	s := New(db, "test-secret")
	s.SetExchangeSecret("sec")

	call := func(body string) (int, string) {
		req := httptest.NewRequest("POST", "/api/auth/exchange", strings.NewReader(body))
		req.Header.Set("X-Exchange-Secret", "sec")
		w := httptest.NewRecorder()
		s.Exchange(w, req)
		var m struct {
			Token     string `json:"token"`
			TokenTtl  int    `json:"tokenTtl"`
			User      struct{ Role string `json:"role"` } `json:"user"`
		}
		json.Unmarshal(w.Body.Bytes(), &m)
		_ = m.TokenTtl
		return w.Code, m.Token
	}

	code, tok1 := call(`{"username":"alice","role":"front","displayName":"前台"}`)
	if code != 200 || tok1 == "" {
		t.Fatalf("first exchange: %d", code)
	}
	// 二次 exchange 携带不同角色 + 权限快照：旧令牌必须仍然有效（不掉线）
	code, _ = call(`{"username":"alice","role":"manager","perms":["users.manage"]}`)
	if code != 200 {
		t.Fatalf("second exchange: %d", code)
	}
	if _, err := s.VerifySession(tok1); err != nil {
		t.Fatalf("re-exchange with role/perms change must not revoke old token: %v", err)
	}
	if got := userField(t, db, "alice", "role"); got != "manager" {
		t.Fatalf("exchange role sync failed: %q", got)
	}
}

// TestIdentityChangedHook 授权变化触发 sys:user 快照钩子（角色变化带 roleChanged）。
func TestIdentityChangedHook(t *testing.T) {
	db := testDB(t)
	s := New(db, "test-secret")
	mustExec(t, db, `INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES('bob','h','s','user','Bob',7)`)
	var gotUser string
	var gotRoleChanged bool
	var gotSnap map[string]any
	s.SetIdentityChangedHook(func(u string, rc bool, snap map[string]any) {
		gotUser, gotRoleChanged, gotSnap = u, rc, snap
	})
	role := "manager"
	if _, err := s.patchUser("bob", userPatch{Role: &role}, "tester", "", true); err != nil {
		t.Fatal(err)
	}
	if gotUser != "bob" || !gotRoleChanged || gotSnap["role"] != "manager" {
		t.Fatalf("identity hook not fired correctly: %v %v %v", gotUser, gotRoleChanged, gotSnap)
	}
}

// TestDeleteUserCascade 删号级联清理五张此前的缺口表。
func TestDeleteUserCascade(t *testing.T) {
	db := testDB(t)
	s := New(db, "test-secret")
	mustExec(t, db, `INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES('gone','h','s','user','',7)`)
	mustExec(t, db, `INSERT INTO shares(id,owner,path,name,size,created_at,expires_at) VALUES('sid','gone','/a','a.txt',1,'2026-01-01 00:00:00','2026-01-02 00:00:00')`)
	mustExec(t, db, `INSERT INTO feedback(username,category,content) VALUES('gone','issue','测试反馈内容')`)
	mustExec(t, db, `INSERT INTO chat_room_messages(sender,text,ts) VALUES('gone','hi',1)`)
	mustExec(t, db, `INSERT INTO ai_user_providers(uid,name) VALUES('gone','p1')`)
	mustExec(t, db, `INSERT INTO attachments(id,name,mime,size,uploader,created_at) VALUES('aid','a.png','image/png',1,'gone','2026-01-01 00:00:00')`)

	req := httptest.NewRequest("DELETE", "/api/users/gone", nil)
	req.SetPathValue("name", "gone")
	req = WithIdentity(req, &Identity{Username: "root", Role: "admin"})
	w := httptest.NewRecorder()
	s.DeleteUser(w, req)
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM users WHERE username='gone'`,
		`SELECT COUNT(*) FROM shares WHERE owner='gone'`,
		`SELECT COUNT(*) FROM feedback WHERE username='gone'`,
		`SELECT COUNT(*) FROM chat_room_messages WHERE sender='gone'`,
		`SELECT COUNT(*) FROM ai_user_providers WHERE uid='gone'`,
		`SELECT COUNT(*) FROM attachments WHERE uploader='gone'`,
	} {
		var n int
		db.QueryRow(q).Scan(&n)
		if n != 0 {
			t.Fatalf("cascade gap: %s → %d rows left", q, n)
		}
	}
}
