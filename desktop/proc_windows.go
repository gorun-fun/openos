//go:build windows

package main

import "os/exec"

func setSysProcAttr(_ *exec.Cmd) {}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil && cmd.ProcessState == nil {
		_ = cmd.Process.Kill()
	}
}
