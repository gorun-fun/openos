package vfs

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	return New(t.TempDir(), db, bus.New())
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

	// 1. 文本附件：落进「文档」，原文落盘
	p1, err := fs.SaveToDocs("alice", "笔记.txt", []byte("hello 中文"))
	if err != nil {
		t.Fatalf("SaveToDocs: %v", err)
	}
	if p1 != "/home/alice/文档/笔记.txt" {
		t.Fatalf("落盘路径错误: %s", p1)
	}
	if got, _ := os.ReadFile(filepath.Join(fs.realHome("alice"), "文档", "笔记.txt")); string(got) != "hello 中文" {
		t.Fatalf("落盘内容错误: %q", got)
	}

	// 2. 内容级幂等：同一附件重复保存返回同一路径、不堆积
	p1b, err := fs.SaveToDocs("alice", "笔记.txt", []byte("hello 中文"))
	if err != nil || p1b != p1 {
		t.Fatalf("幂等失败: %s %v", p1b, err)
	}
	entries, _ := os.ReadDir(filepath.Join(fs.realHome("alice"), "文档"))
	if len(entries) != 1 {
		t.Fatalf("幂等后应只有 1 个文件: %d", len(entries))
	}

	// 3. 同名不同内容：重命名递增
	p2, err := fs.SaveToDocs("alice", "笔记.txt", []byte("另一个内容"))
	if err != nil {
		t.Fatalf("重命名落盘: %v", err)
	}
	if p2 != "/home/alice/文档/笔记 (2).txt" {
		t.Fatalf("重命名路径错误: %s", p2)
	}

	// 4. 二进制附件：原始字节落盘（不再 base64 存储）
	p3, err := fs.SaveToDocs("bob", "图.png", []byte{0x89, 'P', 'N', 'G'})
	if err != nil {
		t.Fatalf("二进制落盘: %v", err)
	}
	if got, _ := os.ReadFile(fs.realPath("bob", p3)); string(got) != string([]byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("二进制存储形态错误: %x", got)
	}
}

