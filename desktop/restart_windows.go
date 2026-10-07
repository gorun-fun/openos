//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// restartSelf 更新完成后以相同参数重启自身（Windows 没有 exec 原位替换语义：
// 拉起独立的新进程后退出旧进程。新进程是 GUI 子系统 exe，无窗口闪烁；其
// attachParentConsole 会挂回同一终端，从 cmd/PowerShell 启动的横幅照常可见）。
// 调用前须已停止 Chrome（user-data-dir 锁不释放，新实例拉不起同目录的 Chrome）。
func restartSelf() {
	exe, err := selfPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "重启失败: %v\n", err)
		os.Exit(1)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "重启失败（%v），请手动启动 %s\n", err, exe)
		os.Exit(1)
	}
	os.Exit(0)
}
