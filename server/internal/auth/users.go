// users.go — 用户管理：列表 / 创建 / 批量同步 / 更新 / 删除 / 自助接口，以及
// 统一变更内核 patchUser。
//
// 登录态稳定化语义（2026-10，见 auth.go 包注释）：
//   - role / perms / displayName / disabled 属授权或展示层数据：变化一律不吊销
//     令牌（ InvalidateCache 立即生效 + sys:user 推送），宿主幂等同步与角色
//     调整不再把用户踢下线；
//   - 密码变更/重置是唯一身份级吊销（BumpTokenVersion，该用户全部旧令牌失效）；
//   - 三条写路径（REST UpdateUser / 批量 SyncUsers / 宿主 Exchange 与 /api/host/*）
//     共用 patchUser，字段出现≠变化、语义完全对齐（历史上 displayName 在 sync
//     会 bump 而 update 不会的不对称已消灭）。
package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"openos/server/internal/store"
	"openos/server/internal/webres"
)

// userPatch 单用户字段级补丁（指针语义：nil=不改动；Perms nil=不改动，
// 非 nil 含空数组=覆盖 users.perms——空数组即显式清空、回落角色推导）。
type userPatch struct {
	Role        *string
	DisplayName *string
	Disabled    *bool
	Perms       []string
	Password    *string // 管理员重置密码（唯一触发身份级吊销的字段）
}

// patchError 带状态码的变更拒绝（调用方按 code 映射 4xx，或作为批量同步的
// skipped 原因文本）。
type patchError struct {
	code int
	msg  string
}

func (e *patchError) Error() string { return e.msg }

// insertUser 统一开通内核（createUserCore / syncUsersCore / Exchange 自动开通 /
// SeedDefaults 四条路径共用）：newSalt + hashPassword + initialTokenVersion + perms
// 序列化一次写全——此前四份 INSERT 各自拼列集已分叉（create 无 disabled、
// exchange/seed 无 perms），新增用户字段只改这里。
func (s *Service) insertUser(username, password, role, displayName string, disabled bool, perms []string) error {
	salt := newSalt()
	permsVal := ""
	if len(perms) > 0 {
		b, _ := json.Marshal(perms)
		permsVal = string(b)
	}
	_, err := s.db.Exec(`INSERT INTO users(username,password_hash,salt,role,display_name,token_version,disabled,perms) VALUES(?,?,?,?,?,?,?,?)`,
		username, hashPassword(password, salt), salt, role, displayName, initialTokenVersion(), boolInt(disabled), permsVal)
	return err
}

// setPassword 统一密码写入（新盐重哈希并失效缓存；bump=true 吊销该用户全部
// 旧令牌——管理员重置/自助改密属身份级事件，登录透明升级的等参数重哈希
// 不吊销）。patchUser / ChangePassword / Login 透明升级三处共用。
func (s *Service) setPassword(username, password string, bump bool) error {
	salt := newSalt()
	if _, err := s.db.Exec(`UPDATE users SET password_hash=?, salt=? WHERE username=?`, hashPassword(password, salt), salt, username); err != nil {
		return err
	}
	if bump {
		s.db.BumpTokenVersion(username)
	}
	s.InvalidateCache(username)
	return nil
}

func patchErr(code int, format string, a ...any) error {
	return &patchError{code: code, msg: fmt.Sprintf(format, a...)}
}

// writePatchErr 按状态码写出变更拒绝（REST 单用户路径用）。
func writePatchErr(w http.ResponseWriter, err error) {
	code := 400
	if pe, ok := err.(*patchError); ok && pe.code != 0 {
		code = pe.code
	}
	webres.Error(w, code, err.Error())
}

