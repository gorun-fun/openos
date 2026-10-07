package main

// 自动更新单元测试：版本比较 / 平台映射 / 目录索引解析 / 状态流转 / 换文件。

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want []int
	}{
		{"v0.0.60", []int{0, 0, 60}},
		{"0.0.60", []int{0, 0, 60}},
		{"0.1.0", []int{0, 1, 0}},
		{"1.2.3.4", []int{1, 2, 3, 4}},
		{"v0.0.59-3-gee224ba", []int{0, 0, 59}}, // 后缀截断
		{"dev", nil},
		{"", nil},
		{"0.x.1", nil},
		{"v1", nil},
	}
	for _, c := range cases {
		got := parseVersion(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parseVersion(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseVersion(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

func TestNewerThan(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.0.60", "0.0.59", true},
		{"0.0.59", "0.0.60", false},
		{"0.0.60", "0.0.60", false},
		{"0.1.0", "0.0.99", true},
		{"1.0.0", "0.99.99", true},
		{"0.0.60", "v0.0.60", false}, // v 前缀兼容
		{"0.0.60", "dev", true},      // dev 视为最低
		{"dev", "0.0.1", false},
		{"dev", "dev", false},
		{"0.0.10", "0.0.9", true}, // 数值比较非字典序
		{"0.0.60", "0.0.59-3-gee224ba", true},
	}
	for _, c := range cases {
		if got := newerThan(c.a, c.b); got != c.want {
			t.Errorf("newerThan(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestUpdatePlatform(t *testing.T) {
	dir, file, err := updatePlatform()
	if err != nil {
		t.Fatalf("当前平台应受支持: %v", err)
	}
	if file != "openos-desktop-"+dir && file != "openos-desktop-"+dir+".exe" {
		t.Errorf("文件名应与平台目录一致: dir=%s file=%s", dir, file)
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(file, ".exe") {
		t.Errorf("Windows 文件应带 .exe 后缀: %s", file)
	}
	if runtime.GOOS != "windows" && strings.HasSuffix(file, ".exe") {
		t.Errorf("非 Windows 文件不应带 .exe 后缀: %s", file)
	}
}

// 目录索引页样例（服务端目录列表格式，摘自 download.gorun.fun/goder/）
const sampleIndexHTML = `<!DOCTYPE html>
<html><head><title>/macos-arm64/</title></head>
<body><table><tbody>
<tr><td class="name"><a href="../">../</a></td></tr>
<tr><td class="name"><a href="0.0.1-alpha.14/">0.0.1-alpha.14/</a></td></tr>
<tr><td class="name"><a href="0.0.59/">0.0.59/</a></td></tr>
<tr><td class="name"><a href="0.0.60/">0.0.60/</a></td></tr>
<tr><td class="name"><a href="0.0.7/">0.0.7/</a></td></tr>
<tr><td class="name"><a href="notes.txt">notes.txt</a></td></tr>
</tbody></table></body></html>`

func TestListRemoteVersions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/macos-arm64/" {
			_, _ = w.Write([]byte(sampleIndexHTML))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	vers, err := listRemoteVersions(srv.URL, "macos-arm64")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(vers) != 4 {
		t.Fatalf("应解析出 4 个版本（../、notes.txt、非版本项排除），got %v", vers)
	}
	if vers[0] != "0.0.60" {
		t.Errorf("最新应为 0.0.60（数值序），got %v", vers)
	}
	// 0.0.7 必须排在 0.0.59 之前之后按数值序：0.0.59 > 0.0.7
	if vers[1] != "0.0.59" || vers[2] != "0.0.7" {
		t.Errorf("版本排序错误: %v", vers)
	}
}

func TestListRemoteVersionsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>no links</html>"))
	}))
	defer srv.Close()
	if _, err := listRemoteVersions(srv.URL, "x"); err == nil {
		t.Fatal("空目录应报错")
	}
}

// TestUpdaterCheckFlow 走通：检查 → hasUpdate → state 快照
func TestUpdaterCheckFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			_, _ = w.Write([]byte(sampleIndexHTML))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("OPENOS_DESKTOP_UPDATE_URL", srv.URL)

	u := newUpdater("0.0.59")
	u.check()
	s := u.state()
	if s.Latest != "0.0.60" || !s.HasUpdate {
		t.Fatalf("应检出 0.0.60 可更新，got %+v", s)
	}
	if s.CheckedAt == "" {
		t.Error("checkedAt 应已记录")
	}

	u2 := newUpdater("0.0.60")
	u2.check()
	if s2 := u2.state(); s2.HasUpdate {
		t.Fatalf("同版本不应提示更新，got %+v", s2)
	}
}

// TestUpdaterStartApply 完整走通下载→校验→换文件（不含重启）
func TestUpdaterStartApply(t *testing.T) {
	// 造一个「当前二进制」：拷贝测试二进制到临时目录当作旧版本
	tmp := t.TempDir()
	old := filepath.Join(tmp, "openos-desktop")
	if err := copyFile(os.Args[0], old); err != nil {
		t.Fatalf("准备旧版本失败: %v", err)
	}
	newBody := "fake-new-binary-content"
	sum := sha256Hex([]byte(newBody))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/"):
			_, _ = w.Write([]byte(sampleIndexHTML))
		case strings.HasSuffix(r.URL.Path, ".sha256"):
			_, _ = w.Write([]byte(sum + "  openos-desktop"))
		default:
			_, _ = w.Write([]byte(newBody))
		}
	}))
	defer srv.Close()
	t.Setenv("OPENOS_DESKTOP_UPDATE_URL", srv.URL)

	// selfPath 读的是测试进程二进制，这里直接验证 apply 的换文件逻辑：
	// 用伪 updater 绕过 selfPath 太绕，改为把 apply 逻辑对 tmp 文件跑一遍——
	// 通过 start 的完整链路依赖 selfPath 指向 go-build 临时目录（会被拒绝），
	// 该防护本身在 TestSelfPathGuard 覆盖；此处直接调 apply。
	u := newUpdater("0.0.59")
	if err := u.applyToFile("0.0.60", old); err != nil {
		t.Fatalf("apply 失败: %v", err)
	}
	got, err := os.ReadFile(old)
	if err != nil || string(got) != newBody {
		t.Fatalf("换文件后内容不符: %q err=%v", got, err)
	}
	if fi, err := os.Stat(old); err != nil || fi.Mode()&0o111 == 0 {
		t.Errorf("新文件须可执行: %v %v", fi.Mode(), err)
	}
	if _, err := os.Stat(old + ".old"); err != nil {
		t.Error("旧版本应备份为 .old")
	}
}

