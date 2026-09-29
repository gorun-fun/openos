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
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
	User string
	Role string
	subs map[string]struct{}
	mu   sync.Mutex
}

// Hub 事件总线中心。
type Hub struct {
	mu       sync.RWMutex
	clients  map[*Client]struct{}
	handlers map[string][]Handler
	upgrader websocket.Upgrader
}

// New 创建 Hub。
func New() *Hub {
	return &Hub{
		clients:  map[*Client]struct{}{},
		handlers: map[string][]Handler{},
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
	}
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

// ServeWS 处理 /ws 升级请求。user/role 由鉴权中间件注入查询参数。
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	user := r.URL.Query().Get("user")
	role := r.URL.Query().Get("role")
	if user == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &Client{hub: h, conn: conn, send: make(chan []byte, 256), User: user, Role: role, subs: map[string]struct{}{}}
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

// Close 优雅关闭：断开全部连接（服务停机时调用）。
func (h *Hub) Close() {
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
