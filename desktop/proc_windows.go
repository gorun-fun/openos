//go:build windows

package main

import (
	"os/exec"
	"strconv"
	"strings"
)

func setSysProcAttr(_ *exec.Cmd) {}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil && cmd.ProcessState == nil {
		_ = cmd.Process.Kill()
	}
}

// ---- reset 子命令的进程操作 ----

// signalDesktopPID 用 taskkill /T 树杀：客户端连同其拉起的 Chrome 子进程一并
// 终止（Windows 无进程组语义，/T 即树杀等价物），同步完成无需等待
func signalDesktopPID(pid int) error {
	return exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
}

// forceKillPID 同 signalDesktopPID（taskkill /F 本就是强制终止）
func forceKillPID(pid int) error {
	return signalDesktopPID(pid)
}

func pidAlive(pid int) bool {
	return tasklistRow(pid) != ""
}

// pidIsDesktop 校验 PID 身份（防 PID 复用误杀）：进程映像名须为 openos-desktop
func pidIsDesktop(pid int) bool {
	return strings.Contains(strings.ToLower(tasklistRow(pid)), "openos-desktop")
}

func tasklistRow(pid int) string {
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// killChromeLeftovers taskkill /T 已树杀 Chrome，无需兜底
func killChromeLeftovers(_ string) {}

// execOutput 跑命令取 stdout（Windows 的 reset 不做 pgrep 兜底扫描，此函数仅为
// 编译对齐保留）
func execOutput(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}
