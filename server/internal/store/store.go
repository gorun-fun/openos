// Package store 管理 SQLite 数据库的初始化、迁移与种子数据。
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Version 服务端版本号（系统信息 / 健康检查使用）。
// 构建期注入 git tag：go build -ldflags "-X openos/server/internal/store.Version=v0.0.7"；
// CI（.gitlab-ci.yml）以 CI_COMMIT_TAG 注入，Makefile release 用 git describe，未注入时显示 dev。
var Version = "dev"

// DB 包裹 sql.DB，提供统一的访问入口。
type DB struct {
	*sql.DB
}

// AppDef 应用注册信息（内置与第三方统一对待）。
type AppDef struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Icon     string          `json:"icon"`
	Category string          `json:"category"`
	Builtin  bool            `json:"builtin"`
	Hidden   bool            `json:"hidden,omitempty"` // 隐藏应用：可编程打开，不出现在启动台/Dock（详情窗口类）
	Config   json.RawMessage `json:"config,omitempty"`
	Width    int             `json:"width"`
	Height   int             `json:"height"`
}

// User 用户记录。
type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
	CreatedAt   string `json:"createdAt"`
}

// File 虚拟文件系统中的条目。
type File struct {
	Path      string `json:"path"`
	IsDir     bool   `json:"isDir"`
	IsBinary  bool   `json:"isBinary"`
	Content   string `json:"content,omitempty"`
	Size      int    `json:"size"`
	UpdatedAt string `json:"updatedAt"`
}

// AuditEntry 审计日志。
type AuditEntry struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Action    string `json:"action"`
	Detail    string `json:"detail"`
	IP        string `json:"ip"`
	CreatedAt string `json:"createdAt"`
}

// ChatMsg goder 对话历史。
type ChatMsg struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"createdAt"`
}

// AIConfig AI 服务配置（单例行）。
type AIConfig struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  salt TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'user',
  display_name TEXT NOT NULL DEFAULT '',
  token_version INTEGER NOT NULL DEFAULT 0,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS apps (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  icon TEXT NOT NULL DEFAULT '',
  category TEXT NOT NULL DEFAULT 'other',
  builtin INTEGER NOT NULL DEFAULT 0,
  hidden INTEGER NOT NULL DEFAULT 0,
  config TEXT NOT NULL DEFAULT '',
  width INTEGER NOT NULL DEFAULT 800,
  height INTEGER NOT NULL DEFAULT 560
);
CREATE TABLE IF NOT EXISTS role_apps (
  role TEXT NOT NULL,
  app_id TEXT NOT NULL,
  PRIMARY KEY (role, app_id)
);
CREATE TABLE IF NOT EXISTS files (
  owner TEXT NOT NULL,
  path TEXT NOT NULL,
  is_dir INTEGER NOT NULL DEFAULT 0,
  is_binary INTEGER NOT NULL DEFAULT 0,
  content TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (owner, path)
);
CREATE TABLE IF NOT EXISTS user_settings (
  username TEXT NOT NULL,
  key TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (username, key)
);
CREATE TABLE IF NOT EXISTS system_kv (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ai_config (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  base_url TEXT NOT NULL DEFAULT '',
  api_key TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS chat_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL,
  action TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  ip TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_chat_user ON chat_messages(username, id);
CREATE INDEX IF NOT EXISTS idx_audit_id ON audit_log(id DESC);
`

// builtinApps 内置应用清单。icon 为前端渲染标识。
var builtinApps = []AppDef{
	{ID: "terminal", Name: "终端", Icon: "terminal", Category: "工具", Builtin: true, Width: 780, Height: 500},
	{ID: "files", Name: "文件", Icon: "files", Category: "工具", Builtin: true, Width: 940, Height: 580},
	{ID: "calculator", Name: "计算器", Icon: "calculator", Category: "工具", Builtin: true, Width: 320, Height: 480},
	{ID: "settings", Name: "系统设置", Icon: "settings", Category: "系统", Builtin: true, Width: 900, Height: 620},
	{ID: "docs", Name: "文档", Icon: "docs", Category: "办公", Builtin: true, Width: 860, Height: 600},
	{ID: "sheets", Name: "表格", Icon: "sheets", Category: "办公", Builtin: true, Width: 960, Height: 620},
	{ID: "slides", Name: "幻灯片", Icon: "slides", Category: "办公", Builtin: true, Width: 960, Height: 620},
	{ID: "pdf", Name: "PDF 阅读器", Icon: "pdf", Category: "办公", Builtin: true, Width: 860, Height: 640},
	{ID: "goder", Name: "Goder - AI助手", Icon: "goder", Category: "AI", Builtin: true, Width: 1220, Height: 780},
	{ID: "notes", Name: "便签", Icon: "notes", Category: "工具", Builtin: true, Width: 780, Height: 560},
	{ID: "welcome", Name: "使用指南", Icon: "welcome", Category: "系统", Builtin: true, Width: 760, Height: 580},
}

