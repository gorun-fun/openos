// 通知中心仓库：仿 macOS —— 应用发通知 → 右上角横幅（toast）+ 通知中心面板
// （菜单栏时钟点开）集中查看。所有应用可经 notify() 或总线主题 sys:notify 发送：
//
//   notify({ title: '下载完成', body: 'report.pdf', appId: 'files' })   // 本机通知
//   notify({ title: '公告', broadcast: true })                          // 广播给所有在线端
//   服务端定向：hub.PublishTo(user, "sys:notify", { title, body, appId })
//
// 历史与偏好（声音/勿扰）持久化到服务端 user_settings 的 notifications 键
// （localStorage 仅缓存，与 notes 同一模式）；通知横幅不入持久化，重启即清。
import { defineStore } from 'pinia'
import { useSession } from './session'
import { api } from '../api/client'
import { bus } from '../bus/bus'

export interface OsNotification {
  id: string
  title: string
  body?: string
  /** 来源应用 id（横幅/面板点击可打开该应用；缺省为系统通知） */
  appId?: string
  ts: number
  read?: boolean
}

export interface NotifyInput {
  title: string
  body?: string
  appId?: string
  /** 广播到总线 sys:notify（其他在线端各自弹出并记入通知中心） */
  broadcast?: boolean
}

const MAX_KEEP = 100        // 通知中心最多保留条数
const MAX_TOASTS = 3        // 同屏横幅上限（超出顶掉最老的）
const SETTINGS_KEY = 'notifications'
const KEY = () => `openos_notifications_${useSession().user?.username || 'default'}`

// 本标签页标识：bus.emit 的本地分发会回到自己（from=me），同用户多标签页 from 也
// 相同，因此用随机 tab id 标记自己发出的广播并忽略之，避免重复弹出。
const TAB_ID = Math.random().toString(36).slice(2, 10)

let loadedFor = ''       // 已加载用户名（幂等）
let serverReady = false  // 服务端数据就位后的变更才回写
let saveTimer = 0

function parse(raw: string | null | undefined): OsNotification[] {
  try {
    const arr = JSON.parse(raw || '[]')
    return Array.isArray(arr) ? arr.filter((n) => n && n.id && n.title) : []
  } catch { return [] }
}

// ---- 提示音：WebAudio 合成短促「叮-咚」（无外部资源；浏览器要求用户交互后
//      AudioContext 才能出声，静默失败即可） ----
let audioCtx: AudioContext | null = null
function playChime() {
  try {
    audioCtx = audioCtx || new AudioContext()
    if (audioCtx.state === 'suspended') void audioCtx.resume()
    const t0 = audioCtx.currentTime
    for (const [i, freq] of [830, 620].entries()) {
      const osc = audioCtx.createOscillator()
      const gain = audioCtx.createGain()
      osc.type = 'sine'
      osc.frequency.value = freq
      gain.gain.setValueAtTime(0.0001, t0 + i * 0.16)
      gain.gain.exponentialRampToValueAtTime(0.12, t0 + i * 0.16 + 0.02)
      gain.gain.exponentialRampToValueAtTime(0.0001, t0 + i * 0.16 + 0.34)
      osc.connect(gain).connect(audioCtx.destination)
      osc.start(t0 + i * 0.16)
      osc.stop(t0 + i * 0.16 + 0.4)
    }
  } catch { /* 静默：无 AudioContext 权限时不出声 */ }
}

interface State {
  list: OsNotification[]
  /** 正在展示的横幅（不持久化） */
  toasts: OsNotification[]
  sound: boolean
  dnd: boolean
  panelOpen: boolean
}