// patchUser 统一用户变更内核（UpdateUser / SyncUsers / Exchange / /api/host/* 共用）：
//   - 只应用真正变化的字段（字段出现≠变化，幂等同步不产生写入、审计与推送噪音）；
//   - role/perms/displayName/disabled 变化：不吊销令牌，InvalidateCache 立即生效，
//     并经 sys:user 推送最新身份（角色变化另广播 sys:apps-changed）；
//   - 密码重置：BumpTokenVersion（该用户全部旧令牌失效）——唯一身份级吊销；
//   - 禁用不 bump：REST/WS/introspect 的 disabled 实时判定已即时拦截；
//   - 末位管理员保护（降级/禁用）与非法字段校验仅在实际变化时生效；
//   - operatorAdmin=false（REST 侧持 users.manage 的非 admin 操作者）防提权：
//     不得操作 admin 用户、不得授予 admin 角色（host 密钥通道等效 admin 信任）。
//
// 返回是否有字段实际变化；拒绝时返回 *patchError。
func (s *Service) patchUser(name string, p userPatch, operator, ip string, operatorAdmin bool) (bool, error) {
	info, err := s.db.GetUserAuth(name)
	if err != nil {
		return false, patchErr(500, "读取用户失败")
	}
	if !info.Exists {
		return false, patchErr(404, "用户不存在")
	}
	// 防提权：admin 资源（admin 用户本体 / admin 角色授予）只对 admin 与宿主通道开放
	if !operatorAdmin {
		if info.Role == "admin" {
			return false, patchErr(403, "操作 admin 用户需要管理员权限")
		}
		if p.Role != nil && *p.Role == "admin" {
			return false, patchErr(403, "授予 admin 角色需要管理员权限")
		}
	}
	changed := false
	roleChanged := false
	if p.Role != nil && *p.Role != info.Role {
		if !validRole(*p.Role) {
			return false, patchErr(400, "非法角色")
		}
		if info.Role == "admin" && *p.Role != "admin" && s.db.CountAdmins() <= 1 {
			return false, patchErr(400, "系统至少需要保留一名管理员")
		}
		if _, err := s.db.Exec(`UPDATE users SET role=? WHERE username=?`, *p.Role, name); err != nil {
			return false, patchErr(500, "保存角色失败")
		}
		changed, roleChanged = true, true
		s.db.Audit(operator, "user.role", name+" → "+*p.Role, ip)
	}
	if p.Disabled != nil && *p.Disabled != info.Disabled {
		if info.Role == "admin" && *p.Disabled && s.db.CountAdmins() <= 1 {
			return false, patchErr(400, "不能禁用最后一名管理员")
		}
		if _, err := s.db.Exec(`UPDATE users SET disabled=? WHERE username=?`, boolInt(*p.Disabled), name); err != nil {
			return false, patchErr(500, "保存禁用态失败")
		}
		changed = true
		s.db.Audit(operator, "user.disabled", name+" = "+map[bool]string{true: "on", false: "off"}[*p.Disabled], ip)
	}
	if p.DisplayName != nil && *p.DisplayName != "" && *p.DisplayName != info.DisplayName {
		if utf8.RuneCountInString(*p.DisplayName) > 64 {
			return false, patchErr(400, "显示名过长（最多 64 个字符）")
		}
		if _, err := s.db.Exec(`UPDATE users SET display_name=? WHERE username=?`, *p.DisplayName, name); err != nil {
			return false, patchErr(500, "保存显示名失败")
		}
		changed = true
		s.db.Audit(operator, "user.displayname", name+" → "+*p.DisplayName, ip)
	}
	if p.Perms != nil {
		v := ""
		if len(p.Perms) > 0 {
			b, _ := json.Marshal(p.Perms)
			v = string(b)
		}
		var old string
		s.db.QueryRow(`SELECT perms FROM users WHERE username=?`, name).Scan(&old)
		if old != v {
			if _, err := s.db.Exec(`UPDATE users SET perms=? WHERE username=?`, v, name); err != nil {
				return false, patchErr(500, "同步权限失败")
			}
			changed = true
			s.db.Audit(operator, "user.perms", fmt.Sprintf("%s 权限覆盖 %d 项", name, len(p.Perms)), ip)
		}
	}
	if p.Password != nil && *p.Password != "" {
		if WeakPassword(*p.Password) {
			return false, patchErr(400, "密码至少 8 位，且需同时包含字母与数字")
		}
		// bump=true：密码重置是唯一身份级吊销（该用户全部旧令牌失效）
		if err := s.setPassword(name, *p.Password, true); err != nil {
			return false, patchErr(500, "保存密码失败")
		}
		changed = true
		s.db.Audit(operator, "user.password", name+" 密码重置", ip)
	}
	if changed {
		s.InvalidateCache(name)
		s.notifyIdentityChanged(name, roleChanged)
	}
	return changed, nil
}

