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
import { useNotifications } from '../stores/notifications'
import { useCalendar } from '../stores/calendar'
import { useDock } from '../stores/dock'
import { openPath } from '../apps/openWith'
import { agentTools, runAgentTool } from './agentTools'
import {
  installBuiltinCapabilities, listCapabilities, runCapability,
  openAppView, findView,
} from './capabilities'
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
    // 入系统通知中心：右上角横幅 + 面板可回看（替代旧的纯 toast 轻提示）
    useNotifications().notify({
      appId: 'goder',
      title: type === 'error' ? 'Goder 出错' : 'Goder 提示',
      body: String(text),
    })
    return { ok: true }
  },

  /** 在线自定义工具表（宿主/应用经 registerAgentTool 注册；osmcp tools/list 合并展示）。 */
  tools_list() {
    return { tools: agentTools() }
  },

  /** 执行自定义工具（os_app_tool 下发；run 回调由桌面壳执行，窗口无需在场）。 */
  async tool_call({ tool, args }) {
    if (!tool) throw new Error('缺少 tool')
    const data = await runAgentTool(String(tool), args || {})
    return { tool: String(tool), result: data ?? null }
  },

  /** 日历日程（数据权威在前端 calendar store：user_settings calendar.events）。 */
  calendar_do({ action, id, title, date, time, remind }) {
    const cal = useCalendar()
    switch (action) {
      case 'list': {
        const today = new Date()
        const pad = (n: number) => String(n).padStart(2, '0')
        const from = `${today.getFullYear()}-${pad(today.getMonth() + 1)}-${pad(today.getDate())}`
        return cal.events
          .filter((e) => !date || e.date === date)
          .sort((a, b) => (a.date + (a.time || '~')).localeCompare(b.date + (b.time || '~')))
          .slice(0, date ? 100 : 30)
          .map((e) => ({ id: e.id, title: e.title, date: e.date, time: e.time, remind: e.remind, upcoming: e.date >= from }))
      }
      case 'add': {
        if (!title || !date) throw new Error('缺少 title/date（date 形如 2026-10-05）')
        const e = cal.addEvent({ title: String(title), date: String(date), time: time ? String(time) : undefined, remind })
        return { id: e.id, title: e.title, date: e.date, time: e.time }
      }
      case 'remove':
        if (!id) throw new Error('缺少 id')
        cal.removeEvent(String(id))
        return { ok: true }
      default: throw new Error(`未知日历动作：${action}（list/add/remove）`)
    }
  },

  /** Dock 定制（pinned 持久化 user_settings dock 键）。 */
  dock_do({ action, appId, appIds }) {
    const dock = useDock()
    switch (action) {
      case 'list': return { pinned: dock.pinned, effective: dock.effective }
      case 'pin': {
        if (!appId) throw new Error('缺少 appId')
        dock.pinAt(String(appId), dock.effective.length)
        return { pinned: dock.pinned }
      }
      case 'unpin': {
        if (!appId) throw new Error('缺少 appId')
        dock.unpin(String(appId))
        return { pinned: dock.pinned }
      }
      case 'reorder': {
        if (!Array.isArray(appIds) || !appIds.length) throw new Error('缺少 appIds 数组')
        const last = String(appIds[appIds.length - 1])
        dock.pinAt(last, 0)
        for (let i = 1; i < appIds.length; i++) dock.pinAt(String(appIds[i]), i)
        return { pinned: dock.pinned }
      }
      default: throw new Error(`未知 Dock 动作：${action}（list/pin/unpin/reorder）`)
    }
  },

  // ---- 能力暴露层（CAP，见 capabilities.ts）：通用清单 + 执行 + 可视化 ----

  /** 全量能力清单（os_caps）：四层来源合并，按权限过滤。 */
  caps_list({ appId }) {
    const rows = listCapabilities()
    return { apps: appId ? rows.filter((r) => r.appId === String(appId)) : rows }
  },

  /** 执行应用业务操作（os_app_call）：fn/http 无窗口执行，仅 window 模式自动开窗。 */
  async cap_call({ appId, capability, args }) {
    if (!appId || !capability) throw new Error('缺少 appId/capability')
    const data = await runCapability(String(appId), 'action', String(capability), args || {})
    return { appId: String(appId), capability: String(capability), result: data ?? null }
  },

  /** 只读数据查询（os_app_query）：不打开应用直接读数。 */
  async cap_query({ appId, query, args }) {
    if (!appId || !query) throw new Error('缺少 appId/query')
    const data = await runCapability(String(appId), 'query', String(query), args || {})
    return { appId: String(appId), query: String(query), result: data ?? null }
  },

  /** 可视化操作（os_app_ui）：打开/聚焦应用到视图并高亮锚点；view 缺省时仅开窗。 */
  async app_ui({ appId, view, props, anchor, title }) {
    if (!appId) throw new Error('缺少 appId')
    const v = view ? findView(String(appId), String(view)) : null
    if (view && !v) {
      throw new Error(`视图不存在：${appId}.${view}（可用 views 见 os_caps；或省略 view 直接传 props/anchor）`)
    }
    const spec = {
      props: props != null ? props : v?.props,
      anchor: anchor != null ? String(anchor) : v?.anchor,
      title: title != null ? String(title) : v?.title,
    }
    const r = await openAppView(String(appId), spec)
    return {
      windowId: r.windowId,
      highlighted: r.highlighted,
      anchor: spec.anchor || null,
      steps: v?.steps || null,
      note: r.highlighted === false && spec.anchor
        ? '窗口已打开，但锚点未命中（iframe 网页应用无法高亮内部元素；内置/bundle 应用需在模板标记 data-os-cap）'
        : undefined,
    }
  },
}

/** 挂载前端桥（Desktop onMounted 调用），返回卸载函数。 */
export function installAgentBridge(): () => void {
  installBuiltinCapabilities() // 内置应用示范能力（calendar/notes，幂等）
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
