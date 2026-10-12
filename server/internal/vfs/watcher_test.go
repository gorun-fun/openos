package vfs

import (
	"os"
	"path/filepath"
	"testing"
)

type watchEvent struct {
	user, topic string
	payload     map[string]any
}

func newTestWatcher(root string) (*homeWatcher, *[]watchEvent) {
	var events []watchEvent
	w := &homeWatcher{
		homesDir: root,
		publish: func(user, topic string, payload any) {
			if m, ok := payload.(map[string]any); ok {
				events = append(events, watchEvent{user, topic, m})
			}
		},
		interval: fsWatchInterval,
		baseline: map[string]map[string]fileFinger{},
	}
	return w, &events
}

// 模拟 goder/真实终端绕过 REST 的外部写盘：轮询应检测新增/修改/删除，
// 且每轮按用户合并成一条事件。
func TestHomeWatcherDetectsExternalChanges(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "alice", "goder", "proj")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, events := newTestWatcher(root)

	// 首轮只建基线：磁盘上已有的不是「变更」
	w.scanOnce()
	if len(*events) != 0 {
		t.Fatalf("首轮应只建基线不发事件, got %d", len(*events))
	}

	// 外部写盘：子目录新增文件 + home 根建空目录 + 改已有文件内容
	if err := os.WriteFile(filepath.Join(sub, "b.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "alice", "新目录"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.txt"), []byte("hello world 变更"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.scanOnce()
	if len(*events) != 1 {
		t.Fatalf("一轮多处变更应合并成 1 条事件, got %d", len(*events))
	}
	e := (*events)[0]
	if e.user != "alice" || e.topic != "fs:changed" {
		t.Fatalf("事件目标错误: %+v", e)
	}
	if e.payload["kind"] != "watch" {
		t.Fatalf("kind 应为 watch: %+v", e.payload)
	}
	// 变更横跨 goder/proj 与 home 根（新目录），公共父目录收敛到 home 根
	if got := e.payload["path"]; got != "/home/alice" {
		t.Fatalf("公共父目录应为 /home/alice, got %v", got)
	}

	// 同目录两个文件变更 → path 精确到该目录
	*events = nil
	if err := os.WriteFile(filepath.Join(sub, "b.go"), []byte("changed!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "c.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.scanOnce()
	if len(*events) != 1 || (*events)[0].payload["path"] != "/home/alice/goder/proj" {
		t.Fatalf("同目录变更应指向该目录: %+v", *events)
	}

	// 无变更轮不发事件
	*events = nil
	w.scanOnce()
	if len(*events) != 0 {
		t.Fatalf("无变更不应发事件, got %d", len(*events))
	}

	// 删除文件同样发事件
	*events = nil
	if err := os.Remove(filepath.Join(sub, "c.txt")); err != nil {
		t.Fatal(err)
	}
	w.scanOnce()
	if len(*events) != 1 || (*events)[0].payload["path"] != "/home/alice/goder/proj" {
		t.Fatalf("删除应发事件指向所在目录: %+v", *events)
	}
}

// 新用户目录出现只建基线；目录消失静默丢弃基线（删号级联），不 panic 不发事件。
func TestHomeWatcherUserLifecycle(t *testing.T) {
	root := t.TempDir()
	w, events := newTestWatcher(root)

	if err := os.MkdirAll(filepath.Join(root, "bob"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bob", "f"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.scanOnce()
	if len(*events) != 0 {
		t.Fatalf("新用户首轮应只建基线, got %d", len(*events))
	}

	if err := os.WriteFile(filepath.Join(root, "bob", "g"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.scanOnce()
	if len(*events) != 1 || (*events)[0].user != "bob" {
		t.Fatalf("第二轮应检测到 bob 的变更: %+v", *events)
	}

	*events = nil
	if err := os.RemoveAll(filepath.Join(root, "bob")); err != nil {
		t.Fatal(err)
	}
	w.scanOnce()
	if len(*events) != 0 {
		t.Fatalf("用户目录消失不应发事件, got %d", len(*events))
	}
	if _, ok := w.baseline["bob"]; ok {
		t.Fatal("用户目录消失后基线应被丢弃")
	}
}

func TestCommonParentDir(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"/home/u/a/f1", "/home/u/a/f2"}, "/home/u/a"},
		{[]string{"/home/u/a/f1", "/home/u/b/f2"}, "/home/u"},
		{[]string{"/home/u/a/x/f", "/home/u/a/y/f"}, "/home/u/a"},
		{[]string{"/home/u/f"}, "/home/u"},
		{[]string{"/home/u/a"}, "/home/u"}, // 目录条目本身取其父
	}
	for _, c := range cases {
		if got := commonParentDir(c.in); got != c.want {
			t.Errorf("commonParentDir(%v)=%q want %q", c.in, got, c.want)
		}
	}
}