// ---- REST handlers（登录 mux；main 按 users.manage 接管，admin 恒直通） ----

// ListUsers GET /api/users —— 全字段列表（含权限快照与 token 版本，管理 UI 用）。
func (s *Service) ListUsers(w http.ResponseWriter, r *http.Request) {
	s.listUsersCore(w)
}

func (s *Service) listUsersCore(w http.ResponseWriter) {
	rows, err := s.db.Query(`SELECT id,username,display_name,role,disabled,perms,token_version,created_at FROM users ORDER BY id`)
	if err != nil {
		webres.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()
	users := []store.User{}
	for rows.Next() {
		var u store.User
		var disabled int
		var perms string
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &disabled, &perms, &u.TokenVersion, &u.CreatedAt); err != nil {
			continue
		}
		u.Disabled = disabled != 0
		if perms != "" && perms != "null" {
			_ = json.Unmarshal([]byte(perms), &u.Perms)
		}
		if u.Perms == nil {
			u.Perms = []string{}
		}
		users = append(users, u)
	}
	webres.WriteJSON(w, users)
}

// CreateUser POST /api/users —— hostMode=false（REST）；宿主通道见 hostapi.go。
func (s *Service) CreateUser(w http.ResponseWriter, r *http.Request) {
	s.createUserCore(w, r, FromContext(r).Username, false)
}

func (s *Service) createUserCore(w http.ResponseWriter, r *http.Request, operator string, hostMode bool) {
	var req struct {
		Username, Password, Role, DisplayName string
		Perms                                 []string // 可选：初始权限快照
	}
	if err := jsonDecode(r, &req); err != nil || req.Username == "" {
		webres.Error(w, 400, "参数错误")
		return
	}
	// 宿主通道 / 外部身份模式：密码留空 → 随机值（本地登录通道不可用或交由宿主）
	if (hostMode || s.externalAuth) && req.Password == "" {
		req.Password = randomHex(24)
	}
	if WeakPassword(req.Password) {
		webres.Error(w, 400, "密码至少 8 位，且需同时包含字母与数字")
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	if !validRole(req.Role) {
		webres.Error(w, 400, "非法角色")
		return
	}
	if err := s.insertUser(req.Username, req.Password, req.Role, req.DisplayName, false, req.Perms); err != nil {
		webres.Error(w, 400, "创建失败（用户名可能已存在）")
		return
	}
	s.db.Audit(operator, "user.create", req.Username+" role="+req.Role, s.clientIP(r))
	webres.WriteJSON(w, ok())
}

// SyncUsers POST /api/users/sync —— 宿主/管理员批量幂等同步用户。
//
// 不存在则开通（随机密码，登录只能经宿主），存在则经 patchUser 按字段更新
//（变化即时生效不吊销）。不在名单内的用户不动（单向同步语义，删除走
// DELETE /api/users/{name}）。拒绝项（末位管理员/非法字段等）进 skipped 不中断整批。
func (s *Service) SyncUsers(w http.ResponseWriter, r *http.Request) {
	s.syncUsersCore(w, r, FromContext(r).Username)
}

func (s *Service) syncUsersCore(w http.ResponseWriter, r *http.Request, operator string) {
	var req struct {
		Users []struct {
			Username    string   `json:"username"`
			Role        string   `json:"role"`
			DisplayName string   `json:"displayName"`
			Disabled    *bool    `json:"disabled"`
			Perms       []string `json:"perms"` // nil=不变，非 nil（含空数组）=覆盖
		} `json:"users"`
	}
	if err := jsonDecode(r, &req); err != nil || len(req.Users) == 0 {
		webres.Error(w, 400, "参数错误：需要 users 数组")
		return
	}
	ip := s.clientIP(r)
	created, updated := 0, 0
	skipped := []string{}
	for _, u := range req.Users {
		if u.Username == "" {
			continue
		}
		if u.Role == "" {
			u.Role = "user"
		}
		if !validRole(u.Role) {
			skipped = append(skipped, u.Username+"（非法角色）")
			continue
		}
		info, err := s.db.GetUserAuth(u.Username)
		if err != nil {
			skipped = append(skipped, u.Username+"（读取失败）")
			continue
		}
		if !info.Exists {
			pw := randomHex(24)
			disabled := u.Disabled != nil && *u.Disabled
			if err := s.insertUser(u.Username, pw, u.Role, u.DisplayName, disabled, u.Perms); err != nil {
				skipped = append(skipped, u.Username+"（开通失败）")
				continue
			}
			s.db.Audit(operator, "user.sync-create", u.Username+" role="+u.Role, ip)
			created++
			continue
		}
		p := userPatch{Role: &u.Role, DisplayName: &u.DisplayName, Disabled: u.Disabled, Perms: u.Perms}
		changed, err := s.patchUser(u.Username, p, operator, ip, true)
		if err != nil {
			skipped = append(skipped, u.Username+"（"+err.Error()+"）")
			continue
		}
		if changed {
			updated++
		}
	}
	webres.WriteJSON(w, map[string]any{"created": created, "updated": updated, "skipped": skipped})
}

// UpdateUser PUT /api/users/{name} —— 改角色/显示名/重置密码/禁用/权限快照。
// perms 字段为本次重构新增（旧版静默丢弃——hostsdk SyncUser 传了不生效的历史
// bug 已修）；语义与 SyncUsers/Exchange 完全对齐。
func (s *Service) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id := FromContext(r)
	s.updateUserCore(w, r, id.Username, id.Role == "admin")
}

