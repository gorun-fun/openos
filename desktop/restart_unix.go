//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// restartSelf 更新完成后以相同参数重启自身（unix：syscall.Exec 原位替换进程
// 映像——PID 不变，run.sh 的 pidfile 依然有效，stdout/stderr 也随进程保留，
// 日志继续写同一个文件）。调用前须已停止 Chrome（profile 锁不释放，新实例
// 拉不起同目录的 Chrome）。
func restartSelf() {
	exe, err := selfPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "重启失败: %v\n", err)
		os.Exit(1)
	}
	argv := append([]string{exe}, os.Args[1:]...)
	if err := syscall.Exec(exe, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "重启失败（%v），请手动启动 %s\n", err, exe)
		os.Exit(1)
	}
}
