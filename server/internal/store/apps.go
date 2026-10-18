// apps.go — 应用注册表与角色授权矩阵（apps / role_apps 表）。
package store

import (
	"encoding/json"
	"errors"
)

// AppDef 应用注册信息（内置与第三方统一对待）。
type AppDef struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Icon     string          `json:"icon"`
	Category string          `json:"category"`
	Builtin  bool            `json:"builtin"`
	Locked   bool            `json:"locked,omitempty"` // 宿主锁定应用：宿主经交换密钥注册，管理界面不可编辑/删除
	Hidden   bool            `json:"hidden,omitempty"` // 隐藏应用：可编程打开，不出现在启动台/Dock（详情窗口类）
	Config   json.RawMessage `json:"config,omitempty"`
	Version  string          `json:"version,omitempty"` // 应用版本（应用商店安装时写入，管理接口编辑不动它）
	Source   string          `json:"source,omitempty"`  // 安装来源（"market" = 应用商店；空 = 管理员手工注册）
	Width    int             `json:"width"`
	Height   int             `json:"height"`
}

// appCols apps 表的统一列清单：SELECT / INSERT 共用，加列时只改这里与 scanApp。
const appCols = "id,name,icon,category,builtin,locked,hidden,config,version,source,width,height"

// scanner 兼容 *sql.Rows 与 *sql.Row 的扫描接口。
type scanner interface{ Scan(dest ...any) error }

// scanApp 按列清单扫描一行应用记录并完成 int→bool / string→RawMessage 解码，
// ListApps / ListAppsForRole / AppByID 三条读路径共用。
func scanApp(row scanner) (AppDef, error) {
	var a AppDef
	var cfg string
	var locked, hidden int
	if err := row.Scan(&a.ID, &a.Name, &a.Icon, &a.Category, &a.Builtin, &locked, &hidden, &cfg, &a.Version, &a.Source, &a.Width, &a.Height); err != nil {
		return a, err
	}
	a.Locked = locked != 0
	a.Hidden = hidden != 0
	if cfg != "" {
		a.Config = json.RawMessage(cfg)
	}
	return a, nil
}

