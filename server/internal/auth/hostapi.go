// hostapi.go — 宿主互操作：令牌交换（exchange）、令牌内省（introspect）与
// /api/host/* 机对机管理接口（X-Exchange-Secret 守护）。
//
// 宿主应用（grf-hotel / grf-os 等）经 hostsdk 与 openos 互操作，三条通道：
//   - POST /api/auth/exchange：宿主 SSO 认证后为用户换取 OpenOS JWT；
//   - POST /api/auth/introspect：宿主业务后端验证 OpenOS JWT（认证反向互通）；
//   - /api/host/*：宿主管理 OpenOS 用户体系（用户列表/创建/批量同步/更新/删除、
//     角色与权限点目录、declare 投影）。取代旧「exchange 铸造 admin 服务账号令牌
//     再调管理 REST」的间接路径——不再依赖 OPENOS_EXCHANGE_ROLES 白名单放行
//     admin，也不再产生假 admin 用户行。
//
// 安全边界：整组共用 OPENOS_EXCHANGE_SECRET（恒定时间比对）。该密钥是宿主与
// openos 之间的全信任凭证（与 declare 同级）——持有者可管理全部用户体系（含
// 设置 admin 角色）；OPENOS_EXCHANGE_ROLES 仅约束 exchange 的用户令牌铸造面。
package auth

import (
	"crypto/subtle"
	"net/http"

	"openos/server/internal/store"
)

// requireHostSecret 机对机守护（/api/host/* 组共用；失败时已写出响应）。
func (s *Service) requireHostSecret(w http.ResponseWriter, r *http.Request) bool {
	ip := s.clientIP(r)
	if s.exchangeSecret == "" {
		jsonError(w, http.StatusServiceUnavailable, "宿主接口未启用（需配置 OPENOS_EXCHANGE_SECRET）")
		return false
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Exchange-Secret")), []byte(s.exchangeSecret)) != 1 {
		s.db.Audit("?", "auth.host.deny", "path="+r.URL.Path+" ip="+ip, ip)
		jsonError(w, http.StatusUnauthorized, "密钥错误")
		return false
	}
	return true
}

