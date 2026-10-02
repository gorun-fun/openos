//go:build !windows

package main

import (
	"os/exec"
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
