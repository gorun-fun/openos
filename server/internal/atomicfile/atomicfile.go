// Package atomicfile 统一「同目录临时文件 + rename」的原子落盘：写一半崩溃/
// 断电时目标文件要么是旧内容要么不存在，绝不会是半截新内容（rename 在同一
// 文件系统内原子，故临时文件必须与目标同目录）。market bundle 落盘与 attach
// 附件落盘共用（整体写用 Write，流式写用 Stage/Commit/Abort）。
package atomicfile

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Write 原子写文件：数据整体写入同目录随机名暂存后 rename 到 path。perm 为
// 目标文件权限（Commit 时校正，rename 后即最终权限）。
func Write(path string, data []byte, perm os.FileMode) error {
	f, err := Stage(path, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Abort()
		return err
	}
	return f.Commit()
}

// Staged 目标同目录的暂存文件（流式写场景）：边写边落暂存，写完 Commit 原子
// 改名到目标；中途失败 Abort 清理暂存。Commit/Abort 之后不可复用。
type Staged struct {
	tmp  string
	dst  string
	f    *os.File
	perm os.FileMode
}

// Write 流式写入暂存文件（实现 io.Writer 语义）。
func (s *Staged) Write(p []byte) (int, error) { return s.f.Write(p) }

// Stage 在目标同目录创建随机名暂存文件（O_EXCL 独占创建语义，可立即写入）。
// 随机名 + 独占创建取代旧版固定 <path>.tmp：固定名可被预置符号链接劫持
//（O_WRONLY|O_TRUNC 跟随链接截断攻击者选中的目标文件，安全审计 2026-10
// 第二轮——数据目录对 goder/终端可写的部署形态下属真实面）；CreateTemp 恒
// 0600，最终权限在 Commit 时校正。
func Stage(path string, perm os.FileMode) (*Staged, error) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	f, err := os.CreateTemp(dir, base+".*.tmp")
	if err != nil {
		return nil, err
	}
	return &Staged{tmp: f.Name(), dst: path, f: f, perm: perm}, nil
}

// Commit 关闭暂存文件并原子改名到目标路径；任一步失败即清理暂存（不留垃圾）。
func (s *Staged) Commit() error {
	if err := s.f.Close(); err != nil {
		os.Remove(s.tmp)
		return err
	}
	if err := os.Chmod(s.tmp, s.perm); err != nil {
		os.Remove(s.tmp)
		return err
	}
	if err := os.Rename(s.tmp, s.dst); err != nil {
		os.Remove(s.tmp)
		return err
	}
	return nil
}

// Abort 放弃暂存：关句柄并删除暂存文件（幂等；删除失败静默——留个 .tmp 垃圾
// 优于误删别的文件）。
func (s *Staged) Abort() {
	_ = s.f.Close()
	_ = os.Remove(s.tmp)
}

// WriteJSON 便捷形态：缩进 Marshal（尾带换行）后原子写（steward 状态文件与
// 转制货架 catalog 共用）。
func WriteJSON(path string, v any, perm os.FileMode) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return Write(path, append(raw, '\n'), perm)
}
