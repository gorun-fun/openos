package main

// 版本检测与自动更新。发布通道 = download.gorun.fun/openos/cli（与 goder /
// grf-harness 同款「平台/版本/运行文件」三层目录布局）：
//
//	https://download.gorun.fun/openos/cli/<平台目录>/<版本>/<运行文件>
//	  平台目录：macos（amd64）/ macos-arm64 / linux / linux-arm64 /
//	           windows / windows-arm64（沿用 goder 的平台命名约定）
//	  版本目录：git tag 去掉 v 前缀（v0.0.60 → 0.0.60），每个版本一个目录
//	  运行文件：openos-desktop-<平台>[-arm64][.exe]（裸可执行文件）+
//	           同名 .sha256 校验文件 + notes.txt 发布说明
//
// 流程：启动时后台静默检查（列平台目录索引页取最大版本号）→ 有新版时设置页
// 显示「立即更新」→ 下载到同目录 .new（sha256 校验防半截/错档）→ 原子换文件
//（旧件改 .old、新件就位；失败回滚）→ 通知主流程收尾重启（unix 原位 exec 同
// PID，Windows 拉新进程退出）。所有状态（进度/错误）经设置页 /api/version
// 只读暴露。OPENOS_DESKTOP_UPDATE_URL 可覆盖通道地址（测试用）。

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// version 构建期注入（-ldflags "-X main.version=..."）：tag 精确命中为版本号
//（如 0.0.60），否则 dev（本地构建）。显示与更新比较都用它
var version = "dev"

const (
	// defaultUpdateBase 官方发布通道（download.gorun.fun = 10.8.0.5:/data/download）
	defaultUpdateBase = "https://download.gorun.fun/openos/cli"
	// updateCheckTimeout 检查（列目录）/校验文件拉取的超时；下载不设整体超时
	updateCheckTimeout = 10 * time.Second
	// updateStaleAfter 设置页多久没查就同步再查一次
	updateStaleAfter = 10 * time.Minute
	// updateRestartDelay 通知主流程重启前的缓冲：给设置页一点时间展示「正在重启」
	updateRestartDelay = 1200 * time.Millisecond
)

// updateBase 发布通道地址（OPENOS_DESKTOP_UPDATE_URL 可覆盖）
func updateBase() string {
	if v := strings.TrimSpace(os.Getenv("OPENOS_DESKTOP_UPDATE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultUpdateBase
}

// updatePlatform 当前平台在发布通道的目录名与运行文件名
func updatePlatform() (dir, file string, err error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/amd64":
		dir = "macos"
	case "darwin/arm64":
		dir = "macos-arm64"
	case "linux/amd64":
		dir = "linux"
	case "linux/arm64":
		dir = "linux-arm64"
	case "windows/amd64":
		dir = "windows"
	case "windows/arm64":
		dir = "windows-arm64"
	default:
		return "", "", fmt.Errorf("平台 %s/%s 无发布通道", runtime.GOOS, runtime.GOARCH)
	}
	file = "openos-desktop-" + dir
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	return dir, file, nil
}

// parseVersion 解析版本号为数值分量（"v0.0.60"/"0.0.60" → [0,0,60]）；
// 去掉 -/+ 后缀，任一分量非数字（dev 等）返回 nil 表示不可比较
func parseVersion(s string) []int {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return nil
	}
	nums := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		nums[i] = n
	}
	return nums
}

