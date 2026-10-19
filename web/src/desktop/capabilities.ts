// capabilities — goder 能力暴露层（CAP，Capability Access Protocol）。
//
// 背景：osmcp 的 os_* 内置工具按领域硬编码（窗口/便签/日历…），宿主业务功能
// 只能靠 registerAgentTool 逐个手写，没有统一的发现、描述与执行协议。本模块
// 把「应用暴露给 AI 的能力」收敛为一份标准清单（manifest），四层来源同一协议：
//
//   L0 系统级   osmcp 内置 os_* 工具（窗口/桌面/FS/总线，保留不动）
//   L1 平台应用 内置应用随代码注册（本文件尾部 installBuiltinCapabilities，
//               calendar/notes 为首批示范：无窗口直读 store）
//   L2 宿主声明 createOpenOS({ capabilities: { rooms: { queries:[…], actions:[…] } } })
//               —— grf-hotel/grf-os 零侵入接入，数据层面用 http 模式零代码声明
//   L3 动态应用 registerApp({ capabilities }) / OpenOS.registerCapabilities（商店
//               bundle 应用经 window.OpenOS）；schema 应用在 config.agentCaps
//               声明（纯 JSON，仅 http/window 模式）——管理员配置即暴露
//
// 能力三分类 + 三执行模式：
//   queries  只读数据查询 —— 无需打开应用即可执行（目标 2 的「不打开应用取数」）
//   actions  业务操作     —— fn（桌面壳执行 run 回调）/ http（声明式 REST，
//                            相对路径走 API 客户端带登录态）/ window（开窗带 props）
//   views    可视化入口   —— 打开应用到指定视图 + data-os-cap 锚点高亮 + 步骤
//                            引导（目标 1 的「可视化帮用户操作」）
//
// 执行由 agentBridge 的 cap_* 桥动作承接，osmcp 侧对应通用工具
// os_caps / os_app_query / os_app_call / os_app_ui（tools.go registerCAPTools）。
// 旧 registerAgentTool（P6）完整保留：清单里以 origin:'legacy' 并入，同一执行
// 管线（os_app_call 也能调）。
//
// 权限语义：def.perm 声明功能权限点——无权限则不出清单、拒绝执行（UX 门控）；
// 真正的鉴权权威恒在数据所有方（openos REST / 宿主后端），桥只是调用通道。
import { useApps } from '../stores/apps'
import { useWindows } from '../stores/windows'
import { useCalendar } from '../stores/calendar'
import { useNotes } from '../stores/notes'
import { can } from '../ui/perms'
import { api, parseAppConfig, type AppDef } from '../api/client'
import { agentTools, runAgentTool } from './agentTools'
import type { Note } from '../stores/notes'

// ---- 清单协议类型 ----

/** http 模式声明：相对 url（/api/…）走 OpenOS API 客户端自动携带登录态；
 *  绝对 url 裸 fetch（防令牌外泄），auth:true 时强制携带（同源宿主后端）。 */
export interface CapHTTPSpec {
  /** 缺省 GET。GET/DELETE 时 args 映射为 query 参数；POST/PUT/PATCH 时 args 原样作为 JSON body */
  method?: string
  url: string
  headers?: Record<string, string>
  /** 响应取值点路径（如 "data.items"），缺省整个响应体 */
  path?: string
  /** 绝对地址时携带登录态（默认 false） */
  auth?: boolean
}

/** window 模式声明：需要应用 UI 语境的操作/可视化等价入口。 */
export interface CapWindowSpec {
  /** 打开窗口的 props（应用按 props 路由到对应视图，如 { view:'checkin', roomId } ） */
  props?: any
  /** 打开后高亮的锚点（data-os-cap 名或 CSS 选择器） */
  anchor?: string
  title?: string
}

/** 单个能力（query 或 action）。run/http/window 至少提供其一：
 *  优先级 run > http > window（action 三者可并存——run 走无窗口执行，
 *  window 供 os_app_ui 可视化路径选择）。 */
