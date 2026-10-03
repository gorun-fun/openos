// 窗口管理仓库：打开/关闭/焦点/最小化/最大化/吸附/几何/视口跟随。
import { defineStore } from 'pinia'
import { useApps } from './apps'
import { useDesktop } from './desktop'
import { isMobile, mobileViewportH } from '../ui/formfactor'

export interface WinState {
  id: string
  appId: string
  title: string
  x: number
  y: number
  w: number
  h: number
  z: number
  minimized: boolean
  maximized: boolean
  prev?: { x: number; y: number; w: number; h: number }
  props?: any // 打开参数（例如文件路径）
}

let seq = 0
let topZ = 10

export const MENUBAR_H = 28
export const DOCK_H = 84

// 桌面可用区域（菜单栏与 Dock 之间）。
// 开启「最大化自动隐藏 Dock」且存在最大化窗口时不为 Dock 预留高度，
// 窗口占满全高，Dock 以悬浮方式从底部滑出（见 Dock.vue）。
// 移动形态：无菜单栏/Dock 常驻，高度跟 visualViewport——软键盘弹出时
// 全屏窗口收缩到键盘上方，底部胶囊同步让位。
export function viewport() {
  if (isMobile.value) {
    return { x: 0, y: 0, w: window.innerWidth, h: mobileViewportH() }
  }
  let dockH = DOCK_H
  try {
    const desktop = useDesktop()
    const wins = useWindows()
    if (desktop.dockAutoHide && wins.list.some((w) => w.maximized)) dockH = 0
  } catch { /* pinia 未就绪时按默认视口 */ }
  return {
    x: 0,
    y: MENUBAR_H,
    w: window.innerWidth,
    h: window.innerHeight - MENUBAR_H - dockH,
  }
}

export type SnapZone = 'left' | 'right' | null

