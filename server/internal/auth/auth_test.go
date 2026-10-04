package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openos/server/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestJWTRoundTrip(t *testing.T) {
	s := New(testDB(t), "test-secret")
	tok, err := s.Issue("alice", "admin", 3)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Verify(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if c.Sub != "alice" || c.Role != "admin" || c.TV != 3 {
		t.Fatalf("claims mismatch: %+v", c)
	}
}

func TestJWTRejectsBadToken(t *testing.T) {
	s := New(testDB(t), "test-secret")
	tok, _ := s.Issue("alice", "admin", 0)
	if _, err := s.Verify(tok + "x"); err == nil {
		t.Fatal("tampered token should fail")
	}
	if _, err := s.Verify("not.a.jwt"); err == nil {
		t.Fatal("malformed token should fail")
	}
	// 过期
	s2 := New(testDB(t), "test-secret")
	s2.ttl = -time.Hour
	expired, _ := s2.Issue("alice", "admin", 0)
	if _, err := s2.Verify(expired); err == nil {
		t.Fatal("expired token should fail")
	}
}

func TestPasswordHashAndStrength(t *testing.T) {
	if hashPassword("secret", "salt") != hashPassword("secret", "salt") {
		t.Fatal("hash must be deterministic")
	}
	if hashPassword("secret", "salt1") == hashPassword("secret", "salt2") {
		t.Fatal("different salts must produce different hashes")
	}
	weak := []string{"short1", "allletters", "12345678", ""}
	for _, p := range weak {
		if !WeakPassword(p) {
			t.Errorf("WeakPassword(%q) = false, want true", p)
		}
	}
	if WeakPassword("goodpass1") {
		t.Error("goodpass1 should be accepted")
	}
}

func TestBruteForceLockout(t *testing.T) {
	s := New(testDB(t), "secret")
	key := "bob|1.2.3.4"
	for i := 0; i < maxFails; i++ {
		s.fail(key)
	}
	if d := s.locked(key); d <= 0 {
		t.Fatal("key should be locked after maxFails")
	}
	s.success(key)
	if d := s.locked(key); d != 0 {
		t.Fatal("success should clear lock")
	}
}

func TestIntrospect(t *testing.T) {
	db := testDB(t)
	s := New(db, "test-secret")
	s.SetExchangeSecret("sec")
	if _, err := db.Exec(`INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES('alice','h','s','front','张前台',7)`); err != nil {
		t.Fatal(err)
	}
	tok, _ := s.Issue("alice", "front", 7)

	call := func(secret, token string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/api/auth/introspect", strings.NewReader(`{"token":"`+token+`"}`))
		req.Header.Set("X-Exchange-Secret", secret)
		w := httptest.NewRecorder()
		s.Introspect(w, req)
		var m map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatalf("bad json: %v (%s)", err, w.Body.String())
		}
		return w.Code, m
	}

	// 未启用（未配置密钥）→ 503
	s2 := New(testDB(t), "test-secret")
	w := httptest.NewRecorder()
	s2.Introspect(w, httptest.NewRequest("POST", "/api/auth/introspect", strings.NewReader(`{"token":"x"}`)))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled service should 503, got %d", w.Code)
	}
	// 密钥错误 → 401
	if code, _ := call("wrong", tok); code != http.StatusUnauthorized {
		t.Fatalf("bad secret should 401, got %d", code)
	}
	// 有效令牌 → active:true 且身份齐全
	code, m := call("sec", tok)
	if code != 200 || m["active"] != true {
		t.Fatalf("valid token should be active: %d %v", code, m)
	}
	if m["username"] != "alice" || m["role"] != "front" || m["displayName"] != "张前台" || m["sub"] != "alice" {
		t.Fatalf("identity mismatch: %v", m)
	}
	if _, ok := m["exp"].(float64); !ok {
		t.Fatalf("exp missing: %v", m)
	}
	// 坏令牌 → active:false（200）
	if code, m = call("sec", tok+"x"); code != 200 || m["active"] != false {
		t.Fatalf("tampered token should be inactive: %d %v", code, m)
	}
	// 吊销（token_version 变化）→ active:false
	db.BumpTokenVersion("alice")
	s.InvalidateCache("alice")
	if code, m = call("sec", tok); code != 200 || m["active"] != false {
		t.Fatalf("revoked token should be inactive: %d %v", code, m)
	}
}