export interface CapDef {
  /** 短名（[a-z0-9_]，应用内唯一） */
  name: string
  title?: string
  /** 给 AI 的详细说明（越具体越容易被正确调用） */
  description?: string
  /** 功能权限点（UI 门控，权威鉴权在数据所有方） */
  perm?: string
  /** 参数 JSON Schema（{type:'object',properties:{…}}） */
  params?: Record<string, any>
  /** 返回值说明 */
  result?: string
  /** fn 模式：桌面壳执行（应用窗口无需在场） */
  run?: (args: any) => any | Promise<any>
  /** http 模式：声明式 REST 调用（零代码） */
  http?: CapHTTPSpec
  /** window 模式：开/聚焦窗口并携带 props */
  window?: CapWindowSpec
}

/** 可视化操作入口：os_app_ui 打开应用到指定视图，可带锚点高亮与步骤引导。 */
export interface CapViewDef {
  name: string
  title?: string
  description?: string
  perm?: string
  props?: any
  anchor?: string
  /** 分步操作说明（AI 可转述给用户的引导文案） */
  steps?: string[]
}

/** 一个应用的能力清单。未知字段一律忽略（前向兼容），version 语义自定。 */
export interface AppCapabilities {
  appId: string
  /** 能力域显示名（缺省应用名） */
  label?: string
  version?: string
  queries?: CapDef[]
  actions?: CapDef[]
  views?: CapViewDef[]
}

export type CapOrigin = 'builtin' | 'host' | 'runtime' | 'schema' | 'legacy'

/** 清单条目（listCapabilities 输出，供 AI 阅读——不带 run 回调）。 */
export interface CapListing {
  appId: string
  label: string
  origin: CapOrigin
  /** 该应用当前打开的窗口数（AI 据此选择无窗口执行还是可视化操作） */
  openWindows: number
  queries?: Array<Omit<CapDef, 'run'> & { origin?: CapOrigin }>
  actions?: Array<Omit<CapDef, 'run'> & { origin?: CapOrigin }>
  views?: CapViewDef[]
}

// ---- 注册表 ----

interface Entry {
  caps: AppCapabilities
  origin: CapOrigin
}

const registry = new Map<string, Entry>()

const NAME_RE = /^[a-z0-9_]+$/

/** 注册/覆盖一个应用的能力清单（同 appId 后注册者胜）。 */
export function registerCapabilities(caps: AppCapabilities, origin: CapOrigin = 'runtime') {
  if (!caps || !caps.appId) throw new Error('registerCapabilities: 缺少 appId')
  registry.set(caps.appId, { caps, origin })
}

/** 注销（应用卸载/运行期反注册）。 */
export function unregisterCapabilities(appId: string) {
  registry.delete(appId)
}

/** createOpenOS 静态声明批注册（宿主配置面，键 = appId）。 */
export function installCapabilities(
  decl: Record<string, Omit<AppCapabilities, 'appId'>> | undefined,
  origin: CapOrigin = 'host',
) {
  for (const [appId, c] of Object.entries(decl || {})) {
    registerCapabilities({ appId, ...c }, origin)
  }
}

// ---- schema 应用 config.agentCaps 派生（L3：管理员 JSON 配置即暴露） ----

/** apps 表 config 里声明的 agentCaps（纯 JSON：只认 http/window/描述字段）。 */
export interface AgentCapsConfig {
  label?: string
  queries?: CapDef[]
  actions?: CapDef[]
  views?: CapViewDef[]
}

function sanitizeDefs(defs: CapDef[] | undefined): CapDef[] {
  // JSON 来源只认 http/window 模式（run 回调不可能出现在配置里）；http 须有 url
  const out: CapDef[] = []
  for (const d of defs || []) {
    if (!d || !NAME_RE.test(String(d.name))) continue
    const clean: any = { ...d }
    delete clean.run
    if (clean.http && !clean.http.url) delete clean.http
    if (clean.http || clean.window) out.push(clean)
  }
  return out
}

/** schemaCaps 结果缓存（MCP 每次工具调用都会走 listCapabilities/resolveCapability/
 *  findView，避免对全部 apps 配置重复 JSON.parse）：key = apps 数组引用 + 长度——
 *  refresh 整组替换数组即换引用，OpenOS.registerApp/unregisterApp 原地 push/splice
 *  只变长度，任一变化即失效重解析；返回条目为只读共享（消费方均不改动）。 */
