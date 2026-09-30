// Package bus 实现基于 WebSocket 的事件总线。
//
// 通信协议（JSON，每行一条）：
//
//	客户端 → 服务端: {"action":"sub","topics":["app:*"]}         订阅
//	客户端 → 服务端: {"action":"unsub","topics":["app:*"]}       退订
//	客户端 → 服务端: {"action":"pub","topic":"app:x","payload":{...}} 发布
//	服务端 → 客户端: {"topic":"app:x","payload":{...},"from":"alice","ts":1700000000}
//
// 主题匹配规则：精确匹配；以 "*" 结尾时前缀匹配；"*" 匹配全部。
package bus

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Envelope 总线消息。
type Envelope struct {
	Action  string          `json:"action,omitempty"` // sub / unsub / pub（仅客户端→服务端）
	Topics  []string        `json:"topics,omitempty"`
	Topic   string          `json:"topic"`
	Payload json.RawMessage `json:"payload,omitempty"`
	From    string          `json:"from,omitempty"`
	TS      int64           `json:"ts"`
}

// Handler 服务端主题处理器：返回值将发布到 replyTo 主题。
type Handler func(env Envelope, reply func(topic string, payload any))

// Client 一个 WebSocket 连接。
type Client struct {
	hub   *Hub
	conn  *websocket.Conn
	send  chan []byte
	User  string
	Role  string
	token string // 原始 JWT：供周期复核（吊销 / 过期检测）
	subs  map[string]struct{}
	mu    sync.Mutex
}

// Hub 事件总线中心。
type Hub struct {
	mu       sync.RWMutex
	clients  map[*Client]struct{}
	handlers map[string][]Handler
	upgrader websocket.Upgrader

	// 连接周期复核：返回 false 的连接被踢下线（令牌吊销 / 过期后 30s 内生效）
	revalidate func(user, role, token string) bool
	stopCh     chan struct{}
	stopOnce   sync.Once
}

// New 创建 Hub。
func New() *Hub {
	return &Hub{
		clients:  map[*Client]struct{}{},
		handlers: map[string][]Handler{},
		stopCh:   make(chan struct{}),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
	}
}

// revalidateInterval 周期复核间隔。令牌吊销（改角色/改密/删用户）在此窗口内踢下线。
const revalidateInterval = 30 * time.Second

// SetRevalidator 注册连接周期复核函数（仅允许设置一次，注册后启动后台复核）。
// 函数应做全量校验：签名、有效期、用户存在性、角色与 token_version 是否仍匹配。
func (h *Hub) SetRevalidator(fn func(user, role, token string) bool) {
	h.mu.Lock()
	if h.revalidate != nil {
		h.mu.Unlock()
		return
	}
	h.revalidate = fn
	h.mu.Unlock()
	go h.revalidateLoop()
}

func (h *Hub) revalidateLoop() {
	t := time.NewTicker(revalidateInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			for _, c := range h.invalidClients() {
				// WriteControl 可与其他写并发；先发关闭帧再断开，客户端会带新令牌重连
				_ = c.conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "凭证已失效"),
					time.Now().Add(time.Second))
				c.conn.Close()
			}
		case <-h.stopCh:
			return
		}
	}
}

// invalidClients 返回未通过复核的连接快照（不持锁关闭，避免与读循环死锁）。
func (h *Hub) invalidClients() []*Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	fn := h.revalidate
	if fn == nil {
		return nil
	}
	var bad []*Client
	for c := range h.clients {
		if !fn(c.User, c.Role, c.token) {
			bad = append(bad, c)
		}
	}
	return bad
}

// Handle 注册服务端主题处理器（如终端命令）。
func (h *Hub) Handle(pattern string, fn Handler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handlers[pattern] = append(h.handlers[pattern], fn)
}

// Publish 服务端主动向所有匹配订阅者发布事件。
func (h *Hub) Publish(topic string, payload any) {
	h.dispatch(nil, Envelope{Topic: topic, Payload: mustJSON(payload), From: "system", TS: time.Now().Unix()})
}

// PublishTo 向指定用户发布事件。
func (h *Hub) PublishTo(user, topic string, payload any) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.User == user {
			c.deliver(Envelope{Topic: topic, Payload: mustJSON(payload), From: "system", TS: time.Now().Unix()})
		}
	}
}

func (h *Hub) dispatch(from *Client, env Envelope) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c == from {
			continue // 不回声给发布者，前端本地总线已做即时分发
		}
		if c.matches(env.Topic) {
			c.deliver(env)
		}
	}
}

func (h *Hub) runHandlers(env Envelope, from *Client) {
	h.mu.RLock()
	var fns []Handler
	for pattern, hs := range h.handlers {
		if matchTopic(pattern, env.Topic) {
			fns = append(fns, hs...)
		}
	}
	h.mu.RUnlock()
	if len(fns) == 0 {
		return
	}
	reply := func(topic string, payload any) {
		h.PublishTo(from.User, topic, payload)
	}
	for _, fn := range fns {
		fn(env, reply)
	}
}

func matchTopic(pattern, topic string) bool {
	if pattern == "*" || pattern == topic {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(topic, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

func (c *Client) matches(topic string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for p := range c.subs {
		if matchTopic(p, topic) {
			return true
		}
	}
	return false
}

func (c *Client) deliver(env Envelope) {
	data, err := json.Marshal(env)
	if err != nil {
		return
	}
	select {
	case c.send <- data:
	default: // 发送缓冲满则丢弃，避免拖死
	}
}

// ServeWS 处理 /ws 升级请求。身份（user/role/token）由调用方完成鉴权后显式传入，
// 本包不再从查询参数读取，杜绝连接内的身份伪造。
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request, user, role, token string) {
	if user == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &Client{hub: h, conn: conn, send: make(chan []byte, 256), User: user, Role: role, token: token, subs: map[string]struct{}{}}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	h.Publish("sys:presence", map[string]any{"user": user, "online": true})
	go c.writePump()
	c.readPump()
}

func (c *Client) readPump() {
	defer func() {
		c.hub.mu.Lock()
		delete(c.hub.clients, c)
		c.hub.mu.Unlock()
		c.conn.Close()
		c.hub.Publish("sys:presence", map[string]any{"user": c.User, "online": false})
	}()
	c.conn.SetReadLimit(1 << 20)
	c.conn.SetReadDeadline(time.Now().Add(24 * time.Hour))
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		switch env.Action {
		case "sub":
			c.mu.Lock()
			for _, t := range env.Topics {
				c.subs[t] = struct{}{}
			}
			c.mu.Unlock()
		case "unsub":
			c.mu.Lock()
			for _, t := range env.Topics {
				delete(c.subs, t)
			}
			c.mu.Unlock()
		case "pub":
			env.From = c.User
			env.TS = time.Now().Unix()
			if env.Payload == nil {
				env.Payload = json.RawMessage(`null`)
			}
			c.hub.dispatch(c, env)
			c.hub.runHandlers(env, c)
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case data, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// Close 优雅关闭：停止复核循环并断开全部连接（服务停机时调用）。
func (h *Hub) Close() {
	h.stopOnce.Do(func() { close(h.stopCh) })
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		close(c.send)
		c.conn.Close()
	}
	h.clients = map[*Client]struct{}{}
}

// OnlineUsers 当前在线用户列表。
func (h *Hub) OnlineUsers() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	set := map[string]struct{}{}
	out := []string{}
	for c := range h.clients {
		if _, ok := set[c.User]; !ok {
			set[c.User] = struct{}{}
			out = append(out, c.User)
		}
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		log.Println("bus: marshal payload:", err)
		return json.RawMessage(`null`)
	}
	return b
}
