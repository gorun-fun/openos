// Dock 仓库：用户自定义的固定应用与顺序，持久化到服务端 user_settings 的 dock 键
// （JSON 数组；localStorage 仅缓存，与 notifications/日历同一模式）。
// pinned === null 表示尚未自定义：Dock 跟随默认列表（宿主 dockOrder 排序的全部
// 可见应用）。用户一旦拖动排序 / 固定 / 移除即固化数组（可为空 = 只剩启动台、
// 临时驻留的运行中应用与废纸篓）；未固定的运行中应用由 Dock 组件追加在末尾。
import { defineStore } from 'pinia'
import { useSession } from './session'
import { useApps } from './apps'
import { shell } from '../shell'
import { api } from '../api/client'

const SETTINGS_KEY = 'dock'
const KEY = () => `openos_dock_${useSession().user?.username || 'default'}`

/** 默认 Dock 列表（未自定义时）：宿主 dockOrder 排序的全部可见应用。 */
function defaultDockIds(): string[] {
  const apps = useApps().visible
  const order = shell.dockOrder
  if (!order.length) return apps.map((a) => a.id)
  const rank = (id: string) => {
    const i = order.indexOf(id)
    return i === -1 ? order.length : i
  }
  return [...apps]
    .sort((a, b) => rank(a.id) - rank(b.id) || a.id.localeCompare(b.id))
    .map((a) => a.id)
}

function parseIds(raw: string | null | undefined): string[] | null {
  if (!raw) return null
  try {
    const arr = JSON.parse(raw)
    return Array.isArray(arr) ? arr.filter((x) => typeof x === 'string') : null
  } catch { return null }
}

interface State {
  /** null = 未自定义（跟随默认列表） */
  pinned: string[] | null
  loaded: boolean
}

export const useDock = defineStore('dock', {
  state: (): State => ({ pinned: null, loaded: false }),
  getters: {
    /** 当前固定顺序（未自定义时即时计算默认列表，应用列表就绪后自动重算）。 */
    effective: (s) => s.pinned ?? defaultDockIds(),
    effectiveSet(): Set<string> { return new Set(this.effective) },
  },
  actions: {
    async load() {
      if (this.loaded) return
      // 本地缓存先行，避免已自定义用户先闪烁默认列表
      const cached = parseIds(localStorage.getItem(KEY()))
      if (cached) this.pinned = cached
      try {
        const s = await api.get<Record<string, string>>('/api/settings')
        // 服务端无记录 = 从未自定义：以服务端为准清回 null（忽略本地缓存残留）
        this.pinned = parseIds(s[SETTINGS_KEY])
      } catch { /* 离线容忍：保留本地缓存 */ }
      this.loaded = true
    },
    save() {
      if (this.pinned == null) return
      try { localStorage.setItem(KEY(), JSON.stringify(this.pinned)) } catch { /* 存储满静默 */ }
      api.put('/api/settings', { key: SETTINGS_KEY, value: JSON.stringify(this.pinned) }).catch(() => {})
    },
    /** 固定到固定列表 index 处（已固定则移动，越界钳制）并持久化。 */
    pinAt(appId: string, index: number) {
      if (!useApps().byId(appId)) return
      const base = this.effective.filter((id) => id !== appId)
      base.splice(Math.max(0, Math.min(index, base.length)), 0, appId)
      this.pinned = base
      this.save()
    },
    /** 取消固定（未固定/临时驻留的运行应用忽略）。 */
    unpin(appId: string) {
      if (!this.effectiveSet.has(appId)) return
      this.pinned = this.effective.filter((id) => id !== appId)
      this.save()
    },
  },
})
