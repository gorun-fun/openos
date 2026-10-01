// bridge.go — 前端桥：窗口/桌面/便签状态在浏览器里，服务端工具经总线
// 请求-应答往返到发起用户的桌面执行。
//
// 协议（goder 应用就开在桌面里，发起 AI 调工具时该用户前端必然在线）：
//
//	下行  hub.PublishTo(user, "sys:agent", {id, action, args})
//	      —— sys:* 在前端 DEFAULT_REMOTE_SUBS 中，PublishTo 定向到该用户的
//	         浏览器；前端 agentBridge.ts 执行后应答
//	上行  bus.remote("agent:resp", {id, ok, data|error}) → hub.Handle
//	      —— agent:resp 不在前端默认订阅集，不会泄露给其他用户的浏览器；
//	         服务端校验 env.From 与请求目标用户一致后才回填
package osmcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"openos/server/internal/bus"
)

// bridgeTimeout 单次前端往返的等待上限（前端断线重连的兜底）。
const bridgeTimeout = 15 * time.Second

type bridgeResult struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

type pending struct {
	user string
	ch   chan bridgeResult
}

type bridge struct {
	hub     *bus.Hub
	timeout time.Duration

	mu      sync.Mutex
	pending map[string]*pending
	seq     atomic.Uint64
}

func newBridge(hub *bus.Hub) *bridge {
	return &bridge{hub: hub, timeout: bridgeTimeout, pending: map[string]*pending{}}
}

// call 向某用户的桌面发起动作并等待应答。返回前端回传的 data 原文。
func (b *bridge) call(user, action string, args map[string]any) (json.RawMessage, error) {
	if b.hub == nil {
		return nil, errors.New("总线未接入")
	}
	id := fmt.Sprintf("os-%d-%d", time.Now().UnixNano(), b.seq.Add(1))
	p := &pending{user: user, ch: make(chan bridgeResult, 1)}
	b.mu.Lock()
	b.pending[id] = p
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()

	if args == nil {
		args = map[string]any{}
	}
	b.hub.PublishTo(user, "sys:agent", map[string]any{"id": id, "action": action, "args": args})

	select {
	case res := <-p.ch:
		if !res.OK {
			if res.Error == "" {
				res.Error = "桌面执行失败"
			}
			return nil, errors.New(res.Error)
		}
		return res.Data, nil
	case <-time.After(b.timeout):
		return nil, errors.New("桌面未应答（浏览器可能离线或正在重连），请稍后重试")
	}
}

// onResp 前端应答（挂 hub.Handle("agent:resp")）。env.From 由总线按已认证
// 连接注入，校验与请求目标用户一致，杜绝替他人应答/伪造应答。
func (b *bridge) onResp(env bus.Envelope) {
	var p struct {
		ID    string          `json:"id"`
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.ID == "" {
		return
	}
	b.mu.Lock()
	pd := b.pending[p.ID]
	b.mu.Unlock()
	if pd == nil || env.From != pd.user {
		return
	}
	select {
	case pd.ch <- bridgeResult{OK: p.OK, Data: p.Data, Error: p.Error}:
	default: // 已有人应答（多标签页）或已超时
	}
}