let schemaCapsCache: { src: AppDef[]; len: number; entries: Entry[] } | null = null

function schemaCaps(): Entry[] {
  const apps = useApps()
  const arr = apps.apps
  const hit = schemaCapsCache
  if (hit && hit.src === arr && hit.len === arr.length) return hit.entries
  const out: Entry[] = []
  for (const app of arr) {
    const raw = (app as any).config
    if (!raw) continue
    const cfg = parseAppConfig(app)
    if (cfg === undefined) continue // 非法 JSON（与原实现 catch { continue } 同义）
    const ac = cfg?.agentCaps
    if (!ac || typeof ac !== 'object') continue
    out.push({
      origin: 'schema',
      caps: {
        appId: app.id,
        label: ac.label || app.name,
        queries: sanitizeDefs(ac.queries),
        actions: sanitizeDefs(ac.actions),
        views: (ac.views || []).filter((v: any) => v && NAME_RE.test(String(v.name))),
      },
    })
  }
  schemaCapsCache = { src: arr, len: arr.length, entries: out }
  return out
}

// ---- 旧 agentTools 兼容包装（origin:'legacy'，并入同一执行管线） ----

function legacyCaps(): Entry[] {
  const byApp = new Map<string, CapDef[]>()
  for (const t of agentTools()) {
    // meta.name 为完整名 app_<appId>_<name>；appId 字段是注册时的归属域
    const prefix = `app_${t.appId}_`
    const short = t.name.startsWith(prefix) && t.name.length > prefix.length
      ? t.name.slice(prefix.length)
      : t.name
    if (!byApp.has(t.appId)) byApp.set(t.appId, [])
    byApp.get(t.appId)!.push({
      name: short,
      title: short,
      description: t.description,
      params: t.params,
      run: (args: any) => runAgentTool(t.name, args),
    })
  }
  return [...byApp.entries()].map(([appId, actions]) => ({
    origin: 'legacy' as CapOrigin,
    caps: { appId, label: appId, actions } as AppCapabilities,
  }))
}

// ---- 清单聚合 ----

const stripRun = (d: CapDef): Omit<CapDef, 'run'> => {
  const rest: any = { ...d }
  delete rest.run
  return rest
}

const permVisible = (d: { perm?: string }) => !d.perm || can(d.perm)

/** 全量能力清单（os_caps 桥动作）：注册表 + schema 配置 + legacy，按权限过滤。 */
export function listCapabilities(): CapListing[] {
  const windows = useWindows()
  const apps = useApps()
  const entries = [
    ...[...registry.values()],
    ...schemaCaps(),
    ...legacyCaps(),
  ]
  // 同 appId 多来源合并（builtin/host/runtime 注册表优先，schema/legacy 追加）
  const merged = new Map<string, CapListing>()
  for (const e of entries) {
    const appId = e.caps.appId
    let row = merged.get(appId)
    if (!row) {
      const def = apps.byId(appId)
      row = {
        appId,
        label: e.caps.label || def?.name || appId,
        origin: e.origin,
        openWindows: windows.list.filter((w) => w.appId === appId && !w.minimized).length,
      }
      merged.set(appId, row)
    }
    for (const q of e.caps.queries || []) {
      if (permVisible(q) && !(row.queries || []).some((x) => x.name === q.name)) {
        (row.queries ||= []).push({ ...stripRun(q), origin: e.origin })
      }
    }
    for (const a of e.caps.actions || []) {
      if (permVisible(a) && !(row.actions || []).some((x) => x.name === a.name)) {
        (row.actions ||= []).push({ ...stripRun(a), origin: e.origin })
      }
    }
    for (const v of e.caps.views || []) {
      if (permVisible(v) && !(row.views || []).some((x) => x.name === v.name)) {
        (row.views ||= []).push(v)
      }
    }
  }
  return [...merged.values()].sort((a, b) => a.appId.localeCompare(b.appId))
}

// ---- 执行 ----

interface Resolved {
  kind: 'query' | 'action'
  def: CapDef
  origin: CapOrigin
}

