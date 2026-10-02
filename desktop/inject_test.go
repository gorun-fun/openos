package main

// 注入脚本是手工拼接的 IIFE 字符串，SyntaxError 会被客户端的 `_,_=` 静默吞掉、
// 表现为注入整体失效（历史踩坑：少一个闭合大括号）。凡注入源改动必须过
// node --check；本机无 node 则跳过（CI 有 node 时兜底）。设置页 HTML 内嵌的
// <script> 块一并提取校验。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func checkJS(t *testing.T, dir, name, src string) {
	t.Helper()
	f := filepath.Join(dir, name+".js")
	if err := os.WriteFile(f, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", "--check", f).CombinedOutput(); err != nil {
		t.Errorf("%s 语法错误: %v\n%s", name, err, out)
	}
}

func TestInjectSourcesSyntax(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("本机无 node，跳过注入脚本语法检查")
	}
	dir := t.TempDir()
	mark := markSource(123, 9333, "https://nocode.sankuai.com/", "http://127.0.0.1:17700")
	checkJS(t, dir, "mark", mark)
	if !strings.Contains(mark, `"settingsUrl":"http://127.0.0.1:17700"`) {
		t.Error("标记应含 settingsUrl 字段")
	}
	if markDown := markSource(123, 9333, "", ""); strings.Contains(markDown, "settingsUrl") {
		t.Error("设置服务降级时标记不应含 settingsUrl")
	}
	checkJS(t, dir, "shim", cookieShimSource())
	// 设置页 HTML：提取全部 <script> 块（head 的 embed 检测 + body 的页面逻辑）校验
	page := renderSettingsPage("https://os.example.com", []string{"https://os.example.com"}, "https://down.example.com")
	scripts := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllStringSubmatch(page, -1)
	if len(scripts) < 2 {
		t.Fatal("设置页应至少有两个 <script> 块（embed 检测 + 页面逻辑）")
	}
	for i, m := range scripts {
		checkJS(t, dir, "settings-page-"+strconv.Itoa(i), m[1])
	}
}