func (s *Service) updateUserCore(w http.ResponseWriter, r *http.Request, operator string, operatorAdmin bool) {
	name := r.PathValue("name")
	var req struct {
		Role        *string   `json:"role"`
		DisplayName *string   `json:"displayName"`
		Password    *string   `json:"password"`
		Disabled    *bool     `json:"disabled"`
		Perms       *[]string `json:"perms"` // nil=不变，非 nil（含空数组）=覆盖
	}
	if err := jsonDecode(r, &req); err != nil {
		webres.Error(w, 400, "参数错误")
		return
	}
	p := userPatch{Role: req.Role, DisplayName: req.DisplayName, Password: req.Password, Disabled: req.Disabled}
	if req.Perms != nil {
		p.Perms = *req.Perms
	}
	if _, err := s.patchUser(name, p, operator, s.clientIP(r), operatorAdmin); err != nil {
		writePatchErr(w, err)
		return
	}
	webres.WriteJSON(w, ok())
}

// DeleteUser DELETE /api/users/{name} —— 级联清理该用户全部数据（行级 + 钩子）。
func (s *Service) DeleteUser(w http.ResponseWriter, r *http.Request) {
	s.deleteUserCore(w, r, FromContext(r).Username)
}

func (s *Service) deleteUserCore(w http.ResponseWriter, r *http.Request, operator string) {
	name := r.PathValue("name")
	info, _ := s.db.GetUserAuth(name)
	if !info.Exists {
		webres.Error(w, 404, "用户不存在")
		return
	}
	if info.Role == "admin" && !info.Disabled && s.db.CountAdmins() <= 1 {
		webres.Error(w, 400, "不能删除最后一名管理员")
		return
	}
	// 行级级联：账号体系 + 设置 + 文件 + 聊天（私聊/聊天室）+ 分享链接 + 反馈 +
	// AI 每用户供应商 + 附件元数据（审计日志按保留期独立清理，保留追责线索）。
	// 附件磁盘文件由 main 注入的钩子（attachStore.DeleteByUploader）清理。
	for _, stmt := range []string{
		`DELETE FROM users WHERE username=?`,
		`DELETE FROM user_settings WHERE username=?`,
		`DELETE FROM files WHERE owner=?`,
		`DELETE FROM chat_messages WHERE username=?`,
		`DELETE FROM chat_room_messages WHERE sender=?`,
		`DELETE FROM shares WHERE owner=?`,
		`DELETE FROM feedback WHERE username=?`,
		`DELETE FROM ai_user_providers WHERE uid=?`,
		`DELETE FROM attachments WHERE uploader=?`,
	} {
		s.db.Exec(stmt, name)
	}
	s.InvalidateCache(name)
	s.db.Audit(operator, "user.delete", name, s.clientIP(r))
	// 级联清理磁盘数据（真实终端沙箱 / goder 会话目录 / 附件文件），由 main 注入
	if s.onUserDeleted != nil {
		s.onUserDeleted(name)
	}
	webres.WriteJSON(w, ok())
}