/** 按 (appId, kind, name) 解析能力：注册表 → schema 配置 → legacy。 */
function resolveCapability(appId: string, kind: 'query' | 'action', name: string): Resolved {
  const pool: Array<{ defs?: CapDef[]; origin: CapOrigin }> = []
  const e = registry.get(appId)
  if (e) pool.push({ defs: kind === 'query' ? e.caps.queries : e.caps.actions, origin: e.origin })
  for (const sc of schemaCaps()) {
    if (sc.caps.appId === appId) {
      pool.push({ defs: kind === 'query' ? sc.caps.queries : sc.caps.actions, origin: sc.origin })
    }
  }
  if (kind === 'action') {
    for (const lc of legacyCaps()) {
      if (lc.caps.appId === appId) pool.push({ defs: lc.caps.actions, origin: 'legacy' })
    }
  }
  for (const p of pool) {
    const def = (p.defs || []).find((d) => d.name === name)
    if (def) return { kind, def, origin: p.origin }
  }
  throw new Error(`能力不存在：${appId}.${kind}.${name}（可用清单见 os_caps）`)
}

/** http 模式执行：GET/DELETE → args 映射 query 参数；POST/PUT/PATCH → args 作 JSON body。 */
async function httpExec(spec: CapHTTPSpec, args: any): Promise<any> {
  const method = (spec.method || 'GET').toUpperCase()
  const a = args && typeof args === 'object' && !Array.isArray(args) ? args : {}
  if (/^https?:\/\//i.test(spec.url)) {
    // 绝对地址：默认裸 fetch 防令牌外泄；auth:true 强制携带（宿主同源后端）
    const resp = await fetch(spec.url, {
      method,
      headers: { 'Content-Type': 'application/json', ...(spec.headers || {}) },
      credentials: spec.auth ? 'include' : 'same-origin',
      body: ['GET', 'HEAD', 'DELETE'].includes(method) ? undefined : JSON.stringify(a),
    })
    if (!resp.ok) throw new Error(`HTTP ${resp.status} ${spec.url}`)
    const data = await resp.json().catch(() => null)
    return getPath(data, spec.path)
  }
  // 相对路径：统一走 API 客户端（自动登录态 / 401 处理）
  let url = spec.url
  let body: unknown
  if (method === 'GET' || method === 'DELETE') {
    const qs = new URLSearchParams()
    for (const [k, v] of Object.entries(a)) {
      if (v != null) qs.set(k, String(v))
    }
    const s = qs.toString()
    if (s) url += (url.includes('?') ? '&' : '?') + s
  } else {
    body = a
  }
  const data = await (method === 'GET'
    ? api.get<any>(url, spec.headers)
    : method === 'DELETE'
      ? api.del<any>(url)
      : method === 'PUT'
        ? api.put<any>(url, body, spec.headers)
        : api.post<any>(url, body, spec.headers))
  return getPath(data, spec.path)
}

/** 点路径取值："data.items" → data?.items（越界安全，同 SchemaApp）。 */
function getPath(data: any, path?: string): any {
  if (!path) return data
  return String(path).split('.').reduce((acc: any, k) => (acc == null ? undefined : acc[k.trim()]), data)
}

/**
 * 执行一个能力（agentBridge cap_call / cap_query 下发）。
 * 查询恒为只读路径；action 优先级 run > http > window（window 模式开窗即执行）。
 */
export async function runCapability(
  appId: string,
  kind: 'query' | 'action',
  name: string,
  args: any,
): Promise<any> {
  const { def } = resolveCapability(appId, kind, name)
  if (def.perm && !can(def.perm)) {
    throw new Error(`需要权限 ${def.perm}（${appId}.${name}）`)
  }
  const a = args && typeof args === 'object' && !Array.isArray(args) ? args : {}
  if (def.run) return def.run(a)
  if (def.http) return httpExec(def.http, a)
  if (kind === 'action' && def.window) {
    // 仅 window 模式的 action：打开（或聚焦）应用窗口到对应视图
    const r = await openAppView(appId, def.window)
    return { opened: true, windowId: r.windowId, note: 'window 模式能力：已打开应用窗口执行' }
  }
  throw new Error(`${appId}.${name} 未提供实现（run/http/window 均缺）`)
}

