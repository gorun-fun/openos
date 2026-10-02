package main

// reset 子命令的纯逻辑测试：profile 匹配与数据目录特征判定（进程操作部分依赖
// 运行环境，由 e2e 覆盖）。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArgsUsesProfile(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	def := filepath.Join(home, ".openos-desktop")
	cases := []struct {
		name, args, profile string
		want                bool
	}{
		{"显式相等", "/opt/openos-desktop -profile /tmp/a -url http://x", "/tmp/a", true},
		{"显式相等=形态", "/opt/openos-desktop -profile=/tmp/a", "/tmp/a", true},
		{"显式不等", "/opt/openos-desktop -profile /tmp/b", "/tmp/a", false},
		{"未指定+默认目录", "/opt/openos-desktop -url http://x", def, true},
		{"未指定+非默认目录", "/opt/openos-desktop -url http://x", "/tmp/a", false},
		{"指定了但空值+非默认", "/opt/openos-desktop -profile", "/tmp/a", false},
	}
	for _, c := range cases {
		if got := argsUsesProfile(c.args, c.profile); got != c.want {
			t.Errorf("%s: argsUsesProfile(%q, %q) = %v, want %v", c.name, c.args, c.profile, got, c.want)
		}
	}
}

func TestHasClientMarks(t *testing.T) {
	dir := t.TempDir()
	// 空目录 = 允许（全新目录，无损失）
	ents, _ := os.ReadDir(dir)
	if hasClientMarks(ents) {
		t.Fatal("空目录不应有特征")
	}
	// 无关文件 = 拒绝
	os.WriteFile(filepath.Join(dir, "random.txt"), []byte("x"), 0o644)
	ents, _ = os.ReadDir(dir)
	if hasClientMarks(ents) {
		t.Fatal("无关文件不应被认作客户端数据目录")
	}
	// Chrome 特征（Default 目录 / Local State）与客户端特征（config.json / client.pid）
	for _, mark := range []string{"Default", "Local State", "config.json", "client.pid"} {
		d := t.TempDir()
		if mark == "Default" {
			os.Mkdir(filepath.Join(d, "Default"), 0o755)
		} else {
			os.WriteFile(filepath.Join(d, mark), []byte("x"), 0o644)
		}
		ents, _ = os.ReadDir(d)
		if !hasClientMarks(ents) {
			t.Errorf("特征 %q 应被识别", mark)
		}
	}
}