// ---- 自助接口 ----

// ChangePassword PUT /api/users/password —— 自助改密：bump 使旧设备全部下线
//（身份级事件，合法吊销），新令牌由本次响应外的前端重新登录/续期取得。
func (s *Service) ChangePassword(w http.ResponseWriter, r *http.Request) {
	if s.externalAuth {
		webres.Error(w, http.StatusForbidden, "密码由统一身份系统管理（OPENOS_AUTH_MODE=external），本地改密已停用")
		return
	}
	id := FromContext(r)
	var req struct{ Old, New string }
	jsonDecode(r, &req)
	info, _ := s.db.GetUserAuth(id.Username)
	if !info.Exists || checkPassword(req.Old, info.Salt, info.PasswordHash) == 0 {
		webres.Error(w, 400, "原密码错误")
		return
	}
	if WeakPassword(req.New) {
		webres.Error(w, 400, "新密码至少 8 位，且需同时包含字母与数字")
		return
	}
	_ = s.setPassword(id.Username, req.New, true) // bump：旧设备全部下线（唯一身份级吊销）
	s.db.Audit(id.Username, "user.password", "self（旧设备全部下线）", s.clientIP(r))
	webres.WriteJSON(w, ok())
}

// SetDisplayName PUT /api/users/display-name —— 自助修改显示名（展示层字段，
// 不涉权限不吊销；就地生效——前端 session.user 由 sys:user 推送回正）。
func (s *Service) SetDisplayName(w http.ResponseWriter, r *http.Request) {
	id := FromContext(r)
	var req struct {
		DisplayName string `json:"displayName"`
	}
	if err := jsonDecode(r, &req); err != nil {
		webres.Error(w, 400, "参数错误")
		return
	}
	name := strings.TrimSpace(req.DisplayName)
	if name == "" {
		webres.Error(w, 400, "显示名不能为空")
		return
	}
	if utf8.RuneCountInString(name) > 64 {
		webres.Error(w, 400, "显示名过长（最多 64 个字符）")
		return
	}
	s.db.Exec(`UPDATE users SET display_name=? WHERE username=?`, name, id.Username)
	s.InvalidateCache(id.Username)
	s.db.Audit(id.Username, "user.displayname", "self → "+name, s.clientIP(r))
	s.notifyIdentityChanged(id.Username, false)
	webres.WriteJSON(w, ok())
}

// SeedDefaults 创建默认演示账号（仅当用户表为空且 OPENOS_SEED_DEMO 未关闭；
// 宿主集成场景应设 OPENOS_SEED_DEMO=0 或 OPENOS_AUTH_MODE=external，账号体系
// 完全交由集成方）。产品部署后请立即修改密码。
func (s *Service) SeedDefaults() error {
	if s.externalAuth || os.Getenv("OPENOS_SEED_DEMO") == "0" {
		return nil
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n > 0 {
		return nil
	}
	defaults := []struct{ u, p, role, disp string }{
		{"admin", "admin123", "admin", "管理员"},
		{"user", "user123", "user", "普通用户"},
		{"guest", "guest123", "guest", "访客"},
	}
	for _, d := range defaults {
		if err := s.insertUser(d.u, d.p, d.role, d.disp, false, nil); err != nil {
			return err
		}
	}
	return nil
}
