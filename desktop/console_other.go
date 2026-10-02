//go:build !windows

package main

// attachParentConsole 非 Windows 平台为 no-op：console 子系统本就关联 stdio，
// GUI 子系统的终端挂回桥只对 Windows 构建有意义（见 console_windows.go）
func attachParentConsole() {}

// fatalDialog 非 Windows 平台为 no-op：stderr 恒可用，无需弹窗兜底
func fatalDialog(err error) {}
