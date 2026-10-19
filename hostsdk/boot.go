// boot.go — 宿主启动/停机样板（自 grf-hotel/grf-os main.go 同构段提炼）。
//
// openos 子进程启动 → 应用种子（失败仅日志，下次启动重试）→ 路由装配 →
// AISyncer 套餐同步与宿主后台任务 → HTTP 服务 → 信号/ctx 触发优雅停机（先停
// 后台任务与 HTTP——给在途请求 10s 排空、超时后按 Close 语义掐断长请求，再
// 终止 openos 子进程）。宿主 main 收敛为：装配 App 后一行 Boot。
package hostsdk

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// BootManager Boot 所需的子进程托管面（*Manager 天然实现；测试注入假件）。
type BootManager interface {
	Start() error
	Stop()
}

// BootConfig 启动编排配置。
type BootConfig struct {
	Name    string      // 宿主名（启动日志标识，如 grf-os / grf-hotel）
	Manager BootManager // openos 子进程托管器（nil = 未托管，仅起 HTTP）
	// Seed 应用/角色种子（hostsdk.Seed 委托；失败仅日志，下次启动重试——
	// 桌面框架缺种子只影响应用面，不阻断宿主业务）。
	Seed func() error
	// Setup 路由注册等启动装配（种子之后、起服务之前调用一次）。
	Setup func()
	// AISync token-hub 套餐同步器（grf-hotel 守卫形态：THBase/THKey 未配齐
	// 静默跳过；nil = 不启动）。
	AISync *AISyncer
	// Workers 宿主附加后台任务（goroutine 承载，ctx 随停机取消，如 steward）。
	Workers []func(ctx context.Context)
	// HTTP 面：Addr 与 Listener 二选一（Listener 优先，测试注入用）。
	Addr     string
	Listener net.Listener
	Handler  http.Handler
	// 超时按宿主量纲自定（0 = 不设）：grf-os 代理大文件上传放宽到 15 分钟
	//（ReadHeaderTimeout 仍收紧防慢速连接；WS 升级 Hijack 后不再生效），
	// grf-hotel 常规 15s/60s。
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
}

// Boot 启动编排并阻塞直至停机完成（宿主 main 直接调用；返回 nil = 正常退出，
// 非 nil = 启动/服务故障，调用方 log.Fatal）。停机触发：ctx 取消、SIGINT/
// SIGTERM 或 HTTP 服务故障；顺序恒为「先 HTTP 后子进程」。
func Boot(ctx context.Context, cfg BootConfig) error {
	if cfg.Manager != nil {
		if err := cfg.Manager.Start(); err != nil {
			return fmt.Errorf("openos 桌面框架启动失败: %w", err)
		}
	}
	if cfg.Seed != nil {
		if err := cfg.Seed(); err != nil {
			log.Printf("[openos] 应用种子失败（将在下次启动重试）: %v", err)
		}
	}
	if cfg.Setup != nil {
		cfg.Setup()
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if aiSyncReady(cfg.AISync) {
		go cfg.AISync.Run(runCtx)
	}
	for _, wk := range cfg.Workers {
		if wk != nil {
			go wk(runCtx)
		}
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           cfg.Handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
	}
	serveErr := make(chan error, 1)
	go func() {
		var err error
		if cfg.Listener != nil {
			err = srv.Serve(cfg.Listener)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	addr := cfg.Addr
	if cfg.Listener != nil {
		addr = cfg.Listener.Addr().String()
	}
	log.Printf("[boot] %s 启动 http://localhost%s", cfg.Name, addr)
	var bootErr error
	select {
	case <-ctx.Done():
	case <-sigCh:
	case err := <-serveErr:
		bootErr = fmt.Errorf("服务退出: %w", err)
	}
	// 优雅停机：先停同步任务与 HTTP（10s 排空；超时后 Close 语义掐断长请求，
	// 不直接丢在途响应），再终止 openos 子进程
	log.Println("正在停机…")
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	if cfg.Manager != nil {
		cfg.Manager.Stop()
	}
	if bootErr == nil {
		log.Println("已退出")
	}
	return bootErr
}

// aiSyncReady AISyncer 配齐判定（grf-hotel 守卫形态：THBase/THKey 均非空才启动，
// 未配齐静默跳过；grf-os 旧形态不设守卫、Run 每轮报错刷日志，已收敛）。
func aiSyncReady(s *AISyncer) bool {
	return s != nil && s.thBase != "" && s.thKey != ""
}
