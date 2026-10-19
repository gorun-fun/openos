// seed.go — 宿主应用/角色/授权的启动期种子与调和编排（自 grf-hotel/grf-os
// 同构实现提炼）。
//
// 两宿主各自维护过两套「首次种子防重启覆盖」workaround：grf-os 以「角色授权
// 列表含宿主专属标记应用（gateway）」判已种子（无数据库形态），grf-hotel 以
// 宿主库 os_role_seed 标记表判已种子——同一件事的两种形态，统一为 Seeded/
// OnSeeded 回调（判定与落标记均归宿主）。退休应用清退对账 ListApps：仅删自家
// 种子残留（locked 且非 builtin 行；商店安装行 source=market 恒 locked=0，绝不
// 误删——grf-hotel RetireStoreApps 的幂等自守卫语义，每次启动对账一遍）。可选
// 演示账号禁用严格匹配 openos 种子三元组（用户名+显示名完全一致），绝不误伤
// 宿主经 exchange 开通的真实账号。角色目录声明走 DeclareRoles（roleWriteback
// 由 AppsDecl 携带，全量语义）。
package hostsdk

import (
	"log"
	"net/http"
	"net/url"
	"strings"
)

// AppsDecl 宿主声明清单（启动期一次性交给 Seed 编排）。
type AppsDecl struct {
	// Apps 宿主业务应用（统一 locked=true 注册：设置「应用管理」禁编辑/删除
	// 防误操作；id 必须与前端 createOpenOS({apps}) 键一致）。
	Apps []map[string]any
	// RoleApps 角色 → 默认授权矩阵（仅首次种子；覆盖语义——openos 内置应用
	// 需显式列出才会保留）。
	RoleApps map[string][]string
	// Roles 角色目录声明（managed_by='host' 只读投影；Apps 字段语义见 RoleDecl，
	// 首次授权走本矩阵的宿主应恒传 nil 绝不覆盖运行期 role_apps）。
	Roles []RoleDecl
	// Perms 权限点目录声明（设置「角色与权限」按 group 分组展示）。
	Perms []PermDecl
	// RoleWriteback 宿主角色编辑回写地址（DeclareRoles 第三参：非空 = 接管
	// 宿主角色编辑写（grf-hotel 形态）；空串 = 不接管/清除声明（grf-os 形态））。
	RoleWriteback string
}

// SeedOptions 种子编排可选项（零值 = 仅注册应用 + 首次授权矩阵 + 角色声明）。
type SeedOptions struct {
	// Seeded 角色已种子判定（返回 true 跳过首次授权写入——运行期修改不被重启
	// 覆盖；重置默认矩阵 = 清标记后重启）。grf-os 形态：access 列表含宿主专属
	// 标记应用；grf-hotel 形态：宿主库 os_role_seed 表行。nil = 恒未种子。
	Seeded func(role string, access map[string][]string) bool
	// OnSeeded 角色首次种子成功回调（grf-hotel 落 os_role_seed 标记；错误上抛
	// 中止种子，下次启动重试）。
	OnSeeded func(role string) error
	// RetiredApps 退休应用 id 清单（存量 locked 种子行清退；商店安装行绝不删）。
	RetiredApps []string
	// RetiredPrefixes 退休应用 id 前缀（grf-os th-*/gw-*/lg-* 细粒度旧应用形态）。
	RetiredPrefixes []string
	// ReconcileApps 定向调和应用 id：按 RoleApps 矩阵对现状收敛（矩阵含 = 确保、
	// 不含 = 收回；不动清单外应用的运行期授权，启动期幂等修正）。grf-os 的
	// goder/terminal 存量补授与 home/pipeline/fixer 下线残留收回同为此机制。
	ReconcileApps []string
	// DemoUsers 演示账号禁用（用户名 → 显示名，严格匹配 openos 种子三元组：
	// {"admin":"管理员","user":"普通用户","guest":"访客"}）。
	DemoUsers map[string]string
}

