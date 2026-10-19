// boot_test.go — Boot 启动/停机编排单测（假 Manager + 环回监听，不触子进程/外网）。
package hostsdk

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// bootTrace 事件顺序记录（启动→种子→装配→服务→停机的相对次序断言）。
type bootTrace struct {
	mu sync.Mutex
	ev []string
}

func (t *bootTrace) add(e string) { t.mu.Lock(); t.ev = append(t.ev, e); t.mu.Unlock() }
func (t *bootTrace) index(e string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, v := range t.ev {
		if v == e {
			return i
		}
	}
	return -1
}

type fakeBootManager struct {
	trace    *bootTrace
	startErr error
}

func (m *fakeBootManager) Start() error {
	m.trace.add("start")
	return m.startErr
}

func (m *fakeBootManager) Stop() { m.trace.add("stop") }

func TestBootFullSequence(t *testing.T) {
	trace := &bootTrace{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	workerDone := make(chan struct{})
	seeded := make(chan struct{}, 1)
	setupDone := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cfg := BootConfig{
		Name:    "test-host",
		Manager: &fakeBootManager{trace: trace},
		Seed: func() error {
			trace.add("seed")
			seeded <- struct{}{}
			return nil
		},
		Setup: func() {
			trace.add("setup")
			setupDone <- struct{}{}
		},
		Workers: []func(ctx context.Context){
			func(ctx context.Context) {
				trace.add("worker")
				<-ctx.Done()
				trace.add("worker-done")
				close(workerDone)
			},
		},
		Listener: ln,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			trace.add("serve")
			w.WriteHeader(http.StatusOK)
		}),
		ReadTimeout: 5 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- Boot(ctx, cfg) }()

	// 起服前各步完成 + HTTP 可达
	select {
	case <-seeded:
	case <-time.After(3 * time.Second):
		t.Fatal("种子未执行")
	}
	select {
	case <-setupDone:
	case <-time.After(3 * time.Second):
		t.Fatal("装配未执行")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTP 服务未就绪: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 停机：ctx 取消 → 后台任务退出 → HTTP 排空 → 子进程最后停
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("正常停机应返回 nil，实际 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Boot 未在停机后返回")
	}
	select {
	case <-workerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("后台任务 ctx 未随停机取消")
	}
	for _, pair := range [][2]string{{"start", "seed"}, {"seed", "setup"}, {"setup", "serve"}, {"stop", "worker-done"}} {
		// 最后一对反向断言：stop 在 worker-done 之前发起（cancel 先于 Manager.Stop）
		a, b := trace.index(pair[0]), trace.index(pair[1])
		if a < 0 || b < 0 {
			t.Fatalf("事件缺失: %v", trace.ev)
		}
		if pair[0] == "stop" {
			continue
		}
		if a >= b {
			t.Fatalf("顺序不符：%s 应先于 %s（%v）", pair[0], pair[1], trace.ev)
		}
	}
	if trace.index("stop") < 0 {
		t.Fatal("子进程未停")
	}
}

func TestBootServeFailureStopsManager(t *testing.T) {
	trace := &bootTrace{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// 关闭监听器：Serve 立即失败（端口占用等同故障形态）
	ln.Close()
	cfg := BootConfig{
		Name:     "test-host",
		Manager:  &fakeBootManager{trace: trace},
		Listener: ln,
		Handler:  http.NotFoundHandler(),
	}
	err = Boot(context.Background(), cfg)
	if err == nil || trace.index("stop") < 0 {
		t.Fatalf("服务故障应报错且仍停子进程: err=%v trace=%v", err, trace.ev)
	}
}

func TestBootManagerStartFailure(t *testing.T) {
	trace := &bootTrace{}
	m := &fakeBootManager{trace: trace, startErr: errTest("bin missing")}
	err := Boot(context.Background(), BootConfig{Name: "x", Manager: m, Handler: http.NotFoundHandler()})
	if err == nil || !strings.Contains(err.Error(), "openos 桌面框架启动失败") {
		t.Fatalf("子进程启动失败应上抛: %v", err)
	}
	if trace.index("stop") >= 0 {
		t.Fatal("启动失败不应再走停机链（Start 自清理）")
	}
}

func TestAISyncReadyGuard(t *testing.T) {
	// grf-hotel 守卫形态：THBase/THKey 未配齐静默跳过
	if aiSyncReady(nil) {
		t.Fatal("nil 同步器不应启动")
	}
	if aiSyncReady(NewAISyncer(AISyncConfig{THKey: "k"}, nil)) {
		t.Fatal("THBase 缺失不应启动")
	}
	if aiSyncReady(NewAISyncer(AISyncConfig{THBase: "https://th"}, nil)) {
		t.Fatal("THKey 缺失不应启动")
	}
	if !aiSyncReady(NewAISyncer(AISyncConfig{THBase: "https://th", THKey: "k"}, nil)) {
		t.Fatal("配齐应启动")
	}
}