// newerThan 判定版本 a 是否高于 b。不可解析（dev）视为最低：本地 dev 构建对任何
// 线上版本都提示可更新（给手工构建的旧客户端一个升到正式版的出口）
func newerThan(a, b string) bool {
	va, vb := parseVersion(a), parseVersion(b)
	if va == nil {
		return false
	}
	if vb == nil {
		return true
	}
	for i := 0; i < len(va) || i < len(vb); i++ {
		x, y := 0, 0
		if i < len(va) {
			x = va[i]
		}
		if i < len(vb) {
			y = vb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// verHrefRe 目录索引页里的版本子目录链接（服务端目录列表格式：
// <a href="0.0.60/">；取以数字开头、不含路径分隔的名字，与 CI 消费
// grf-harness 目录的 grep 同款口径）
var verHrefRe = regexp.MustCompile(`href="([0-9][^"/]*)/"`)

// listRemoteVersions 拉取平台目录索引页，解析全部版本号（降序，[0] 最新）
func listRemoteVersions(base, dir string) ([]string, error) {
	client := &http.Client{Timeout: updateCheckTimeout}
	resp, err := client.Get(base + "/" + dir + "/")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("版本目录拉取失败：HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range verHrefRe.FindAllStringSubmatch(string(body), -1) {
		if parseVersion(m[1]) != nil {
			out = append(out, m[1])
		}
	}
	if len(out) == 0 {
		return nil, errors.New("版本目录为空或格式不符")
	}
	sort.Slice(out, func(i, j int) bool { return newerThan(out[i], out[j]) })
	return out, nil
}

// updateState 设置页只读暴露的更新状态快照
type updateState struct {
	Current    string  `json:"current"`
	Latest     string  `json:"latest,omitempty"`
	HasUpdate  bool    `json:"hasUpdate"`
	Updating   bool    `json:"updating"`
	Restarting bool    `json:"restarting"`
	Progress   float64 `json:"progress"` // 0~1；-1 = 未知总长
	Error      string  `json:"error,omitempty"`
	CheckedAt  string  `json:"checkedAt,omitempty"`
}

// updater 更新器：状态 + 单飞检查 + 下载替换。
//
// 状态字段统一由 mu 保护；checkMu 让并发检查串行（设置页与启动检查同时发起时
// 后者排队、拿到前者刚写入的结果再决定要不要再查）。restartCh 是给主流程的
// 重启信号（缓冲 1，主流程 select 消费）。
type updater struct {
	mu         sync.Mutex
	current    string
	latest     string
	hasUpdate  bool
	checkedAt  time.Time
	checking   bool
	updating   bool
	restarting bool
	progress   float64
	lastErr    string

	checkMu  sync.Mutex
	restartCh chan struct{}
}

func newUpdater(cur string) *updater {
	return &updater{current: cur, progress: -1, restartCh: make(chan struct{}, 1)}
}

// restartReady 主流程 select 消费：下载替换已完成，收尾重启
func (u *updater) restartReady() <-chan struct{} { return u.restartCh }

// stale 距上次检查是否已超过刷新窗口
func (u *updater) stale() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return time.Since(u.checkedAt) > updateStaleAfter
}

// check 执行一次线上版本检查并写状态（单飞：并发调用排队）。检查失败不清掉
// 已知的新版本信息，只记录错误——上次已知「有更新」的结论不该被一次网络抖动抹掉
func (u *updater) check() {
	u.checkMu.Lock()
	defer u.checkMu.Unlock()
	u.mu.Lock()
	if u.updating || u.restarting {
		u.mu.Unlock()
		return
	}
	u.checking = true
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.checking = false
		u.mu.Unlock()
	}()
	dir, _, err := updatePlatform()
	if err == nil {
		var vers []string
		vers, err = listRemoteVersions(updateBase(), dir)
		if err == nil {
			latest := vers[0]
			u.mu.Lock()
			u.latest = latest
			u.hasUpdate = newerThan(latest, u.current)
			u.checkedAt = time.Now()
			u.lastErr = ""
			u.mu.Unlock()
			return
		}
	}
	u.mu.Lock()
	u.checkedAt = time.Now()
	u.lastErr = err.Error()
	u.mu.Unlock()
}

// state 状态快照（设置页 /api/version 用）
func (u *updater) state() updateState {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := updateState{
		Current: u.current, Latest: u.latest, HasUpdate: u.hasUpdate,
		Updating: u.updating, Restarting: u.restarting,
		Progress: u.progress, Error: u.lastErr,
	}
	if !u.checkedAt.IsZero() {
		s.CheckedAt = u.checkedAt.Format(time.RFC3339)
	}
	return s
}

// start 触发更新（设置页 POST /api/update）：后台下载替换，完成后置 restarting
// 并通知主流程重启。返回错误仅表示「没开始」（已在更新/无可更新）。
func (u *updater) start() error {
	u.mu.Lock()
	if u.updating || u.restarting {
		u.mu.Unlock()
		return errors.New("更新正在进行中")
	}
	if !u.hasUpdate {
		u.mu.Unlock()
		return errors.New("当前已是最新版本")
	}
	u.updating = true
	u.progress = -1
	u.lastErr = ""
	ver := u.latest
	u.mu.Unlock()

	go func() {
		err := u.apply(ver)
		u.mu.Lock()
		if err != nil {
			u.updating = false
			u.lastErr = err.Error()
			u.mu.Unlock()
			fmt.Fprintf(os.Stderr, "自动更新失败: %v\n", err)
			return
		}
		u.restarting = true
		u.mu.Unlock()
		fmt.Printf("已更新到 %s，%v 后重启客户端…\n", ver, updateRestartDelay)
		time.Sleep(updateRestartDelay)
		select {
		case u.restartCh <- struct{}{}:
		default:
		}
	}()
	return nil
}

// apply 下载指定版本、sha256 校验、原子换文件（定位自身路径后委托 applyToFile）
func (u *updater) apply(ver string) error {
	exe, err := selfPath()
	if err != nil {
		return err
	}
	return u.applyToFile(ver, exe)
}

// applyToFile 下载指定版本到 exe 同目录并原子换文件。文件先落 <exe>.new 再
// 两步改名（旧件 → .old，新件 → 原名），任一步失败回滚；.old 由下次启动清理。
func (u *updater) applyToFile(ver, exe string) error {
	dir, file, err := updatePlatform()
	if err != nil {
		return err
	}
	base := updateBase()
	fileURL := fmt.Sprintf("%s/%s/%s/%s", base, dir, ver, file)
	sumURL := fileURL + ".sha256"

	// 期望的 sha256 先取（有则强校验；缺校验文件时仅记录、不强拦——通道是
	// https，完整性另有传输层保障）
	want, sumErr := fetchText(sumURL)

	client := &http.Client{Timeout: 0} // 下载大文件不设整体超时
	resp, err := client.Get(fileURL)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}

	newPath := exe + ".new"
	f, err := os.OpenFile(newPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	h := sha256.New()
	total := resp.ContentLength
	var done int64
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := io.MultiWriter(f, h).Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(newPath)
				return fmt.Errorf("写临时文件失败: %w", werr)
			}
			done += int64(n)
			u.mu.Lock()
			if total > 0 {
				u.progress = float64(done) / float64(total)
			} else {
				u.progress = -1
			}
			u.mu.Unlock()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(newPath)
			return fmt.Errorf("下载中断: %w", rerr)
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(newPath)
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if sumErr == nil && want != "" {
		if !strings.EqualFold(strings.Fields(want)[0], got) {
			os.Remove(newPath)
			return errors.New("sha256 校验不符，已放弃替换")
		}
	} else {
		fmt.Printf("更新包无 sha256 校验文件（%v），跳过校验\n", sumErr)
	}

	// 两步改名换文件（Windows 允许改名运行中的 exe，只是不能删/覆写）
	oldPath := exe + ".old"
	os.Remove(oldPath)
	if err := os.Rename(exe, oldPath); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("备份旧版本失败: %w", err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		if rb := os.Rename(oldPath, exe); rb != nil {
			return fmt.Errorf("新版本就位失败且回滚失败（%v），请手动把 %s 改回 %s", err, oldPath, exe)
		}
		os.Remove(newPath)
		return fmt.Errorf("新版本就位失败（已回滚）: %w", err)
	}
	return nil
}

// fetchText 拉小文本（sha256 校验文件），失败返回错误
func fetchText(u string) (string, error) {
	client := &http.Client{Timeout: updateCheckTimeout}
	resp, err := client.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// selfPath 当前可执行文件的真实路径（解析符号链接）。go run 的临时产物拒绝
// 自更新（换文件毫无意义且会污染构建缓存目录）。
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if strings.Contains(exe, "go-build") {
		return "", errors.New("go run 的临时构建不支持自更新，请先 go build")
	}
	return exe, nil
}

// removeStaleUpdate 启动时清理上次更新留下的 <exe>.old（best-effort）
func removeStaleUpdate() {
	exe, err := selfPath()
	if err != nil {
		return
	}
	_ = os.Remove(exe + ".old")
}
