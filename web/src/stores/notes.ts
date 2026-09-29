// 便签数据仓库：便签应用与桌面便签小组件共用的数据源，按用户持久化到 localStorage。
// 登录/登出会整页刷新（401 全局登出），因此每次页面加载一次即可。
import { defineStore } from 'pinia'
import { useSession } from './session'

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

let loadedFor = '' // 已加载的用户名（幂等防重复读盘）
const KEY = () => `openos_notes_${useSession().user?.username || 'default'}`

export const useNotes = defineStore('notes', {
  state: (): { notes: Note[] } => ({ notes: [] }),
  getters: {
    byId: (s) => (id: string) => s.notes.find((n) => n.id === id),
  },
  actions: {
    /** 加载当前用户便签（幂等；未登录时忽略）。 */
    load() {
      const user = useSession().user?.username
      if (!user || loadedFor === user) return
      loadedFor = user
      try {
        const raw = JSON.parse(localStorage.getItem(KEY()) || '[]')
        this.notes = Array.isArray(raw) ? raw.filter((n) => n && n.id) : []
      } catch { this.notes = [] }
    },
    save() {
      try { localStorage.setItem(KEY(), JSON.stringify(this.notes)) } catch { /* 存储满时静默 */ }
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