// ListApps 返回全部已注册应用。
func (db *DB) ListApps() ([]AppDef, error) {
	rows, err := db.Query(`SELECT ` + appCols + ` FROM apps ORDER BY builtin DESC, category, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppDef{} // 恒返回数组（空授权角色的前端按数组消费，null 会崩）
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListAppsForRole 返回某角色可访问的应用。
func (db *DB) ListAppsForRole(role string) ([]AppDef, error) {
	rows, err := db.Query(`SELECT `+appCols+` FROM apps a JOIN role_apps r ON r.app_id=a.id
		WHERE r.role=? ORDER BY a.builtin DESC, a.category, a.id`, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppDef{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AppByID 按 ID 查询单个应用（应用代理路由逐请求加载配置）。
func (db *DB) AppByID(id string) (AppDef, bool) {
	a, err := scanApp(db.QueryRow(`SELECT `+appCols+` FROM apps WHERE id=?`, id))
	if err != nil {
		return a, false
	}
	return a, true
}

// RoleCanAccessApp 角色是否被授权访问某应用（与 ListAppsForRole 同一张 role_apps）。
func (db *DB) RoleCanAccessApp(role, appID string) bool {
	var n int
	if err := db.QueryRow(`SELECT 1 FROM role_apps WHERE role=? AND app_id=?`, role, appID).Scan(&n); err != nil {
		return false
	}
	return true
}

// UpsertApp 注册或更新第三方应用。
func (db *DB) UpsertApp(a AppDef) error {
	cfg := ""
	if len(a.Config) > 0 {
		cfg = string(a.Config)
		// 客户端曾以 JSON.stringify 提交（config 字段是带引号的 JSON 字符串），
		// 落库前剥一层编码，保证库内统一为裸 JSON 文本
		var inner string
		if a.Config[0] == '"' && json.Unmarshal(a.Config, &inner) == nil {
			cfg = inner
		}
	}
	if a.Width <= 0 {
		a.Width = 800
	}
	if a.Height <= 0 {
		a.Height = 560
	}
	_, err := db.Exec(`INSERT INTO apps(`+appCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, icon=excluded.icon, category=excluded.category,
		hidden=excluded.hidden, config=excluded.config, width=excluded.width, height=excluded.height,
		version=CASE WHEN excluded.version<>'' THEN excluded.version ELSE apps.version END,
		source=CASE WHEN excluded.source<>'' THEN excluded.source ELSE apps.source END,
		locked=MAX(apps.locked, excluded.locked)`,
		a.ID, a.Name, a.Icon, a.Category, boolInt(a.Builtin), boolInt(a.Locked), boolInt(a.Hidden), cfg, a.Version, a.Source, a.Width, a.Height)
	return err
}

// ErrAppProtected 删除目标为内置或宿主锁定的受保护应用。
var ErrAppProtected = errors.New("app is builtin or locked")

// DeleteApp 删除应用及其授权。内置（builtin）与宿主锁定（locked）应用不可删；
// 仅当应用行确实被删除时才回收授权，避免受保护应用的 role_apps 被误清空。
func (db *DB) DeleteApp(id string) error {
	return db.deleteApp(id, false)
}

// DeleteAppForced 宿主服务专用（交换密钥调用）：允许删除宿主锁定（locked）应用
// ——宿主清退自己注册的应用属正当管理操作；内置（builtin）应用仍不可删。
func (db *DB) DeleteAppForced(id string) error {
	return db.deleteApp(id, true)
}

func (db *DB) deleteApp(id string, allowLocked bool) error {
	q := `DELETE FROM apps WHERE id=? AND builtin=0 AND locked=0`
	if allowLocked {
		q = `DELETE FROM apps WHERE id=? AND builtin=0`
	}
	res, err := db.Exec(q, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAppProtected
	}
	_, err = db.Exec(`DELETE FROM role_apps WHERE app_id=?`, id)
	return err
}

// RoleAccessMap 返回全部角色 → 应用授权矩阵。
func (db *DB) RoleAccessMap() (map[string][]string, error) {
	rows, err := db.Query(`SELECT role, app_id FROM role_apps`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string][]string{}
	for rows.Next() {
		var r, id string
		if err := rows.Scan(&r, &id); err != nil {
			return nil, err
		}
		m[r] = append(m[r], id)
	}
	return m, rows.Err()
}

// SetRoleApps 覆盖某角色的应用授权。
func (db *DB) SetRoleApps(role string, ids []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM role_apps WHERE role=?`, role); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO role_apps(role,app_id) VALUES(?,?)`, role, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetAppRoles 覆盖某应用被授权的角色集合（应用商店安装用：安装时一次性选定角色，
// 与按角色覆盖的 SetRoleApps 互为转置，不动其他应用的授权）。
func (db *DB) SetAppRoles(appID string, roles []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM role_apps WHERE app_id=?`, appID); err != nil {
		return err
	}
	for _, r := range roles {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO role_apps(role,app_id) VALUES(?,?)`, r, appID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListRoles 返回系统中出现过的全部角色（用户表与授权矩阵的并集，含宿主应用注入的自定义角色）。
// 外部身份模式下 user/guest 是宿主机制角色，不进清单（见 DB.externalIdentity）。
func (db *DB) ListRoles() ([]string, error) {
	rows, err := db.Query(`SELECT role FROM users UNION SELECT role FROM role_apps`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	// 三基础角色恒占 seen（可能尚无用户/授权记录，也防 UNION 同名重复）；
	// 外部身份模式下 user/guest 是宿主机制角色，只占位不进清单
	seen["admin"], seen["user"], seen["guest"] = true, true, true
	out := []string{"admin"}
	if !db.externalIdentity {
		out = append(out, "user", "guest")
	}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		if r != "" && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

// roleLabelsKV 角色显示名在 system_kv 中的存储键（宿主注入，见 RoleLabels）。
const roleLabelsKV = "role_labels"

// RoleLabels 读取角色显示名映射（宿主注入：键为角色 key，值为展示名，如
// boss → 老板/业主）。未注入或数据损坏时返回空映射，调用方回落内置文案。
func (db *DB) RoleLabels() map[string]string {
	raw, err := db.GetSystemKV(roleLabelsKV)
	if raw == "" || err != nil {
		return map[string]string{}
	}
	var m map[string]string
	if json.Unmarshal([]byte(raw), &m) != nil {
		return map[string]string{}
	}
	return m
}

// SetRoleLabels 全量覆盖角色显示名（传空映射即清除）。
func (db *DB) SetRoleLabels(m map[string]string) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return db.SetSystemKV(roleLabelsKV, string(b))
}
