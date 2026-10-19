// atomicfile_test.go — 原子写基本语义：内容/权限落位、覆写整体替换、不留 .tmp、
// 流式 Commit/Abort 清理路径。
package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// assertNoTmp 确认目标旁没有 .tmp 暂存残留（Commit/Abort 的清理语义）。
func assertNoTmp(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("不应残留暂存文件 %s.tmp", path)
	}
}

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")

	if err := Write(p, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil || string(got) != "v1" {
		t.Fatalf("应写入 v1: %q err=%v", got, err)
	}
	assertNoTmp(t, p)
	// perm 落到目标文件（Windows 权限模型不同，跳过断言）
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
			t.Fatalf("权限应为 0600，got %v", st.Mode().Perm())
		}
	}

	// 覆写更长的旧内容 → 整体替换无残留尾巴（rename 而非原地覆盖的语义）
	if err := Write(p, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(p)
	if err != nil || string(got) != "v2" {
		t.Fatalf("覆写后应为 v2（无旧内容尾巴）: %q err=%v", got, err)
	}
}

func TestStagedCommit(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "blob")

	st, err := Stage(dst, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	// 分片流式写（attach 上传的形态：多轮 Write 后一次 Commit）
	for _, chunk := range []string{"openos", "附件", "流式"} {
		if _, err := st.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "openos附件流式" {
		t.Fatalf("流式内容不符: %q err=%v", got, err)
	}
	assertNoTmp(t, dst)
}

func TestStagedAbort(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "blob")

	st, err := Stage(dst, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Write([]byte("半截")); err != nil {
		t.Fatal(err)
	}
	st.Abort()
	// 目标不出现、暂存清理；Abort 幂等（重复调用不报错）
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("Abort 后目标文件不应存在")
	}
	assertNoTmp(t, dst)
	st.Abort()
}

func TestStageKeepsExistingTarget(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(dst, []byte("旧内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 暂存中途放弃：既有目标原样保留（写一半崩溃时读端看到的仍是完整旧内容）
	st, err := Stage(dst, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Write([]byte("新内容半截")); err != nil {
		t.Fatal(err)
	}
	st.Abort()
	got, _ := os.ReadFile(dst)
	if string(got) != "旧内容" {
		t.Fatalf("放弃暂存后旧内容应保留: %q", got)
	}
	if strings.HasPrefix(string(got), "新内容") {
		t.Fatal("半截新内容不应出现")
	}
}
