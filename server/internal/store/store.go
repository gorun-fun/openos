// Package store 管理 SQLite 数据库的初始化、迁移与种子数据。
// 领域代码按文件拆分：apps.go（应用注册/授权）、chat.go（对话历史/聊天室）、
// feedback.go、attachments.go、settings.go（用户设置/系统 KV）、roles.go（角色目录）、
// aiproviders.go（AI 配置）；本文件保留建库/迁移/种子、审计、用户认证支撑与系统统计。
package store

import (
	"database/sql"
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
	// externalIdentity 外部身份模式（OPENOS_AUTH_MODE=external）：user/guest 是
	// 宿主与 openos 之间的机制角色（如访客兜底桌面），不构成 openos 侧可管理的
	// 角色概念——角色目录/角色清单读取时过滤，存储与授权数据不动。
	externalIdentity bool
}

// User 用户记录。
type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
	// Perms 用户级权限快照（空=无覆盖，回落角色推导）；TokenVersion 供管理端
	// 对账（身份级吊销版本，密码重置递增）。
	Perms        []string `json:"perms"`
	TokenVersion int64    `json:"tokenVersion"`
	CreatedAt    string   `json:"createdAt"`
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
  perms TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS apps (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  icon TEXT NOT NULL DEFAULT '',
  category TEXT NOT NULL DEFAULT 'other',
  builtin INTEGER NOT NULL DEFAULT 0,
  locked INTEGER NOT NULL DEFAULT 0,
  hidden INTEGER NOT NULL DEFAULT 0,
  config TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  width INTEGER NOT NULL DEFAULT 800,
  height INTEGER NOT NULL DEFAULT 560
);
CREATE TABLE IF NOT EXISTS role_apps (
  role TEXT NOT NULL,
  app_id TEXT NOT NULL,
  PRIMARY KEY (role, app_id)
);
CREATE TABLE IF NOT EXISTS roles (
  key TEXT PRIMARY KEY,
  label TEXT NOT NULL DEFAULT '',
  builtin INTEGER NOT NULL DEFAULT 0,
  managed_by TEXT NOT NULL DEFAULT 'system',
  sort INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS role_perms (
  role TEXT NOT NULL,
  perm TEXT NOT NULL,
  PRIMARY KEY (role, perm)
);
CREATE TABLE IF NOT EXISTS perm_defs (
  code TEXT PRIMARY KEY,
  grp TEXT NOT NULL DEFAULT '',
  label TEXT NOT NULL DEFAULT '',
  managed_by TEXT NOT NULL DEFAULT 'system'
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
CREATE TABLE IF NOT EXISTS ai_user_providers (
  uid TEXT NOT NULL,
  name TEXT NOT NULL,
  base_url TEXT NOT NULL DEFAULT '',
  api_key TEXT NOT NULL DEFAULT '',
  models TEXT NOT NULL DEFAULT '[]',
  is_default INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (uid, name)
);
CREATE TABLE IF NOT EXISTS ai_user_provider_names (
  name TEXT PRIMARY KEY
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
CREATE TABLE IF NOT EXISTS attachments (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  mime TEXT NOT NULL,
  size INTEGER NOT NULL,
  uploader TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_attach_created ON attachments(created_at);
CREATE TABLE IF NOT EXISTS chat_room_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  sender TEXT NOT NULL,
  text TEXT NOT NULL DEFAULT '',
  attach_id TEXT NOT NULL DEFAULT '',
  attach_name TEXT NOT NULL DEFAULT '',
  attach_mime TEXT NOT NULL DEFAULT '',
  attach_size INTEGER NOT NULL DEFAULT 0,
  ts INTEGER NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_chat_room_id ON chat_room_messages(id DESC);
CREATE TABLE IF NOT EXISTS shares (
  id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  path TEXT NOT NULL,
  name TEXT NOT NULL,
  size INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_shares_owner ON shares(owner, created_at DESC);
CREATE TABLE IF NOT EXISTS feedback (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL,
  category TEXT NOT NULL DEFAULT 'other',
  content TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  reply TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_feedback_user ON feedback(username, id DESC);
`

// files 表已随 VFS 磁盘化弃用（统一 home 落在 <data>/homes/<user>）：schema 仅为
// 存量库 migrateFromDB 一次性迁移保留，业务代码一律不读写。

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
	{ID: "calendar", Name: "日历", Icon: "calendar", Category: "工具", Builtin: true, Width: 980, Height: 660},
	{ID: "welcome", Name: "使用指南", Icon: "welcome", Category: "系统", Builtin: true, Width: 760, Height: 580},
	{ID: "store", Name: "应用商店", Icon: "store", Category: "系统", Builtin: true, Width: 980, Height: 640},
	{ID: "feedback", Name: "反馈", Icon: "feedback", Category: "系统", Builtin: true, Width: 860, Height: 600},
	// 代码管家（自 grf-os 宿主上游化）：git 仓库接管 → 问题队列（issue 同步/
	// 上报/流水线转修）→ Goder 会话修复 → 提交直推可选发版；含 GitLab 版本×CI
	// 流水线工作面与 AI 转制上架。默认仅 admin（见 defaultRoleApps）。
	{ID: "steward", Name: "代码管家", Icon: "steward", Category: "AI", Builtin: true, Width: 1280, Height: 800},
}

// vfsAppIDs 依赖虚拟文件系统/终端的内置应用（OPENOS_VFS=off 时整体下架，
// 业务宿主的文件与文档数据交由集成方自己管理）。
var vfsAppIDs = map[string]bool{
	"terminal": true, "files": true, "docs": true, "sheets": true, "slides": true, "pdf": true,
}

// goderAppID goder 应用（OPENOS_GODER=off 时下架，纯业务宿主无需 AI IDE）。
const goderAppID = "goder"

// marketAppID 应用商店（OPENOS_MARKET=off 时下架，自维护应用列表的宿主无需商店）。
const marketAppID = "store"

// stewardAppID 代码管家（OPENOS_STEWARD=off 时下架：纯轻量宿主无需代码修复面）。
const stewardAppID = "steward"

// 默认角色 → 应用授权矩阵。
var defaultRoleApps = map[string][]string{
	"admin": {"terminal", "files", "calculator", "settings", "docs", "sheets", "slides", "pdf", "goder", "notes", "calendar", "welcome", "store", "feedback", "steward"},
	"user":  {"terminal", "files", "calculator", "docs", "sheets", "slides", "pdf", "goder", "notes", "calendar", "welcome", "store", "feedback"},
	"guest": {"calculator", "notes", "calendar", "welcome", "feedback"},
}

// StorageQuota 每用户虚拟文件空间上限（字节，按内容长度估算）。
const StorageQuota = 100 * 1024 * 1024

// ---- 功能裁剪选项（宿主集成：SQLite 只保留演示数据与每用户界面设置） ----

type config struct {
	withoutVFS       bool
	withoutGoder     bool
	withoutMarket    bool
	withoutSteward   bool
	externalIdentity bool
}

// Option 功能裁剪选项，配合 Open 使用。
type Option func(*config)

// WithoutSteward 下架代码管家（OPENOS_STEWARD=off 时由 main 传入）。
func WithoutSteward() Option { return func(c *config) { c.withoutSteward = true } }

// WithoutVFS 下架 VFS/终端类内置应用（OPENOS_VFS=off 时由 main 传入）。
func WithoutVFS() Option { return func(c *config) { c.withoutVFS = true } }

// WithoutGoder 下架 goder 应用（OPENOS_GODER=off 时由 main 传入）。
func WithoutGoder() Option { return func(c *config) { c.withoutGoder = true } }

// WithoutMarket 下架应用商店（OPENOS_MARKET=off 时由 main 传入）。
func WithoutMarket() Option { return func(c *config) { c.withoutMarket = true } }

// ExternalIdentity 外部身份模式（OPENOS_AUTH_MODE=external 时由 main 传入）：
// 身份托管于宿主/IdP，user/guest 退化为宿主侧机制角色（访客兜底桌面等），
// 角色目录与角色清单不再返回它们（admin 保留：openos 自身管理语义仍在）。
func ExternalIdentity() Option { return func(c *config) { c.externalIdentity = true } }

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
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}
	db := &DB{DB: d, externalIdentity: cfg.externalIdentity}
	if err := db.migrateColumns(); err != nil {
		return nil, err
	}
	if err := db.migrateSystemKV(); err != nil {
		return nil, err
	}
	if err := db.seed(cfg); err != nil {
		return nil, err
	}
	if err := db.EnsureRolesBootstrap(); err != nil {
		return nil, fmt.Errorf("roles bootstrap: %w", err)
	}
	return db, nil
}

// migrateColumns 为旧库补列（幂等）。
func (db *DB) migrateColumns() error {
	type col struct{ table, name, ddl string }
	for _, c := range []col{
		{"users", "token_version", "ALTER TABLE users ADD COLUMN token_version INTEGER NOT NULL DEFAULT 0"},
		{"users", "disabled", "ALTER TABLE users ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0"},
		{"users", "perms", "ALTER TABLE users ADD COLUMN perms TEXT NOT NULL DEFAULT ''"},
		{"files", "is_binary", "ALTER TABLE files ADD COLUMN is_binary INTEGER NOT NULL DEFAULT 0"},
		{"apps", "hidden", "ALTER TABLE apps ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0"},
		{"apps", "locked", "ALTER TABLE apps ADD COLUMN locked INTEGER NOT NULL DEFAULT 0"},
		{"apps", "version", "ALTER TABLE apps ADD COLUMN version TEXT NOT NULL DEFAULT ''"},
		{"apps", "source", "ALTER TABLE apps ADD COLUMN source TEXT NOT NULL DEFAULT ''"},
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
		if cfg.withoutMarket && a.ID == marketAppID {
			continue
		}
		if cfg.withoutSteward && a.ID == stewardAppID {
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
		if _, err := db.Exec(`INSERT INTO apps(id,name,icon,category,builtin,locked,hidden,config,width,height) VALUES(?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, icon=excluded.icon, config=excluded.config, width=excluded.width, height=excluded.height`,
			a.ID, a.Name, a.Icon, a.Category, boolInt(a.Builtin), boolInt(a.Locked), boolInt(a.Hidden), cfgJSON, a.Width, a.Height); err != nil {
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

// ---- 用户 / 认证支撑 ----

// UserAuthInfo 供 JWT 校验的用户态。
type UserAuthInfo struct {
	Exists       bool
	Role         string
	PasswordHash string
	Salt         string
	DisplayName  string
	TokenVersion int64
	Disabled     bool
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

// ---- 数据保留期（防无限增长） ----

// PruneAuditBefore 删除 cutoff 之前的审计日志，返回删除行数。
// login.fail 是最大来源（公开接口 + 限流），无保留期时单日可膨胀数十万行。
func (db *DB) PruneAuditBefore(cutoff time.Time) (int64, error) {
	return db.pruneBefore("audit_log", cutoff)
}

func (db *DB) pruneBefore(table string, cutoff time.Time) (int64, error) {
	res, err := db.Exec(`DELETE FROM `+table+` WHERE created_at < ?`, cutoff.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- 系统统计 ----

// SystemStats 系统信息。
type SystemStats struct {
	Version      string `json:"version"`
	UserCount    int    `json:"userCount"`
	AppCount     int    `json:"appCount"`
	FileCount    int    `json:"fileCount"`
	StorageUsed  int64  `json:"storageUsed"`
	QuotaPerUser int64  `json:"quotaPerUser"`
	StartedAt    string `json:"startedAt"`
}

// Stats 汇总系统统计。文件统计（FileCount/StorageUsed）由调用方以统一 home 磁盘
// 遍历（fs.DiskStats）填入——files 表已弃用不读写（VFS 关闭的裁剪部署该两项为 0）。
func (db *DB) Stats(startedAt time.Time) SystemStats {
	s := SystemStats{Version: Version, StartedAt: startedAt.Format(time.RFC3339), QuotaPerUser: StorageQuota}
	db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&s.UserCount)
	db.QueryRow(`SELECT COUNT(*) FROM apps`).Scan(&s.AppCount)
	return s
}
