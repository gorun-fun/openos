// install.go — 插件安装面：把 openos 接入每个 goder 用户的 gh。
//
// 接入方式（保住「settings.json 无 plugins 键 = 自动发现」语义，不写 URL
// 条目把 gh 切到显式列表模式）：
//   - 自动发现模式：向 <serve-data>/users/<san>/grf-home/plugins/ 落一个
//     wrapper（unix sh / Windows .cmd），内容烙 openos-server 可执行路径、
//     MCP 端点、令牌与用户名，gh 自动发现直接收编（stdio 插件）。
//   - 显式列表模式（settings.json 有 plugins 键）：把 [exe, mcp-shim,
//     endpoint, token, uid] 命令项 upsert 进数组，其余条目原样保留。
//
// 时机：Start 时扫描存量用户目录自愈（可执行路径变化）；新用户在 /goder-api
// 首次请求时 EnsureUser（先于 goder-serve 懒启动该用户的 gh，保证插件在 gh
// 启动那一刻已就位）。安装幂等，内容不变不写。
package osmcp

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// pluginName gh 侧的插件名（工具归属展示 mcp__openos__*）。
const pluginName = "openos"

// uidSan 用户名 → 目录名清洗规则，与 goder ghserver 的 manager.go uidSan
// 严格一致（grf-home 路径由 goder-serve 按 UID 生成，两边必须同步改）。
var uidSan = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// userGrfHome 某用户的 GRF_HOME（goder-serve PerUserHome 布局）。
func userGrfHome(dataDir, uid string) string {
	san := uidSan.ReplaceAllString(uid, "_")
	return filepath.Join(dataDir, "goder", "serve-data", "users", san, "grf-home")
}

func wrapperFileName() string {
	if runtime.GOOS == "windows" {
		return pluginName + ".cmd"
	}
	return pluginName
}

// shQuote shell 单引号包裹（uid 无字符集限制，可能含空格/引号/元字符）。
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func wrapperContent(exe, endpoint, token, uid string) string {
	if runtime.GOOS == "windows" {
		// Windows 双引号包裹（含引号的极端用户名不支持，主流部署为 unix/docker）
		return "@echo off\r\n\"" + exe + "\" mcp-shim " + endpoint + " " + token + " \"" + uid + "\" %*\r\n"
	}
	return "#!/bin/sh\nexec \"" + exe + "\" mcp-shim " + endpoint + " " + token + " " + shQuote(uid) + " \"$@\"\n"
}

// installPlugin 幂等安装一个用户：落 wrapper + 显式列表模式下 upsert 命令项。
func installPlugin(exe, grfHome, endpoint, token, uid string) error {
	pluginsDir := filepath.Join(grfHome, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return fmt.Errorf("创建插件目录: %w", err)
	}
	p := filepath.Join(pluginsDir, wrapperFileName())
	content := wrapperContent(exe, endpoint, token, uid)
	if raw, err := os.ReadFile(p); err != nil || string(raw) != content {
		// 0700：wrapper 内嵌 MCP 端点令牌，多用户机器上不给其他本地用户读权限
		if err := os.WriteFile(p, []byte(content), 0o700); err != nil {
			return fmt.Errorf("写入插件 wrapper: %w", err)
		}
	}
	return upsertSettings(filepath.Join(grfHome, "settings.json"), exe, endpoint, token, uid)
}

// upsertSettings settings.json 有 plugins 键（显式列表模式）时把 openos 命令项
// upsert 进去；无键（自动发现）或无文件不动。
func upsertSettings(settingsPath, exe, endpoint, token, uid string) error {
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil // 无 settings.json = 自动发现模式，wrapper 已生效
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil // 损坏文件不碰
	}
	cur, has := all["plugins"]
	if !has {
		return nil // 自动发现模式
	}
	var list []map[string]any
	if err := json.Unmarshal(cur, &list); err != nil {
		return fmt.Errorf("settings.json plugins 数组解析失败: %w", err)
	}
	entry := map[string]any{
		"name":    pluginName,
		"command": []string{exe, "mcp-shim", endpoint, token, uid},
	}
	replaced := false
	for i, p := range list {
		if name, _ := p["name"].(string); name == pluginName {
			list[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, entry)
	}
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	all["plugins"] = data
	out, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, append(out, '\n'), 0o600)
}

