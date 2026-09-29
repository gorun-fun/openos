package auth

import (
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