// ---- 可视化：打开视图 + 锚点高亮 ----

/** 打开（或聚焦）应用到指定视图；anchor 存在时挂载稳定后高亮（异步重试等待应用渲染）。 */
export async function openAppView(
  appId: string,
  spec: { props?: any; anchor?: string; title?: string },
): Promise<{ windowId: string; highlighted?: boolean }> {
  const windows = useWindows()
  const id = windows.open(appId, spec.props, spec.title)
  if (!id) throw new Error(`应用不存在或无权打开：${appId}`)
  let highlighted = false
  if (spec.anchor) {
    // 应用组件是异步加载的，锚点出现时间不定：500ms 间隔重试至多 ~6s
    for (let i = 0; i < 12 && !highlighted; i++) {
      await sleep(500)
      highlighted = tryHighlight(appId, spec.anchor)
    }
  }
  return { windowId: id, highlighted }
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms))
}

/**
 * 在应用窗口内高亮一个元素：优先按 data-os-cap="<name>" 锚点约定匹配，
 * 匹配不到再当 CSS 选择器（限定在该窗口根内，选择器非法/未命中返回 false）。
 * 同应用多窗口时作用于最上层窗口。命中后滚动入视野 + 描边脉冲 2.6s。
 */
export function tryHighlight(appId: string, anchor: string): boolean {
  const roots = [...document.querySelectorAll<HTMLElement>(`.window[data-app="${CSS.escape(appId)}"]`)]
  if (!roots.length) return false
  const root = roots.sort((a, b) => Number(b.style.zIndex || 0) - Number(a.style.zIndex || 0))[0]
  let el: HTMLElement | null = root.querySelector<HTMLElement>(`[data-os-cap="${CSS.escape(anchor)}"]`)
  if (!el) {
    try {
      el = root.querySelector<HTMLElement>(anchor)
    } catch {
      return false // 非法选择器
    }
  }
  if (!el) return false
  el.scrollIntoView({ block: 'center', behavior: 'smooth' })
  el.classList.remove('os-cap-hl')
  void el.offsetWidth // 重新触发动画
  el.classList.add('os-cap-hl')
  window.setTimeout(() => el.classList.remove('os-cap-hl'), 2600)
  return true
}

/** 解析 views 声明（os_app_ui 下发）：view 名 → 打开参数；无权限或不存在返回 null。 */
export function findView(appId: string, name: string): CapViewDef | null {
  const pools: Array<{ views?: CapViewDef[] }> = []
  const e = registry.get(appId)
  if (e) pools.push(e.caps)
  for (const sc of schemaCaps()) if (sc.caps.appId === appId) pools.push(sc.caps)
  for (const p of pools) {
    const v = (p.views || []).find((x) => x.name === name)
    if (v && permVisible(v)) return v
  }
  return null
}

// ---- L1 示范：内置应用首批能力（数据权威在前端 store，无窗口可直读） ----

const noteRow = (n: Note) => ({ id: n.id, color: n.color, updated: n.updated, preview: n.text.slice(0, 80) })