// readWrapperUID 从既有 wrapper 内容里抠出烙写的用户名（存量自愈用：目录名是
// 清洗后的，无法可靠反推原始用户名）。wrapper 形如
//
//	exec "<exe>" mcp-shim <endpoint> <token> '<uid>' "$@"    （unix，uid 已单引号转义）
//	"<exe>" mcp-shim <endpoint> <token> "<uid>" %*            （windows）
//
// endpoint/token 不含引号与空白，故 mcp-shim 之后的第一/最后一对引号必属 uid。
func readWrapperUID(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := string(raw)
	i := strings.Index(s, "mcp-shim ")
	if i < 0 {
		return ""
	}
	rest := s[i+len("mcp-shim "):]
	// 剥掉结尾转发参数（unix "$@" / windows %*）
	if j := strings.LastIndex(rest, `"$@"`); j >= 0 {
		rest = rest[:j]
	}
	if j := strings.LastIndex(rest, `%*`); j >= 0 {
		rest = rest[:j]
	}
	rest = strings.TrimSpace(rest)
	// 取第一对引号（含中间内容）为 uid
	start := strings.IndexAny(rest, `'"`)
	if start < 0 {
		return ""
	}
	q := rest[start]
	end := strings.LastIndexByte(rest, q)
	if end <= start {
		return ""
	}
	uid := rest[start+1 : end]
	if runtime.GOOS != "windows" {
		// 还原 shQuote 的 '\'' 转义
		uid = strings.ReplaceAll(uid, `'\''`, `'`)
	}
	return uid
}

// ---- Manager 侧的安装调度（幂等缓存 + 存量扫描） ----

// ensMark 安装指纹：wrapper 与 settings.json 的 (mtime, size)，任一变化即重装
// （覆盖 goder-serve 首启后才播种 settings.json 的竞态）。
type ensMark struct{ wrapMtime, wrapSize, setMtime, setSize int64 }

func statMark(p string) (int64, int64) {
	if st, err := os.Stat(p); err == nil {
		return st.ModTime().UnixNano(), st.Size()
	}
	return 0, 0
}

// EnsureUser 确保某用户的插件已安装（/goder-api 请求路径上调用，幂等低开销）。
func (m *Manager) EnsureUser(uid string) {
	if m.disabled || uid == "" || m.exe == "" {
		return
	}
	grfHome := userGrfHome(m.deps.DataDir, uid)
	wm, ws := statMark(filepath.Join(grfHome, "plugins", wrapperFileName()))
	sm, ss := statMark(filepath.Join(grfHome, "settings.json"))
	m.installMu.Lock()
	mk, ok := m.marks[uid]
	m.installMu.Unlock()
	if ok && mk == (ensMark{wm, ws, sm, ss}) {
		return // 已安装且文件无变化
	}
	m.installUser(uid)
}

// installUser 无条件执行安装并记录指纹（失败不记，下次重试）。
func (m *Manager) installUser(uid string) {
	if m.exe == "" {
		return
	}
	grfHome := userGrfHome(m.deps.DataDir, uid)
	if err := installPlugin(m.exe, grfHome, m.Endpoint(), m.cfg.Token, uid); err != nil {
		log.Printf("osmcp: 安装插件失败(%s): %v", uid, err)
		return
	}
	wm, ws := statMark(filepath.Join(grfHome, "plugins", wrapperFileName()))
	sm, ss := statMark(filepath.Join(grfHome, "settings.json"))
	m.installMu.Lock()
	m.marks[uid] = ensMark{wm, ws, sm, ss}
	m.installMu.Unlock()
}

// healAll 扫描存量用户目录自愈安装（启动时调用一次：可执行路径/端点可能变化）。
func (m *Manager) healAll() {
	if m.disabled || m.exe == "" {
		return
	}
	usersDir := filepath.Join(m.deps.DataDir, "goder", "serve-data", "users")
	entries, err := os.ReadDir(usersDir)
	if err != nil {
		return // 目录尚不存在 = 还没有 goder 用户
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		grfHome := filepath.Join(usersDir, e.Name(), "grf-home")
		if st, err := os.Stat(grfHome); err != nil || !st.IsDir() {
			continue
		}
		uid := readWrapperUID(filepath.Join(grfHome, "plugins", wrapperFileName()))
		if uid == "" {
			continue // 无法识别用户名，等该用户下次走 /goder-api 时 EnsureUser 兜底
		}
		m.installUser(uid)
		n++
	}
	if n > 0 {
		log.Printf("osmcp: 已自愈 %d 个存量用户的桌面控制插件", n)
	}
}
