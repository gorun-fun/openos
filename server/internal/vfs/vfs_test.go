package vfs

import (
	"encoding/base64"
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

// TestSaveToDocs 聊天附件归档：文本/二进制落盘、内容级幂等、同名重命名、文档目录自动补齐。
func TestSaveToDocs(t *testing.T) {
	fs := newFS(t)

	// 1. 文本附件：落进「文档」，文本直存
	p1, err := fs.SaveToDocs("alice", "笔记.txt", []byte("hello 中文"))
	if err != nil {
		t.Fatalf("SaveToDocs: %v", err)
	}
	if p1 != "/home/alice/文档/笔记.txt" {
		t.Fatalf("落盘路径错误: %s", p1)
	}

	// 2. 内容级幂等：同一附件重复保存返回同一路径、不堆积
	p1b, err := fs.SaveToDocs("alice", "笔记.txt", []byte("hello 中文"))
	if err != nil || p1b != p1 {
		t.Fatalf("幂等失败: %s %v", p1b, err)
	}
	var n int
	fs.db.QueryRow(`SELECT COUNT(*) FROM files WHERE owner=? AND path LIKE '/home/alice/文档/%'`, "alice").Scan(&n)
	if n != 1 {
		t.Fatalf("幂等后应只有 1 个文件: %d", n)
	}

	// 3. 同名不同内容：重命名递增
	p2, err := fs.SaveToDocs("alice", "笔记.txt", []byte("另一个内容"))
	if err != nil {
		t.Fatalf("重命名落盘: %v", err)
	}
	if p2 != "/home/alice/文档/笔记 (2).txt" {
		t.Fatalf("重命名路径错误: %s", p2)
	}

	// 4. 二进制附件：base64 存储
	p3, err := fs.SaveToDocs("bob", "图.png", []byte{0x89, 'P', 'N', 'G'})
	if err != nil {
		t.Fatalf("二进制落盘: %v", err)
	}
	var isBin int
	var content string
	fs.db.QueryRow(`SELECT is_binary,content FROM files WHERE owner=? AND path=?`, "bob", p3).Scan(&isBin, &content)
	if isBin != 1 || content != base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("二进制存储形态错误: isBin=%d", isBin)
	}
}
