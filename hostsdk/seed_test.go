// seed_test.go — Seed 种子/调和编排单测（httptest 假 openos /api/host/* 面）。
package hostsdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fakeOpenos 模拟 openos 管理 API（/api/host/* 子集：应用/授权/声明/用户）。
type fakeOpenos struct {
	mu       sync.Mutex
	apps     []HostApp
	access   map[string][]string
	users    []HostUser
	upserts  []map[string]any
	deletes  []string
	setApps  map[string][]string // PUT access 的角色 → 列表（按调用顺序记）
	setOrder []string
	declared []map[string]any
	disables map[string]bool
	failApp  bool // POST /api/host/apps 恒 500
}

func newFakeOpenos() *fakeOpenos {
	return &fakeOpenos{
		access:   map[string][]string{},
		disables: map[string]bool{},
		setApps:  map[string][]string{},
	}
}

func (f *fakeOpenos) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	guard := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("X-Exchange-Secret") != "sec" {
			http.Error(w, `{"error":"密钥不符"}`, http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/host/apps", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.apps == nil {
			f.apps = []HostApp{}
		}
		writeTestJSON(w, http.StatusOK, f.apps)
	})
	mux.HandleFunc("POST /api/host/apps", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failApp {
			http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
			return
		}
		f.upserts = append(f.upserts, body)
		id, _ := body["id"].(string)
		replaced := false
		for i := range f.apps {
			if f.apps[i].ID == id {
				f.apps[i] = HostApp{ID: id}
				replaced = true
			}
		}
		if !replaced {
			f.apps = append(f.apps, HostApp{ID: id})
		}
		writeTestJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("DELETE /api/host/apps/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		id := r.PathValue("id")
		f.mu.Lock()
		defer f.mu.Unlock()
		for i := range f.apps {
			if f.apps[i].ID == id {
				f.apps = append(f.apps[:i], f.apps[i+1:]...)
				f.deletes = append(f.deletes, id)
				break
			}
		}
		writeTestJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/host/apps/access", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		writeTestJSON(w, http.StatusOK, map[string]any{"access": f.access})
	})
	mux.HandleFunc("PUT /api/host/apps/access", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		var body struct {
			Role string   `json:"role"`
			Apps []string `json:"apps"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.access[body.Role] = body.Apps
		f.setApps[body.Role] = body.Apps
		f.setOrder = append(f.setOrder, body.Role)
		writeTestJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/host/declare", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.declared = append(f.declared, body)
		f.mu.Unlock()
		writeTestJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/host/users", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.users == nil {
			f.users = []HostUser{}
		}
		writeTestJSON(w, http.StatusOK, f.users)
	})
	mux.HandleFunc("PUT /api/host/users/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.disables[r.PathValue("name")] = true
		f.mu.Unlock()
		writeTestJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeOpenos) manager(t *testing.T) *Manager {
	t.Helper()
	srv := f.server(t)
	return New(Config{Addr: strings.TrimPrefix(srv.URL, "http://"), ExchangeSecret: "sec"})
}

func seedDecl() AppsDecl {
	return AppsDecl{
		Apps: []map[string]any{
			{"id": "rooms", "name": "房态盘", "icon": "rooms", "category": "业务"},
			{"id": "ledger", "name": "记账", "icon": "ledger", "category": "业务"},
		},
		RoleApps: map[string][]string{
			"admin": {"rooms", "ledger", "settings"},
			"user":  {"rooms", "notes"},
		},
		Roles: []RoleDecl{
			{Key: "admin", Label: "管理员", Perms: []string{"*"}, Apps: nil},
			{Key: "user", Label: "用户", Apps: nil},
		},
		Perms:         []PermDecl{{Code: "rooms.view", Group: "业务", Label: "房态查看"}},
		RoleWriteback: "",
	}
}

func TestSeedBasic(t *testing.T) {
	f := newFakeOpenos()
	m := f.manager(t)
	if err := Seed(m, seedDecl(), SeedOptions{}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.upserts) != 2 {
		t.Fatalf("应注册 2 个应用，实际 %d", len(f.upserts))
	}
	for _, up := range f.upserts {
		if up["locked"] != true {
			t.Fatalf("应用必须 locked 注册: %+v", up)
		}
	}
	// 首次授权矩阵：两角色各一次覆盖写入
	if !reflect.DeepEqual(f.setApps["admin"], []string{"rooms", "ledger", "settings"}) ||
		!reflect.DeepEqual(f.setApps["user"], []string{"rooms", "notes"}) {
		t.Fatalf("授权矩阵种子不符: %+v", f.setApps)
	}
	// 声明体：角色/权限点/writeback（空串 = 不接管）
	if len(f.declared) != 1 {
		t.Fatalf("应恰好声明一次，实际 %d", len(f.declared))
	}
	decl := f.declared[0]
	if decl["roleWriteback"] != "" {
		t.Fatalf("roleWriteback 应为空串: %v", decl["roleWriteback"])
	}
	if roles, _ := decl["roles"].([]any); len(roles) != 2 {
		t.Fatalf("声明角色数不符: %v", decl["roles"])
	}
}

func TestSeedSkipsSeededRoles(t *testing.T) {
	f := newFakeOpenos()
	m := f.manager(t)
	// grf-os 形态标记：access 列表含宿主专属标记应用 gateway 即视为已种子
	f.access = map[string][]string{"admin": {"gateway", "settings"}}
	marked := map[string]bool{}
	if err := Seed(m, seedDecl(), SeedOptions{
		Seeded:   func(role string, access map[string][]string) bool { return hasAppID(access[role], "gateway") },
		OnSeeded: func(role string) error { marked[role] = true; return nil },
	}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, dup := f.setApps["admin"]; dup {
		t.Fatal("已种子角色不应覆盖（运行期修改保护）")
	}
	if !reflect.DeepEqual(f.setApps["user"], []string{"rooms", "notes"}) {
		t.Fatalf("未种子角色应正常种子: %+v", f.setApps)
	}
	if marked["admin"] || !marked["user"] {
		t.Fatalf("OnSeeded 只对本次种子角色回调: %v", marked)
	}
}

func TestSeedRetireGuard(t *testing.T) {
	f := newFakeOpenos()
	f.apps = []HostApp{
		{ID: "stale-app", Locked: true},                  // 退休 id：自家种子残留 → 清退
		{ID: "th-old", Locked: true},                     // 退休前缀 th-*：清退
		{ID: "meituan", Locked: false, Source: "market"}, // 商店安装行：绝不删
		{ID: "settings", Locked: true, Builtin: true},    // 内置应用：不删
		{ID: "rooms", Locked: true},                      // 现行清单应用：不删
		{ID: "unrelated", Locked: true},                  // 未命中清单：不动
	}
	m := f.manager(t)
	if err := Seed(m, seedDecl(), SeedOptions{
		RetiredApps:     []string{"stale-app", "meituan"},
		RetiredPrefixes: []string{"th-", "gw-"},
	}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !reflect.DeepEqual(f.deletes, []string{"stale-app", "th-old"}) {
		t.Fatalf("清退名单不符（商店行/内置/现行应用不得误删）: %v", f.deletes)
	}
	for _, id := range []string{"meituan", "settings", "rooms", "unrelated"} {
		found := false
		for _, a := range f.apps {
			if a.ID == id {
				found = true
			}
		}
		if !found {
			t.Fatalf("应用 %s 不应被删除", id)
		}
	}
}

func TestSeedReconcile(t *testing.T) {
	f := newFakeOpenos()
	f.access = map[string][]string{
		"admin": {"settings", "goder", "home"}, // goder 要收回（矩阵不含）、home 下线残留收回
		"user":  {"notes"},                     // rooms 矩阵含 → 补授
	}
	m := f.manager(t)
	decl := seedDecl()
	decl.RoleApps = map[string][]string{
		"admin": {"rooms"}, // 矩阵不含 goder/home → 收回
		"user":  {"rooms"}, // 矩阵含 rooms → 补授
	}
	if err := Seed(m, decl, SeedOptions{
		Seeded:        func(string, map[string][]string) bool { return true }, // 跳过首次种子，只看调和
		ReconcileApps: []string{"goder", "home", "rooms"},
	}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !reflect.DeepEqual(f.access["admin"], []string{"settings", "rooms"}) {
		t.Fatalf("admin 调和结果不符: %v", f.access["admin"])
	}
	if !reflect.DeepEqual(f.access["user"], []string{"notes", "rooms"}) {
		t.Fatalf("user 调和结果不符: %v", f.access["user"])
	}
}

func TestSeedDemoUsers(t *testing.T) {
	f := newFakeOpenos()
	f.users = []HostUser{
		{Username: "admin", DisplayName: "管理员"},                // 精确匹配 → 禁用
		{Username: "user", DisplayName: "张三"},                  // 显示名不符（真实账号重名）→ 不动
		{Username: "guest", DisplayName: "访客", Disabled: true}, // 已禁用 → 不重复写
	}
	m := f.manager(t)
	if err := Seed(m, seedDecl(), SeedOptions{
		DemoUsers: map[string]string{"admin": "管理员", "user": "普通用户", "guest": "访客"},
	}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.disables["admin"] || f.disables["user"] || f.disables["guest"] {
		t.Fatalf("演示账号禁用判定不符: %v", f.disables)
	}
}

func TestSeedUpsertError(t *testing.T) {
	f := newFakeOpenos()
	f.failApp = true
	m := f.manager(t)
	if err := Seed(m, seedDecl(), SeedOptions{}); err == nil {
		t.Fatal("注册失败应上抛错误")
	}
}
