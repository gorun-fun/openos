// roles.go — 角色目录与功能级权限点（2026-10 P2 重构）。
//
// 双模式：
//   - system 模式（独立部署/demo）：roles/role_perms/perm_defs 表为权威，
//     admin 可经设置页编辑（角色目录 + 每角色权限点 + 应用授权三合一）；
//   - host 模式（宿主集成）：角色与权限的**权威存储留在宿主自己的数据库**，
//     宿主经 POST /api/host/declare（交换密钥守护）声明式全量投影到本表
//     （managed_by='host'，openos 侧只读展示、不做种子覆盖）——消灭宿主侧
//     os_role_seed 标记表 / 「access 含 gateway」这类种子 workaround。
//
// 权限点语义与 grf-hotel 的 HasPerm 完全一致：admin 角色直通全部；权限集含
// `*` 通配全部；其余按精确 code 匹配（点分层级如 room.view 未来可扩展前缀通配）。
package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// RoleDef 角色目录条目（含权限点与应用授权快照）。
type RoleDef struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	Builtin   bool     `json:"builtin"`
	ManagedBy string   `json:"managedBy"` // system | host
	Sort      int      `json:"sort"`
	Perms     []string `json:"perms"`
	Apps      []string `json:"apps"`
}

// PermDef 权限点目录条目。
type PermDef struct {
	Code      string `json:"code"`
	Group     string `json:"group"`
	Label     string `json:"label"`
	ManagedBy string `json:"managedBy"` // system | host
}

// builtinPermDefs openos 自身的功能级权限点（demo 模式下 admin 恒持 `*`）。
// 现阶段仅作能力就位（端点仍走 RequireAdmin），为后续细分授权留位；宿主经
// declare 注入自己的业务权限点（managed_by='host'）。
var builtinPermDefs = []PermDef{
	{Code: "users.manage", Group: "system", Label: "用户管理（创建/角色/禁用/删除）", ManagedBy: "system"},
	{Code: "apps.manage", Group: "system", Label: "应用注册与管理", ManagedBy: "system"},
	{Code: "market.manage", Group: "system", Label: "商店目录源与安装", ManagedBy: "system"},
	{Code: "ai.manage", Group: "system", Label: "AI 服务配置", ManagedBy: "system"},
	{Code: "system.manage", Group: "system", Label: "系统信息与审计日志", ManagedBy: "system"},
}

// builtinRoles demo/独立部署的内置三角色。
var builtinRoles = []RoleDef{
	{Key: "admin", Label: "管理员", Builtin: true, ManagedBy: "system", Sort: 1, Perms: []string{"*"}},
	{Key: "user", Label: "用户", Builtin: true, ManagedBy: "system", Sort: 2},
	{Key: "guest", Label: "访客", Builtin: true, ManagedBy: "system", Sort: 3},
}

// EnsureRolesBootstrap 角色目录自举（store.Open 内调用，幂等）：
//   - 空表时种子内置三角色 + 内置权限点目录 + admin 的 `*`；
//   - 无论如何都把 users/role_apps 里出现过的角色补进目录（存量库无损迁移，
//     label 留空由展示层回落内置文案）。
func (db *DB) EnsureRolesBootstrap() error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM roles`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		for _, r := range builtinRoles {
			if _, err := db.Exec(`INSERT INTO roles(key,label,builtin,managed_by,sort) VALUES(?,?,?,?,?)`,
				r.Key, r.Label, boolInt(r.Builtin), r.ManagedBy, r.Sort); err != nil {
				return err
			}
			for _, p := range r.Perms {
				if _, err := db.Exec(`INSERT OR IGNORE INTO role_perms(role,perm) VALUES(?,?)`, r.Key, p); err != nil {
					return err
				}
			}
		}
	}
	for _, p := range builtinPermDefs {
		// upsert：修正早期种子遗漏的 managed_by（幂等）
		if _, err := db.Exec(`INSERT INTO perm_defs(code,grp,label,managed_by) VALUES(?,?,?,?)
			ON CONFLICT(code) DO UPDATE SET grp=excluded.grp, label=excluded.label, managed_by=excluded.managed_by`,
			p.Code, p.Group, p.Label, p.ManagedBy); err != nil {
			return err
		}
	}
	// 存量角色并入目录（users 表与授权矩阵出现过的全部角色）
	if _, err := db.Exec(`INSERT OR IGNORE INTO roles(key,label,builtin,managed_by,sort)
		SELECT role,'',0,'system',99 FROM (
			SELECT role FROM users UNION SELECT role FROM role_apps
		) WHERE role!=''`); err != nil {
		return err
	}
	return nil
}

// ListRoleDefs 全部角色目录（含每角色权限点与应用授权，按 sort、key 稳定排序）。
// 外部身份模式下 user/guest 是宿主机制角色，不进目录（见 DB.externalIdentity）。
func (db *DB) ListRoleDefs() ([]RoleDef, error) {
	rows, err := db.Query(`SELECT key,label,builtin,managed_by,sort FROM roles ORDER BY sort, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoleDef
	for rows.Next() {
		var r RoleDef
		var builtin int
		if err := rows.Scan(&r.Key, &r.Label, &builtin, &r.ManagedBy, &r.Sort); err != nil {
			return nil, err
		}
		if db.externalIdentity && (r.Key == "user" || r.Key == "guest") {
			continue
		}
		r.Builtin = builtin == 1
		r.Perms = []string{}
		r.Apps = []string{}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	perms, err := db.RolePermsMap()
	if err != nil {
		return nil, err
	}
	apps, err := db.RoleAccessMap()
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Perms = perms[out[i].Key]
		out[i].Apps = apps[out[i].Key]
	}
	return out, nil
}