export const useNotifications = defineStore('notifications', {
  state: (): State => ({
    list: [], toasts: [], sound: true, dnd: false, panelOpen: false,
  }),
  getters: {
    unread: (s) => s.list.filter((n) => !n.read).length,
  },
  actions: {
    /** 加载历史与偏好（幂等；未登录时忽略）并订阅总线，返回退订函数。 */
    install(): () => void {
      const user = useSession().user?.username
      if (user && loadedFor !== user) {
        loadedFor = user
        try {
          const cached = JSON.parse(localStorage.getItem(KEY()) || 'null')
          if (cached) {
            this.list = parse(JSON.stringify(cached.list))
            this.sound = cached.sound !== false
            this.dnd = cached.dnd === true
          }
        } catch { /* 本地缓存损坏则忽略 */ }
        void api.get<Record<string, string>>('/api/settings').then((s) => {
          if (s[SETTINGS_KEY]) {
            try {
              const d = JSON.parse(s[SETTINGS_KEY])
              this.list = d.list || []
              this.sound = d.sound !== false
              this.dnd = d.dnd === true
            } catch { /* 损坏数据忽略 */ }
          }
          serverReady = true
        }).catch(() => { /* 离线容忍：继续用本地缓存 */ })
      }
      // 总线：服务端定向（osmcp 等）与其他端的广播
      return bus.on('sys:notify', (p: any) => {
        if (!p?.title || p._src === TAB_ID) return
        this.notify({ title: String(p.title), body: p.body ? String(p.body) : undefined, appId: p.appId ? String(p.appId) : undefined })
      })
    },

    /** 发送一条通知：入列表 + 弹横幅（勿扰时只入列表）+ 可选广播到总线。 */
    notify(input: NotifyInput): OsNotification {
      const n: OsNotification = {
        id: Date.now().toString(36) + Math.random().toString(36).slice(2, 7),
        title: input.title,
        body: input.body,
        appId: input.appId,
        ts: Date.now(),
      }
      this.list.unshift(n)
      if (this.list.length > MAX_KEEP) this.list.length = MAX_KEEP
      if (!this.dnd) {
        this.toasts.push(n)
        if (this.toasts.length > MAX_TOASTS) this.toasts.splice(0, this.toasts.length - MAX_TOASTS)
        if (this.sound) playChime()
      }
      this.save()
      if (input.broadcast) {
        bus.emit('sys:notify', { title: n.title, body: n.body, appId: n.appId, _src: TAB_ID })
      }
      return n
    },

    /** 横幅消失（自动/手动），不影响通知中心列表。 */
    toastDismiss(id: string) {
      const i = this.toasts.findIndex((t) => t.id === id)
      if (i >= 0) this.toasts.splice(i, 1)
    },
    /** 从通知中心删除一条（同时收起对应横幅）。 */
    dismiss(id: string) {
      this.list = this.list.filter((n) => n.id !== id)
      this.toastDismiss(id)
      this.save()
    },
    clear() {
      this.list = []
      this.save()
    },
    openPanel() {
      this.panelOpen = true
      this.markRead()
    },
    closePanel() { this.panelOpen = false },
    togglePanel() { this.panelOpen ? this.closePanel() : this.openPanel() },
    markRead() {
      if (!this.list.some((n) => !n.read)) return
      for (const n of this.list) n.read = true
      this.save()
    },
    setSound(v: boolean) { this.sound = v; this.save() },
    setDnd(v: boolean) { this.dnd = v; this.save() },

    /** 保存：localStorage 即时写（缓存），服务端 400ms 防抖回写（权威）。 */
    save() {
      try {
        localStorage.setItem(KEY(), JSON.stringify({ list: this.list, sound: this.sound, dnd: this.dnd }))
      } catch { /* 存储满时静默 */ }
      if (!serverReady) return
      clearTimeout(saveTimer)
      saveTimer = window.setTimeout(() => {
        api.put('/api/settings', {
          key: SETTINGS_KEY,
          value: JSON.stringify({ list: this.list, sound: this.sound, dnd: this.dnd }),
        }).catch(() => {})
      }, 400)
    },
  },
})

/** 便捷函数：任一模块（含 SDK 第三方脚本）直接调用，无需组合式上下文。 */
export function notify(input: NotifyInput) {
  return useNotifications().notify(input)
}
