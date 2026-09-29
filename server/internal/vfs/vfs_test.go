package vfs

import (
	"testing"

	"openos/server/internal/bus"
	"openos/server/internal/store"
)

func newFS(t *testing.T) *FS {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(t.TempDir(), db, bus.New(), nil)
}

func TestCleanBlocksEscape(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"/home/alice/ok.txt", false},
		{"/home/alice/../bob/secret", true},   // 越权到他人 home
		{"/etc/passwd", true},                 // 系统
		{"../../etc/passwd", true},            // 相对路径越权
		{"/home/alice/a/../../..", false},      // 归一后为 /（仅根列表，无越权）
		{"/home/alice/文档/../表格/x.csv", false}, // 归一后仍在 home 内
		{"/", false},                          // 根（列出 home 时由上层处理）
	}
	for _, c := range cases {
		_, err := clean("alice", c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("clean(alice, %q) err=%v, wantErr=%v", c.in, err, c.wantErr)
		}
	}
}

func TestSplitQuoted(t *testing.T) {
	got := splitQuoted(`echo "hello world" > a.txt`)
	want := []string{"echo", "hello world", ">", "a.txt"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestIsTextExt(t *testing.T) {
	if !isTextExt("/home/a/notes.MD") {
		t.Error(".md should be text")
	}
	if isTextExt("/home/a/photo.png") {
		t.Error(".png should be binary")
	}
}
