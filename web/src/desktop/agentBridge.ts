// agentBridge — goder 桌面控制插件（openos MCP）的前端桥。
//
// 服务端 osmcp 包把 AI 的工具调用经总线下发：
//   下行  hub.PublishTo(user, "sys:agent", { id, action, args })
//         （sys:* 在 DEFAULT_REMOTE_SUBS 中，PublishTo 定向到当前用户）
//   上行  bus.remote("agent:resp", { id, ok, data | error })
//         （agent:resp 不在默认订阅集，不会泄露给其他用户；服务端
//          hub.Handle 校验发布者与请求目标一致后唤醒等待中的工具调用）
//
// 这里以白名单动作注册表承接执行（窗口/桌面/便签状态在本页面里，goder
// 应用就开在桌面中，发起调用时本桥必然在线）。由 Desktop 挂载/卸载。
import { bus } from '../bus/bus'
import { useWindows } from '../stores/windows'
import { useDesktop } from '../stores/desktop'
import { useNotes } from '../stores/notes'
import { toast } from '../ui/toast'
import { openPath } from '../apps/openWith'
import type { Note } from '../stores/notes'

interface AgentReq {
  id?: string
  action?: string
  args?: any
}

type Action = (args: any) => any

const asBool = (v: any): boolean => v === true || v === 'on' || v === 1 || v === '1'

const noteRow = (n: Note) => ({ id: n.id, color: n.color, updated: n.updated, preview: n.text.slice(0, 80) })

// 白名单动作注册表：与 server/internal/osmcp/tools.go 的工具一一对应。
const ACTIONS: Record<string, Action> = {
  /** 桌面实时状态：窗口列表 + 外观 + 便签概要 + 视口。 */
  state() {
    const windows = useWindows()
    const desktop = useDesktop()
    const notes = useNotes()
    return {
      windows: windows.list.map((w) => ({
        id: w.id,
        appId: w.appId,
        title: w.title,
        x: w.x, y: w.y, w: w.w, h: w.h,
        minimized: w.minimized,
        maximized: w.maximized,
        active: w.id === windows.activeId,
      })),
      activeWindowId: windows.activeId,
      dark: desktop.dark,
      wallpaper: desktop.wallpaper,
      wallpaperCount: desktop.wallpapers.length,
      dockAutoHide: desktop.dockAutoHide,
      notes: notes.notes.map(noteRow),
      viewport: { w: window.innerWidth, h: window.innerHeight },
    }
  },

  window_open({ appId, props, title }) {
    if (!appId) throw new Error('缺少 appId')
    const id = useWindows().open(String(appId), props, title ? String(title) : undefined)
    if (!id) throw new Error(`应用不存在或无权打开：${appId}`)
    return { windowId: id }
  },

  window_close({ windowId, appId }) {
    const windows = useWindows()
    if (windowId) { windows.close(String(windowId)); return { ok: true } }
    if (appId) { windows.closeApp(String(appId)); return { ok: true } }
    throw new Error('需要 windowId 或 appId')
  },

  window_do({ action, windowId, zone, bounds, title }) {
    const windows = useWindows()
    const id = String(windowId || windows.activeId || '')
    if (!windows.list.some((w) => w.id === id)) throw new Error(`窗口不存在：${id || '(未指定)'}`)
    switch (action) {
      case 'focus': windows.focus(id); break
      case 'minimize': windows.minimize(id); break
      case 'toggle_maximize': windows.toggleMaximize(id); break
      case 'snap':
        if (zone !== 'left' && zone !== 'right') throw new Error('zone 须为 left 或 right')
        windows.snap(id, zone)
        break
      case 'unsnap': windows.unsnap(id); break
      case 'set_bounds': {
        if (!bounds || typeof bounds !== 'object') throw new Error('缺少 bounds {x,y,w,h}')
        windows.setBounds(id, bounds)
        break
      }
      case 'set_title':
        if (!title) throw new Error('缺少 title')
        windows.setTitle(id, String(title))
        break
      default: throw new Error(`未知窗口动作：${action}`)
    }
    return { windowId: id }
  },

  app_open_file({ path }) {
    if (!path) throw new Error('缺少 path')
    const id = openPath(String(path))
    if (!id) throw new Error('打开失败（应用不可用）')
    return { windowId: id }
  },

  desktop_do({ action, arg }) {
    const desktop = useDesktop()
    switch (action) {
      case 'wallpaper':
        desktop.setWallpaper(Number(arg))
        return { wallpaper: desktop.wallpaper }
      case 'dark':
        desktop.setDark(asBool(arg))
        return { dark: desktop.dark }
      case 'dock_autohide':
        desktop.setDockAutoHide(asBool(arg))
        return { dockAutoHide: desktop.dockAutoHide }
      default: throw new Error(`未知桌面动作：${action}`)
    }
  },

  notes_do({ action, id, text, color }) {
    const notes = useNotes()
    switch (action) {
      case 'list': return notes.notes.map(noteRow)
      case 'create': {
        const n = notes.create(text == null ? '' : String(text), Number.isFinite(Number(color)) ? Number(color) : undefined)
        return noteRow(n)
      }
      case 'update': {
        if (!id) throw new Error('缺少 id')
        if (text != null) notes.setText(String(id), String(text))
        if (Number.isFinite(Number(color))) notes.setColor(String(id), Number(color))
        const n = notes.byId(String(id))
        if (!n) throw new Error(`便签不存在：${id}`)
        return noteRow(n)
      }
      case 'remove':
        if (!id) throw new Error('缺少 id')
        notes.remove(String(id))
        return { ok: true }
      default: throw new Error(`未知便签动作：${action}`)
    }
  },

  notify({ text, type }) {
    if (!text) throw new Error('缺少 text')
    toast(String(text), { type: type === 'success' || type === 'error' ? type : 'info' })
    return { ok: true }
  },
}

/** 挂载前端桥（Desktop onMounted 调用），返回卸载函数。 */
export function installAgentBridge(): () => void {
  return bus.on('sys:agent', (p: AgentReq) => {
    const req = p || {}
    if (!req.id || !req.action) return
    Promise.resolve()
      .then(() => {
        const fn = ACTIONS[req.action!]
        if (!fn) throw new Error(`未知动作：${req.action}`)
        return fn(req.args || {})
      })
      .then((data) => bus.remote('agent:resp', { id: req.id, ok: true, data: data ?? null }))
      .catch((e: any) => bus.remote('agent:resp', { id: req.id, ok: false, error: String(e?.message || e) }))
  })
}
