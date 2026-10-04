// 事件总线客户端：本地即时分发 + WebSocket 跨端广播，统一 API。
//
// 用法：
//   bus.on('app:calc', (payload, from) => {...})   订阅（支持前缀通配 "app:*"）
//   bus.emit('app:calc', { a: 1 })                 发布（本地 + 远程）
//   bus.emit('app:calc', { a: 1 }, { remote: false }) 仅本地
//   bus.remote('term:alice', 'ls')                 仅发给服务端处理器（不回声本地）
//
// 断线处理：先探测会话活性再重连——令牌被吊销（改密/禁用/删号/角色变更）时 REST
// 401，立即走全局登出回登录页并带提示，不再静默退避重试；网络/服务闪断则照常
// 指数退避重连（上限 20 次）。
import { getToken, triggerUnauthorized } from '../api/client'

type Handler = (payload: any, from: string, topic: string) => void

// 默认远程订阅的系统主题；业务/第三方主题在 bus.on 注册时自动补远程订阅
const DEFAULT_REMOTE_SUBS = ['sys:*', 'fs:changed', 'app:*', 'chat:*']

type Envelope = {
  action?: 'sub' | 'unsub' | 'pub'
  topics?: string[]
  topic?: string
  payload?: any
  from?: string
  ts?: number
}

class Bus {
  private handlers = new Map<string, Set<Handler>>()
  private ws: WebSocket | null = null
  private queue: Envelope[] = []
  private retry = 0
  private closed = false
  private me = ''
  /** 已远程订阅的主题集合（重连重放的唯一事实来源） */
  private remoteSubs = new Set<string>()

  /** 连接服务端总线（登录后调用）。 */
  connect(user: string) {
    this.me = user
    this.closed = false
    this.remoteSubs = new Set([...DEFAULT_REMOTE_SUBS, ...this.handlers.keys()])
    this.open()
  }

  private open() {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    const ws = new WebSocket(`${proto}://${location.host}/ws?token=${encodeURIComponent(getToken())}&user=${encodeURIComponent(this.me)}`)
    this.ws = ws
    ws.onopen = () => {
      this.retry = 0
      // 重连后重放订阅与积压消息
      if (this.remoteSubs.size) this.send({ action: 'sub', topics: [...this.remoteSubs] })
      while (this.queue.length) this.send(this.queue.shift()!)
      // 连接（含断线重连）成功即广播：服务端重启窗口期加载的应用列表等状态可借此自愈
      this.dispatchLocal('sys:bus-reconnected', { me: this.me }, 'system')
    }
    ws.onmessage = (ev) => {
      try {
        const env: Envelope = JSON.parse(ev.data)
        if (env.topic) this.dispatchLocal(env.topic, env.payload, env.from || 'system')
      } catch { /* ignore */ }
    }
    ws.onclose = () => {
      this.ws = null
      if (this.closed) return
      // 重连前探测会话：区分「网络/服务闪断」与「令牌已被吊销」。401 时全局登出
      // 接管（triggerUnauthorized 会带 sessionStorage 提示到登录页）；网络不可达
      // 或会话有效则视同闪断，照常退避重连。
      void fetch('/api/auth/me', { headers: { Authorization: 'Bearer ' + getToken() } })
        .then((r) => {
          if (r.status !== 401) return
          this.closed = true // 会话已死，停止重连；重新登录后 connect() 复位
          triggerUnauthorized()
        })
        .catch(() => { /* 网络不可达：视同闪断 */ })
        .finally(() => {
          if (!this.closed && this.retry < 20) {
            this.retry++
            setTimeout(() => this.open(), Math.min(1000 * this.retry, 8000))
          }
        })
    }
  }

  disconnect() {
    this.closed = true
    this.ws?.close()
    this.ws = null
  }

  /** 订阅主题。pattern 支持结尾 "*" 通配。返回取消函数。
   *  注册新主题时会自动向服务端补发远程订阅（否则要等下次重连才能收到推送）。 */
  on(pattern: string, fn: Handler): () => void {
    if (!this.handlers.has(pattern)) this.handlers.set(pattern, new Set())
    this.handlers.get(pattern)!.add(fn)
    this.subscribePattern(pattern)
    return () => {
      const set = this.handlers.get(pattern)
      set?.delete(fn)
      if (set && set.size === 0) {
        this.handlers.delete(pattern)
        // 本地无消费者且非默认系统主题 → 退订远程
        if (!DEFAULT_REMOTE_SUBS.includes(pattern)) {
          this.remoteSubs.delete(pattern)
          if (this.ws?.readyState === WebSocket.OPEN) this.send({ action: 'unsub', topics: [pattern] })
        }
      }
    }
  }

  /** 一次性订阅。 */
  once(topic: string, fn: Handler): () => void {
    const off = this.on(topic, (p, from, t) => { off(); fn(p, from, t) })
    return off
  }

  /** 发布事件：默认本地分发 + 远程广播（服务器不会再回声给本连接）。 */
  emit(topic: string, payload?: any, opts: { remote?: boolean } = {}) {
    const { remote = true } = opts
    this.dispatchLocal(topic, payload, this.me)
    if (remote) {
      const env: Envelope = { action: 'pub', topic, payload }
      if (this.ws?.readyState === WebSocket.OPEN) this.send(env)
      else this.queue.push(env)
    }
  }

  /** 直接发往服务端处理器（如终端命令 topic: term:<user>），不做本地分发。 */
  remote(topic: string, payload?: any) {
    const env: Envelope = { action: 'pub', topic, payload }
    if (this.ws?.readyState === WebSocket.OPEN) this.send(env)
    else this.queue.push(env)
  }

  private send(env: Envelope) {
    this.ws?.send(JSON.stringify(env))
  }

  private subscribePattern(p: string) {
    if (this.remoteSubs.has(p)) return
    this.remoteSubs.add(p)
    // 未连接时无需入队：onopen 会按 remoteSubs 全量重放
    if (this.ws?.readyState === WebSocket.OPEN) this.send({ action: 'sub', topics: [p] })
  }

  private dispatchLocal(topic: string, payload: any, from: string) {
    for (const [pattern, fns] of this.handlers) {
      if (pattern === topic || pattern === '*' ||
        (pattern.endsWith('*') && topic.startsWith(pattern.slice(0, -1)))) {
        for (const fn of [...fns]) {
          try { fn(payload, from, topic) } catch (e) { console.error('bus handler error:', e) }
        }
      }
    }
  }
}

export const bus = new Bus()
