// 事件总线客户端：本地即时分发 + WebSocket 跨端广播，统一 API。
//
// 用法：
//   bus.on('app:calc', (payload, from) => {...})   订阅（支持前缀通配 "app:*"）
//   bus.emit('app:calc', { a: 1 })                 发布（本地 + 远程）
//   bus.emit('app:calc', { a: 1 }, { remote: false }) 仅本地
//   bus.remote('term:alice', 'ls')                 仅发给服务端处理器（不回声本地）
import { getToken } from '../api/client'

type Handler = (payload: any, from: string, topic: string) => void

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

  /** 连接服务端总线（登录后调用）。 */
  connect(user: string) {
    this.me = user
    this.closed = false
    this.open()
    this.subscribeRemote(['sys:*', 'fs:changed', 'app:*', 'chat:*'])
  }

  private open() {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    const ws = new WebSocket(`${proto}://${location.host}/ws?token=${encodeURIComponent(getToken())}&user=${encodeURIComponent(this.me)}`)
    this.ws = ws
    ws.onopen = () => {
      this.retry = 0
      // 重连后重放订阅与积压消息
      const subs = this.collectPatterns()
      if (subs.length) this.send({ action: 'sub', topics: subs })
      while (this.queue.length) this.send(this.queue.shift()!)
    }
    ws.onmessage = (ev) => {
      try {
        const env: Envelope = JSON.parse(ev.data)
        if (env.topic) this.dispatchLocal(env.topic, env.payload, env.from || 'system')
      } catch { /* ignore */ }
    }
    ws.onclose = () => {
      this.ws = null
      if (!this.closed && this.retry < 20) {
        this.retry++
        setTimeout(() => this.open(), Math.min(1000 * this.retry, 8000))
      }
    }
  }

  disconnect() {
    this.closed = true
    this.ws?.close()
    this.ws = null
  }

  /** 订阅主题。pattern 支持结尾 "*" 通配。返回取消函数。 */
  on(pattern: string, fn: Handler): () => void {
    if (!this.handlers.has(pattern)) this.handlers.set(pattern, new Set())
    this.handlers.get(pattern)!.add(fn)
    return () => { this.handlers.get(pattern)?.delete(fn) }
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

  private collectPatterns(): string[] {
    // 重连后需要向服务端重放全部远程订阅（含默认系统主题）
    const remote = new Set<string>(['sys:*', 'fs:changed', 'app:*', 'chat:*'])
    for (const p of this.handlers.keys()) remote.add(p)
    return [...remote]
  }

  private subscribeRemote(topics: string[]) {
    const env: Envelope = { action: 'sub', topics }
    if (this.ws?.readyState === WebSocket.OPEN) this.send(env)
    else this.queue.push(env)
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