function calendarCaps(): AppCapabilities {
  return {
    appId: 'calendar',
    label: '日历',
    queries: [{
      name: 'events',
      title: '查询日程',
      description: '列出日历日程；date=YYYY-MM-DD 只看当天，缺省返回未来日程（按日期时间排序，上限 100）。',
      params: {
        type: 'object',
        properties: { date: { type: 'string', description: '日期 YYYY-MM-DD（可选）' } },
      },
      result: '日程数组 [{id,title,date,time,remind,upcoming}]',
      run: (args: any) => {
        const cal = useCalendar()
        const date = args?.date ? String(args.date) : ''
        const today = new Date()
        const pad = (n: number) => String(n).padStart(2, '0')
        const from = `${today.getFullYear()}-${pad(today.getMonth() + 1)}-${pad(today.getDate())}`
        return cal.events
          .filter((e) => !date || e.date === date)
          .filter((e) => date || e.date >= from)
          .sort((a, b) => (a.date + (a.time || '~')).localeCompare(b.date + (b.time || '~')))
          .slice(0, 100)
          .map((e) => ({ id: e.id, title: e.title, date: e.date, time: e.time, remind: e.remind, upcoming: e.date >= from }))
      },
    }],
    actions: [
      {
        name: 'event_add',
        title: '新增日程',
        description: '新增一条日历日程（提醒随日程存日历 store，到期经通知中心弹出）。',
        params: {
          type: 'object',
          properties: {
            title: { type: 'string', description: '标题' },
            date: { type: 'string', description: '日期 YYYY-MM-DD' },
            time: { type: 'string', description: '时间 HH:MM（可选）' },
            remind: { type: 'string', enum: ['at', '5m', '15m', '1h', '1d'], description: '提醒时机（可选）：at=准点 / 5m / 15m / 1h / 1d 提前' },
          },
          required: ['title', 'date'],
        },
        run: (args: any) => {
          if (!args?.title || !args?.date) throw new Error('缺少 title/date')
          const REMINDS = ['at', '5m', '15m', '1h', '1d']
          const remind = REMINDS.includes(args.remind) ? args.remind : undefined
          const e = useCalendar().addEvent({
            title: String(args.title), date: String(args.date),
            time: args.time ? String(args.time) : undefined,
            remind,
          })
          return { id: e.id, title: e.title, date: e.date, time: e.time, remind: e.remind }
        },
      },
      {
        name: 'event_remove',
        title: '删除日程',
        params: {
          type: 'object',
          properties: { id: { type: 'string', description: '日程 id（events 查询获取）' } },
          required: ['id'],
        },
        run: (args: any) => {
          if (!args?.id) throw new Error('缺少 id')
          useCalendar().removeEvent(String(args.id))
          return { ok: true }
        },
      },
    ],
    views: [{
      name: 'month',
      title: '打开月历',
      description: '打开日历应用月视图。',
      props: {},
    }],
  }
}

function notesCaps(): AppCapabilities {
  const col = (args: any) => (Number.isFinite(Number(args?.color)) ? Number(args.color) : undefined)
  return {
    appId: 'notes',
    label: '便签',
    queries: [{
      name: 'list',
      title: '查询便签',
      description: '列出全部桌面便签（含 80 字预览）。',
      result: '便签数组 [{id,color,updated,preview}]',
      run: () => useNotes().notes.map(noteRow),
    }],
    actions: [
      {
        name: 'create',
        title: '新建便签',
        params: {
          type: 'object',
          properties: {
            text: { type: 'string', description: '便签文本（可选）' },
            color: { type: 'number', description: '颜色下标 0-4（可选）' },
          },
        },
        run: (args: any) => noteRow(useNotes().create(args?.text == null ? '' : String(args.text), col(args))),
      },
      {
        name: 'update',
        title: '修改便签',
        params: {
          type: 'object',
          properties: {
            id: { type: 'string', description: '便签 id' },
            text: { type: 'string', description: '新文本（可选）' },
            color: { type: 'number', description: '颜色下标 0-4（可选）' },
          },
          required: ['id'],
        },
        run: (args: any) => {
          if (!args?.id) throw new Error('缺少 id')
          const notes = useNotes()
          if (args.text != null) notes.setText(String(args.id), String(args.text))
          if (col(args) != null) notes.setColor(String(args.id), col(args)!)
          const n = notes.byId(String(args.id))
          if (!n) throw new Error(`便签不存在：${args.id}`)
          return noteRow(n)
        },
      },
      {
        name: 'remove',
        title: '删除便签',
        params: {
          type: 'object',
          properties: { id: { type: 'string', description: '便签 id' } },
          required: ['id'],
        },
        run: (args: any) => {
          if (!args?.id) throw new Error('缺少 id')
          useNotes().remove(String(args.id))
          return { ok: true }
        },
      },
    ],
  }
}

/** 安装内置应用示范能力（installAgentBridge 时调用，幂等）。 */
export function installBuiltinCapabilities() {
  registerCapabilities(calendarCaps(), 'builtin')
  registerCapabilities(notesCaps(), 'builtin')
}
