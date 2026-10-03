package attach

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openos/server/internal/store"
)

// newUploadRequest 构造 multipart 上传请求。
func newUploadRequest(t *testing.T, field, filename, contentType string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := map[string]string{}
	if contentType != "" {
		h["Content-Type"] = contentType
	}
	fw, err := mw.CreatePart(fileHeader(filename, h))
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(content)
	mw.Close()
	r := httptest.NewRequest("POST", "/api/attachments", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

func fileHeader(filename string, h map[string]string) map[string][]string {
	hh := map[string][]string{}
	for k, v := range h {
		hh[k] = []string{v}
	}
	// multipart.CreatePart 的 header 键须 canonical；文件名走 Content-Disposition
	hh["Content-Disposition"] = []string{`form-data; name="file"; filename="` + filename + `"`}
	return hh
}

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := New(t.TempDir(), db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// TestSaveAndGet 上传 → 元数据/落盘 → Get 命中；文件名清洗与 mime 嗅探。
func TestSaveAndGet(t *testing.T) {
	s := newStore(t)

	// PNG 魔数（内容嗅探胜过声明）
	png := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 64)...)
	r := newUploadRequest(t, "file", "../evil/截图 画布.png", "text/plain", png)
	m, err := s.Save("alice", r)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if m.MIME != "image/png" {
		t.Errorf("mime 嗅探错误: %q", m.MIME)
	}
	if m.Name != "evil/截图 画布.png" && !strings.Contains(m.Name, "png") {
		t.Errorf("文件名清洗异常: %q", m.Name)
	}
	if m.Uploader != "alice" || m.Size != int64(len(png)) {
		t.Errorf("元数据错误: %+v", m)
	}
	if raw, err := os.ReadFile(s.FilePath(m.ID)); err != nil || len(raw) != len(png) {
		t.Errorf("落盘内容错误: %v", err)
	}
	if got, ok := s.Get(m.ID); !ok || got.ID != m.ID {
		t.Errorf("Get 未命中: %+v %v", got, ok)
	}
	if _, ok := s.Get("no-such"); ok {
		t.Error("未知 id 不应命中")
	}

	// 纯文本：文本内容可被嗅探（text/plain; charset=utf-8），嗅探值优先于声明
	r2 := newUploadRequest(t, "file", "notes.txt", "text/markdown", []byte("# hello 中文内容"))
	m2, err := s.Save("bob", r2)
	if err != nil {
		t.Fatalf("Save2: %v", err)
	}
	if !strings.HasPrefix(m2.MIME, "text/plain") {
		t.Errorf("文本嗅探 mime 错误: %q", m2.MIME)
	}
}

// TestSaveLimits 缺字段、超限。
func TestSaveLimits(t *testing.T) {
	s := newStore(t)
	if _, err := s.Save("a", httptest.NewRequest("POST", "/x", strings.NewReader("not multipart"))); err == nil {
		t.Error("非 multipart 应报错")
	}
	r := newUploadRequest(t, "file", "big.bin", "", bytes.Repeat([]byte{0}, MaxSize+1))
	if _, err := s.Save("a", r); err == nil {
		t.Error("超限应报错")
	}
	// 失败不留临时文件
	matches, _ := filepath.Glob(filepath.Join(s.dir, "*"))
	for _, m := range matches {
		if strings.HasSuffix(m, ".tmp") {
			t.Errorf("失败后残留临时文件: %s", m)
		}
	}
}

// TestInlineWhitelist 内联白名单：图片直显，SVG/HTML 强制下载。
func TestInlineWhitelist(t *testing.T) {
	for _, m := range []string{"image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp", "image/avif"} {
		if !Inline(m) {
			t.Errorf("%s 应可内联", m)
		}
	}
	for _, m := range []string{"image/svg+xml", "text/html", "application/pdf", "application/octet-stream"} {
		if Inline(m) {
			t.Errorf("%s 不可内联（XSS）", m)
		}
	}
}

// TestPrune 保留期清理：行与文件一并删除。
func TestPrune(t *testing.T) {
	s := newStore(t)
	r := newUploadRequest(t, "file", "old.txt", "text/plain", []byte("old"))
	m, err := s.Save("a", r)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if n := s.Prune(time.Now().Add(time.Hour)); n != 1 {
		t.Fatalf("应清理 1 个: %d", n)
	}
	if _, ok := s.Get(m.ID); ok {
		t.Error("清理后元数据应消失")
	}
	if _, err := os.Stat(s.FilePath(m.ID)); !os.IsNotExist(err) {
		t.Error("清理后文件应删除")
	}
	if n := s.Prune(time.Now().Add(time.Hour)); n != 0 {
		t.Errorf("重复清理应为 0: %d", n)
	}
	_ = io.Discard
}