// TestHandlersRoundtrip REST 全链路磁盘化后的基本行为：Write→List→Read→Raw→Mkdir→Copy→Rename→Remove。
func TestHandlersRoundtrip(t *testing.T) {
	fs := newFS(t)

	do := func(method, target, body, user string, h func(http.ResponseWriter, *http.Request)) (*httptest.ResponseRecorder, map[string]any) {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, target, rd)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req = auth.WithIdentity(req, &auth.Identity{Username: user, Role: "user"})
		w := httptest.NewRecorder()
		h(w, req)
		var m map[string]any
		json.Unmarshal(w.Body.Bytes(), &m)
		return w, m
	}

	// 写入 → 读回
	if w, _ := do("PUT", "/api/fs/write", `{"path":"/home/alice/notes/x.md","content":"# hi"}`, "alice", fs.Write); w.Code != 200 {
		t.Fatalf("write: %d %s", w.Code, w.Body.String())
	}
	if got, _ := os.ReadFile(fs.realPath("alice", "/home/alice/notes/x.md")); string(got) != "# hi" {
		t.Fatalf("write 落盘内容: %q", got)
	}
	// List：父目录自动补齐 + 目录在前
	w, m := do("GET", "/api/fs/list?path=/home/alice", "", "alice", fs.List)
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	items, _ := m["__no"].([]any)
	_ = items
	var lst []map[string]any
	json.Unmarshal(w.Body.Bytes(), &lst)
	if len(lst) != 1 || lst[0]["path"] != "/home/alice/notes" || lst[0]["isDir"] != true {
		t.Fatalf("list 内容: %s", w.Body.String())
	}
	// Read：文本直读
	w, m = do("GET", "/api/fs/read?path=/home/alice/notes/x.md", "", "alice", fs.Read)
	if w.Code != 200 || m["content"] != "# hi" || m["isBinary"] != false {
		t.Fatalf("read: %d %v", w.Code, m)
	}
	// 二进制 Read：base64 返回（旧契约保持）
	os.WriteFile(fs.realPath("alice", "/home/alice/notes/b.png"), []byte{0x89, 'P'}, 0o644)
	w, m = do("GET", "/api/fs/read?path=/home/alice/notes/b.png", "", "alice", fs.Read)
	if w.Code != 200 || m["isBinary"] != true || m["content"] != base64.StdEncoding.EncodeToString([]byte{0x89, 'P'}) {
		t.Fatalf("binary read: %d %v", w.Code, m)
	}
	// Raw：原始字节
	w, _ = do("GET", "/api/fs/raw?path=/home/alice/notes/b.png", "", "alice", fs.Raw)
	if w.Code != 200 || string(w.Body.Bytes()) != "\x89P" {
		t.Fatalf("raw: %d %q", w.Code, w.Body.String())
	}
	// Mkdir 幂等 + 同名文件冲突
	if w, _ = do("POST", "/api/fs/mkdir", `{"path":"/home/alice/notes"}`, "alice", fs.Mkdir); w.Code != 200 {
		t.Fatalf("mkdir 幂等失败: %d", w.Code)
	}
	if w, _ = do("POST", "/api/fs/mkdir", `{"path":"/home/alice/notes/x.md"}`, "alice", fs.Mkdir); w.Code != 400 {
		t.Fatalf("mkdir 同名文件应 400: %d", w.Code)
	}
	// Copy 子树 + 目标冲突 409
	if w, _ = do("POST", "/api/fs/copy", `{"from":"/home/alice/notes","to":"/home/alice/notes2"}`, "alice", fs.Copy); w.Code != 200 {
		t.Fatalf("copy: %d %s", w.Code, w.Body.String())
	}
	if got, _ := os.ReadFile(fs.realPath("alice", "/home/alice/notes2/x.md")); string(got) != "# hi" {
		t.Fatalf("copy 落盘: %q", got)
	}
	if w, _ = do("POST", "/api/fs/copy", `{"from":"/home/alice/notes","to":"/home/alice/notes2"}`, "alice", fs.Copy); w.Code != 409 {
		t.Fatalf("copy 目标存在应 409: %d", w.Code)
	}
	// Rename
	if w, _ = do("POST", "/api/fs/rename", `{"from":"/home/alice/notes2","to":"/home/alice/notes3"}`, "alice", fs.Rename); w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(fs.realPath("alice", "/home/alice/notes3/x.md")); err != nil {
		t.Fatalf("rename 后源应移动: %v", err)
	}
	// Remove 递归
	if w, _ = do("DELETE", "/api/fs/remove?path=/home/alice/notes3", "", "alice", fs.Remove); w.Code != 200 {
		t.Fatalf("remove: %d", w.Code)
	}
	if _, err := os.Stat(fs.realPath("alice", "/home/alice/notes3")); !os.IsNotExist(err) {
		t.Fatalf("remove 后应不存在: %v", err)
	}
	// 越权访问他人 home 恒 403
	if w, _ = do("GET", "/api/fs/read?path=/home/bob/secret.txt", "", "alice", fs.Read); w.Code != 403 {
		t.Fatalf("越权应 403: %d", w.Code)
	}
}

// TestSearchNameMatch 名称搜索：隐藏文件不参与、目录整棵跳过。
func TestSearchNameMatch(t *testing.T) {
	fs := newFS(t)
	home := fs.realHome("alice")
	os.MkdirAll(filepath.Join(home, "proj"), 0o755)
	os.MkdirAll(filepath.Join(home, ".Trash"), 0o755)
	os.WriteFile(filepath.Join(home, "proj", "app.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(home, ".Trash", "app.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(home, ".hidden.md"), []byte("x"), 0o644)

	req := httptest.NewRequest("GET", "/api/fs/search?q=app", nil)
	req = auth.WithIdentity(req, &auth.Identity{Username: "alice", Role: "user"})
	w := httptest.NewRecorder()
	fs.Search(w, req)
	var out []map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out) != 1 || out[0]["path"] != "/home/alice/proj/app.md" {
		t.Fatalf("search 结果: %s", w.Body.String())
	}
}

