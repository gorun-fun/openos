// OpenOS 服务端入口：组装存储、认证、事件总线、虚拟文件系统与 AI 服务。
//
// 产品级特性：
//   - panic 恢复 + 访问日志 + 安全响应头 + 全局限流 + 登录防爆破（auth 包）
//   - 优雅停机（SIGINT/SIGTERM：停止接收新请求、断开总线连接）
//   - AI 配置在线热更新、审计日志、系统统计、版本接口
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"openos/server/internal/ai"
	"openos/server/internal/attach"
	"openos/server/internal/auth"
	"openos/server/internal/bus"
	"openos/server/internal/calendar"
	"openos/server/internal/goder"
	"openos/server/internal/market"
	"openos/server/internal/osmcp"
	"openos/server/internal/store"
	"openos/server/internal/vfs"
	"openos/server/internal/webproxy"
)

func main() {
	// goder/gh 桌面控制插件的 stdio↔HTTP 适配子命令：早于一切服务初始化分派
	// （shim 路径不打开数据库、不监听端口）
	if len(os.Args) > 1 && os.Args[1] == "mcp-shim" {
		os.Exit(osmcp.RunShim(os.Args[1:]))
	}
	startedAt := time.Now()
	dataDir := envOr("OPENOS_DATA", "data")
	// 功能裁剪（宿主集成：OPENOS_VFS=off / OPENOS_GODER=off 时下架对应内置应用与能力）
	var storeOpts []store.Option
	if envOr("OPENOS_VFS", "") == "off" {
		storeOpts = append(storeOpts, store.WithoutVFS())
	}
	if envOr("OPENOS_GODER", "") == "off" {
		storeOpts = append(storeOpts, store.WithoutGoder())
	}
	if envOr("OPENOS_MARKET", "") == "off" {
		storeOpts = append(storeOpts, store.WithoutMarket())
	}
	// 外部身份模式（身份托管宿主/IdP）：user/guest 退化为宿主机制角色，角色目录不收录
	if os.Getenv("OPENOS_AUTH_MODE") == "external" {
		storeOpts = append(storeOpts, store.ExternalIdentity())
	}
	db, err := store.Open(dataDir, storeOpts...)
	if err != nil {
		log.Fatal("打开数据库失败:", err)
	}

	authSvc := auth.New(db, os.Getenv("OPENOS_JWT_SECRET"))
	authSvc.SetAuthMode(os.Getenv("OPENOS_AUTH_MODE"))
	authSvc.SetExchangeSecret(os.Getenv("OPENOS_EXCHANGE_SECRET"))
	authSvc.SetExchangeRoleWhitelist(os.Getenv("OPENOS_EXCHANGE_ROLES"))
	// 令牌有效期可配（默认 24h；宿主长会话场景可设 168h 等，前端按响应 tokenTtl
	// 自适应续期间隔）
	if v := os.Getenv("OPENOS_TOKEN_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			authSvc.SetTokenTTL(d)
		} else {
			log.Printf("auth: 警告：OPENOS_TOKEN_TTL=%q 不是合法 duration（如 168h），沿用默认 24h", v)
		}
	}
	// 安全提醒（安全审计 2026-10）：交换密钥 + 空白名单 = 密钥泄露可铸任意角色（含
	// admin）。宿主部署应显式配置 OPENOS_EXCHANGE_ROLES。
	// 注意：/api/host/* 机对机组（用户体系管理）与 declare 同为全信任通道，
	// OPENOS_EXCHANGE_ROLES 仅约束 exchange 的用户令牌铸造面。
	if os.Getenv("OPENOS_EXCHANGE_SECRET") != "" && strings.TrimSpace(os.Getenv("OPENOS_EXCHANGE_ROLES")) == "" {
		log.Printf("auth: 警告：OPENOS_EXCHANGE_SECRET 已配置但未设置 OPENOS_EXCHANGE_ROLES 白名单，交换密钥一旦泄露可铸造任意角色（含 admin）")
	}
	if err := authSvc.SeedDefaults(); err != nil {
		log.Fatal("初始化默认账号失败:", err)
	}
	hub := bus.New()
	// 总线连接周期复核（30s）：令牌吊销（改密/删用户）或禁用、过期后踢下线，客户端
	// 会用本地（可能已续期）的令牌自动重连，令牌确实失效则重连被 401 拒绝。
	// 角色不参与复核比较——授权变化实时生效不掉线（身份与授权分离，2026-10）。
	hub.SetRevalidator(func(user, role, token string) bool {
		// 短票连接没有长期令牌可验：按用户态复核（吊销/禁用仍即时生效）
		if strings.HasPrefix(token, "ticket:") {
			exists, _, _, disabled := authSvc.CurrentUserRole(user)
			return exists && !disabled
		}
		id, err := authSvc.VerifySession(token)
		return err == nil && id.Username == user
	})
	aiSvc := ai.New(db)
	fs := vfs.New(dataDir, db, hub, aiSvc.Ask)
	goderMgr := goder.New(dataDir)
	goderMgr.Start() // OPENOS_GODER=off 或二进制缺失时自动降级（available=false）
	// AI ↔ goder 联动（代理形态，安全审计 2026-10）：系统设置的模型配置以托管供应商
	//（OpenOS）写入各 goder 用户的 settings.json 并设为默认模型；条目指向 openos
	// 自身的 OpenAI 兼容代理端点（/api/ai/v1/*，每用户代理密钥鉴权）——平台 API
	// Key 只留服务端，不再落任何 goder 用户家目录。goder 设置页经反代注入 managed
	// 只读展示。
	selfPort := "8081"
	if _, p, err := net.SplitHostPort(envOr("OPENOS_ADDR", "127.0.0.1:8081")); err == nil && p != "" {
		selfPort = p
	}
	goderMgr.SetAIProxy("http://127.0.0.1:"+selfPort+"/api/ai/v1", authSvc.AIProxyKey)
	goderMgr.SetProviderDB(db)
	goderMgr.SetAIProviderSource(func() (goder.ProviderConfig, bool) {
		base, key, model, ok := aiSvc.Provider()
		return goder.ProviderConfig{BaseURL: base, APIKey: key, Model: model}, ok
	})
	aiSvc.AfterSave = func(base, key, model string) {
		if n := goderMgr.SyncAIProvider(goder.ProviderConfig{BaseURL: base, APIKey: key, Model: model}, true); n > 0 {
			log.Printf("goder: AI 模型配置已同步 %d 个用户", n)
		}
	}
	// 宿主声明的每用户供应商（如 token-hub 套餐派生的 per-user 网关接入）：
	// 替换后刷新声明名缓存并全量重渲染（forceDefault=true：声明 default 的
	// 用户立即切默认模型，宿主是声明的权威源）
	aiSvc.AfterUserProvidersSave = func() {
		goderMgr.RefreshManagedNames()
		base, key, model, _ := aiSvc.Provider()
		if n := goderMgr.SyncAIProvider(goder.ProviderConfig{BaseURL: base, APIKey: key, Model: model}, true); n > 0 {
			log.Printf("goder: 宿主声明的每用户供应商已渲染 %d 个用户", n)
		}
	}
	// 启动自愈：托管供应商与声明条目可能在 goder 设置页被用户改动/删除，
	// 保用户自选的默认模型（平台未配置 AI 也执行——声明条目独立于平台配置）
	{
		base, key, model, _ := aiSvc.Provider()
		if n := goderMgr.SyncAIProvider(goder.ProviderConfig{BaseURL: base, APIKey: key, Model: model}, false); n > 0 {
			log.Printf("goder: 启动时补齐 %d 个用户的托管 AI 供应商", n)
		}
	}
	// 用户身份（角色/权限/显示名/禁用）变化推送：sys:user 定向送达该用户全部
	// 连接，前端 session store 就地回正（不再依赖 401/重登感知）；角色变化另
	// 广播 sys:apps-changed 让所有在线用户刷新应用列表（授权范围可能变了）。
	authSvc.SetIdentityChangedHook(func(user string, roleChanged bool, snap map[string]any) {
		hub.PublishTo(user, "sys:user", snap)
		if roleChanged {
			hub.Publish("sys:apps-changed", map[string]any{"user": user})
		}
	})
	// goder 桌面控制插件（openos MCP）：gh 侧经 wrapper 自动发现接入，
	// 向 AI 暴露 os_* 工具（窗口/应用/桌面/便签/VFS/设置/通知/总线发布）
	osmcpMgr := osmcp.New(osmcp.Deps{
		DB: db, Hub: hub, FS: fs, DataDir: dataDir,
		Version: store.Version, StartedAt: startedAt,
		GoderStatus: goderMgr.Status,
		OnlineUsers: hub.OnlineUsers,
	})
	osmcpMgr.Start()
	hub.Handle("agent:resp", osmcpMgr.OnBridgeResp)
	// 聊天室消息持久化：总线 chat:room 处理器落库（From/TS 由 Hub 强制注入不可伪造，
	// 每条消息到达服务端恰一次=天然去重；发送者本地即时回显不经此处）。payload 与
	// 前端约定 {text≤500, attach?{id,name,mime,size}}，服务端再做防御性截断。
	hub.Handle("chat:room", func(env bus.Envelope, _ func(string, any)) {
		var p struct {
			Text   string `json:"text"`
			Attach *struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				MIME string `json:"mime"`
				Size int64  `json:"size"`
			} `json:"attach"`
		}
		if json.Unmarshal(env.Payload, &p) != nil {
			return
		}
		text := strings.TrimSpace(p.Text)
		if len([]rune(text)) > 500 {
			text = string([]rune(text)[:500])
		}
		var ref *store.AttachmentRef
		if p.Attach != nil && p.Attach.ID != "" && len(p.Attach.ID) <= 64 {
			ref = &store.AttachmentRef{
				ID: p.Attach.ID, Name: truncateRunes(p.Attach.Name, 200),
				MIME: truncateRunes(p.Attach.MIME, 100), Size: p.Attach.Size,
			}
		}
		if text == "" && ref == nil {
			return
		}
		if err := db.InsertChatRoomMessage(store.ChatRoomMessage{
			From: env.From, Text: text, Attach: ref, TS: env.TS,
		}); err != nil {
			log.Printf("chat: 消息落库失败: %v", err)
		}
	})
	// 系统附件存储（聊天附件 / bundle 应用文件分享；目录 <data>/attachments）
	attachStore, err := attach.New(filepath.Join(dataDir, "attachments"), db)
	if err != nil {
		log.Fatalf("附件存储初始化失败: %v", err)
	}
	// 删除用户后的级联磁盘清理：真实终端沙箱 + goder 会话数据 + 附件文件
	//（附件的行级清理由 auth 包 deleteUserCore 完成，磁盘文件在此清理）
	authSvc.SetUserDeletedHook(func(name string) {
		fs.DeleteHome(name)
		goderMgr.DeleteUserData(name)
		attachStore.DeleteByUploader(name)
	})
	// 数据保留期（防无限增长；0 = 永久保留）：启动清理一次，此后每天一次
	// （含 vfs 到期分享链接清理）
	go scheduleRetention(db, attachStore, fs)

	mux := http.NewServeMux()

	// verifyQueryAuth query 鉴权（?ticket= 一次性短票优先，兼容 ?token=）：
	// /ws、附件下载、商店 bundle 取件等无法携带 header 的端点共用。短票只携带
	// 用户名，角色实时查库拼出；长期 JWT 出 URL 属过渡兼容（安全审计 2026-10）。
	verifyQueryAuth := func(r *http.Request) (*auth.Identity, error) {
		if user, ok := authSvc.ConsumeTicketFromRequest(r); ok {
			exists, role, _, disabled := authSvc.CurrentUserRole(user)
			if !exists || disabled {
				return nil, fmt.Errorf("unauthorized")
			}
			return &auth.Identity{Username: user, Role: role}, nil
		}
		return authSvc.VerifySession(r.URL.Query().Get("token"))
	}

	// ---- 公开接口 ----
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		// 公开面收敛为最小探测（安全审计 2026-10：版本/AI 状态/在线人数属侦察信息，
		// 仅登录后可见——菜单栏/小组件等桌面消费方均带凭证请求）
		out := map[string]any{"ok": true, "app": "OpenOS", "uptimeSec": int(time.Since(startedAt).Seconds())}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			if c, err := r.Cookie(auth.TokenCookie); err == nil {
				token = c.Value
			}
		}
		if _, err := authSvc.VerifySession(token); err == nil {
			out["version"] = store.Version
			out["ai"] = aiSvc.Configured()
			out["online"] = hub.OnlineUsers()
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("POST /api/auth/login", authSvc.Login)
	mux.HandleFunc("GET /api/auth/logout", authSvc.Logout)
	// 登出改 POST（GET 保留兼容期，前端与宿主切换完成后移除）——GET 副作用端点
	// 是 CSRF 面（跨站 <img> 即可登出他人），安全审计 2026-10。
	mux.HandleFunc("POST /api/auth/logout", authSvc.Logout)
	// 宿主应用令牌交换（仅 OPENOS_EXCHANGE_SECRET 配置后可用）
	mux.HandleFunc("POST /api/auth/exchange", authSvc.Exchange)
	// 业务后端验证 OpenOS JWT（认证反向互通，与令牌交换共用 X-Exchange-Secret）
	mux.HandleFunc("POST /api/auth/introspect", authSvc.Introspect)
	// OpenAI 兼容代理端点（goder 托管供应商接入；每用户 AI 代理密钥鉴权，
	// 平台 Key 只留服务端——见 ai.ProxyChatCompletions）
	mux.HandleFunc("POST /api/ai/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if _, ok := authSvc.UserForAIProxyKey(key); !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		aiSvc.ProxyChatCompletions(w, r)
	})
	// 宿主定向事件推送（与令牌交换共用 X-Exchange-Secret）：
	// {users: ["用户名", ...], topic: "sys:notify", payload: {...}} → 逐用户
	// hub.PublishTo。典型用途：宿主业务事件（如加入申请待审批）即时送达桌面
	// 通知中心（sys:notify 主题前端默认订阅）。不在线的用户自然无投递，
	// 送达语义由宿主侧的兜底（列表角标/轮询）承担。
	// P4 起支持 perm 收件人（与 users 二选一、并存取并集）：{perm: "computer.view"}
	// → 服务端按角色权限矩阵展开全部持权且启用中的用户（取代宿主侧
	// notifyPermHolders 类 SQL——权限点的权威投影已在 openos，收件人展开就地完成）。
	mux.HandleFunc("POST /api/hub/publish", func(w http.ResponseWriter, r *http.Request) {
		if !authSvc.CheckExchangeSecret(r.Header.Get("X-Exchange-Secret")) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Users   []string       `json:"users"`
			Perm    string         `json:"perm"`
			Topic   string         `json:"topic"`
			Payload map[string]any `json:"payload"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil ||
			req.Topic == "" || (len(req.Users) == 0 && req.Perm == "") {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		users := req.Users
		if req.Perm != "" {
			users = mergeUnique(users, usersHoldingPerm(db, req.Perm))
		}
		n := 0
		for _, u := range users {
			if u != "" {
				hub.PublishTo(u, req.Topic, req.Payload)
				n++
			}
		}
		writeJSON(w, map[string]any{"ok": true, "users": n})
	})

	// 宿主声明式角色/权限投影（P2 2026-10）：宿主权威库的全量角色清单（键/显示名/
	// 权限点/应用授权）+ 权限点目录，幂等同步到 roles/role_perms/role_apps
	//（managed_by='host'）。取代宿主侧 os_role_seed 标记表 / 「access 含 gateway」
	// 这类种子 workaround。与令牌交换/定向推送共用 X-Exchange-Secret。
	// 清单外仍被用户绑定的 host 角色不删，响应 unresolved 报告（宿主先迁用户再
	// 重 declare，幂等收敛）——直接删会让用户挂着不存在的角色悬空。
	mux.HandleFunc("POST /api/host/declare", func(w http.ResponseWriter, r *http.Request) {
		if !authSvc.CheckExchangeSecret(r.Header.Get("X-Exchange-Secret")) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Roles []store.HostRoleEntry `json:"roles"`
			Perms []store.PermDef       `json:"perms"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(req.Roles) == 0 && len(req.Perms) == 0 {
			http.Error(w, "roles 与 perms 至少一项", http.StatusBadRequest)
			return
		}
		for _, e := range req.Roles {
			if e.Key == "" || !auth.ValidRoleKey(e.Key) {
				http.Error(w, "非法角色键: "+e.Key, http.StatusBadRequest)
				return
			}
		}
		unresolved, err := db.DeclareHostRoles(req.Roles, req.Perms)
		if err != nil {
			http.Error(w, "声明失败: "+err.Error(), 500)
			return
		}
		db.Audit("host", "host.declare", fmt.Sprintf("%d 角色 / %d 权限点", len(req.Roles), len(req.Perms)), "")
		authSvc.InvalidateAllCache() // 角色矩阵变化：整体清用户态缓存，权限即时生效
		hub.Publish("sys:apps-changed", map[string]any{"source": "host-declare"})
		if unresolved == nil {
			unresolved = []store.StaleRole{}
		}
		writeJSON(w, map[string]any{"ok": true, "roles": len(req.Roles), "unresolved": unresolved})
	})

	// ---- 宿主机对机 API 组（/api/host/*，X-Exchange-Secret 守护）——用户体系
	// 全量管理 + 应用管理 + AI 每用户供应商。取代旧「exchange 铸 admin 服务账号
	// 令牌再调管理 REST」的间接路径：不再依赖 OPENOS_EXCHANGE_ROLES 放行 admin、
	// 不再产生假 admin 用户行。hostsdk 的 Manager 全部封装已切到本组。 ----
	// hostOnly 密钥守护包装（与 declare 同款 401 语义）。
	hostOnly := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !authSvc.CheckExchangeSecret(r.Header.Get("X-Exchange-Secret")) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			fn(w, r)
		}
	}
	// 用户体系（handler 在 auth/hostapi.go，内部自带密钥守护与审计 operator="host"）
	mux.HandleFunc("GET /api/host/users", authSvc.HostListUsers)
	mux.HandleFunc("POST /api/host/users", authSvc.HostCreateUser)
	mux.HandleFunc("POST /api/host/users/sync", authSvc.HostSyncUsers)
	mux.HandleFunc("PUT /api/host/users/{name}", authSvc.HostUpdateUser)
	mux.HandleFunc("DELETE /api/host/users/{name}", authSvc.HostDeleteUser)
	// 角色与权限点目录（只读对账；投影写入走 POST /api/host/declare）
	mux.HandleFunc("GET /api/host/roles", authSvc.HostRoles)
	// 应用注册（宿主通道恒信任 locked——密钥本身即宿主身份）与强制删除
	mux.HandleFunc("POST /api/host/apps", hostOnly(func(w http.ResponseWriter, r *http.Request) {
		var a store.AppDef
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&a); err != nil || a.ID == "" || a.Name == "" {
			http.Error(w, "参数错误：需要 id 与 name", http.StatusBadRequest)
			return
		}
		a.ID = sanitizeID(a.ID)
		a.Builtin = false
		if err := db.UpsertApp(a); err != nil {
			http.Error(w, "注册失败", 500)
			return
		}
		db.Audit("host", "app.register", a.ID, "")
		hub.Publish("sys:apps-changed", map[string]any{"id": a.ID})
		writeJSON(w, map[string]string{"ok": "1"})
	}))
	mux.HandleFunc("DELETE /api/host/apps/{id}", hostOnly(func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("id")
		if err := db.DeleteAppForced(appID); err != nil {
			http.Error(w, "删除失败: "+err.Error(), http.StatusForbidden)
			return
		}
		market.RemoveBundles(appID)
		db.Audit("host", "app.delete", appID, "")
		hub.Publish("sys:apps-changed", nil)
		writeJSON(w, map[string]string{"ok": "1"})
	}))
	// 角色应用授权映射（读取对账 / 按角色全量覆盖）与角色显示名
	mux.HandleFunc("GET /api/host/apps/access", hostOnly(func(w http.ResponseWriter, r *http.Request) {
		m, _ := db.RoleAccessMap()
		roles, _ := db.ListRoles()
		if roles == nil {
			roles = []string{"admin", "user", "guest"}
		}
		writeJSON(w, map[string]any{"roles": roles, "access": m, "labels": db.RoleLabels()})
	}))
	mux.HandleFunc("PUT /api/host/apps/access", hostOnly(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Role string   `json:"role"`
			Apps []string `json:"apps"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req) != nil || req.Role == "" {
			http.Error(w, "参数错误", http.StatusBadRequest)
			return
		}
		if err := db.SetRoleApps(req.Role, req.Apps); err != nil {
			http.Error(w, "保存失败", 500)
			return
		}
		db.Audit("host", "apps.access", "role="+req.Role, "")
		hub.Publish("sys:apps-changed", map[string]any{"role": req.Role})
		writeJSON(w, map[string]string{"ok": "1"})
	}))
	mux.HandleFunc("PUT /api/host/apps/role-labels", hostOnly(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Labels map[string]string `json:"labels"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req) != nil {
			http.Error(w, "参数错误", http.StatusBadRequest)
			return
		}
		if req.Labels == nil {
			req.Labels = map[string]string{}
		}
		if err := db.SetRoleLabels(req.Labels); err != nil {
			http.Error(w, "保存失败", 500)
			return
		}
		db.Audit("host", "apps.role-labels", fmt.Sprintf("%d 项", len(req.Labels)), "")
		writeJSON(w, map[string]string{"ok": "1"})
	}))
	// AI 每用户供应商（handler 与 REST 版共用，见 ai 包）
	mux.HandleFunc("GET /api/host/ai/user-providers", hostOnly(aiSvc.GetUserProviders))
	mux.HandleFunc("PUT /api/host/ai/user-providers", hostOnly(aiSvc.SaveUserProviders))

	// ---- WebSocket 事件总线（query 鉴权：优先 30s 一次性短票 ?ticket=，兼容 ?token=；
	// 升级时全量校验用户态，与 REST 同一套判定） ----
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		if user, ok := authSvc.ConsumeTicketFromRequest(r); ok {
			exists, role, _, disabled := authSvc.CurrentUserRole(user)
			if !exists || disabled {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			hub.ServeWS(w, r, user, role, "ticket:"+user)
			return
		}
		token := r.URL.Query().Get("token")
		id, err := authSvc.VerifySession(token)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		hub.ServeWS(w, r, id.Username, id.Role, token)
	})

	// ---- 需要登录的接口 ----
	authed := http.NewServeMux()
	// 当前在线用户快照（增量事件走总线 sys:presence 主题；聊天等应用启动时取一次基线）
	authed.HandleFunc("GET /api/presence", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"online": hub.OnlineUsers()})
	})
	// 聊天室历史消息分页（登录即可读——总线 chat:room 广播本就全员可订阅，同边界）：
	// ?before=<id> 取更早一页（0=最新一页），?limit= 默认 50 上限 100。
	authed.HandleFunc("GET /api/chat/history", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 100 {
			limit = 50
		}
		before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		msgs, err := db.ChatRoomHistory(before, limit)
		if err != nil {
			http.Error(w, "查询失败", 500)
			return
		}
		writeJSON(w, map[string]any{"messages": msgs})
	})
	// 聊天附件归档到当前用户「文档」目录（幂等）：发送方上传后自动存、接收方点击
	// 时存自己那份，随后前端经 openFile 用对应应用打开（图片→文件应用预览）。
	authed.HandleFunc("POST /api/chat/attach/save", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.ID == "" {
			http.Error(w, "参数错误：需要 id", 400)
			return
		}
		m, ok := attachStore.Get(req.ID)
		if !ok {
			http.Error(w, "附件不存在", http.StatusNotFound)
			return
		}
		data, err := os.ReadFile(attachStore.FilePath(m.ID))
		if err != nil {
			http.Error(w, "附件文件已被清理（保留期 30 天）", http.StatusNotFound)
			return
		}
		p, err := fs.SaveToDocs(auth.FromContext(r).Username, m.Name, data)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]any{"ok": "1", "path": p})
	})

	// ---- 用户反馈（反馈应用）----
	// 类别/状态白名单（前端同词表渲染标签）。
	var feedbackCategories = map[string]string{
		"suggest": "功能建议", "issue": "问题报告", "experience": "体验反馈", "other": "其他",
	}
	var feedbackStatuses = map[string]string{
		"pending": "待处理", "processing": "处理中", "resolved": "已解决", "closed": "已关闭",
	}
	// 提交反馈（登录即可）：广播 sys:feedback 让打开中的反馈应用即时刷新，
	// 并给在线管理员发通知中心提醒（不在线自然无投递，下次打开应用即见）。
	authed.HandleFunc("POST /api/feedback", func(w http.ResponseWriter, r *http.Request) {
		id := auth.FromContext(r)
		var req struct{ Category, Content string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "参数错误", 400)
			return
		}
		req.Content = strings.TrimSpace(req.Content)
		name, ok := feedbackCategories[req.Category]
		if !ok {
			http.Error(w, "类别不合法", 400)
			return
		}
		if l := len([]rune(req.Content)); l < 5 || l > 4000 {
			http.Error(w, "反馈内容需 5~4000 字", 400)
			return
		}
		fid, err := db.InsertFeedback(id.Username, req.Category, req.Content)
		if err != nil {
			http.Error(w, "提交失败，请稍后再试", 500)
			return
		}
		db.Audit(id.Username, "feedback.submit", name, "")
		hub.Publish("sys:feedback", map[string]any{"kind": "new", "id": fid})
		for _, admin := range db.AdminUsernames() {
			if admin == id.Username {
				continue
			}
			hub.PublishTo(admin, "sys:notify", map[string]any{
				"title": "收到新反馈",
				"body":  id.Username + " 提交了「" + name + "」",
				"appId": "feedback",
			})
		}
		writeJSON(w, map[string]any{"ok": "1", "id": fid})
	})
	// 反馈列表：管理员看全部（?mine=1 只看本人），其他角色只看本人。
	authed.HandleFunc("GET /api/feedback", func(w http.ResponseWriter, r *http.Request) {
		id := auth.FromContext(r)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 200 {
			limit = 100
		}
		all := id.Role == "admin" && r.URL.Query().Get("mine") != "1"
		list, err := db.ListFeedback(id.Username, all, limit)
		if err != nil {
			http.Error(w, "查询失败", 500)
			return
		}
		writeJSON(w, list) // 空列表恒为 [] 而非 null（ListFeedback 已保证）
	})
	// 处理反馈（admin）：改状态 / 回复；回复或状态有变化时通知提交者。
	authed.HandleFunc("PUT /api/feedback/{id}", auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "参数错误：需要数字 id", 400)
			return
		}
		var req struct{ Status, Reply string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "参数错误", 400)
			return
		}
		if _, ok := feedbackStatuses[req.Status]; !ok {
			http.Error(w, "状态不合法", 400)
			return
		}
		if l := len([]rune(req.Reply)); l > 2000 {
			http.Error(w, "回复过长（上限 2000 字）", 400)
			return
		}
		before, _ := db.FeedbackByID(fid)
		updated, err := db.UpdateFeedback(fid, req.Status, req.Reply)
		if err != nil || !updated {
			http.Error(w, "反馈不存在或更新失败", http.StatusNotFound)
			return
		}
		admin := auth.FromContext(r)
		db.Audit(admin.Username, "feedback.update", strconv.FormatInt(fid, 10), feedbackStatuses[req.Status])
		hub.Publish("sys:feedback", map[string]any{"kind": "update", "id": fid})
		// 提交者收到处理结果通知（管理员自己给自己的反馈处理不重复打扰）
		if before.Username != "" && before.Username != admin.Username &&
			(before.Reply != req.Reply || before.Status != req.Status) {
			body := "状态已更新为「" + feedbackStatuses[req.Status] + "」"
			if strings.TrimSpace(req.Reply) != "" {
				body += "，管理员已回复"
			}
			hub.PublishTo(before.Username, "sys:notify", map[string]any{
				"title": "你的反馈有新进展",
				"body":  body,
				"appId": "feedback",
			})
		}
		writeJSON(w, map[string]any{"ok": "1"})
	})).ServeHTTP)

	// 附件上传（multipart 字段 file，单文件 ≤10MB；Content-Type 边界由浏览器生成，
	// 此处不可预置 json）——MaxBytesReader 硬限请求体（10MB 文件 + multipart 开销）
	authed.HandleFunc("POST /api/attachments", func(w http.ResponseWriter, r *http.Request) {
		id := auth.FromContext(r)
		r.Body = http.MaxBytesReader(w, r.Body, attach.MaxSize+(1<<20))
		m, err := attachStore.Save(id.Username, r)
		if err != nil {
			log.Printf("attach: 上传失败 user=%s: %v", id.Username, err)
			http.Error(w, "上传失败："+err.Error(), 400)
			return
		}
		db.Audit(id.Username, "attach.upload", m.Name, "")
		writeJSON(w, m)
	})
	// 附件下载：<img>/<a> 标签带不了 Authorization header，鉴权走查询参数 token
	//（与 /ws、bundle 取件同例）。图片白名单内联显示，其余（含 SVG/HTML，可携带
	// 脚本）一律 octet-stream 下载——附件服务不可变成 HTML 注入源。
	mux.HandleFunc("GET /api/attachments/{id}", func(w http.ResponseWriter, r *http.Request) {
		if _, err := verifyQueryAuth(r); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		m, ok := attachStore.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		// RFC 5987：中文文件名
		disposition := "attachment; filename=\"" + sanitizeFilenameASCII(m.Name) + "\"; filename*=UTF-8''" + url.PathEscape(m.Name)
		w.Header().Set("Content-Disposition", disposition)
		if attach.Inline(m.MIME) {
			w.Header().Set("Content-Type", m.MIME)
			w.Header().Set("X-Content-Type-Options", "nosniff")
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}
		w.Header().Set("Cache-Control", "private, max-age=3600")
		http.ServeFile(w, r, attachStore.FilePath(m.ID))
	})
	authed.HandleFunc("GET /api/auth/me", authSvc.Me)
	authed.HandleFunc("GET /api/auth/refresh", authSvc.Refresh)
	// 一次性短票签发（登录态）：/ws 与附件/商店取件等 query 鉴权场景替代长期 JWT 入 URL
	authed.HandleFunc("POST /api/auth/ws-ticket", authSvc.WsTicket)
	authed.HandleFunc("PUT /api/users/password", authSvc.ChangePassword)
	authed.HandleFunc("PUT /api/users/display-name", authSvc.SetDisplayName)

	authed.HandleFunc("GET /api/apps", func(w http.ResponseWriter, r *http.Request) {
		id := auth.FromContext(r)
		if id.Role == "admin" && r.URL.Query().Get("all") == "1" {
			apps, _ := db.ListApps()
			writeJSON(w, apps)
			return
		}
		apps, _ := db.ListAppsForRole(id.Role)
		writeJSON(w, apps)
	})
	authed.HandleFunc("POST /api/apps", func(w http.ResponseWriter, r *http.Request) {
		id := auth.FromContext(r)
		if !id.Can("apps.manage") {
			http.Error(w, "需要权限：apps.manage", http.StatusForbidden)
			return
		}
		var a store.AppDef
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil || a.ID == "" || a.Name == "" {
			http.Error(w, "参数错误：需要 id 与 name", 400)
			return
		}
		a.ID = sanitizeID(a.ID)
		a.Builtin = false
		// locked（宿主锁定）只信任携带交换密钥的宿主服务调用：管理员经界面/API
		// 注册的一律普通应用；已锁定的应用仅宿主服务可改，管理员改配置会被拒
		if !authSvc.CheckExchangeSecret(r.Header.Get("X-Exchange-Secret")) {
			if cur, ok := db.AppByID(a.ID); ok && cur.Locked {
				http.Error(w, "宿主内置应用不可编辑", http.StatusForbidden)
				return
			}
			a.Locked = false
		}
		if err := db.UpsertApp(a); err != nil {
			http.Error(w, "注册失败", 500)
			return
		}
		db.Audit(id.Username, "app.register", a.ID, "")
		hub.Publish("sys:apps-changed", map[string]any{"id": a.ID})
		writeJSON(w, map[string]string{"ok": "1"})
	})
	authed.HandleFunc("DELETE /api/apps/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := auth.FromContext(r)
		if !id.Can("apps.manage") {
			http.Error(w, "需要权限：apps.manage", http.StatusForbidden)
			return
		}
		appID := r.PathValue("id")
		// 宿主服务调用（带交换密钥）可删除自己注册的 locked 应用：宿主清退应用
		//（如下线旧版应用清单）是正当管理操作；界面与管理员 API 仍不可删 locked
		del := db.DeleteApp
		if authSvc.CheckExchangeSecret(r.Header.Get("X-Exchange-Secret")) {
			del = db.DeleteAppForced
		}
		if err := del(appID); err != nil {
			http.Error(w, "内置或宿主锁定的应用不可删除", http.StatusForbidden)
			return
		}
		market.RemoveBundles(appID) // 商店 bundle 应用卸载即清理组件文件（非 bundle / 商店关闭时为空操作）
		db.Audit(id.Username, "app.delete", appID, "")
		hub.Publish("sys:apps-changed", nil)
		writeJSON(w, map[string]string{"ok": "1"})
	})
	authed.HandleFunc("GET /api/apps/access", func(w http.ResponseWriter, r *http.Request) {
		if !auth.FromContext(r).Can("apps.manage") {
			http.Error(w, "需要权限：apps.manage", http.StatusForbidden)
			return
		}
		m, _ := db.RoleAccessMap()
		roles, _ := db.ListRoles()
		if roles == nil {
			roles = []string{"admin", "user", "guest"}
		}
		// labels：宿主注入的角色显示名（未注入为空对象，前端回落内置文案）
		writeJSON(w, map[string]any{"roles": roles, "access": m, "labels": db.RoleLabels()})
	})
	// 角色显示名（宿主注入，如把集成系统的角色 key 映射为中文展示名）：全量覆盖
	// 语义，宿主在启动/角色变更时整体推送，与 role_apps 同样由宿主侧维护。
	authed.HandleFunc("PUT /api/apps/role-labels", func(w http.ResponseWriter, r *http.Request) {
		id := auth.FromContext(r)
		if !id.Can("apps.manage") {
			http.Error(w, "需要权限：apps.manage", http.StatusForbidden)
			return
		}
		var req struct {
			Labels map[string]string `json:"labels"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "参数错误", 400)
			return
		}
		if req.Labels == nil {
			req.Labels = map[string]string{}
		}
		if err := db.SetRoleLabels(req.Labels); err != nil {
			http.Error(w, "保存失败", 500)
			return
		}
		db.Audit(id.Username, "apps.role-labels", fmt.Sprintf("%d 项", len(req.Labels)), "")
		writeJSON(w, map[string]string{"ok": "1"})
	})
	authed.HandleFunc("PUT /api/apps/access", func(w http.ResponseWriter, r *http.Request) {
		id := auth.FromContext(r)
		if !id.Can("apps.manage") {
			http.Error(w, "需要权限：apps.manage", http.StatusForbidden)
			return
		}
		var req struct {
			Role string   `json:"role"`
			Apps []string `json:"apps"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Role == "" {
			http.Error(w, "参数错误", 400)
			return
		}
		if err := db.SetRoleApps(req.Role, req.Apps); err != nil {
			http.Error(w, "保存失败", 500)
			return
		}
		db.Audit(id.Username, "apps.access", "role="+req.Role, "")
		hub.Publish("sys:apps-changed", map[string]any{"role": req.Role})
		writeJSON(w, map[string]string{"ok": "1"})
	})

	// ---- 角色目录与功能级权限（P2 2026-10）----
	// 目录对所有登录用户可读（角色/权限的展示性信息；编辑需 system.manage，
	// admin 恒直通——宿主托管角色只读，host 模式权威在宿主自己的数据库）。
	authed.HandleFunc("GET /api/roles", func(w http.ResponseWriter, r *http.Request) {
		roles, err := db.ListRoleDefs()
		if err != nil {
			http.Error(w, "读取角色目录失败", 500)
			return
		}
		perms, err := db.PermsCatalog()
		if err != nil {
			http.Error(w, "读取权限点目录失败", 500)
			return
		}
		if roles == nil {
			roles = []store.RoleDef{}
		}
		if perms == nil {
			perms = []store.PermDef{}
		}
		writeJSON(w, map[string]any{"roles": roles, "perms": perms})
	})
	authed.HandleFunc("PUT /api/roles/{key}", func(w http.ResponseWriter, r *http.Request) {
		id := auth.FromContext(r)
		if !id.Can("system.manage") {
			http.Error(w, "需要权限：system.manage", http.StatusForbidden)
			return
		}
		key := r.PathValue("key")
		var req struct {
			Label string   `json:"label"`
			Perms []string `json:"perms"`
			Apps  []string `json:"apps"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || !auth.ValidRoleKey(key) {
			http.Error(w, "参数错误", 400)
			return
		}
		if err := db.UpsertRole(key, req.Label, req.Perms); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if req.Apps != nil {
			if err := db.SetRoleApps(key, req.Apps); err != nil {
				http.Error(w, "保存应用授权失败", 500)
				return
			}
		}
		authSvc.InvalidateAllCache() // 角色矩阵变化：整体清用户态缓存立即生效
		db.Audit(id.Username, "roles.upsert", key, "")
		hub.Publish("sys:apps-changed", map[string]any{"role": key})
		writeJSON(w, map[string]string{"ok": "1"})
	})
	authed.HandleFunc("DELETE /api/roles/{key}", func(w http.ResponseWriter, r *http.Request) {
		id := auth.FromContext(r)
		if !id.Can("system.manage") {
			http.Error(w, "需要权限：system.manage", http.StatusForbidden)
			return
		}
		key := r.PathValue("key")
		var n int
		db.QueryRow(`SELECT COUNT(*) FROM users WHERE role=?`, key).Scan(&n)
		if n > 0 {
			http.Error(w, fmt.Sprintf("仍有 %d 个用户绑定该角色，先调整他们的角色", n), 400)
			return
		}
		if err := db.DeleteRole(key); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		authSvc.InvalidateAllCache()
		db.Audit(id.Username, "roles.delete", key, "")
		hub.Publish("sys:apps-changed", map[string]any{"role": key})
		writeJSON(w, map[string]string{"ok": "1"})
	})

	// ---- 应用商店（OPENOS_MARKET=off 时路由不注册，商店应用也不入种子）----
	// 目录对所有登录用户可浏览（安装按钮仅管理员）；安装 = 写 apps 表 + 按所选角色授权。
	// 目录源多级：自定义源（管理员经商店设置维护，system_kv 持久化）+ 官方默认源
	// （OPENOS_MARKET_URL，=off 只关默认源、自定义源仍可用）并发拉取合并（自定义源
	// 排前可遮蔽同 id 条目），任一源成功即生效、全部失败沿用现行，初始兜底 = 随
	// 二进制发布的内置清单。更新货架只改远端 JSON 无需发版。
	if envOr("OPENOS_MARKET", "") != "off" {
		marketDefault := envOr("OPENOS_MARKET_URL", "https://download.gorun.fun/openos/market/catalog.json")
		if marketDefault == "off" {
			marketDefault = ""
		}
		// bundle 组件落盘目录（安装时下载、运行期本地提供）
		market.SetBundleDir(filepath.Join(dataDir, "market", "bundles"))
		// 先恢复持久化的自定义源（system_kv market_sources，JSON 数组）再启动拉取循环：
		// StartRemote 的 goroutine 启动即 RefreshNow，顺序颠倒会让首次拉取漏掉自定义源
		if raw, _ := db.GetSystemKV("market_sources"); raw != "" {
			var urls []string
			if json.Unmarshal([]byte(raw), &urls) == nil {
				market.SetCustomSources(urls)
			}
		}
		market.StartRemote(marketDefault)
		// 目录源管理（admin）：GET 查看默认/自定义源；PUT 全量覆盖自定义源并立即拉取
		authed.HandleFunc("GET /api/market/sources", func(w http.ResponseWriter, r *http.Request) {
			if !auth.FromContext(r).Can("market.manage") {
				http.Error(w, "需要权限：market.manage", http.StatusForbidden)
				return
			}
			writeJSON(w, map[string]any{"default": market.DefaultURL(), "custom": market.CustomSources()})
		})
		authed.HandleFunc("PUT /api/market/sources", func(w http.ResponseWriter, r *http.Request) {
			id := auth.FromContext(r)
			if !id.Can("market.manage") {
				http.Error(w, "需要权限：market.manage", http.StatusForbidden)
				return
			}
			var req struct {
				Urls []string `json:"urls"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				http.Error(w, "参数错误", 400)
				return
			}
			clean, err := market.SetCustomSources(req.Urls)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			b, _ := json.Marshal(clean)
			if err := db.SetSystemKV("market_sources", string(b)); err != nil {
				http.Error(w, "保存失败", 500)
				return
			}
			db.Audit(id.Username, "market.sources", fmt.Sprintf("%d 个自定义源", len(clean)), "")
			okN, total := market.RefreshNow() // 保存即拉取，管理端保存后货架即时更新
			writeJSON(w, map[string]any{"ok": "1", "custom": clean, "sourcesOk": okN, "entries": total})
		})
		// 商店「刷新」按钮：立即并发拉取全部远程目录源（与周期循环经 refreshMu 串行）。
		// 仅 admin——虽拉取目标只来自管理员配置，但任意用户可触发会放大对配置源的
		// 请求频率（安全审计 2026-10）。
		authed.HandleFunc("POST /api/market/refresh", func(w http.ResponseWriter, r *http.Request) {
			if id := auth.FromContext(r); id == nil || !id.Can("market.manage") {
				http.Error(w, "需要权限：market.manage", http.StatusForbidden)
				return
			}
			okN, total := market.RefreshNow()
			writeJSON(w, map[string]any{"sourcesOk": okN, "entries": total})
		})
		authed.HandleFunc("GET /api/market/apps", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, market.List(db))
		})
		authed.HandleFunc("POST /api/market/install", func(w http.ResponseWriter, r *http.Request) {
			id := auth.FromContext(r)
			if !id.Can("market.manage") {
				http.Error(w, "需要权限：market.manage", http.StatusForbidden)
				return
			}
			var req struct {
				ID    string   `json:"id"`
				Roles []string `json:"roles"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
				http.Error(w, "参数错误：需要 id", 400)
				return
			}
			e, err := market.Install(db, req.ID, req.Roles)
			if errors.Is(err, market.ErrNotFound) {
				http.Error(w, "目录中不存在该应用", http.StatusNotFound)
				return
			}
			if errors.Is(err, market.ErrProtected) {
				http.Error(w, "应用 ID 与内置或宿主锁定应用冲突", http.StatusForbidden)
				return
			}
			if err != nil {
				http.Error(w, "安装失败", 500)
				return
			}
			db.Audit(id.Username, "market.install", e.ID+"@"+e.Version, "")
			hub.Publish("sys:apps-changed", map[string]any{"id": e.ID})
			writeJSON(w, map[string]string{"ok": "1"})
		})
		// bundle 组件取件：前端 BundleApp 容器用动态 import() 加载——ESM 模块请求
		// 无法携带 Authorization header，鉴权走查询参数 token（与 /ws 同例）。
		// 逐请求校验：登录态 + 应用为商店安装的 bundle 形态 + 当前角色获授权。
		mux.HandleFunc("GET /api/market/bundle/{id}", func(w http.ResponseWriter, r *http.Request) {
			idn, err := verifyQueryAuth(r)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			appID := r.PathValue("id")
			cur, ok := db.AppByID(appID)
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			path, err := market.BundlePath(cur) // 内部校验 source=market 且 kind=bundle、文件存在
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			if !db.RoleCanAccessApp(idn.Role, appID) {
				http.Error(w, "需要管理员授权该应用", http.StatusForbidden)
				return
			}
			// 版本固定在文件名里，内容不可变，可放心让浏览器缓存模块
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "private, max-age=3600")
			http.ServeFile(w, r, path)
		})
	}

	// 用户管理（users.manage：admin 恒直通；2026-10 起权限点实际接管，宿主/管理员
	// 可下放用户管理而不必给 admin。sync 为批量幂等同步；防提权约束在 patchUser：
	// 非 admin 操作者不可动 admin 用户/授 admin 角色）
	authed.HandleFunc("GET /api/users", auth.RequirePerm("users.manage")(http.HandlerFunc(authSvc.ListUsers)).ServeHTTP)
	authed.HandleFunc("POST /api/users", auth.RequirePerm("users.manage")(http.HandlerFunc(authSvc.CreateUser)).ServeHTTP)
	authed.HandleFunc("POST /api/users/sync", auth.RequirePerm("users.manage")(http.HandlerFunc(authSvc.SyncUsers)).ServeHTTP)
	authed.HandleFunc("PUT /api/users/{name}", auth.RequirePerm("users.manage")(http.HandlerFunc(authSvc.UpdateUser)).ServeHTTP)
	authed.HandleFunc("DELETE /api/users/{name}", auth.RequirePerm("users.manage")(http.HandlerFunc(authSvc.DeleteUser)).ServeHTTP)

	// 审计日志 / 系统统计（system.manage）
	authed.HandleFunc("GET /api/audit", auth.RequirePerm("system.manage")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries, err := db.ListAudit(200)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, entries)
	})).ServeHTTP)
	authed.HandleFunc("GET /api/stats", auth.RequirePerm("system.manage")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, db.Stats(startedAt))
	})).ServeHTTP)

	// 虚拟文件系统（OPENOS_VFS=off 时统一 503）
	authed.HandleFunc("GET /api/fs/list", fs.Guard(fs.List))
	authed.HandleFunc("GET /api/fs/read", fs.Guard(fs.Read))
	authed.HandleFunc("GET /api/fs/raw", fs.Guard(fs.Raw))
	authed.HandleFunc("GET /api/fs/usage", fs.Guard(fs.Usage))
	authed.HandleFunc("GET /api/fs/search", fs.Guard(fs.Search))
	authed.HandleFunc("POST /api/fs/upload", fs.Guard(fs.Upload))
	authed.HandleFunc("PUT /api/fs/write", fs.Guard(fs.Write))
	authed.HandleFunc("POST /api/fs/mkdir", fs.Guard(fs.Mkdir))
	authed.HandleFunc("POST /api/fs/touch", fs.Guard(fs.Touch))
	authed.HandleFunc("DELETE /api/fs/remove", fs.Guard(fs.Remove))
	authed.HandleFunc("POST /api/fs/rename", fs.Guard(fs.Rename))
	authed.HandleFunc("POST /api/fs/copy", fs.Guard(fs.Copy))

	// 文件分享链接（跨用户共享）：消费端点在 authed——分享 id 是能力、登录态是身份，
	// 前端经 /#share=<id> 片段拉起接收弹窗（见 web/src/ui/shareDialog.ts）
	authed.HandleFunc("POST /api/fs/shares", fs.Guard(fs.CreateShare))
	authed.HandleFunc("GET /api/fs/shares", fs.Guard(fs.ListShares))
	authed.HandleFunc("DELETE /api/fs/shares/{id}", fs.Guard(fs.RevokeShare))
	authed.HandleFunc("GET /api/share/{id}", fs.Guard(fs.ShareInfo))
	authed.HandleFunc("GET /api/share/{id}/download", fs.Guard(fs.ShareDownload))
	authed.HandleFunc("POST /api/share/{id}/save", fs.Guard(fs.ShareSave))

	// 用户个性化设置
	authed.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		user := auth.FromContext(r).Username
		out := map[string]string{}
		rows, _ := db.Query(`SELECT key,value FROM user_settings WHERE username=?`, user)
		defer rows.Close()
		for rows.Next() {
			var k, v string
			rows.Scan(&k, &v)
			out[k] = v
		}
		writeJSON(w, out)
	})
	authed.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		user := auth.FromContext(r).Username
		var req struct{ Key, Value string }
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Key == "" {
			http.Error(w, "参数错误", 400)
			return
		}
		if len(req.Key) > 128 || len(req.Value) > 1<<20 {
			http.Error(w, "键值超长", 400)
			return
		}
		db.SetUserSetting(user, req.Key, req.Value)
		db.Audit(user, "settings.put", req.Key, "")
		writeJSON(w, map[string]string{"ok": "1"})
	})

	// AI（goder / AI 助手）
	authed.HandleFunc("POST /api/ai/chat", aiSvc.Chat)
	authed.HandleFunc("GET /api/ai/history", aiSvc.History)
	authed.HandleFunc("DELETE /api/ai/history", aiSvc.ClearHistory)
	authed.HandleFunc("GET /api/ai/config", auth.RequirePerm("ai.manage")(http.HandlerFunc(aiSvc.GetConfig)).ServeHTTP)
	authed.HandleFunc("PUT /api/ai/config", auth.RequirePerm("ai.manage")(http.HandlerFunc(aiSvc.SaveConfig)).ServeHTTP)
	authed.HandleFunc("POST /api/ai/test", auth.RequirePerm("ai.manage")(http.HandlerFunc(aiSvc.Test)).ServeHTTP)
	// 宿主声明的每用户供应商（ai.manage；宿主服务身份走 /api/host/ai/user-providers）
	authed.HandleFunc("GET /api/ai/user-providers", auth.RequirePerm("ai.manage")(http.HandlerFunc(aiSvc.GetUserProviders)).ServeHTTP)
	authed.HandleFunc("PUT /api/ai/user-providers", auth.RequirePerm("ai.manage")(http.HandlerFunc(aiSvc.SaveUserProviders)).ServeHTTP)
	// AI 服务状态（所有登录用户可读：设置页展示当前模型与 goder 启用状态，不含密钥）
	authed.HandleFunc("GET /api/ai/status", func(w http.ResponseWriter, r *http.Request) {
		_, _, model, _ := aiSvc.Provider()
		writeJSON(w, map[string]any{
			"configured": aiSvc.Configured(), "model": model, "demo": !aiSvc.Configured(),
			"goder":         goderMgr.Status(),
			"userProviders": db.CountAIUserProviders(),
		})
	})

	// goder 集成状态（前端引导）
	authed.HandleFunc("GET /api/goder/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, goderMgr.Status())
	})

	// 日历：中国法定节假日（含调休安排）查询，内置已发布年份 + 在线同步 + 缓存
	authed.HandleFunc("GET /api/calendar/holidays", calendar.HolidaysHandler)

	// 终端能力（真实 shell 是否开启）
	authed.HandleFunc("GET /api/term/capability", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"realShell": fs.RealShellEnabled()})
	})

	// goder-serve 反代（同源 /goder-api，注入 OpenOS 登录身份；需登录）
	goderIdentity := func(r *http.Request) (string, string, bool) {
		id := auth.FromContext(r)
		if id == nil {
			return "", "", false
		}
		name := id.Username
		if info, err := db.GetUserAuth(id.Username); err == nil && info.DisplayName != "" {
			name = info.DisplayName
		}
		return id.Username, name, true
	}
	goderProxy := goderMgr.ProxyHandler(goderIdentity)
	mux.Handle("/goder-api/", authSvc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 首次到达时为该用户落桌面控制插件 wrapper 并确保托管 AI 供应商就位
		// （均先于 goder-serve 懒启动该用户的 gh，保证在 gh 启动那一刻已生效；
		// 幂等低开销，指纹不变直接跳过）
		if id := auth.FromContext(r); id != nil {
			osmcpMgr.EnsureUser(id.Username)
			goderMgr.EnsureAIUser(id.Username)
		}
		goderProxy.ServeHTTP(w, r)
	})))

	// goder 嵌入库（自包含 ESM）
	mux.HandleFunc("GET /vendor/goder.js", goderMgr.ServeJS)

	// 登录后前端会调用一次 /api/auth/ready，为用户补种 home 目录
	mux.Handle("GET /api/auth/ready", authSvc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.EnsureHome(auth.FromContext(r).Username)
		writeJSON(w, map[string]string{"ok": "1"})
	})))
	mux.Handle("/api/", authSvc.Middleware(authed))

	// ---- 前端静态资源（生产模式） ----
	dist := envOr("OPENOS_DIST", "web/dist")
	if st, err := os.Stat(dist); err == nil && st.IsDir() {
		fileServer := http.FileServer(http.Dir(dist))
		mux.Handle("/", spaHandler(dist, fileServer))
	}

	// ---- 应用代理（网页应用 config.kind=proxy 的服务端反代，见 README「应用代理」）----
	// 同源 iframe 的子资源请求经 openos_token Cookie 自动携带登录态，
	// 包内再按 role_apps 校验该用户对此应用的访问权。
	root := http.Handler(mux)
	if envOr("OPENOS_PROXY", "") != "off" {
		px := webproxy.New(db)
		mux.Handle("/app/{id}/", authSvc.Middleware(http.HandlerFunc(px.Handler)))
		// CatchEscape 挂在整棵路由树外层：代理页面逃逸到本源任意路径（不止 SPA
		// 兜底——目标站常用 /api 等本源真实路由前缀，SSO 跳转 location.href=
		// '/api/auth/sso/login' 会撞 OpenOS 自身路由 404，路径重合甚至误调系统
		// 接口）的请求按 Referer 307 回代理路径。仍在安全头/日志/限流内层。
		root = webproxy.CatchEscape(db, mux)
	}

	// ---- 中间件链：恢复 → 安全头 → 访问日志 → 限流 ----
	handler := withRecovery(withSecurityHeaders(withLogging(rateLimit(authSvc, root))))

	addr := envOr("OPENOS_ADDR", "127.0.0.1:8081")
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		log.Printf("OpenOS v%s 启动于 http://%s （AI: %v）", store.Version, addr, aiSvc.Configured())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	// 优雅停机
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("正在停机（最长等待 10s）…")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hub.Close()
	goderMgr.Stop()
	osmcpMgr.Stop()
	srv.Shutdown(ctx)
	log.Println("已退出")
}

// ---- 中间件 ----

// withRecovery 捕获 handler panic，返回 500 而不是进程退出。
func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("panic %s %s: %v", r.Method, r.URL.Path, err)
				http.Error(w, `{"error":"服务器内部错误"}`, 500)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withSecurityHeaders 附加基础安全响应头。
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

// withLogging 结构化访问日志（排除健康检查噪音）。
func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" || r.URL.Path == "/favicon.ico" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		log.Printf("%s %s %d %s %s", clientIP(r), r.Method, rw.status, time.Since(start).Round(time.Millisecond), r.URL.Path)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// WebSocket 升级需要底层连接，必须透传 Hijacker / Flusher。
func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not implement http.Hijacker")
	}
	return h.Hijack()
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// rateLimit 全局 IP 限流：600 次/分钟，突发 1200。覆盖 /api、/ws、/goder-api
// 与 /app（webproxy 反代——登录用户可绕开 /api 桶打代理与子进程，安全审计 2026-09）。
func rateLimit(_ *auth.Service, next http.Handler) http.Handler {
	var mu sync.Mutex
	buckets := map[string]*tokenBucket{}
	get := func(ip string) *tokenBucket {
		mu.Lock()
		defer mu.Unlock()
		b, ok := buckets[ip]
		if !ok {
			if len(buckets) > 10000 { // 防内存膨胀
				buckets = map[string]*tokenBucket{}
			}
			b = &tokenBucket{tokens: 1200, last: time.Now()}
			buckets[ip] = b
		}
		return b
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api") || p == "/ws" || strings.HasPrefix(p, "/goder-api") || strings.HasPrefix(p, "/app/") {
			if !get(clientIP(r)).allow(600.0/60.0, 1200) {
				http.Error(w, `{"error":"请求过于频繁"}`, 429)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func (b *tokenBucket) allow(rate float64, burst float64) bool {
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * rate
	if b.tokens > burst {
		b.tokens = burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// scheduleRetention 数据保留期清理：审计日志与 AI 对话历史（含终端伪用户 __term__
// 的孤儿数据）按天数删除。OPENOS_AUDIT_RETENTION_DAYS / OPENOS_CHAT_RETENTION_DAYS
// 控制（默认 90 天，0 = 永久保留）。启动即清理一次，此后每天一次。
func scheduleRetention(db *store.DB, attachStore *attach.Store, fs *vfs.FS) {
	run := func() {
		if d := envDays("OPENOS_AUDIT_RETENTION_DAYS", 90); d > 0 {
			if n, err := db.PruneAuditBefore(time.Now().AddDate(0, 0, -d)); err == nil && n > 0 {
				log.Printf("保留期清理：audit_log 删除 %d 行（%d 天前）", n, d)
			}
		}
		if d := envDays("OPENOS_CHAT_RETENTION_DAYS", 90); d > 0 {
			if n, err := db.PruneChatBefore(time.Now().AddDate(0, 0, -d)); err == nil && n > 0 {
				log.Printf("保留期清理：chat_messages 删除 %d 行（%d 天前）", n, d)
			}
			if n, err := db.PruneChatRoomBefore(time.Now().AddDate(0, 0, -d)); err == nil && n > 0 {
				log.Printf("保留期清理：chat_room_messages 删除 %d 行（%d 天前）", n, d)
			}
		}
		if d := envDays("OPENOS_ATTACH_RETENTION_DAYS", 30); d > 0 {
			if n := attachStore.Prune(time.Now().AddDate(0, 0, -d)); n > 0 {
				log.Printf("保留期清理：attachments 删除 %d 个（%d 天前）", n, d)
			}
		}
		fs.CleanupExpiredShares()
	}
	run()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for range t.C {
		run()
	}
}

// sanitizeFilenameASCII Content-Disposition 的 filename= 回退段只留 ASCII 安全字符
// （中文与特殊字符走 filename*=UTF-8” 段；双引号/分号等会破坏头结构）。
func sanitizeFilenameASCII(name string) string {
	var b strings.Builder
	for _, c := range name {
		if c >= 0x20 && c < 0x7f && c != '"' && c != '\\' && c != ';' {
			b.WriteRune(c)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "file"
	}
	return out
}

// truncateRunes 按字符数截断（防御性限长，落库字段用）。
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func envDays(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// spaHandler 前端路由回退到 index.html；同时落静态资源缓存策略：
// /assets/* 是 vite 内容寻址产物（文件名带 hash）→ immutable 缓存一年（回访零请求）；
// 其余（index.html/sw.js/manifest/图标等无 hash 文件）→ no-cache，每次带条件请求
// 再验证——否则浏览器启发式缓存持有旧 index.html，发版删除旧 chunk 后老页面
// 懒加载应用全部 404（v0.0.43 部署时实测踩中）。
func spaHandler(dist string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dist, filepath.Clean(r.URL.Path))
		if _, err := os.Stat(p); err != nil && !strings.Contains(r.URL.Path, ".") {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(dist, "index.html"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func sanitizeID(s string) string {
	// 保留点号：前端按域名自动生成的应用 ID（如 web-www.baidu.com）与服务端清洗规则
	// 必须一致，否则图标等以落库前 ID 拼出的派生 URL 会指向不存在的应用
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

// usersHoldingPerm 全部持某权限点且启用中的用户（perm 收件人展开）：
// admin 直通 + 角色权限矩阵（role_perms 精确/* 通配）+ 用户级权限快照
// （users.perms JSON 数组，宿主接管模式）三路合并，与 Identity.Can 同语义。
func usersHoldingPerm(db *store.DB, perm string) []string {
	rows, err := db.Query(`
		SELECT DISTINCT u.username FROM users u
		LEFT JOIN role_perms rp ON rp.role = u.role
		WHERE u.disabled = 0 AND (
			u.role = 'admin'
			OR rp.perm = ? OR rp.perm = '*'
			OR EXISTS (SELECT 1 FROM json_each(CASE WHEN u.perms != '' THEN u.perms ELSE '[]' END) je
				WHERE je.value IN (?, '*'))
		)`, perm, perm)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err == nil && u != "" {
			out = append(out, u)
		}
	}
	return out
}

// mergeUnique 两个用户列表去重合并（保序）。
func mergeUnique(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, u := range list {
			if u != "" && !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	return out
}
