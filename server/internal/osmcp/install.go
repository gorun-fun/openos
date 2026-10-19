// install.go — 插件安装面：把 openos 接入每个 goder 用户的 gh（经
// goder-serve 宿主管理面声明式安装）。
//
// 安装内容 = 一个 stdio 插件：自动发现模式落 wrapper（内容烙 openos-server
// 可执行路径、MCP 端点、令牌与用户名，gh 目录扫描直接收编）；显式列表模式
// （settings.json 有 plugins 键）upsert 命令项——双模式落盘与「无 plugins 键
// = 自动发现」语义全部由 goder-serve 承载（POST /admin/plugins/install，
// 幂等、内容不变不写），openos 不再直写 grf-home；规格存档含原始 uid，
// 存量自愈清单经管理面回读（旧实现解析 wrapper 内容反抠用户名的整块逻辑
// 由此删除）。
//
// 时机：Start 时对存量用户自愈（可执行路径/端点可能变化；goder-serve 启动
// 有竞态，自愈在等待就绪后异步执行，未部署则静默跳过）；新用户在
// /goder-api 首次请求时 EnsureUser（先于该用户 gh 的懒启动，保证插件在 gh
// 启动那一刻已就位）。本进程以「上次成功安装的命令行摘要」做跳过缓存：
// 命令行未变即不再重放（服务端安装本身幂等，缓存只为省请求路径上的
// 环回往返；进程重启后缓存为空，首个请求重放一次无害）。
package osmcp

import (
	"context"
	"log"
	"time"

	"openos/server/internal/goder"
)

// pluginName gh 侧的插件名（工具归属展示 mcp__openos__*）。
const pluginName = "openos"

// GoderAdmin goder 宿主管理面消费口（*goder.Manager 实现；接口化供测试
// 注入假件，osmcp 不反向依赖 goder 内部）。
type GoderAdmin interface {
	InstallHostPlugin(ctx context.Context, uid, name string, command []string) error
	ListHostPlugins(ctx context.Context) ([]goder.AdminPluginItem, error)
	EnsureReady(wait time.Duration) bool
}

// pluginCommand 某用户的完整插件 argv（exe + mcp-shim 子命令 + 端点 + 令牌
// + 用户名；wrapper 与 settings 命令项均由 goder-serve 按此渲染）。
func (m *Manager) pluginCommand(uid string) []string {
	return []string{m.exe, "mcp-shim", m.Endpoint(), m.userToken(uid), uid}
}

// commandDigest 命令行摘要（跳过缓存键）。
func commandDigest(cmd []string) string {
	d := ""
	for _, c := range cmd {
		d += c + "\x00"
	}
	return d
}

// EnsureUser 确保某用户的插件已安装（/goder-api 请求路径上调用，幂等低开销：
// 命令行与上次成功安装一致即跳过）。
func (m *Manager) EnsureUser(uid string) {
	if m.disabled || uid == "" || m.exe == "" || m.deps.Goder == nil {
		return
	}
	digest := commandDigest(m.pluginCommand(uid))
	m.installMu.Lock()
	prev, ok := m.installed[uid]
	m.installMu.Unlock()
	if ok && prev == digest {
		return
	}
	m.installUser(uid, digest)
}

// installUser 执行安装并记录摘要（失败不记，下次重试）。
func (m *Manager) installUser(uid, digest string) {
	if m.exe == "" || m.deps.Goder == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := m.deps.Goder.InstallHostPlugin(ctx, uid, pluginName, m.pluginCommand(uid)); err != nil {
		log.Printf("osmcp: 安装插件失败(%s): %v", uid, err)
		return
	}
	m.installMu.Lock()
	m.installed[uid] = digest
	m.installMu.Unlock()
}

// healAll 存量用户自愈安装（启动时调用：可执行路径/端点可能变化）。清单经
// 管理面回读（规格存档含原始 uid）；goder-serve 就绪存在启动竞态，等待
// 就绪后异步执行——未部署/超时则跳过，相关用户下次走 /goder-api 时由
// EnsureUser 兜底。
func (m *Manager) healAll() {
	if m.disabled || m.exe == "" || m.deps.Goder == nil {
		return
	}
	go func() {
		if !m.deps.Goder.EnsureReady(30 * time.Second) {
			return
		}
		if n := m.healUsers(); n > 0 {
			log.Printf("osmcp: 已自愈 %d 个存量用户的桌面控制插件", n)
		}
	}()
}

// healUsers 自愈本体（同步；调用方保证 goder-serve 已就绪）：回读插件清单，
// 对本插件（name 匹配）的每个用户以当前 exe/端点/令牌重装。返回自愈数。
func (m *Manager) healUsers() int {
	if m.exe == "" || m.deps.Goder == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	items, err := m.deps.Goder.ListHostPlugins(ctx)
	if err != nil {
		log.Printf("osmcp: 存量自愈清单获取失败: %v", err)
		return 0
	}
	n := 0
	for _, it := range items {
		if it.Name != pluginName {
			continue
		}
		m.installUser(it.UID, commandDigest(m.pluginCommand(it.UID)))
		n++
	}
	return n
}