// Exchange POST /api/auth/exchange —— 宿主应用服务端以共享密钥换取 OpenOS JWT。
//
// 供「宿主应用集成 OpenOS」场景：宿主完成自己的 SSO 认证后，服务端到服务端
// 调用本端点为该用户换取 OpenOS 身份（浏览器无需再过 OpenOS 登录）。
//   - 请求头 X-Exchange-Secret 与 OPENOS_EXCHANGE_SECRET 恒定时间比对；
//   - 配置 OPENOS_EXCHANGE_ROLES 白名单后，共享密钥泄露也无法铸造白名单外
//     角色（如 admin）；
//   - 用户不存在时自动开通（随机不可登录密码，登录只能经宿主，displayName
//     作为初始显示名写入）；
//   - 角色/权限快照经统一变更内核 patchUser 同步：**变化不吊销旧令牌**（该
//     用户其它设备的会话稳定，授权实时生效）；已有用户的显示名不随 exchange
//     覆盖（改名走 users/sync / UpdateUser / 账户页自助接口）；
//   - 响应与登录一致：{token, tokenTtl, user}，并种同源 Cookie 供嵌入组件使用。
func (s *Service) Exchange(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.exchangeSecret == "" {
		jsonError(w, http.StatusServiceUnavailable, "令牌交换未启用")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Exchange-Secret")), []byte(s.exchangeSecret)) != 1 {
		s.db.Audit("?", "auth.exchange.deny", "ip="+ip, ip)
		jsonError(w, http.StatusUnauthorized, "交换密钥错误")
		return
	}
	var req struct {
		Username    string   `json:"username"`
		Role        string   `json:"role"`
		DisplayName string   `json:"displayName"`
		// Perms 功能级权限点快照（宿主接管模式：宿主权威库算出的该用户权限并集）。
		// nil=不变（兼容旧宿主）；非 nil（含空数组）=覆盖 users.perms，空数组即
		// 显式清空（回落角色推导）。
		Perms []string `json:"perms"`
	}
	if err := jsonDecode(r, &req); err != nil || req.Username == "" {
		jsonError(w, 400, "参数错误")
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	if !validRole(req.Role) {
		jsonError(w, 400, "非法角色")
		return
	}
	if s.exchangeRoles != nil && !s.exchangeRoles[req.Role] {
		s.db.Audit(req.Username, "auth.exchange.deny", "role not allowed: "+req.Role, ip)
		jsonError(w, 400, "角色不在交换白名单内（OPENOS_EXCHANGE_ROLES）")
		return
	}

	info, err := s.db.GetUserAuth(req.Username)
	if err != nil {
		jsonError(w, 500, "读取用户失败")
		return
	}
	if !info.Exists {
		// 自动开通：密码置为随机值（宿主体系外的登录通道不可用）
		pw := randomHex(24)
		salt := newSalt()
		if _, err := s.db.Exec(`INSERT INTO users(username,password_hash,salt,role,display_name,token_version) VALUES(?,?,?,?,?,?)`,
			req.Username, hashPassword(pw, salt), salt, req.Role, req.DisplayName, initialTokenVersion()); err != nil {
			jsonError(w, 400, "开通用户失败（用户名可能已存在）")
			return
		}
		s.InvalidateCache(req.Username)
		s.db.Audit(req.Username, "user.exchange-provision", "role="+req.Role, ip)
	} else if info.Disabled {
		jsonError(w, 403, "用户已被禁用")
		return
	}

	// 角色与权限快照同步（宿主权威）经统一变更内核：变化即时生效、不吊销该用户
	// 其它设备的令牌（登录态稳定化）。宿主通道等效 admin 信任（白名单已在上方
	// 单独校验）。
	p := userPatch{Role: &req.Role, Perms: req.Perms}
	if _, err := s.patchUser(req.Username, p, "exchange", ip, true); err != nil {
		writePatchErr(w, err)
		return
	}

	// 以库中最新状态（patch 可能调整了 role）签发
	info, err = s.db.GetUserAuth(req.Username)
	if err != nil || !info.Exists {
		jsonError(w, 500, "读取用户失败")
		return
	}
	token, err := s.Issue(req.Username, info.Role, info.TokenVersion)
	if err != nil {
		jsonError(w, 500, "签发令牌失败")
		return
	}
	s.setSessionCookie(w, r, token, int(s.ttl.Seconds()))
	s.db.Audit(req.Username, "auth.exchange", "role="+info.Role, ip)
	writeJSON(w, map[string]any{"token": token, "tokenTtl": int(s.ttl.Seconds()), "user": map[string]any{
		"username": req.Username, "role": info.Role, "displayName": info.DisplayName,
		"perms": s.effectivePerms(req.Username, info.Role),
	}})
}

// Introspect POST /api/auth/introspect —— 业务后端验证 OpenOS JWT（认证反向互通）。
//
// 宿主/业务后端拿到前端携带的 OpenOS JWT 后，服务端到服务端调用本端点确认令牌
// 活性并取回身份，建立「OpenOS 用户 ↔ 业务账号」映射（如酒店后端把 OpenOS 壳内
// 应用的请求映射到 staff）。协议为 RFC 7662 风格的子集：
//   - 守护与令牌交换同一把 OPENOS_EXCHANGE_SECRET（X-Exchange-Secret 头）；未配置
//     时端点 503，独立部署安全面不变；
//   - 判定与 REST/WS 同源（签名/有效期/用户存在/token_version/禁用态），被吊销
//     令牌（改密/删号）或禁用账号立即 active:false；**角色变化不再 inactive**——
//     授权实时生效（身份与授权分离），返回的 role 为实时库值；
//   - 响应恒 200：有效 {"active":true,sub,username,role,displayName,exp,iat}，
//     无效 {"active":false}（不区分原因，保持协议简单）；
//   - 逐请求一跳代价可观，调用方应缓存（建议 TTL 取 min(exp-now, 60s)，对齐服务端
//     用户态 10s 缓存的吊销时延）。
func (s *Service) Introspect(w http.ResponseWriter, r *http.Request) {
	if s.exchangeSecret == "" {
		jsonError(w, http.StatusServiceUnavailable, "令牌内省未启用（需配置 OPENOS_EXCHANGE_SECRET）")
		return
	}
	ip := s.clientIP(r)
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Exchange-Secret")), []byte(s.exchangeSecret)) != 1 {
		s.db.Audit("?", "auth.introspect.deny", "ip="+ip, ip)
		jsonError(w, http.StatusUnauthorized, "密钥错误")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := jsonDecode(r, &req); err != nil || req.Token == "" {
		jsonError(w, 400, "参数错误")
		return
	}
	c, err := s.Verify(req.Token)
	if err != nil {
		writeJSON(w, map[string]any{"active": false})
		return
	}
	exists, role, tv, disabled := s.currentUserRole(c.Sub)
	if !exists || tv != c.TV || disabled {
		writeJSON(w, map[string]any{"active": false})
		return
	}
	info, _ := s.db.GetUserAuth(c.Sub)
	writeJSON(w, map[string]any{
		"active": true, "sub": c.Sub, "username": c.Sub,
		"role": role, "displayName": info.DisplayName,
		"exp": c.Exp, "iat": c.Iat,
	})
}

// ---- /api/host/* 机对机用户体系管理（公开 mux + 密钥守护；审计 operator="host"） ----

// HostListUsers GET /api/host/users —— 全字段用户列表（含权限快照与 token 版本）。
func (s *Service) HostListUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireHostSecret(w, r) {
		return
	}
	s.listUsersCore(w)
}

// HostCreateUser POST /api/host/users —— 显式建用户（宿主权威；空密码 → 随机值）。
func (s *Service) HostCreateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireHostSecret(w, r) {
		return
	}
	s.createUserCore(w, r, "host", true)
}

// HostSyncUsers POST /api/host/users/sync —— 批量幂等同步（语义同 REST 版）。
func (s *Service) HostSyncUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireHostSecret(w, r) {
		return
	}
	s.syncUsersCore(w, r, "host")
}

// HostUpdateUser PUT /api/host/users/{name} —— 字段级更新（含 perms，语义同 REST 版）。
func (s *Service) HostUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireHostSecret(w, r) {
		return
	}
	s.updateUserCore(w, r, "host", true)
}

// HostDeleteUser DELETE /api/host/users/{name} —— 删除并级联清理。
func (s *Service) HostDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireHostSecret(w, r) {
		return
	}
	s.deleteUserCore(w, r, "host")
}

// HostRoles GET /api/host/roles —— 角色目录 + 权限点目录（宿主侧对账/展示用；
// 投影写入走 POST /api/host/declare）。
func (s *Service) HostRoles(w http.ResponseWriter, r *http.Request) {
	if !s.requireHostSecret(w, r) {
		return
	}
	roles, err := s.db.ListRoleDefs()
	if err != nil {
		jsonError(w, 500, "读取角色目录失败: "+err.Error())
		return
	}
	perms, err := s.db.PermsCatalog()
	if err != nil {
		jsonError(w, 500, "读取权限点目录失败: "+err.Error())
		return
	}
	if roles == nil {
		roles = []store.RoleDef{}
	}
	if perms == nil {
		perms = []store.PermDef{}
	}
	writeJSON(w, map[string]any{"roles": roles, "perms": perms})
}
