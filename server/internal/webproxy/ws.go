// WebSocket 反代：把代理路径下的 WS 请求桥接到目标站的 ws/wss 端点，
// 双向泵转数据帧（控制帧由 gorilla 各自应答）。
package webproxy

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WS 转发的防御参数：
//   - 保活：空闲连接经中间网络设备（NAT/LB）常被静默掐断，且浏览器 WebSocket
//     不会主动发 ping——代理侧周期 ping（浏览器按 RFC 自动回 pong）。WriteControl
//     与其余方法并发安全，不与泵转的数据帧写竞争。
//   - 读超时：在 pong 与任意到达消息上续期，超时即视为死连接退出泵转——
//     服务进程无全局 WriteTimeout，挂死的泵转 goroutine 是隐性泄漏。
//   - 帧上限：防异常/恶意上游单帧把内存撑爆（gorilla 默认无上限）。
const (
	wsPingInterval = 30 * time.Second
	wsReadTimeout  = 150 * time.Second // > 3×ping 间隔，容忍两次 pong 丢失
	wsWriteTimeout = 30 * time.Second  // 单条数据帧的写窗口
	wsCtrlWindow   = 10 * time.Second  // ping 控制帧的写窗口
	wsMaxMessage   = 16 << 20
)

func (p *Proxy) serveWS(w http.ResponseWriter, r *http.Request, tu *url.URL, cfg *proxyConfig) {
	// cfg 可为 nil（/px/ 通用转发形态：无管理员配置头与配置目标放行）
	var cfgHost string
	var dialHeader http.Header
	if cfg != nil {
		cfgHost = cfgURLHost(cfg.URL)
		dialHeader = cfg.dialHeader()
	}
	tu.RawQuery = stripPxMark(tu.RawQuery) // 还原标记不外带
	dialURL := &url.URL{Scheme: tu.Scheme, Host: tu.Host, Path: tu.Path, RawPath: tu.RawPath, RawQuery: tu.RawQuery}
	dialer := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		// SSRF 防线与 HTTP 转发同一条（guardedDial：解析一次按 IP 直连），配置目标主机放行
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return guardedDial(ctx, network, addr, cfgHost)
		},
	}
	// 握手同样带上管理员配置的自定义请求头（内网 WS 网关常按头鉴权）
	upconn, _, err := dialer.Dial(dialURL.String(), dialHeader)
	if err != nil {
		http.Error(w, "上游 WebSocket 连接失败："+err.Error(), http.StatusBadGateway)
		return
	}
	defer upconn.Close()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }} // 登录态已由 Cookie 中间件校验
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// 每条连接恰好一个读 goroutine（各自的 wsPump），SetReadDeadline 与 pong
	// 续期全部发生在该 goroutine 上（gorilla 连接不保证跨 goroutine 的读侧并发）
	for _, c := range []*websocket.Conn{upconn, conn} {
		c.SetReadLimit(wsMaxMessage)
		_ = c.SetReadDeadline(time.Now().Add(wsReadTimeout))
		c.SetPongHandler(func(string) error { return c.SetReadDeadline(time.Now().Add(wsReadTimeout)) })
	}
	stop := make(chan struct{})
	var pinger sync.WaitGroup
	pinger.Add(1)
	go func() {
		defer pinger.Done()
		tick := time.NewTicker(wsPingInterval)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				dl := time.Now().Add(wsCtrlWindow)
				_ = upconn.WriteControl(websocket.PingMessage, nil, dl)
				_ = conn.WriteControl(websocket.PingMessage, nil, dl)
			}
		}
	}()
	done := make(chan struct{}, 1)
	go func() { wsPump(upconn, conn); done <- struct{}{} }()
	wsPump(conn, upconn)
	close(stop)
	<-done
	pinger.Wait()
}

// wsPump 单向泵消息（控制帧由 gorilla 各自应答，跨连接透传数据帧即可）。
func wsPump(dst, src *websocket.Conn) {
	for {
		// 读超时在此续期（而非仅靠 pong）：有业务流量的连接不依赖保活即存活
		_ = src.SetReadDeadline(time.Now().Add(wsReadTimeout))
		t, data, err := src.ReadMessage()
		if err != nil {
			return
		}
		_ = dst.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
		if err := dst.WriteMessage(t, data); err != nil {
			return
		}
	}
}