export const useWindows = defineStore('windows', {
  state: () => ({ list: [] as WinState[], activeId: '' as string }),
  getters: {
    visible: (s) => s.list.filter((w) => !w.minimized),
    runningAppIds: (s) => [...new Set(s.list.map((w) => w.appId))],
    active: (s) => s.list.find((w) => w.id === s.activeId),
  },
  actions: {
    open(appId: string, props?: any, title?: string, size?: { w: number; h: number }) {
      const app = useApps().byId(appId)
      if (!app) return null
      // 同应用同参数的窗口若已存在且被最小化，则聚焦恢复，避免堆积重复窗口
      const dup = this.list.find((w) => w.appId === appId && JSON.stringify(w.props) === JSON.stringify(props))
      if (dup) {
        this.focus(dup.id)
        return dup.id
      }
      const vp = viewport()
      // 移动形态：单窗口全屏（无自由几何/吸附概念），minimized 挂后台、
      // 多任务卡片恢复即 focus（下方 dup 分支天然支持）
      if (isMobile.value) {
        const id = `win-${++seq}`
        this.list.push({
          id, appId, title: title || app.name,
          x: 0, y: 0, w: vp.w, h: vp.h,
          z: ++topZ, minimized: false, maximized: true, props,
        })
        this.activeId = id
        return id
      }
      const w = Math.min(size?.w || app.width || 800, vp.w - 40)
      const h = Math.min(size?.h || app.height || 560, vp.h - 30)
      const n = this.list.length
      const x = Math.max(8, Math.round((vp.w - w) / 2 + ((n % 6) - 2.5) * 28))
      const y = Math.max(vp.y + 8, Math.round((vp.h - h) / 2 + vp.y + ((n % 6) - 2.5) * 22))
      const id = `win-${++seq}`
      this.list.push({
        id, appId,
        title: title || app.name,
        x, y, w, h,
        z: ++topZ,
        minimized: false, maximized: false, props,
      })
      this.activeId = id
      return id
    },
    close(id: string) {
      this.list = this.list.filter((w) => w.id !== id)
      if (this.activeId === id) {
        const last = [...this.list].filter((w) => !w.minimized).sort((a, b) => b.z - a.z)[0]
        this.activeId = last?.id || ''
      }
    },
    closeApp(appId: string) {
      for (const w of this.list.filter((x) => x.appId === appId)) this.close(w.id)
    },
    focus(id: string) {
      const w = this.list.find((v) => v.id === id)
      if (!w) return
      w.z = ++topZ
      w.minimized = false
      this.activeId = id
    },
    minimize(id: string) {
      const w = this.list.find((v) => v.id === id)
      if (w) w.minimized = true
      if (this.activeId === id) {
        const last = [...this.list].filter((v) => !v.minimized && v.id !== id).sort((a, b) => b.z - a.z)[0]
        this.activeId = last?.id || ''
      }
    },
    toggleMaximize(id: string) {
      if (isMobile.value) return // 移动形态恒全屏，无最大化语义
      const w = this.list.find((v) => v.id === id)
      if (!w) return
      const vp = viewport()
      if (w.maximized && w.prev) {
        Object.assign(w, w.prev, { maximized: false, prev: undefined })
      } else {
        w.prev = { x: w.x, y: w.y, w: w.w, h: w.h }
        Object.assign(w, { x: 0, y: vp.y, w: vp.w, h: vp.h, maximized: true })
      }
    },
    /** 半屏吸附（左/右）。 */
    snap(id: string, zone: SnapZone) {
      if (isMobile.value) return
      const w = this.list.find((v) => v.id === id)
      if (!w || !zone) return
      const vp = viewport()
      w.prev = w.prev || { x: w.x, y: w.y, w: w.w, h: w.h }
      w.maximized = false
      Object.assign(w, {
        x: zone === 'left' ? 0 : Math.round(vp.w / 2),
        y: vp.y,
        w: Math.round(vp.w / 2),
        h: vp.h,
      })
    },
    /** 拖动已吸附/最大化的窗口时先还原尺寸。 */
    unsnap(id: string) {
      const w = this.list.find((v) => v.id === id)
      if (w && w.prev && (w.maximized || w.w >= viewport().w - 8)) {
        const px = w.prev.x
        Object.assign(w, w.prev, { maximized: false, prev: undefined })
        w.x = Math.max(0, px)
      } else if (w) {
        w.maximized = false
        w.prev = undefined
      }
    },
    setBounds(id: string, b: Partial<Pick<WinState, 'x' | 'y' | 'w' | 'h'>>) {
      const w = this.list.find((v) => v.id === id)
      if (w) Object.assign(w, b)
    },
    setTitle(id: string, title: string) {
      const w = this.list.find((w) => w.id === id)
      if (w) w.title = title
    },
    /** 浏览器窗口尺寸变化：最大化窗口跟随，其余窗口保持在视口内。 */
    onViewportResize() {
      const vp = viewport()
      for (const w of this.list) {
        if (w.maximized) {
          Object.assign(w, { x: 0, y: vp.y, w: vp.w, h: vp.h })
          continue
        }
        w.w = Math.min(w.w, vp.w)
        w.h = Math.min(w.h, vp.h)
        w.x = Math.min(Math.max(-w.w + 120, w.x), vp.w - 120) // 至少露出 120px 可拖回
        w.y = Math.min(Math.max(MENUBAR_H, w.y), vp.y + vp.h - 40)
      }
    },
    /** 按 z 序循环切换窗口（Alt+`）。 */
    cycle() {
      if (isMobile.value) return
      const vis = [...this.list].filter((w) => !w.minimized).sort((a, b) => b.z - a.z)
      if (vis.length < 2) {
        if (vis[0]) this.focus(vis[0].id)
        return
      }
      this.focus(vis[vis.length - 1].id) // 切换到最底层的窗口
    },
    /** 移动形态：回桌面 = 全部窗口最小化挂后台（多任务卡片中可恢复，任务不中断）。 */
    goHomeAll() {
      for (const w of this.list) w.minimized = true
      this.activeId = ''
    },
  },
})
