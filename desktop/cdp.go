package main

// 极简 CDP（Chrome DevTools Protocol）客户端：只做三件事——维持浏览器级
// WebSocket、按 id 收发命令、把事件推给消费者。协议面收敛在本仓库用到的
// 方法（Target.* / Page.* / Runtime.*），不引入第三方 CDP 库依赖。

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *cdpError) Error() string { return fmt.Sprintf("cdp 错误 %d: %s", e.Code, e.Message) }

type cdpEvent struct {
	Method    string
	Params    json.RawMessage
	SessionID string
}

type cdpReply struct {
	Result json.RawMessage `json:"result"`
	Error  *cdpError       `json:"error"`
}

type cdp struct {
	ws         *websocket.Conn
	wmu        sync.Mutex // WriteJSON 串行化（读写各允许一个 goroutine）
	mu         sync.Mutex
	nextID     int
	pending    map[int]chan *cdpReply
	events     chan cdpEvent
	fetchEvent chan cdpEvent // Fetch 拦截事件专用通道：暂停中的请求必须被继续，绝不能丢
	dead       chan struct{}
	deadOnce   sync.Once
}

func dialCDP(wsURL string) (*cdp, error) {
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, err
	}
	c := &cdp{
		ws:         ws,
		pending:    make(map[int]chan *cdpReply),
		events:     make(chan cdpEvent, 8192),
		fetchEvent: make(chan cdpEvent, 1024),
		dead:       make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// Dead 在连接断开（读循环出错/对端关闭）时关闭
func (c *cdp) Dead() <-chan struct{} { return c.dead }

// Events 事件流；消费者卡住时读循环会丢弃积压事件（桌面客户端经 Dead/进程退出兜底）
func (c *cdp) Events() <-chan cdpEvent { return c.events }

// FetchEvents 关键事件流（Fetch.requestPaused / Fetch.authRequired / Target 附身与销毁）。
// readLoop 对此通道阻塞投递而非丢弃：暂停中的请求不继续页面就挂死；Target 事件丢了
// 会漏注入或漏退出（曾因通用通道溢出变僵尸进程）。消费者 fetchHandler 把 Target 事件
// 阻塞转投通用通道
func (c *cdp) FetchEvents() <-chan cdpEvent { return c.fetchEvent }

// call 发送一条命令并等待应答；sessionId 非空时作用于该子目标会话（flat 模式）
func (c *cdp) call(method string, params any, sessionId string) (json.RawMessage, error) {
	var body json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		body = b
	}
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan *cdpReply, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	envelope := map[string]any{"id": id, "method": method}
	if body != nil {
		envelope["params"] = body
	}
	if sessionId != "" {
		envelope["sessionId"] = sessionId
	}
	c.wmu.Lock()
	err := c.ws.WriteJSON(envelope)
	c.wmu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return nil, r.Error
		}
		return r.Result, nil
	case <-time.After(15 * time.Second):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("cdp: %s 应答超时", method)
	case <-c.dead:
		return nil, errors.New("cdp: 连接已断开")
	}
}

// eventWanted 限定进入事件通道的方法：Target 附身/销毁 + SameSite Cookie 桥所需的
// 两个网络事件 + 主帧导航守卫（requestWillBeenSent 等高频事件一律不转发）
func eventWanted(method string) bool {
	switch method {
	case "Target.attachedToTarget", "Target.targetDestroyed",
		"Runtime.executionContextCreated", "Page.frameNavigated",
		"Network.responseReceived", "Network.responseReceivedExtraInfo":
		return true
	}
	return false
}

func (c *cdp) readLoop() {
	for {
		var msg struct {
			ID        int             `json:"id"`
			Result    json.RawMessage `json:"result"`
			Error     *cdpError       `json:"error"`
			Method    string          `json:"method"`
			Params    json.RawMessage `json:"params"`
			SessionID string          `json:"sessionId"`
		}
		if err := c.ws.ReadJSON(&msg); err != nil {
			c.deadOnce.Do(func() { close(c.dead) })
			return
		}
		if msg.ID != 0 { // 命令应答
			c.mu.Lock()
			ch := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- &cdpReply{Result: msg.Result, Error: msg.Error}
			}
			continue
		}
		if msg.Method != "" { // 事件：只转发关心的方法（网络事件洪峰会淹没通道）
			// 关键事件走专用阻塞通道绝不丢弃：Fetch 暂停的请求不继续页面就挂死；
			// Target 附身/销毁丢了会漏注入或漏退出（曾因溢出变僵尸进程）
			if msg.Method == "Fetch.requestPaused" || msg.Method == "Fetch.authRequired" ||
				msg.Method == "Target.attachedToTarget" || msg.Method == "Target.targetDestroyed" {
				select {
				case c.fetchEvent <- cdpEvent{Method: msg.Method, Params: msg.Params, SessionID: msg.SessionID}:
				case <-c.dead:
					return
				}
				continue
			}
			if eventWanted(msg.Method) {
				select {
				case c.events <- cdpEvent{Method: msg.Method, Params: msg.Params, SessionID: msg.SessionID}:
				default:
				}
			}
		}
	}
}