// Seed 启动期幂等种子编排：注册宿主应用 → 退休对账 → 首次授权矩阵 → 定向调和
// → 角色目录声明 → 演示账号禁用。注册/矩阵种子失败返回错误（宿主仅记日志，
// 下次启动重试）；清退/调和/声明/禁用均为旁路（失败仅日志）。调用前提：Manager
// 已 Start（openos 子进程健康）。
func Seed(m *Manager, decl AppsDecl, opts SeedOptions) error {
	// ① 注册宿主应用（每次启动全量 upsert：版本/图标等元数据以宿主清单为准）
	for _, app := range decl.Apps {
		payload := map[string]any{"locked": true}
		for k, v := range app {
			payload[k] = v
		}
		if err := m.UpsertApp(payload); err != nil {
			return err
		}
	}
	retireStaleApps(m, decl.Apps, opts.RetiredApps, opts.RetiredPrefixes)
	// ② 首次授权矩阵种子（已种子角色跳过——运行期修改不被重启覆盖）
	access, err := m.RoleAppMap()
	if err != nil {
		return err
	}
	if access == nil {
		access = map[string][]string{}
	}
	n := 0
	for role, ids := range decl.RoleApps {
		if (opts.Seeded != nil && opts.Seeded(role, access)) || len(ids) == 0 {
			continue
		}
		if err := m.SetRoleApps(role, ids); err != nil {
			return err
		}
		access[role] = ids
		if opts.OnSeeded != nil {
			if err := opts.OnSeeded(role); err != nil {
				return err
			}
		}
		n++
	}
	// ③ 宿主专属应用定向调和（旁路：失败仅日志，下次启动修正回来）
	for _, appID := range opts.ReconcileApps {
		reconcileApp(m, decl.RoleApps, access, appID)
	}
	// ④ 角色目录/权限点声明（旁路：纯投影数据，openos 缺目录只影响展示）
	if len(decl.Roles) > 0 || len(decl.Perms) > 0 {
		if err := m.DeclareRoles(decl.Roles, decl.Perms, decl.RoleWriteback); err != nil {
			log.Printf("[openos] 角色声明失败: %v", err)
		} else {
			log.Printf("[openos] 角色目录已声明：%d 角色 / %d 权限点", len(decl.Roles), len(decl.Perms))
		}
	}
	// ⑤ 存量库演示账号清理（旁路；注意 openos「最后一名管理员」保护——确保
	// 已有可用管理员之后再禁用演示 admin）
	if len(opts.DemoUsers) > 0 {
		disableDemoUsers(m, opts.DemoUsers)
	}
	log.Printf("[openos] 应用种子完成：%d 个应用，%d 个角色首次种子", len(decl.Apps), n)
	return nil
}

// retireStaleApps 退休应用清退：对账 ListApps，仅删自家种子残留（locked 且非
// builtin、且不在现行清单），商店安装行（source=market，locked=0）绝不误删——
// 幂等自守卫，每次启动对账一遍。清退后应用从桌面消失属预期：由 admin 在商店
// 重新安装并勾选角色即恢复。旁路，失败仅日志。
func retireStaleApps(m *Manager, current []map[string]any, ids, prefixes []string) {
	if len(ids) == 0 && len(prefixes) == 0 {
		return
	}
	apps, err := m.ListApps()
	if err != nil {
		log.Printf("[openos] 退休应用清退跳过（应用列表拉取失败）: %v", err)
		return
	}
	cur := map[string]bool{}
	for _, app := range current {
		if id, _ := app["id"].(string); id != "" {
			cur[id] = true
		}
	}
	n := 0
	for _, app := range apps {
		id := app.ID
		if cur[id] || !app.Locked || app.Builtin || !retiredMatch(id, ids, prefixes) {
			continue
		}
		if err := m.DeleteApp(id); err != nil {
			log.Printf("[openos] 退休应用 %s 清退失败: %v", id, err)
			continue
		}
		n++
	}
	if n > 0 {
		log.Printf("[openos] 已清退退休应用：%d 个（商店安装行不受影响）", n)
	}
}

func retiredMatch(id string, ids, prefixes []string) bool {
	for _, w := range ids {
		if id == w {
			return true
		}
	}
	for _, p := range prefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// reconcileApp 单个应用的授权定向调和：按矩阵 want 与现状 access 收敛（矩阵含
// 该应用 = 确保补授、不含 = 收回下线残留），不动其余应用的运行期授权；失败仅
// 日志（启动期幂等修正，运行期改动会在下次重启修正回来）。
func reconcileApp(m *Manager, matrix map[string][]string, access map[string][]string, appID string) {
	for role, ids := range matrix {
		want := hasAppID(ids, appID)
		if want == hasAppID(access[role], appID) {
			continue
		}
		merged := make([]string, 0, len(access[role])+1)
		for _, id := range access[role] {
			if id != appID {
				merged = append(merged, id)
			}
		}
		if want {
			merged = append(merged, appID)
		}
		if err := m.SetRoleApps(role, merged); err != nil {
			log.Printf("[openos] 角色 %s %s 授权调和失败: %v", role, appID, err)
			continue
		}
		access[role] = merged
	}
}

func hasAppID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// disableDemoUsers 幂等禁用 openos 早期版本种入的演示账号（admin/admin123 等）：
// 子进程以 OPENOS_AUTH_MODE=external 运行后本地登录 403、不再种子，此处清理
// 存量库旧行（禁用实时拦截其全部请求，REST 401/总线踢线）。仅匹配演示种子
// 三元组（用户名 + 显示名完全一致），绝不误伤宿主经 exchange 开通的真实账号；
// 旁路，失败仅日志。
func disableDemoUsers(m *Manager, demo map[string]string) {
	users, err := m.ListUsers()
	if err != nil {
		log.Printf("[openos] 演示账号清理跳过（用户列表拉取失败）: %v", err)
		return
	}
	for _, u := range users {
		if demo[u.Username] == "" || demo[u.Username] != u.DisplayName || u.Disabled {
			continue
		}
		if err := m.Call(http.MethodPut, "/api/host/users/"+url.PathEscape(u.Username),
			map[string]any{"disabled": true}, nil); err != nil {
			log.Printf("[openos] 演示账号 %s 禁用失败: %v", u.Username, err)
			continue
		}
		log.Printf("[openos] 已禁用演示账号 %s（本地登录通道已整体关闭）", u.Username)
	}
}
