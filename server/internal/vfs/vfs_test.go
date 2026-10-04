package vfs

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openos/server/internal/auth"
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

func TestShares(t *testing.T) {
	f := newFS(t)
	f.EnsureHome("alice") // 建 home 与「文档」等目录
	f.db.Exec(`INSERT INTO files(owner,path,is_dir,content) VALUES('alice','/home/alice/notes.md',0,'hello')`)

	do := func(method, target, body, user, pv string, h func(http.ResponseWriter, *http.Request)) (*httptest.ResponseRecorder, map[string]any) {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, target, rd)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.SetPathValue("id", pv) // 直调 handler 须经 SetPathValue 模拟路由填充
		req = auth.WithIdentity(req, &auth.Identity{Username: user, Role: "user"})
		w := httptest.NewRecorder()
		h(w, req)
		var m map[string]any
		json.Unmarshal(w.Body.Bytes(), &m)
		return w, m
	}

	// 目录不能分享
	if w, _ := do("POST", "/api/fs/shares", `{"path":"/home/alice/文档"}`, "alice", "", f.CreateShare); w.Code != 400 {
		t.Fatalf("dir share should 400, got %d", w.Code)
	}
	// 创建 → 拿到 id
	w, m := do("POST", "/api/fs/shares", `{"path":"/home/alice/notes.md"}`, "alice", "", f.CreateShare)
	if w.Code != 200 {
		t.Fatalf("create share: %d %s", w.Code, w.Body.String())
	}
	id, _ := m["id"].(string)
	if len(id) != 32 {
		t.Fatalf("share id should be 128bit hex, got %q", id)
	}
	// bob 查看元信息 + 下载
	if w, m = do("GET", "/api/share/"+id, "", "bob", id, f.ShareInfo); w.Code != 200 || m["owner"] != "alice" || m["name"] != "notes.md" {
		t.Fatalf("share info: %d %v", w.Code, m)
	}
	if w, _ = do("GET", "/api/share/"+id+"/download", "", "bob", id, f.ShareDownload); w.Code != 200 || w.Body.String() != "hello" {
		t.Fatalf("share download: %d %q", w.Code, w.Body.String())
	}
	if disp := w.Header().Get("Content-Disposition"); !strings.Contains(disp, "attachment") {
		t.Fatalf("download must be attachment, got %q", disp)
	}
	// bob 存入自己的文档（SaveToDocs 幂等/配额继承）
	f.EnsureHome("bob")
	if w, m = do("POST", "/api/share/"+id+"/save", "", "bob", id, f.ShareSave); w.Code != 200 {
		t.Fatalf("share save: %d %s", w.Code, w.Body.String())
	}
	if m["path"] != "/home/bob/文档/notes.md" {
		t.Fatalf("save target: %v", m)
	}
	// bob 不能撤销 alice 的分享
	if w, _ = do("DELETE", "/api/fs/shares/"+id, "", "bob", id, f.RevokeShare); w.Code != 403 {
		t.Fatalf("bob revoke should 403, got %d", w.Code)
	}
	// alice 的分享列表
	if w, m = do("GET", "/api/fs/shares", "", "alice", "", f.ListShares); w.Code != 200 {
		t.Fatalf("list shares: %d", w.Code)
	}
	if lst, ok := m["id"].([]any); ok || w.Body.String() == "" {
		_ = lst
	}
	if !strings.Contains(w.Body.String(), id) {
		t.Fatalf("list should contain share id: %s", w.Body.String())
	}
	// alice 撤销 → bob 一律 404
	if w, _ = do("DELETE", "/api/fs/shares/"+id, "", "alice", id, f.RevokeShare); w.Code != 200 {
		t.Fatalf("revoke: %d", w.Code)
	}
	if w, _ = do("GET", "/api/share/"+id, "", "bob", id, f.ShareInfo); w.Code != 404 {
		t.Fatalf("revoked info should 404, got %d", w.Code)
	}
	// 底稿删除 → 链接失聪
	w, m = do("POST", "/api/fs/shares", `{"path":"/home/alice/notes.md"}`, "alice", "", f.CreateShare)
	id2, _ := m["id"].(string)
	f.db.Exec(`DELETE FROM files WHERE owner='alice' AND path='/home/alice/notes.md'`)
	if w, _ = do("GET", "/api/share/"+id2, "", "bob", id2, f.ShareInfo); w.Code != 404 {
		t.Fatalf("dead link should 404, got %d", w.Code)
	}
	// 过期清理
	f.db.Exec(`INSERT INTO shares(id,owner,path,name,size,expires_at) VALUES('dead','alice','/home/alice/x','x',1,?)`, time.Now().Add(-time.Hour).UTC().Format(sqliteTime))
	f.CleanupExpiredShares()
	var n int
	f.db.QueryRow(`SELECT COUNT(*) FROM shares WHERE id='dead'`).Scan(&n)
	if n != 0 {
		t.Fatal("expired share should be cleaned")
	}
}
