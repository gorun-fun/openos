package main

// 服务器设置（OpenOS 兼容端连接）的单元测试：地址规范化、配置读写、Connect 的
// recent 维护与设置页 connect 接口。

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeTargetURL(t *testing.T) {
	ok := []struct{ in, want string }{
		{"https://os.example.com", "https://os.example.com"},
		{"  https://os.example.com/  ", "https://os.example.com/"},
		{"os.example.com", "https://os.example.com"},
		{"os.example.com/x#frag", "https://os.example.com/x"},
		{"localhost:5173", "http://localhost:5173"},
		{"127.0.0.1:8081", "http://127.0.0.1:8081"},
		{"http://127.0.0.1:8081", "http://127.0.0.1:8081"},
	}
	for _, c := range ok {
		got, err := normalizeTargetURL(c.in)
		if err != nil || got != c.want {
			t.Errorf("normalizeTargetURL(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "   ", "ftp://x.com", "://", "http://"} {
		if _, err := normalizeTargetURL(bad); err == nil {
			t.Errorf("normalizeTargetURL(%q) 应报错", bad)
		}
	}
}

func TestServerConfConnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	sw := newServerConf(path, desktopConfig{
		URL:    "http://127.0.0.1:8081",
		Recent: []string{"http://127.0.0.1:8081"},
	}, "http://127.0.0.1:8081")
	if got := sw.Current(); got != "http://127.0.0.1:8081" {
		t.Fatalf("Current() = %q", got)
	}
	if got := sw.Origin(); got != "http://127.0.0.1:8081" { // 源含端口（同源语义）
		t.Fatalf("Origin() = %q", got)
	}
	if !sw.CoversHost("127.0.0.1") {
		t.Fatal("CoversHost(127.0.0.1) 应为 true")
	}

	// 连接在线兼容端：补 https、落盘、recent 置顶
	if _, err := sw.Connect("os.example.com"); err != nil {
		t.Fatal(err)
	}
	if got := sw.Current(); got != "https://os.example.com" {
		t.Fatalf("Connect 后 Current() = %q", got)
	}
	cfg := loadDesktopConfig(path)
	if cfg.URL != "https://os.example.com" {
		t.Fatalf("落盘 cfg.URL = %q", cfg.URL)
	}
	if len(cfg.Recent) != 2 || cfg.Recent[0] != "https://os.example.com" {
		t.Fatalf("落盘 recent = %v", cfg.Recent)
	}
	if !sw.CoversHost("example.com") || sw.CoversHost("xexample.com") {
		t.Fatal("CoversHost 父域判定错误")
	}

	// 切回本地：recent 去重、顺序置顶
	if _, err := sw.Connect("http://127.0.0.1:8081"); err != nil {
		t.Fatal(err)
	}
	cfg = loadDesktopConfig(path)
	if cfg.Recent[0] != "http://127.0.0.1:8081" || len(cfg.Recent) != 2 {
		t.Fatalf("切回后 recent = %v", cfg.Recent)
	}

	// 非法输入被拒且不落盘
	before := loadDesktopConfig(path)
	if _, err := sw.Connect("ftp://x"); err == nil {
		t.Fatal("非法地址应报错")
	}
	if after := loadDesktopConfig(path); after.URL != before.URL {
		t.Fatal("失败连接不应改动配置")
	}
}

func TestDedupeRecent(t *testing.T) {
	old := []string{"a", "b", "c", "d", "e"}
	got := dedupeRecent("b", old)
	if len(got) != 5 || got[0] != "b" || got[1] != "a" || got[2] != "c" {
		t.Fatalf("dedupeRecent = %v", got)
	}
	// 上限 recentMax
	big := []string{"1", "2", "3", "4", "5", "6", "7"}
	got = dedupeRecent("x", big)
	if len(got) != recentMax || got[0] != "x" {
		t.Fatalf("dedupeRecent 上限 = %v", got)
	}
}

func TestSettingsConnectAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	sw := newServerConf(path, desktopConfig{}, "")
	var navigated string
	h := &settingsHandler{conf: sw, nav: func(u string) { navigated = u }}

	req := httptest.NewRequest("POST", "/api/connect", strings.NewReader(`{"url":"os.example.com"}`))
	w := httptest.NewRecorder()
	h.connect(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) || !strings.Contains(w.Body.String(), "https://os.example.com") {
		t.Fatalf("connect 响应异常: %d %s", w.Code, w.Body.String())
	}
	if navigated != "https://os.example.com" {
		t.Fatalf("导航回调 = %q", navigated)
	}

	// 非法地址：400 + 错误信息，且不导航
	req = httptest.NewRequest("POST", "/api/connect", strings.NewReader(`{"url":""}`))
	w = httptest.NewRecorder()
	h.connect(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Fatalf("非法地址响应异常: %d %s", w.Code, w.Body.String())
	}

	// 非 POST 拒绝
	req = httptest.NewRequest("GET", "/api/connect", nil)
	w = httptest.NewRecorder()
	h.connect(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应 405, got %d", w.Code)
	}
}

func TestSettingsPagePickParam(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	sw := newServerConf(path, desktopConfig{URL: "http://127.0.0.1:8081"}, "http://127.0.0.1:8081")
	h := &settingsHandler{conf: sw, nav: func(string) {}}

	// ?pick=1 → 启动选择页形态
	req := httptest.NewRequest("GET", "/?pick=1", nil)
	w := httptest.NewRecorder()
	h.page(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "选择服务器") ||
		strings.Contains(w.Body.String(), "下次启动自动连接") {
		t.Fatalf("pick 形态响应异常: %d %s", w.Code, w.Body.String())
	}

	// 无参数 → 默认设置页形态
	req = httptest.NewRequest("GET", "/", nil)
	w = httptest.NewRecorder()
	h.page(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "下次启动自动连接") {
		t.Fatalf("默认形态响应异常: %d %s", w.Code, w.Body.String())
	}
}

func TestRenderSettingsPage(t *testing.T) {
	page := renderSettingsPage("https://os.example.com", []string{"https://os.example.com"}, "https://down.example.com", false)
	for _, want := range []string{"https://os.example.com", "无法打开", "https://down.example.com",
		"OpenOS · 服务器设置", "当前连接", "下次启动自动连接"} {
		if !strings.Contains(page, want) {
			t.Fatalf("设置页缺 %q", want)
		}
	}
	// 无 unreachable 时不渲染提示块
	page = renderSettingsPage("", nil, "", false)
	if strings.Contains(page, "无法打开") {
		t.Fatal("无 unreachable 不应渲染提示块")
	}
	// pick 形态（启动选择页）：标题与文案切换，且不出现「自动连接」语义
	page = renderSettingsPage("https://os.example.com", []string{"https://os.example.com"}, "", true)
	for _, want := range []string{"OpenOS · 选择服务器", "上次连接", "选择后进入对应桌面"} {
		if !strings.Contains(page, want) {
			t.Fatalf("pick 形态缺 %q", want)
		}
	}
	if strings.Contains(page, "下次启动自动连接") || strings.Contains(page, "OpenOS · 服务器设置") {
		t.Fatal("pick 形态不应保留设置页形态的文案")
	}
}
