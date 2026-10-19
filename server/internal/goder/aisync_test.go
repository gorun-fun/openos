// aisync_test.go — AI 声明合成与管理面客户端行为。写盘语义（保真 upsert/
// 残留清理/默认决策链/内容不变不写/起播竞态）已随声明端点上移 goder-serve
//（用例见 goder 仓 pkg/ghserver/hostsettings_test.go，自此移植）；本文件只测
// openos 侧的声明合成（buildAdminDecl）与幂等确保（EnsureAIUser/SyncAIProvider
// 经 httptest 假管理面）。
package goder

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

func TestBuildAdminDecl(t *testing.T) {
	cfg := ProviderConfig{BaseURL: "http://p", APIKey: "pk", Model: "pm"}
	t.Run("仅平台托管", func(t *testing.T) {
		d := buildAdminDecl(cfg, nil, nil, false)
		if len(d.Entries) != 1 || d.Entries[0].Name != AIProviderName || len(d.Entries[0].Models) != 1 {
			t.Fatalf("托管条目不符: %+v", d.Entries)
		}
		if d.Default != AIProviderName+"/pm" {
			t.Fatalf("默认应为平台托管: %s", d.Default)
		}
		if len(d.Managed) != 1 || d.Managed[0] != AIProviderName {
			t.Fatalf("受管全集应含托管名: %v", d.Managed)
		}
	})
	t.Run("声明条目默认优先于平台托管", func(t *testing.T) {
		extras := []store.AIUserProvider{{
			UID: "u", Name: "TokenHub", BaseURL: "http://th", APIKey: "k",
			Models: []string{"m1", "m2"}, IsDefault: true,
		}}
		d := buildAdminDecl(cfg, extras, []string{"TokenHub"}, false)
		if d.Default != "TokenHub/m1" {
			t.Fatalf("声明 IsDefault 应优先: %s", d.Default)
		}
		if len(d.Entries) != 2 {
			t.Fatalf("应有托管+声明两条: %+v", d.Entries)
		}
	})
	t.Run("要素不全的声明跳过；托管名去重有序", func(t *testing.T) {
		extras := []store.AIUserProvider{
			{UID: "u", Name: "Broken", BaseURL: "http://x", Models: []string{"m"}}, // 缺 key
			{UID: "u", Name: AIProviderName, BaseURL: "http://x", APIKey: "k", Models: []string{"m"}}, // 撞托管名跳过
		}
		d := buildAdminDecl(cfg, extras, []string{"TokenHub", "", AIProviderName}, false)
		if len(d.Entries) != 1 {
			t.Fatalf("无效声明不得入条目: %+v", d.Entries)
		}
		if fmt.Sprint(d.Managed) != "[OpenOS TokenHub]" {
			t.Fatalf("受管全集应去重有序: %v", d.Managed)
		}
	})
	t.Run("平台未配置 → 无托管条目，默认空（走清除分支）", func(t *testing.T) {
		d := buildAdminDecl(ProviderConfig{}, nil, nil, false)
		if len(d.Entries) != 0 || d.Default != "" {
			t.Fatalf("空配置应产出空声明: %+v", d)
		}
	})
}

// fakeAdmin — goder-serve 宿主管理面假件（users / providers GET+PUT）。
type fakeAdmin struct {
	mu    sync.Mutex
	users []AdminUserEntry
	puts  int // PUT 计数（按 san 记明细见 putsBy）
	putsBy map[string]int
	markVer int
	marks map[string]int
	fail  bool
}

func newFakeAdmin(users ...string) *fakeAdmin {
	f := &fakeAdmin{putsBy: map[string]int{}, marks: map[string]int{}}
	for _, u := range users {
		f.users = append(f.users, AdminUserEntry{San: u})
	}
	return f
}

func (f *fakeAdmin) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/goder-api/admin/users", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		f.mu.Lock()
		users := append([]AdminUserEntry(nil), f.users...)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"users": users})
	})
	mux.HandleFunc("/goder-api/admin/users/{uid}/providers", func(w http.ResponseWriter, r *http.Request) {
		uid := r.PathValue("uid")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"mark": fmt.Sprint(f.marks[uid]), "managed": []string{}})
		case http.MethodPut:
			if f.fail {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			f.puts++
			f.putsBy[uid]++
			f.markVer++
			f.marks[uid] = f.markVer
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "applied": true})
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})
	return mux
}

