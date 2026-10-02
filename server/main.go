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
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"openos/server/internal/ai"
	"openos/server/internal/auth"
	"openos/server/internal/bus"
	"openos/server/internal/calendar"
	"openos/server/internal/goder"
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
	db, err := store.Open(dataDir, storeOpts...)
	if err != nil {
		log.Fatal("打开数据库失败:", err)
	}

	authSvc := auth.New(db, os.Getenv("OPENOS_JWT_SECRET"))
	authSvc.SetAuthMode(os.Getenv("OPENOS_AUTH_MODE"))
	authSvc.SetExchangeSecret(os.Getenv("OPENOS_EXCHANGE_SECRET"))
	authSvc.SetExchangeRoleWhitelist(os.Getenv("OPENOS_EXCHANGE_ROLES"))
	if err := authSvc.SeedDefaults(); err != nil {
		log.Fatal("初始化默认账号失败:", err)
	}
	hub := bus.New()
	// 总线连接周期复核（30s）：令牌吊销（改角色/改密/删用户/禁用）或过期后踢下线，
	// 客户端会用本地（可能已续期）的令牌自动重连，令牌确实失效则重连被 401 拒绝。
	hub.SetRevalidator(func(user, role, token string) bool {
		id, err := authSvc.VerifySession(token)
		return err == nil && id.Username == user && id.Role == role
	})
	aiSvc := ai.New(db)
	fs := vfs.New(dataDir, db, hub, aiSvc.Ask)
	goderMgr := goder.New(dataDir)
	goderMgr.Start() // OPENOS_GODER=off 或二进制缺失时自动降级（available=false）
	// AI ↔ goder 联动：系统设置的模型配置以托管供应商（OpenOS）写入各 goder
	// 用户的 settings.json 并设为默认模型；goder 设置页经反代注入 managed 只读展示
	goderMgr.SetAIProviderSource(func() (goder.ProviderConfig, bool) {
		base, key, model, ok := aiSvc.Provider()
		return goder.ProviderConfig{BaseURL: base, APIKey: key, Model: model}, ok
	})
	aiSvc.AfterSave = func(base, key, model string) {
		if n := goderMgr.SyncAIProvider(goder.ProviderConfig{BaseURL: base, APIKey: key, Model: model}, true); n > 0 {
			log.Printf("goder: AI 模型配置已同步 %d 个用户", n)
		}
	}
	// 启动自愈：托管供应商可能在 goder 设置页被用户改动/删除，保用户自选的默认模型
	if base, key, model, ok := aiSvc.Provider(); ok {
		if n := goderMgr.SyncAIProvider(goder.ProviderConfig{BaseURL: base, APIKey: key, Model: model}, false); n > 0 {
			log.Printf("goder: 启动时补齐 %d 个用户的托管 AI 供应商", n)
		}
	}
	// 删除用户后的级联磁盘清理：真实终端沙箱 + goder 会话数据
	authSvc.SetUserDeletedHook(func(name string) {
		fs.DeleteHome(name)
		goderMgr.DeleteUserData(name)
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
	// 数据保留期（防无限增长；0 = 永久保留）：启动清理一次，此后每天一次
	go scheduleRetention(db)

	mux := http.NewServeMux()

	// ---- 公开接口 ----
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"ok": true, "app": "OpenOS", "version": store.Version,
			"ai": aiSvc.Configured(), "online": hub.OnlineUsers(),
			"uptimeSec": int(time.Since(startedAt).Seconds()),
		})
	})
	mux.HandleFunc("POST /api/auth/login", authSvc.Login)
	mux.HandleFunc("GET /api/auth/logout", authSvc.Logout)
	// 宿主应用令牌交换（仅 OPENOS_EXCHANGE_SECRET 配置后可用）
	mux.HandleFunc("POST /api/auth/exchange", authSvc.Exchange)
	// 宿主定向事件推送（与令牌交换共用 X-Exchange-Secret）：
	// {users: ["用户名", ...], topic: "sys:notify", payload: {...}} → 逐用户
	// hub.PublishTo。典型用途：宿主业务事件（如加入申请待审批）即时送达桌面
	// 通知中心（sys:notify 主题前端默认订阅）。不在线的用户自然无投递，
	// 送达语义由宿主侧的兜底（列表角标/轮询）承担。
	mux.HandleFunc("POST /api/hub/publish", func(w http.ResponseWriter, r *http.Request) {
		if !authSvc.CheckExchangeSecret(r.Header.Get("X-Exchange-Secret")) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Users   []string       `json:"users"`
			Topic   string         `json:"topic"`
			Payload map[string]any `json:"payload"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil ||
			req.Topic == "" || len(req.Users) == 0 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		for _, u := range req.Users {
			if u != "" {
				hub.PublishTo(u, req.Topic, req.Payload)
			}
		}
		writeJSON(w, map[string]any{"ok": true, "users": len(req.Users)})
	})

	// ---- WebSocket 事件总线（token 走查询参数；升级时全量校验用户态，与 REST 同一套判定） ----
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
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
	authed.HandleFunc("GET /api/auth/me", authSvc.Me)
	authed.HandleFunc("GET /api/auth/refresh", authSvc.Refresh)
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
		if id.Role != "admin" {
			http.Error(w, "需要管理员权限", http.StatusForbidden)
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
		if id.Role != "admin" {
			http.Error(w, "需要管理员权限", http.StatusForbidden)
			return
		}
		appID := r.PathValue("id")
		if err := db.DeleteApp(appID); err != nil {
			http.Error(w, "内置或宿主锁定的应用不可删除", http.StatusForbidden)
			return
		}
		db.Audit(id.Username, "app.delete", appID, "")
		hub.Publish("sys:apps-changed", nil)
		writeJSON(w, map[string]string{"ok": "1"})
	})
	authed.HandleFunc("GET /api/apps/access", func(w http.ResponseWriter, r *http.Request) {
		if auth.FromContext(r).Role != "admin" {
			http.Error(w, "需要管理员权限", http.StatusForbidden)
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
		if id.Role != "admin" {
			http.Error(w, "需要管理员权限", http.StatusForbidden)
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
		if id.Role != "admin" {
			http.Error(w, "需要管理员权限", http.StatusForbidden)
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

	// 用户管理（admin；sync 为宿主批量幂等同步）
	authed.HandleFunc("GET /api/users", auth.RequireAdmin(http.HandlerFunc(authSvc.ListUsers)).ServeHTTP)
	authed.HandleFunc("POST /api/users", auth.RequireAdmin(http.HandlerFunc(authSvc.CreateUser)).ServeHTTP)
	authed.HandleFunc("POST /api/users/sync", auth.RequireAdmin(http.HandlerFunc(authSvc.SyncUsers)).ServeHTTP)
	authed.HandleFunc("PUT /api/users/{name}", auth.RequireAdmin(http.HandlerFunc(authSvc.UpdateUser)).ServeHTTP)
	authed.HandleFunc("DELETE /api/users/{name}", auth.RequireAdmin(http.HandlerFunc(authSvc.DeleteUser)).ServeHTTP)

	// 审计日志 / 系统统计（admin）
	authed.HandleFunc("GET /api/audit", auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries, err := db.ListAudit(200)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, entries)
	})).ServeHTTP)
	authed.HandleFunc("GET /api/stats", auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		db.SetUserSetting(user, req.Key, req.Value)
		writeJSON(w, map[string]string{"ok": "1"})
	})

	// AI（goder / AI 助手）
	authed.HandleFunc("POST /api/ai/chat", aiSvc.Chat)
	authed.HandleFunc("GET /api/ai/history", aiSvc.History)
	authed.HandleFunc("DELETE /api/ai/history", aiSvc.ClearHistory)
	authed.HandleFunc("GET /api/ai/config", auth.RequireAdmin(http.HandlerFunc(aiSvc.GetConfig)).ServeHTTP)
	authed.HandleFunc("PUT /api/ai/config", auth.RequireAdmin(http.HandlerFunc(aiSvc.SaveConfig)).ServeHTTP)
	authed.HandleFunc("POST /api/ai/test", auth.RequireAdmin(http.HandlerFunc(aiSvc.Test)).ServeHTTP)
	// AI 服务状态（所有登录用户可读：设置页展示当前模型与 goder 启用状态，不含密钥）
	authed.HandleFunc("GET /api/ai/status", func(w http.ResponseWriter, r *http.Request) {
		_, _, model, _ := aiSvc.Provider()
		writeJSON(w, map[string]any{
			"configured": aiSvc.Configured(), "model": model, "demo": !aiSvc.Configured(),
			"goder": goderMgr.Status(),
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

// rateLimit 全局 IP 限流：600 次/分钟，突发 1200（仅 /api 与 /ws）。
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
		if strings.HasPrefix(r.URL.Path, "/api") || r.URL.Path == "/ws" {
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
func scheduleRetention(db *store.DB) {
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
		}
	}
	run()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for range t.C {
		run()
	}
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

// spaHandler 前端路由回退到 index.html。
func spaHandler(dist string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dist, filepath.Clean(r.URL.Path))
		if _, err := os.Stat(p); err != nil && !strings.Contains(r.URL.Path, ".") {
			http.ServeFile(w, r, filepath.Join(dist, "index.html"))
			return
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
