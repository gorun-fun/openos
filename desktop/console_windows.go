//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// hasConsole：AttachConsole 成功（进程已挂上某个终端）后为 true——fatal 的弹窗
// 兜底据此跳过：终端里看得到 stderr 就不再多弹一个框打扰
var hasConsole bool

// attachParentConsole 把 GUI 子系统（-H windowsgui，run.sh build 注入）进程挂回
// 启动它的终端：该构建形态下 stdio 不与控制台关联，从 cmd/PowerShell 启动时启动
// 横幅、reset 子命令的输出与 [y/N] 确认输入全部拿不到。AttachConsole 挂上父进程
// 的控制台后重开 CONOUT$/CONIN$ 恢复三个标准流。失败（双击启动等无父终端场景）
// 静默返回——GUI 主路径本就不依赖 stdio。
func attachParentConsole() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	attach := kernel32.NewProc("AttachConsole")
	// ATTACH_PARENT_PROCESS = 0xFFFFFFFF
	if r, _, _ := attach.Call(^uintptr(0)); r == 0 {
		return
	}
	hasConsole = true
	if con, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = con
		os.Stderr = con
	}
	if con, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = con
	}
}

// fatalDialog：GUI 子系统下 fatal 的兜底——双击启动（无控制台）时 stderr 无处
// 可看，典型如「未找到 Chrome」会直接静默退出、用户毫无线索；弹一个系统错误框。
// 有终端（已在终端语境输出过 stderr）则不弹。
func fatalDialog(err error) {
	if hasConsole {
		return
	}
	user32 := syscall.NewLazyDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")
	title, _ := syscall.UTF16PtrFromString("OpenOS 桌面客户端")
	text, _ := syscall.UTF16PtrFromString(fmt.Sprintf("启动失败：%v", err))
	const (
		mbOK        = 0x0
		mbIconError = 0x10
	)
	_, _, _ = messageBox.Call(0,
		uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)),
		mbOK|mbIconError)
}
