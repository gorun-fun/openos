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
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"openos/server/internal/ai"
	"openos/server/internal/auth"
	"openos/server/internal/bus"
	"openos/server/internal/goder"
	"openos/server/internal/store"
	"openos/server/internal/vfs"
)

func main() {
	startedAt := time.Now()
	dataDir := envOr("OPENOS_DATA", "data")
	db, err := store.Open(dataDir)
	if err != nil {
		log.Fatal("打开数据库失败:", err)
	}

	authSvc := auth.New(db, os.Getenv("OPENOS_JWT_SECRET"))
	authSvc.SetExchangeSecret(os.Getenv("OPENOS_EXCHANGE_SECRET"))
	if err := authSvc.SeedDefaults(); err != nil {
		log.Fatal("初始化默认账号失败:", err)
	}
	hub := bus.New()
	aiSvc := ai.New(db)
	fs := vfs.New(dataDir, db, hub, aiSvc.Ask)
	goderMgr := goder.New(dataDir)
	goderMgr.Start() // 二进制不存在时自动降级（available=false）

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

	// ---- WebSocket 事件总线（token 走查询参数） ----
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		claims, err := authSvc.Verify(r.URL.Query().Get("token"))
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		q.Set("user", claims.Sub)
		q.Set("role", claims.Role)
		r.URL.RawQuery = q.Encode()
		hub.ServeWS(w, r)
	})

	// ---- 需要登录的接口 ----
	authed := http.NewServeMux()
	authed.HandleFunc("GET /api/auth/me", authSvc.Me)
	authed.HandleFunc("GET /api/auth/refresh", authSvc.Refresh)
	authed.HandleFunc("PUT /api/users/password", authSvc.ChangePassword)

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
		db.DeleteApp(r.PathValue("id"))
		db.Audit(id.Username, "app.delete", r.PathValue("id"), "")
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
		writeJSON(w, map[string]any{"roles": roles, "access": m})
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

	// 用户管理（admin）
	authed.HandleFunc("GET /api/users", auth.RequireAdmin(http.HandlerFunc(authSvc.ListUsers)).ServeHTTP)
	authed.HandleFunc("POST /api/users", auth.RequireAdmin(http.HandlerFunc(authSvc.CreateUser)).ServeHTTP)
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

	// 虚拟文件系统
	authed.HandleFunc("GET /api/fs/list", fs.List)
	authed.HandleFunc("GET /api/fs/read", fs.Read)
	authed.HandleFunc("GET /api/fs/raw", fs.Raw)
	authed.HandleFunc("GET /api/fs/usage", fs.Usage)
	authed.HandleFunc("POST /api/fs/upload", fs.Upload)
	authed.HandleFunc("PUT /api/fs/write", fs.Write)
	authed.HandleFunc("POST /api/fs/mkdir", fs.Mkdir)
	authed.HandleFunc("POST /api/fs/touch", fs.Touch)
	authed.HandleFunc("DELETE /api/fs/remove", fs.Remove)
	authed.HandleFunc("POST /api/fs/rename", fs.Rename)

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

	// goder 集成状态（前端引导）
	authed.HandleFunc("GET /api/goder/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, goderMgr.Status())
	})

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
	mux.Handle("/goder-api/", authSvc.Middleware(goderMgr.ProxyHandler(goderIdentity)))

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

	// ---- 中间件链：恢复 → 安全头 → 访问日志 → 限流 ----
	handler := withRecovery(withSecurityHeaders(withLogging(rateLimit(authSvc, mux))))

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
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}