// newFakeManager 把 Manager 指到假管理面（ready 直真、binPath 指向存在文件，
// 绕开子进程生命周期）。
func newFakeManager(t *testing.T, f *fakeAdmin) *Manager {
	t.Helper()
	ts := httptest.NewServer(f.handler(t))
	t.Cleanup(ts.Close)
	exe := filepath.Join(t.TempDir(), "goder-serve")
	os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
	m := &Manager{
		binPath: exe, addr: strings.TrimPrefix(ts.URL, "http://"),
		adminToken: "tok", admin: ts.Client(),
		aiEnsured: map[string]string{},
	}
	m.mu.Lock()
	m.ready = true
	m.started = true
	m.mu.Unlock()
	return m
}

func TestEnsureAIUserIdempotent(t *testing.T) {
	f := newFakeAdmin("u1")
	m := newFakeManager(t, f)
	m.SetAIProviderSource(func() (ProviderConfig, bool) {
		return ProviderConfig{BaseURL: "http://p", APIKey: "pk", Model: "m"}, true
	})
	m.EnsureAIUser("u1")
	if f.puts != 1 {
		t.Fatalf("首调应 PUT 一次,got %d", f.puts)
	}
	m.EnsureAIUser("u1")
	if f.puts != 1 {
		t.Fatalf("指纹+声明未变应跳过,got %d", f.puts)
	}
	// 声明变化（模型改了）→ 重放
	m.SetAIProviderSource(func() (ProviderConfig, bool) {
		return ProviderConfig{BaseURL: "http://p", APIKey: "pk", Model: "m2"}, true
	})
	m.EnsureAIUser("u1")
	if f.puts != 2 {
		t.Fatalf("声明变化应重放,got %d", f.puts)
	}
	// 服务端指纹变化（如用户在 goder 设置页保存过）→ 重放
	f.mu.Lock()
	f.markVer += 100
	f.marks["u1"] = f.markVer
	f.mu.Unlock()
	m.EnsureAIUser("u1")
	if f.puts != 3 {
		t.Fatalf("指纹变化应重放,got %d", f.puts)
	}
}

func TestEnsureAIUserNothingToDo(t *testing.T) {
	f := newFakeAdmin("u1")
	m := newFakeManager(t, f) // 无 aiSource、无 providerDB
	m.EnsureAIUser("u1")
	if f.puts != 0 {
		t.Fatalf("无可声明内容不应调用,got %d", f.puts)
	}
}

func TestSyncAIProvider(t *testing.T) {
	f := newFakeAdmin("u1", "u2")
	m := newFakeManager(t, f)
	m.SetAIProviderSource(func() (ProviderConfig, bool) {
		return ProviderConfig{BaseURL: "http://p", APIKey: "pk", Model: "m"}, true
	})
	if n := m.SyncAIProvider(ProviderConfig{BaseURL: "http://p", APIKey: "pk", Model: "m"}, true); n != 2 {
		t.Fatalf("应写两个用户,got %d", n)
	}
	if f.puts != 2 {
		t.Fatalf("PUT 计数不符,got %d", f.puts)
	}
	if m.LastSyncUsers() != 2 {
		t.Fatalf("状态计数不符")
	}
}

func TestDeclDigestStable(t *testing.T) {
	a := buildAdminDecl(ProviderConfig{BaseURL: "http://p", APIKey: "k", Model: "m"}, nil, nil, false)
	b := buildAdminDecl(ProviderConfig{BaseURL: "http://p", APIKey: "k", Model: "m"}, nil, nil, false)
	if declDigest(a) != declDigest(b) {
		t.Fatal("同声明摘要应一致")
	}
	b.ForceDefault = true
	if declDigest(a) == declDigest(b) {
		t.Fatal("force 变化应反映在摘要")
	}
}
