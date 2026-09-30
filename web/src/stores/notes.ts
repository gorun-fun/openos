// 便签数据仓库：便签应用与桌面便签小组件共用的数据源。
// 权威存储在服务端（user_settings 的 notes 键，跨设备一致），localStorage 作为
// 即时缓存与离线兜底；首次遇到「服务端无数据」时从旧 localStorage 一次性迁移。
// 登录/登出会整页刷新（401 全局登出），因此每次页面加载一次即可。
import { defineStore } from 'pinia'
import { useSession } from './session'
import { api } from '../api/client'

export interface Note {
  id: string
  text: string
  color: number // NOTE_COLORS 下标
  updated: number
}

/** 便签配色（柔和纸感，深浅色模式下文字均可读）。 */
export const NOTE_COLORS = [
  { bg: '#fff3a3', bar: '#f2d84d', text: '#4a3b00' }, // 柠檬黄
  { bg: '#ffd9e0', bar: '#ffadc0', text: '#4a1020' }, // 樱花粉
  { bg: '#d3f5d3', bar: '#a4e3a4', text: '#0e3a0e' }, // 薄荷绿
  { bg: '#d6e7ff', bar: '#a8c8ff', text: '#10264a' }, // 天空蓝
  { bg: '#f0ddff', bar: '#d9b8ff', text: '#30104a' }, // 薰衣草
]

let loadedFor = '' // 已加载的用户名（幂等防重复加载）
let serverReady = false // 服务端数据已就位（就位后的变更才回写服务端）
let saveTimer = 0 // 服务端写回防抖
const KEY = () => `openos_notes_${useSession().user?.username || 'default'}`

function parse(raw: string | null | undefined): Note[] {
  try {
    const arr = JSON.parse(raw || '[]')
    return Array.isArray(arr) ? arr.filter((n) => n && n.id) : []
  } catch { return [] }
}

export const useNotes = defineStore('notes', {
  state: (): { notes: Note[] } => ({ notes: [] }),
  getters: {
    byId: (s) => (id: string) => s.notes.find((n) => n.id === id),
  },
  actions: {
    /** 加载当前用户便签（幂等；未登录时忽略）。服务端为权威源。 */
    load() {
      const user = useSession().user?.username
      if (!user || loadedFor === user) return
      loadedFor = user
      // 先用本地缓存即时上屏，再以服务端数据校正
      this.notes = parse(localStorage.getItem(KEY()))
      void api.get<Record<string, string>>('/api/settings').then((s) => {
        if (s['notes']) {
          this.notes = parse(s['notes'])
        } else if (this.notes.length) {
          // 旧版本数据只在 localStorage：一次性迁移到服务端
          this.save()
        }
        serverReady = true
      }).catch(() => { /* 离线容忍：继续用本地缓存 */ })
    },
    /** 保存：localStorage 即时写（缓存），服务端 400ms 防抖回写（权威）。 */
    save() {
      try { localStorage.setItem(KEY(), JSON.stringify(this.notes)) } catch { /* 存储满时静默 */ }
      if (!serverReady) return
      clearTimeout(saveTimer)
      saveTimer = window.setTimeout(() => {
        api.put('/api/settings', { key: 'notes', value: JSON.stringify(this.notes) }).catch(() => {})
      }, 400)
    },
    create(text = '', color = Math.floor(Math.random() * NOTE_COLORS.length)): Note {
      const n: Note = {
        id: Date.now().toString(36) + Math.random().toString(36).slice(2, 6),
        text, color, updated: Date.now(),
      }
      this.notes.unshift(n)
      this.save()
      return n
    },
    setText(id: string, text: string) {
      const n = this.notes.find((x) => x.id === id)
      if (!n || n.text === text) return
      n.text = text
      n.updated = Date.now()
      this.save()
    },
    setColor(id: string, color: number) {
      const n = this.notes.find((x) => x.id === id)
      if (!n) return
      n.color = ((color % NOTE_COLORS.length) + NOTE_COLORS.length) % NOTE_COLORS.length
      n.updated = Date.now()
      this.save()
    },
    cycleColor(id: string) {
      const n = this.byId(id)
      if (n) this.setColor(id, n.color + 1)
    },
    remove(id: string) {
      this.notes = this.notes.filter((n) => n.id !== id)
      this.save()
    },
  },
})