// vfsAppIDs 依赖虚拟文件系统/终端的内置应用（OPENOS_VFS=off 时整体下架，
// 业务宿主的文件与文档数据交由集成方自己管理）。
var vfsAppIDs = map[string]bool{
	"terminal": true, "files": true, "docs": true, "sheets": true, "slides": true, "pdf": true,
}

// goderAppID goder 应用（OPENOS_GODER=off 时下架，纯业务宿主无需 AI IDE）。
const goderAppID = "goder"

// 默认角色 → 应用授权矩阵。
var defaultRoleApps = map[string][]string{
	"admin": {"terminal", "files", "calculator", "settings", "docs", "sheets", "slides", "pdf", "goder", "notes", "welcome"},
	"user":  {"terminal", "files", "calculator", "docs", "sheets", "slides", "pdf", "goder", "notes", "welcome"},
	"guest": {"calculator", "notes", "welcome"},
}

// StorageQuota 每用户虚拟文件空间上限（字节，按内容长度估算）。
const StorageQuota = 100 * 1024 * 1024

// ---- 功能裁剪选项（宿主集成：SQLite 只保留演示数据与每用户界面设置） ----

type config struct {
	withoutVFS   bool
	withoutGoder bool
}

// Option 功能裁剪选项，配合 Open 使用。
type Option func(*config)

// WithoutVFS 下架 VFS/终端类内置应用（OPENOS_VFS=off 时由 main 传入）。
func WithoutVFS() Option { return func(c *config) { c.withoutVFS = true } }

// WithoutGoder 下架 goder 应用（OPENOS_GODER=off 时由 main 传入）。
func WithoutGoder() Option { return func(c *config) { c.withoutGoder = true } }

// Open 打开（或创建）数据库并完成迁移与种子数据。
func Open(dir string, opts ...Option) (*DB, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dir, "openos.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1) // SQLite 单写者，避免锁冲突
	if _, err := d.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	db := &DB{d}
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}
	if err := db.migrateColumns(); err != nil {
		return nil, err
	}
	if err := db.migrateSystemKV(); err != nil {
		return nil, err
	}
	if err := db.seed(cfg); err != nil {
		return nil, err
	}
	return db, nil
}

// migrateColumns 为旧库补列（幂等）。
func (db *DB) migrateColumns() error {
	type col struct{ table, name, ddl string }
	for _, c := range []col{
		{"users", "token_version", "ALTER TABLE users ADD COLUMN token_version INTEGER NOT NULL DEFAULT 0"},
		{"users", "disabled", "ALTER TABLE users ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0"},
		{"files", "is_binary", "ALTER TABLE files ADD COLUMN is_binary INTEGER NOT NULL DEFAULT 0"},
		{"apps", "hidden", "ALTER TABLE apps ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0"},
	} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, c.table, c.name).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(c.ddl); err != nil {
				return fmt.Errorf("add column %s.%s: %w", c.table, c.name, err)
			}
		}
	}
	return nil
}