// RolePermsMap 全部角色的权限点映射。
func (db *DB) RolePermsMap() (map[string][]string, error) {
	rows, err := db.Query(`SELECT role,perm FROM role_perms ORDER BY role, perm`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string][]string{}
	for rows.Next() {
		var r, p string
		if err := rows.Scan(&r, &p); err != nil {
			return nil, err
		}
		m[r] = append(m[r], p)
	}
	return m, rows.Err()
}

// RolePerms 某角色的权限点集合。
func (db *DB) RolePerms(role string) ([]string, error) {
	rows, err := db.Query(`SELECT perm FROM role_perms WHERE role=? ORDER BY perm`, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UserPerms 用户权限点（按其当前角色展开；admin 由调用方语义直通，此处照实返回行）。
func (db *DB) UserPerms(username string) ([]string, error) {
	var role string
	if err := db.QueryRow(`SELECT role FROM users WHERE username=?`, username).Scan(&role); err != nil {
		return nil, err
	}
	return db.RolePerms(role)
}

// SetRolePerms 覆盖某角色的权限点集合。
func (db *DB) SetRolePerms(role string, perms []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM role_perms WHERE role=?`, role); err != nil {
		return err
	}
	for _, p := range perms {
		if p == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO role_perms(role,perm) VALUES(?,?)`, role, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpsertRole 新建/更新角色目录条目（demo 模式 admin 编辑用）。builtin 角色仅允许
// 改 label/perms，不可改 key；非 builtin 不允许冒充 builtin。managed_by 不可经此
// 接口改成 host（宿主角色只能由 declare 声明）。
func (db *DB) UpsertRole(key, label string, perms []string) error {
	if key == "" {
		return fmt.Errorf("角色键不能为空")
	}
	var managedBy string
	err := db.QueryRow(`SELECT managed_by FROM roles WHERE key=?`, key).Scan(&managedBy)
	switch {
	case err == nil:
		if managedBy == "host" {
			return fmt.Errorf("宿主托管角色不可在本端编辑")
		}
		if _, err := db.Exec(`UPDATE roles SET label=? WHERE key=?`, label, key); err != nil {
			return err
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err := db.Exec(`INSERT INTO roles(key,label,builtin,managed_by,sort) VALUES(?,?,0,'system',50)`, key, label); err != nil {
			return err
		}
	default:
		return err
	}
	return db.SetRolePerms(key, perms)
}

// DeleteRole 删除角色（builtin 与 host 托管角色拒绝；级联清权限点与应用授权。
// 仍被用户绑定的角色由调用方先行校验拒绝）。
func (db *DB) DeleteRole(key string) error {
	var builtin int
	var managedBy string
	if err := db.QueryRow(`SELECT builtin,managed_by FROM roles WHERE key=?`, key).Scan(&builtin, &managedBy); err != nil {
		return fmt.Errorf("角色不存在")
	}
	if builtin == 1 || managedBy == "host" {
		return fmt.Errorf("内置或宿主托管角色不可删除")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DELETE FROM roles WHERE key=?`,
		`DELETE FROM role_perms WHERE role=?`,
		`DELETE FROM role_apps WHERE role=?`,
	} {
		if _, err := tx.Exec(stmt, key); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PermsCatalog 权限点目录（system 在前 host 在后，组内按 code 排序）。
func (db *DB) PermsCatalog() ([]PermDef, error) {
	rows, err := db.Query(`SELECT code,grp,label,managed_by FROM perm_defs ORDER BY managed_by, grp, code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PermDef
	for rows.Next() {
		var p PermDef
		if err := rows.Scan(&p.Code, &p.Group, &p.Label, &p.ManagedBy); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertPermDefs 幂等登记权限点目录条目（宿主 declare 用；不删除既有条目）。
func (db *DB) UpsertPermDefs(defs []PermDef) error {
	for _, p := range defs {
		if p.Code == "" {
			continue
		}
		if _, err := db.Exec(`INSERT INTO perm_defs(code,grp,label,managed_by) VALUES(?,?,?,?)
			ON CONFLICT(code) DO UPDATE SET grp=excluded.grp, label=excluded.label, managed_by=excluded.managed_by`,
			p.Code, p.Group, p.Label, p.ManagedBy); err != nil {
			return err
		}
	}
	return nil
}

// HostRoleEntry declare 声明的单个角色。
type HostRoleEntry struct {
	Key   string   `json:"key"`
	Label string   `json:"label"`
	Perms []string `json:"perms"`
	Apps  []string `json:"apps"`
}

// DeclareHostRoles 宿主声明式全量同步（交换密钥已在路由层守护）：
//   - 清单内角色 upsert 为 managed_by='host'（label/perms/apps 全量覆盖）；
//   - 清单外的既有 host 角色整体移除（权限点与应用授权级联清理）；
//   - system 角色不动（宿主与 openos 内置共存）；
//   - 权限点目录随声明幂等登记（managed_by='host'）。
// 幂等：重复声明同样内容不产生实际变更（内容相同不写，SQLite 自然幂等）。
func (db *DB) DeclareHostRoles(entries []HostRoleEntry, permDefs []PermDef) error {
	// 宿主声明的权限点一律标记 host 归属（防止空值/伪造 system 覆盖内置目录语义）
	for i := range permDefs {
		permDefs[i].ManagedBy = "host"
	}
	if err := db.UpsertPermDefs(permDefs); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	declared := map[string]bool{}
	for _, e := range entries {
		if e.Key == "" {
			continue
		}
		declared[e.Key] = true
		if _, err := tx.Exec(`INSERT INTO roles(key,label,builtin,managed_by,sort) VALUES(?,?,0,'host',10)
			ON CONFLICT(key) DO UPDATE SET label=excluded.label, managed_by='host'`,
			e.Key, e.Label); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM role_perms WHERE role=?`, e.Key); err != nil {
			return err
		}
		for _, p := range e.Perms {
			if p == "" {
				continue
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO role_perms(role,perm) VALUES(?,?)`, e.Key, p); err != nil {
				return err
			}
		}
		// Apps=nil 表示该角色的应用授权**不在宿主权威内**（如 grf-hotel 的
		// role_apps 是运行期唯一存储），不动 role_apps；非 nil 才覆盖。
		if e.Apps != nil {
			if _, err := tx.Exec(`DELETE FROM role_apps WHERE role=?`, e.Key); err != nil {
				return err
			}
			for _, a := range e.Apps {
				if a == "" {
					continue
				}
				if _, err := tx.Exec(`INSERT OR IGNORE INTO role_apps(role,app_id) VALUES(?,?)`, e.Key, a); err != nil {
					return err
				}
			}
		}
	}
	// 清单外的 host 角色移除
	rows, err := tx.Query(`SELECT key FROM roles WHERE managed_by='host'`)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		if !declared[k] {
			stale = append(stale, k)
		}
	}
	rows.Close()
	for _, k := range stale {
		for _, stmt := range []string{
			`DELETE FROM roles WHERE key=? AND managed_by='host'`,
			`DELETE FROM role_perms WHERE role=?`,
			`DELETE FROM role_apps WHERE role=?`,
		} {
			if _, err := tx.Exec(stmt, k); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// HasPermJudge 权限判定（与 grf-hotel Claims.HasPerm 同语义）：
// admin 直通；集合含 `*` 通配；否则精确匹配。
func HasPermJudge(role string, perms []string, perm string) bool {
	if role == "admin" {
		return true
	}
	for _, p := range perms {
		if p == "*" || p == perm {
			return true
		}
	}
	return false
}