// TestSelfPathGuard go run 的临时构建须拒绝自更新
func TestSelfPathGuard(t *testing.T) {
	if !strings.Contains(os.Args[0], "go-build") {
		t.Skip("非 go test 临时目录环境")
	}
	if _, err := selfPath(); err == nil {
		t.Fatal("go run/go test 临时产物应拒绝自更新")
	}
}

// TestUpdaterStartGuards start 的并发/重复触发守卫
func TestUpdaterStartGuards(t *testing.T) {
	u := newUpdater("0.0.59")
	if err := u.start(); err == nil {
		t.Fatal("无更新信息时应拒绝 start")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			_, _ = w.Write([]byte(sampleIndexHTML))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("OPENOS_DESKTOP_UPDATE_URL", srv.URL)
	u.check()
	// hasUpdate=true 但 selfPath 是 go-build 临时目录 → apply 失败落 lastErr
	if err := u.start(); err != nil {
		t.Fatalf("应允许开始: %v", err)
	}
	if err := u.start(); err == nil {
		t.Fatal("更新进行中应拒绝重复 start")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := u.state(); s.Error != "" && !s.Updating {
			return // 已失败收尾（go-build 防护），符合预期
		}
		if s := u.state(); s.Restarting {
			t.Fatal("go run 环境不应走到重启")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("start 后 5s 内未落错误态")
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
