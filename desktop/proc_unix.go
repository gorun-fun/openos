//go:build !windows

package main

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Chrome 编入独立进程组（它会拉起若干 GPU/Network 子进程），退出时整组终止
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil || cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	time.Sleep(300 * time.Millisecond)
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// ---- reset 子命令的进程操作 ----

// signalDesktopPID 优雅终止：客户端收到 SIGTERM 会按进程组连 Chrome 一起清掉
func signalDesktopPID(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// forceKillPID 优雅退出超时后的兜底
func forceKillPID(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// pidIsDesktop 校验 PID 身份（防 PID 复用误杀）：进程命令行须含 openos-desktop
func pidIsDesktop(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	return err == nil && strings.Contains(string(out), "openos-desktop")
}

// killChromeLeftovers 清理残留的 Chrome（客户端被 SIGKILL 兜底后可能留下）：
// 按 --user-data-dir 精确匹配，尾随空格要求其后还有参数（命令行以空格分隔），
// 与 run.sh stop 同款思路防前缀误杀；pkill 失败静默——目录即将删除，无碍
func killChromeLeftovers(profile string) {
	_ = exec.Command("pkill", "-f", "--user-data-dir="+profile+" ").Run()
}

// execOutput 跑命令取 stdout（reset 的枚举用；命令缺失/失败返回错误）
func execOutput(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}