// TestSymlinkEscape 终端可创建指向 home 外的符号链接，REST 读写不得跟随越界。
func TestSymlinkEscape(t *testing.T) {
	fs := newFS(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	os.WriteFile(secret, []byte("topsecret"), 0o644)

	home := fs.realHome("alice")
	os.MkdirAll(home, 0o755)
	os.Symlink(secret, filepath.Join(home, "leak.txt"))
	os.Symlink(outside, filepath.Join(home, "leakdir"))

	if _, err := fs.resolveReal("alice", "/home/alice/leak.txt"); err == nil {
		t.Fatal("指向外部的符号链接应被拒绝")
	}
	if _, err := fs.resolveReal("alice", "/home/alice/leakdir/inner.txt"); err == nil {
		t.Fatal("指向外部的符号链接目录应被拒绝")
	}
	// 经 REST Read 读取同样 404/403
	req := httptest.NewRequest("GET", "/api/fs/read?path=/home/alice/leak.txt", nil)
	req = auth.WithIdentity(req, &auth.Identity{Username: "alice", Role: "user"})
	w := httptest.NewRecorder()
	fs.Read(w, req)
	if w.Code != 404 && w.Code != 403 {
		t.Fatalf("符号链接越界读取应被拒绝: %d", w.Code)
	}
	if w.Body.String() == "topsecret" {
		t.Fatal("泄漏了外部文件内容")
	}
}

// TestMigrateFromDB 存量迁移：SQLite files 表 → 统一 home 磁盘；幂等重放、磁盘非空跳过。
func TestMigrateFromDB(t *testing.T) {
	dbDir := t.TempDir()
	homes := t.TempDir()
	db, err := store.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	// 造旧行：目录 + 文本 + 二进制
	db.Exec(`INSERT INTO files(owner,path,is_dir) VALUES('alice','/home/alice/文档',1)`)
	db.Exec(`INSERT INTO files(owner,path,is_dir,content) VALUES('alice','/home/alice/文档/a.md',0,'hello')`)
	db.Exec(`INSERT INTO files(owner,path,is_dir,is_binary,content) VALUES('alice','/home/alice/b.png',0,1,?)`,
		base64.StdEncoding.EncodeToString([]byte{0x89, 'P'}))

	fs := New(homes, db, bus.New())
	// New 内已迁移：磁盘应有内容、表行应清空
	if got, _ := os.ReadFile(filepath.Join(fs.realHome("alice"), "文档", "a.md")); string(got) != "hello" {
		t.Fatalf("文本迁移失败: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(fs.realHome("alice"), "b.png")); string(got) != string([]byte{0x89, 'P'}) {
		t.Fatalf("二进制应按解码后字节落盘: %x", got)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM files WHERE owner='alice'`).Scan(&n)
	if n != 0 {
		t.Fatalf("迁移成功后表行应清空: %d", n)
	}

	// 磁盘已有内容时插入新行不再迁移（磁盘为权威）
	db.Exec(`INSERT INTO files(owner,path,is_dir,content) VALUES('alice','/home/alice/late.md',0,'late')`)
	fs.migrateFromDB()
	if _, err := os.Stat(filepath.Join(fs.realHome("alice"), "late.md")); !os.IsNotExist(err) {
		t.Fatal("磁盘非空时不应迁移新行")
	}
	var n2 int
	db.QueryRow(`SELECT COUNT(*) FROM files WHERE owner='alice'`).Scan(&n2)
	if n2 != 1 {
		t.Fatalf("该用户表行应保留: %d", n2)
	}
}

// TestEnsureHomeSeeds 首次种子示例文件；.Trash 每次登录补齐；清空后重种。
func TestEnsureHomeSeeds(t *testing.T) {
	fs := newFS(t)
	fs.EnsureHome("alice")
	home := fs.realHome("alice")
	if _, err := os.Stat(filepath.Join(home, "欢迎.md")); err != nil {
		t.Fatal("首次应种欢迎.md")
	}
	if _, err := os.Stat(filepath.Join(home, ".Trash")); err != nil {
		t.Fatal("应建 .Trash")
	}
	// 用户删光后（含 .Trash 被清）再登录：目录补齐 + 重种
	os.RemoveAll(home)
	fs.EnsureHome("alice")
	if _, err := os.Stat(filepath.Join(home, "欢迎.md")); err != nil {
		t.Fatal("清空后应重种")
	}
}

// TestCreateGoderProject goder 项目目录：落在 home/goder 下、重名冲突、非法名拒绝。
func TestCreateGoderProject(t *testing.T) {
	fs := newFS(t)
	dir, err := fs.CreateGoderProject("alice", "demo")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if want := filepath.Join(fs.realHome("alice"), "goder", "demo"); dir != want {
		t.Fatalf("项目目录位置: %s", dir)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("目录应存在: %v", err)
	}
	if _, err := fs.CreateGoderProject("alice", "demo"); err == nil || !os.IsExist(err) {
		t.Fatalf("重名应返回 os.ErrExist: %v", err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "../x"} {
		if _, err := fs.CreateGoderProject("alice", bad); err == nil {
			t.Fatalf("非法项目名 %q 应被拒绝", bad)
		}
	}
}

func TestShares(t *testing.T) {
	f := newFS(t)
	f.EnsureHome("alice") // 建 home 与「文档」等目录
	os.WriteFile(f.realPath("alice", "/home/alice/notes.md"), []byte("hello"), 0o644)

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
	os.Remove(f.realPath("alice", "/home/alice/notes.md"))
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
