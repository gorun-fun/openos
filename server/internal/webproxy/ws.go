// WebSocket 反代：把代理路径下的 WS 请求桥接到目标站的 ws/wss 端点，
// 双向泵转数据帧（控制帧由 gorilla 各自应答）。
package webproxy

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

func (p *Proxy) serveWS(w http.ResponseWriter, r *http.Request, tu *url.URL, cfg *proxyConfig) {
	cfgHost := cfgURLHost(cfg.URL)
	tu.RawQuery = stripPxMark(tu.RawQuery) // 还原标记不外带
	dialURL := &url.URL{Scheme: tu.Scheme, Host: tu.Host, Path: tu.Path, RawPath: tu.RawPath, RawQuery: tu.RawQuery}
	dialer := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		// SSRF 防线与 HTTP 转发同一条（checkDial），配置目标主机放行
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if err := checkDial(cfgHost, addr); err != nil {
				return nil, err
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	// 握手同样带上管理员配置的自定义请求头（内网 WS 网关常按头鉴权）
	upconn, _, err := dialer.Dial(dialURL.String(), cfg.dialHeader())
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
	done := make(chan struct{}, 1)
	go func() { wsPump(upconn, conn); done <- struct{}{} }()
	wsPump(conn, upconn)
	<-done
}

// wsPump 单向泵消息（控制帧由 gorilla 各自应答，跨连接透传数据帧即可）。
func wsPump(dst, src *websocket.Conn) {
	for {
		t, data, err := src.ReadMessage()
		if err != nil {
			return
		}
		if err := dst.WriteMessage(t, data); err != nil {
			return
		}
	}
}