// migrateSystemKV 迁移历史遗留的系统级键：jwt_secret 原挂在 user_settings 的
// 伪用户 __system 名下，与「用户界面设置」数据混存，迁入独立的 system_kv 表。
func (db *DB) migrateSystemKV() error {
	v, err := db.GetUserSetting("__system", "jwt_secret")
	if err != nil || v == "" {
		return err
	}
	if err := db.SetSystemKV("jwt_secret", v); err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM user_settings WHERE username='__system' AND key='jwt_secret'`)
	return err
}

// seed 幂等初始化：内置应用一律 INSERT OR IGNORE（升级新增的 app 也会补上）；
// 仅对**本次新增**的内置应用按默认矩阵补授权——不覆盖管理员已有的定制（如移除某 app）。
// cfg 为功能裁剪：VFS / goder 关闭时对应内置应用不注册（存量则下架回收）。
func (db *DB) seed(cfg *config) error {
	apps := make([]AppDef, 0, len(builtinApps))
	for _, a := range builtinApps {
		if cfg.withoutVFS && vfsAppIDs[a.ID] {
			continue
		}
		if cfg.withoutGoder && a.ID == goderAppID {
			continue
		}
		apps = append(apps, a)
	}
	for _, a := range apps {
		var exists int
		if err := db.QueryRow(`SELECT COUNT(*) FROM apps WHERE id=?`, a.ID).Scan(&exists); err != nil {
			return err
		}
		cfgJSON := ""
		if len(a.Config) > 0 {
			cfgJSON = string(a.Config)
		}
		if _, err := db.Exec(`INSERT INTO apps(id,name,icon,category,builtin,hidden,config,width,height) VALUES(?,?,?,?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, icon=excluded.icon, width=excluded.width, height=excluded.height`,
			a.ID, a.Name, a.Icon, a.Category, boolInt(a.Builtin), boolInt(a.Hidden), cfgJSON, a.Width, a.Height); err != nil {
			return err
		}
		if exists == 0 {
			// 新增应用：按默认矩阵补授权
			for role, ids := range defaultRoleApps {
				for _, id := range ids {
					if id == a.ID {
						if _, err := db.Exec(`INSERT OR IGNORE INTO role_apps(role,app_id) VALUES(?,?)`, role, id); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	// 首次建库：全量默认授权（仅限当前保留的应用集）
	kept := make(map[string]bool, len(apps))
	for _, a := range apps {
		kept[a.ID] = true
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM role_apps`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		for role, ids := range defaultRoleApps {
			for _, id := range ids {
				if kept[id] {
					if _, err := db.Exec(`INSERT OR IGNORE INTO role_apps(role,app_id) VALUES(?,?)`, role, id); err != nil {
						return err
					}
				}
			}
		}
	}
	// 下架清理：已从 builtinApps 移除（或被功能裁剪排除）的内置应用从旧库删除，
	// 连同授权一并回收；仅动 builtin=1 的行，不影响管理员自建的第三方应用。
	rows, err := db.Query(`SELECT id FROM apps WHERE builtin=1`)
	if err != nil {
		return err
	}
	var dropped []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !kept[id] {
			dropped = append(dropped, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range dropped {
		if _, err := db.Exec(`DELETE FROM apps WHERE id=? AND builtin=1`, id); err != nil {
			return err
		}
		if _, err := db.Exec(`DELETE FROM role_apps WHERE app_id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- 审计日志 ----

// Audit 记录一条审计日志（失败不影响主流程）。
func (db *DB) Audit(username, action, detail, ip string) {
	db.Exec(`INSERT INTO audit_log(username,action,detail,ip) VALUES(?,?,?,?)`, username, action, detail, ip)
}

// ListAudit 最近 n 条审计日志。
func (db *DB) ListAudit(n int) ([]AuditEntry, error) {
	rows, err := db.Query(`SELECT id,username,action,detail,ip,created_at FROM audit_log ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Username, &e.Action, &e.Detail, &e.IP, &e.CreatedAt); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- AI 配置 ----

// GetAIConfig 读取 AI 配置；空配置返回 ok=false。
func (db *DB) GetAIConfig() (AIConfig, bool, error) {
	var c AIConfig
	var has int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_config WHERE id=1`).Scan(&has); err != nil {
		return c, false, err
	}
	if has == 0 {
		return c, false, nil
	}
	err := db.QueryRow(`SELECT base_url,api_key,model FROM ai_config WHERE id=1`).Scan(&c.BaseURL, &c.APIKey, &c.Model)
	return c, err == nil, err
}

// SetAIConfig 保存 AI 配置。
func (db *DB) SetAIConfig(c AIConfig) error {
	_, err := db.Exec(`INSERT INTO ai_config(id,base_url,api_key,model,updated_at) VALUES(1,?,?,?,datetime('now'))
		ON CONFLICT(id) DO UPDATE SET base_url=excluded.base_url, api_key=excluded.api_key, model=excluded.model, updated_at=datetime('now')`,
		c.BaseURL, c.APIKey, c.Model)
	return err
}

// ---- goder 对话历史 ----

// AddChatMsg 追加一条对话消息。
func (db *DB) AddChatMsg(username, role, content string) {
	db.Exec(`INSERT INTO chat_messages(username,role,content) VALUES(?,?,?)`, username, role, content)
}

// ListChatMsgs 用户最近 n 条消息（时间正序）。
func (db *DB) ListChatMsgs(username string, n int) ([]ChatMsg, error) {
	rows, err := db.Query(`SELECT role,content,created_at FROM (
		SELECT id,role,content,created_at FROM chat_messages WHERE username=? ORDER BY id DESC LIMIT ?)
		ORDER BY id ASC`, username, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatMsg{}
	for rows.Next() {
		var m ChatMsg
		if err := rows.Scan(&m.Role, &m.Content, &m.CreatedAt); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ClearChatMsgs 清空用户对话历史。
func (db *DB) ClearChatMsgs(username string) {
	db.Exec(`DELETE FROM chat_messages WHERE username=?`, username)
}

// ---- 用户 / 认证支撑 ----

// UserAuthInfo 供 JWT 校验的用户态。
type UserAuthInfo struct {
	Exists         bool
	Role           string
	PasswordHash   string
	Salt           string
	DisplayName    string
	TokenVersion   int64
	Disabled       bool
}

// GetUserAuth 按用户名取认证信息。
func (db *DB) GetUserAuth(username string) (UserAuthInfo, error) {
	var u UserAuthInfo
	var disabled int
	err := db.QueryRow(`SELECT role,password_hash,salt,display_name,token_version,disabled FROM users WHERE username=?`, username).
		Scan(&u.Role, &u.PasswordHash, &u.Salt, &u.DisplayName, &u.TokenVersion, &disabled)
	if err == sql.ErrNoRows {
		return u, nil
	}
	u.Exists = err == nil
	u.Disabled = disabled != 0
	return u, err
}

// CountAdmins 启用中的管理员数量（用于最后管理员保护）。
func (db *DB) CountAdmins() int {
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0`).Scan(&n)
	return n
}

// BumpTokenVersion 使用户已签发的 JWT 全部失效。
func (db *DB) BumpTokenVersion(username string) {
	db.Exec(`UPDATE users SET token_version = token_version + 1 WHERE username=?`, username)
}

// ---- 系统级键值（与用户设置分离：清理用户设置不应波及系统配置） ----

// GetSystemKV 读取系统级键值，不存在返回空串。
func (db *DB) GetSystemKV(key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM system_kv WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetSystemKV 写入系统级键值。
func (db *DB) SetSystemKV(key, value string) error {
	_, err := db.Exec(`INSERT INTO system_kv(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ---- 数据保留期（防无限增长） ----

// PruneAuditBefore 删除 cutoff 之前的审计日志，返回删除行数。
// login.fail 是最大来源（公开接口 + 限流），无保留期时单日可膨胀数十万行。
func (db *DB) PruneAuditBefore(cutoff time.Time) (int64, error) {
	return db.pruneBefore("audit_log", cutoff)
}

// PruneChatBefore 删除 cutoff 之前的 AI 对话历史（含终端伪用户 __term__ 的孤儿数据），返回删除行数。
func (db *DB) PruneChatBefore(cutoff time.Time) (int64, error) {
	return db.pruneBefore("chat_messages", cutoff)
}

func (db *DB) pruneBefore(table string, cutoff time.Time) (int64, error) {
	res, err := db.Exec(`DELETE FROM `+table+` WHERE created_at < ?`, cutoff.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SystemStats 系统信息。
type SystemStats struct {
	Version     string `json:"version"`
	UserCount   int    `json:"userCount"`
	AppCount    int    `json:"appCount"`
	FileCount   int    `json:"fileCount"`
	StorageUsed int64  `json:"storageUsed"`
	QuotaPerUser int64 `json:"quotaPerUser"`
	StartedAt   string `json:"startedAt"`
}

// Stats 汇总系统统计。
func (db *DB) Stats(startedAt time.Time) SystemStats {
	s := SystemStats{Version: Version, StartedAt: startedAt.Format(time.RFC3339), QuotaPerUser: StorageQuota}
	db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&s.UserCount)
	db.QueryRow(`SELECT COUNT(*) FROM apps`).Scan(&s.AppCount)
	db.QueryRow(`SELECT COUNT(*) FROM files WHERE is_dir=0`).Scan(&s.FileCount)
	db.QueryRow(`SELECT COALESCE(SUM(LENGTH(content)),0) FROM files`).Scan(&s.StorageUsed)
	return s
}

// ---- 应用注册表 ----

// ListApps 返回全部已注册应用。
func (db *DB) ListApps() ([]AppDef, error) {
	rows, err := db.Query(`SELECT id,name,icon,category,builtin,hidden,config,width,height FROM apps ORDER BY builtin DESC, category, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppDef{} // 恒返回数组（空授权角色的前端按数组消费，null 会崩）
	for rows.Next() {
		var a AppDef
		var cfg string
		var hidden int
		if err := rows.Scan(&a.ID, &a.Name, &a.Icon, &a.Category, &a.Builtin, &hidden, &cfg, &a.Width, &a.Height); err != nil {
			return nil, err
		}
		a.Hidden = hidden != 0
		if cfg != "" {
			a.Config = json.RawMessage(cfg)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListAppsForRole 返回某角色可访问的应用。
func (db *DB) ListAppsForRole(role string) ([]AppDef, error) {
	rows, err := db.Query(`SELECT a.id,a.name,a.icon,a.category,a.builtin,a.hidden,a.config,a.width,a.height
		FROM apps a JOIN role_apps r ON r.app_id=a.id WHERE r.role=? ORDER BY a.builtin DESC, a.category, a.id`, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppDef{}
	for rows.Next() {
		var a AppDef
		var cfg string
		var hidden int
		if err := rows.Scan(&a.ID, &a.Name, &a.Icon, &a.Category, &a.Builtin, &hidden, &cfg, &a.Width, &a.Height); err != nil {
			return nil, err
		}
		a.Hidden = hidden != 0
		if cfg != "" {
			a.Config = json.RawMessage(cfg)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpsertApp 注册或更新第三方应用。
func (db *DB) UpsertApp(a AppDef) error {
	cfg := ""
	if len(a.Config) > 0 {
		cfg = string(a.Config)
	}
	if a.Width <= 0 {
		a.Width = 800
	}
	if a.Height <= 0 {
		a.Height = 560
	}
	_, err := db.Exec(`INSERT INTO apps(id,name,icon,category,builtin,hidden,config,width,height) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, icon=excluded.icon, category=excluded.category,
		hidden=excluded.hidden, config=excluded.config, width=excluded.width, height=excluded.height`,
		a.ID, a.Name, a.Icon, a.Category, boolInt(a.Builtin), boolInt(a.Hidden), cfg, a.Width, a.Height)
	return err
}

// DeleteApp 删除应用及其授权。
func (db *DB) DeleteApp(id string) error {
	if _, err := db.Exec(`DELETE FROM apps WHERE id=? AND builtin=0`, id); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM role_apps WHERE app_id=?`, id)
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

// ListRoles 返回系统中出现过的全部角色（用户表与授权矩阵的并集，含宿主应用注入的自定义角色）。
func (db *DB) ListRoles() ([]string, error) {
	rows, err := db.Query(`SELECT role FROM users UNION SELECT role FROM role_apps`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	out := []string{"admin", "user", "guest"} // 基础三角色恒在（可能尚无用户/授权记录）
	seen["admin"], seen["user"], seen["guest"] = true, true, true
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

// ---- 用户设置 ----

// GetUserSetting 读取用户级设置，不存在返回空串。
func (db *DB) GetUserSetting(username, key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM user_settings WHERE username=? AND key=?`, username, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetUserSetting 写入用户级设置。
func (db *DB) SetUserSetting(username, key, value string) error {
	_, err := db.Exec(`INSERT INTO user_settings(username,key,value) VALUES(?,?,?)
		ON CONFLICT(username,key) DO UPDATE SET value=excluded.value`, username, key, value)
	return err
}
