package main

// reset 子命令：清空桌面客户端的全部本地数据，回到全新状态。
//
// 清空范围 = 专用 profile 目录整目录删除重建：
//   - Chrome 站点数据：登录态、Cookie、localStorage、缓存（美团等第三方会话）
//   - config.json：服务器配置（OpenOS 兼容端地址）与最近使用列表
//   - client.pid 等运行痕迹
//
// 两条硬约束：
//   - 先停仍在运行的实例再删目录（unix 优雅 SIGTERM——客户端会按进程组连 Chrome
//     一起清掉；Windows taskkill /T 树杀是等价物）。旧版二进制没写 client.pid，
//     unix 上另有 pgrep 按命令行匹配 profile 的兜底扫描；
//   - 防误删：目标目录必须带有客户端数据特征（Default/Local State/config.json/
//     client.pid）或为空目录，home、根与无特征目录一律拒绝——reset 是 rm -rf 级
//     操作，-profile 手滑指错路径不能波及无辜目录。
//
// 交互确认默认开启（显示清空范围与路径），-y 跳过；stdin 非交互（EOF）视为取消。

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func runReset(args []string) int {
	fs := flag.NewFlagSet("reset", flag.ExitOnError)
	profileFlag := fs.String("profile", "", "要清空的用户数据目录（默认 ~/.openos-desktop）")
	yesFlag := fs.Bool("y", false, "跳过确认直接执行")
	_ = fs.Parse(args)

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "openos-desktop reset:", err)
		return 1
	}
	profile := *profileFlag
	if profile == "" {
		profile = filepath.Join(home, ".openos-desktop")
	}
	if abs, err := filepath.Abs(profile); err == nil {
		profile = abs
	}
	if profile == string(filepath.Separator) || profile == home {
		fmt.Fprintf(os.Stderr, "openos-desktop reset: 拒绝清空 %s\n", profile)
		return 1
	}

	// 防误删：不存在的目录没有可清的；非空且无客户端数据特征的目录拒绝
	ents, err := os.ReadDir(profile)
	if err != nil {
		fmt.Printf("目录 %s 不存在，没有需要清空的数据。\n", profile)
		return 0
	}
	if len(ents) > 0 && !hasClientMarks(ents) {
		fmt.Fprintf(os.Stderr, "openos-desktop reset: %s 不是桌面客户端的数据目录（无 Default/Local State/config.json 特征），拒绝清空\n", profile)
		return 1
	}

	if !*yesFlag {
		fmt.Printf("将清空 %s 中的全部数据：\n", profile)
		fmt.Println("  · 浏览器登录态与站点数据（Cookie、localStorage、缓存）")
		fmt.Println("  · 服务器配置与最近使用（config.json）")
		fmt.Print("此操作不可恢复。继续? [y/N] ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("已取消。")
			return 0
		}
	}

	stopInstances(profile)
	killChromeLeftovers(profile)

	if err := os.RemoveAll(profile); err != nil {
		fmt.Fprintf(os.Stderr, "openos-desktop reset: 删除 %s 失败: %v\n", profile, err)
		return 1
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "openos-desktop reset: 重建 %s 失败: %v\n", profile, err)
		return 1
	}
	fmt.Printf("✓ 已清空 %s（登录态、站点数据、服务器配置），下次启动即全新状态。\n", profile)
	return 0
}

// hasClientMarks 判断目录项里是否有客户端数据特征：Chrome user-data-dir 的
// Default/Local State，或客户端自己写的 config.json/client.pid
func hasClientMarks(ents []os.DirEntry) bool {
	for _, e := range ents {
		switch e.Name() {
		case "Default", "Local State", "config.json", "client.pid":
			return true
		}
	}
	return false
}

// stopInstances 停止仍在运行、且使用该 profile 的桌面客户端实例：
// client.pid（本客户端启动时写入）优先，unix 上再兜底扫描同名进程按命令行匹配
//（旧版二进制没写 client.pid 的过渡期）。PID 复用由进程身份校验防误杀。
func stopInstances(profile string) {
	if b, err := os.ReadFile(filepath.Join(profile, "client.pid")); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			stopDesktopPID(pid)
		}
	}
	stopByScan(profile)
}

// stopByScan 枚举同名进程并按命令行匹配 profile（unix：pgrep + ps；Windows 的
// 进程枚举要 PowerShell/wmic，只依赖 client.pid + 提示手动停止）
func stopByScan(profile string) {
	out, err := execOutput("pgrep", "-x", "openos-desktop")
	if err != nil {
		return
	}
	for _, line := range strings.Fields(out) {
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 0 || pid == os.Getpid() {
			continue
		}
		args, _ := execOutput("ps", "-p", line, "-o", "args=")
		if argsUsesProfile(args, profile) {
			stopDesktopPID(pid)
		}
	}
}

// argsUsesProfile 判断客户端命令行是否使用给定 profile：显式 -profile 值相等，
// 或未带 -profile 且目标即默认目录（~/.openos-desktop）。Go flag 只接受
// -profile x 与 -profile=x 两种形态
func argsUsesProfile(args, profile string) bool {
	toks := strings.Fields(args)
	specified := ""
	for i, t := range toks {
		if t == "-profile" || t == "--profile" {
			if i+1 < len(toks) {
				specified = toks[i+1]
			}
			continue
		}
		for _, p := range []string{"-profile=", "--profile="} {
			if strings.HasPrefix(t, p) {
				specified = strings.TrimPrefix(t, p)
			}
		}
	}
	if specified != "" {
		if a, err := filepath.Abs(specified); err == nil && a == profile {
			return true
		}
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return profile == filepath.Join(home, ".openos-desktop")
}

// stopDesktopPID 停止并等待一个客户端实例退出（身份不符的 PID 直接忽略）
func stopDesktopPID(pid int) {
	if pid <= 0 || pid == os.Getpid() || !pidIsDesktop(pid) {
		return
	}
	_ = signalDesktopPID(pid)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && pidAlive(pid) {
		time.Sleep(100 * time.Millisecond)
	}
	if pidAlive(pid) {
		_ = forceKillPID(pid)
	}
}
