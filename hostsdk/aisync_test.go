// aisync_test.go — AISyncer 全链单测（httptest 假 token-hub + 假 openos 声明面）。
package hostsdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTH 模拟 token-hub 管理 API + OpenAI 兼容 /v1/models。
type fakeTH struct {
	mu       sync.Mutex
	users    []aiSyncUser
	created  []string // 建过的 key 名（按 uid 记录）
	keyByUID map[string]string
	models   map[string][]string // key → 模型清单
}

func (f *fakeTH) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/admin/users", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "th-master-key" {
			t.Errorf("管理请求缺主密钥头")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		// 复刻 token-hub 行为：创建过的 key 回填到用户名下
		items := make([]aiSyncUser, 0, len(f.users))
		for _, u := range f.users {
			if k, ok := f.keyByUID[u.UID]; ok && !thHasKey(u.Keys, k) {
				u.Keys = append(u.Keys, aiSyncKey{ID: "k-" + u.UID, Name: goderKeyName, Key: k, Enabled: true})
			}
			items = append(items, u)
		}
		thWriteJSON(w, map[string]any{"items": items})
	})
	mux.HandleFunc("POST /api/admin/keys", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name     string `json:"name"`
			OwnerUID string `json:"owner_uid"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.created = append(f.created, req.OwnerUID)
		f.keyByUID[req.OwnerUID] = "sk-th-" + req.OwnerUID
		f.mu.Unlock()
		thWriteJSON(w, aiSyncKey{Key: "sk-th-" + req.OwnerUID})
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		models := f.models[key]
		f.mu.Unlock()
		if models == nil {
			http.Error(w, `{"error":"invalid key"}`, http.StatusUnauthorized)
			return
		}
		data := make([]map[string]string, 0, len(models))
		for _, m := range models {
			data = append(data, map[string]string{"id": m})
		}
		thWriteJSON(w, map[string]any{"object": "list", "data": data})
	})
	return mux
}

// fakeOS 捕获声明请求的假 openos 调用面。
type fakeOS struct {
	mu    sync.Mutex
	calls []map[string]any
	fail  bool
}

func (f *fakeOS) Call(method, path string, body, out any) error {
	if f.fail {
		return errors.New("openos 不可达")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, body.(map[string]any))
	return nil
}

func (f *fakeOS) lastDecls(t *testing.T) []providerDecl {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("openos 未收到声明请求")
	}
	raw, _ := json.Marshal(f.calls[len(f.calls)-1]["providers"])
	var decls []providerDecl
	json.Unmarshal(raw, &decls)
	return decls
}

func newSyncerForTest(t *testing.T, th *fakeTH, os_ *fakeOS) *AISyncer {
	t.Helper()
	srv := httptest.NewServer(th.handler(t))
	t.Cleanup(srv.Close)
	return NewAISyncer(AISyncConfig{
		THBase: srv.URL, THKey: "th-master-key",
		ProviderName: "TokenHub", SetDefault: true, Interval: time.Minute,
	}, os_)
}

func TestAISyncOnce(t *testing.T) {
	th := &fakeTH{
		users: []aiSyncUser{
			{UID: "u-bound", PlanID: "plan-1", PlanEnabled: true, Enabled: true, Keys: []aiSyncKey{
				{ID: "k1", Name: "goder", Key: "sk-th-u-bound", Enabled: true},
			}},
			{UID: "u-nokey", PlanID: "plan-1", PlanEnabled: true, Enabled: true},
			{UID: "u-unbound", Enabled: true},
			{UID: "u-disabled", PlanID: "plan-1", PlanEnabled: true, Enabled: false},
			{UID: "u-plan-off", PlanID: "plan-2", PlanEnabled: false, Enabled: true},
		},
		models: map[string][]string{
			"sk-th-u-bound": {"glm-4.7", "deepseek-v4.1-flash"},
			"sk-th-u-nokey": {"glm-4.7"},
		},
		keyByUID: map[string]string{},
	}
	os_ := &fakeOS{}
	s := newSyncerForTest(t, th, os_)

	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("期望声明 2 个用户（绑定+新建），得到 %d", n)
	}
	decls := os_.lastDecls(t)
	if len(decls) != 2 {
		t.Fatalf("声明条数 %d", len(decls))
	}
	byUID := map[string]providerDecl{}
	for _, d := range decls {
		byUID[d.UID] = d
	}
	d := byUID["u-bound"]
	if d.Name != "TokenHub" || d.APIKey != "sk-th-u-bound" || d.Default != true ||
		len(d.Models) != 2 || d.Models[1] != "deepseek-v4.1-flash" {
		t.Fatalf("u-bound 声明内容不对: %+v", d)
	}
	if !strings.HasSuffix(d.BaseURL, "/v1") {
		t.Fatalf("base_url 应指向 /v1: %s", d.BaseURL)
	}
	if _, ok := byUID["u-nokey"]; !ok {
		t.Fatal("无令牌用户应自动建 key 并声明")
	}
	for _, uid := range []string{"u-unbound", "u-disabled", "u-plan-off"} {
		if _, ok := byUID[uid]; ok {
			t.Fatalf("%s 不应被声明", uid)
		}
	}

	// 第二轮：幂等（不再新建 key）
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	th.mu.Lock()
	creates := len(th.created)
	th.mu.Unlock()
	if creates != 1 {
		t.Fatalf("第二轮不应重复建 key，累计创建 %d 次", creates)
	}
}

func TestAISyncUsers失败不动声明(t *testing.T) {
	th := &fakeTH{keyByUID: map[string]string{}}
	os_ := &fakeOS{}
	s := newSyncerForTest(t, th, os_)
	s.thBase = "http://127.0.0.1:1" // users 读取必然失败
	if _, err := s.SyncOnce(context.Background()); err == nil {
		t.Fatal("users 读取失败应报错")
	}
	os_.mu.Lock()
	calls := len(os_.calls)
	os_.mu.Unlock()
	if calls != 0 {
		t.Fatal("users 失败时不得写空声明")
	}
}

func TestAISync单用户模型失败跳过(t *testing.T) {
	th := &fakeTH{
		users: []aiSyncUser{
			{UID: "a", PlanID: "p", PlanEnabled: true, Enabled: true, Keys: []aiSyncKey{
				{ID: "k", Name: "goder", Key: "sk-a", Enabled: true},
			}},
			{UID: "b", PlanID: "p", PlanEnabled: true, Enabled: true, Keys: []aiSyncKey{
				{ID: "k2", Name: "goder", Key: "sk-b", Enabled: true},
			}},
		},
		models:   map[string][]string{"sk-b": {"m1"}}, // sk-a 无模型 → 失败
		keyByUID: map[string]string{},
	}
	os_ := &fakeOS{}
	s := newSyncerForTest(t, th, os_)
	s.logf = func(string, ...any) {} // 静默
	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应只声明 1 个用户，得到 %d", n)
	}
	decls := os_.lastDecls(t)
	if decls[0].UID != "b" {
		t.Fatalf("跳过的应是 a，实际声明 %+v", decls)
	}
}

func thWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func thHasKey(keys []aiSyncKey, key string) bool {
	for _, k := range keys {
		if k.Key == key {
			return true
		}
	}
	return false
}
