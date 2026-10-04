// keys.ts — 全局统一键盘中枢（单例服务，与 menu.ts / tooltip.ts 同层）。
//
// 设计：一对 window keydown 监听（capture + bubble）加一个 keyup 监听，
// 形成三条优先级链，所有键盘消费者向这里注册，系统层可以看到全部按键：
//
// 1. capture 链（模态/覆盖层，先于任何元素处理器）：dialog → menu → tip → overlay。
//    处理器返回 true 即消费（preventDefault + stopPropagation，元素层不再收到）。
// 2. bubble 链（在元素处理器之后，应用 @keydown 的 stopPropagation 保护语义不变）：
//    system（系统快捷键）→ desktop（桌面选中层）。未被消费且非输入框打字的按键，
//    最后经 bus.emit('sys:key', info, { remote:false }) 本地总线分发——应用可
//    bus.on('sys:key') 订阅，置 info.claimed = true 表示消费（中枢随之 preventDefault）。
//    输入框打字走 DOM 原生焦点通道、不进总线（防击键记录与噪音）。
// 3. keyup 链：应用切换器「松键提交」、Win/⌘ 单击检测（见 metaSolo 注释）。
//
// 优先级数值越大越先执行；同优先级按注册逆序（LIFO）——后注册（后挂载）的
// 对话框/菜单天然是视觉最上层，先获得按键。
//
// iframe 转发：网页应用聚焦后宿主收不到其内部按键，代理应用（webproxy hook.js）
// 与桌面客户端注入脚本会把 F4 / ⌘·Ctrl+Space / Win·⌘ 单击经 postMessage 转发，
// WebApp 校验来源后调 handleExternalKey 走同一条 bubble 链。
import { bus } from '../bus/bus'

export const SYS_KEY_TOPIC = 'sys:key'

export const KEY_PRIORITY = {
  dialog: 500,   // DialogHost / OsModal 等模态浮层
  menu: 400,     // 全局菜单单例（Esc / 方向键导航）
  tip: 350,      // 悬停提示
  overlay: 300,  // 通知中心等非模态覆盖层
  system: 200,   // 系统快捷键（启动台/聚焦搜索/切换器/窗口管理，Desktop 注册）
  desktop: 50,   // 桌面选中层（方向键/Enter/Delete，Shortcuts 注册）
} as const

export interface KeyInfo {
  type: 'down' | 'up'
  key: string
  code: string
  alt: boolean
  ctrl: boolean
  meta: boolean
  shift: boolean
  repeat: boolean
  /** 仅 type='up'：本次 Win/⌘ 是「按下并松开、中间无其他键」的单击 */
  soloMeta?: boolean
  /** sys:key 订阅者可置 true 表示消费 */
  claimed?: boolean
}

export interface KeyCtx {
  info: KeyInfo
  ev: KeyboardEvent | null
  /** 消费该键：等效于返回 true（preventDefault + stopPropagation + 终止链） */
  claim(): void
}

export type KeyHandler = (ctx: KeyCtx) => boolean | void
export type KeyPhase = 'capture' | 'bubble' | 'keyup'

interface Reg { prio: number; handler: KeyHandler }

const chains: Record<KeyPhase, Reg[]> = { capture: [], bubble: [], keyup: [] }
let installed = false
/** 启动台/聚焦搜索/切换器等全屏覆盖层打开时挂起 desktop 层与总线发布 */
let modalOpen = false

/** Win/⌘ 单击检测：Meta keydown 时不带其他修饰键置位，任何其他 keydown / 窗口
 *  失焦清零；keyup 时仍置位即为「单击」。⌘Tab 等组合因中间有其他键不会误触，
 *  OS 级应用切换（⌘Tab 不透传 Tab）靠 window blur 兜底清零。 */
let metaSolo = false

function isMetaLike(e: KeyboardEvent): boolean {
  return e.key === 'Meta' || e.key === 'OS' || e.code === 'MetaLeft' || e.code === 'MetaRight'
}

function toInfo(e: KeyboardEvent, type: 'down' | 'up'): KeyInfo {
  return {
    type,
    key: e.key,
    code: e.code,
    alt: e.altKey,
    ctrl: e.ctrlKey,
    meta: e.metaKey,
    shift: e.shiftKey,
    repeat: e.repeat,
    soloMeta: type === 'up' ? metaSolo : undefined,
  }
}

function dispatch(phase: KeyPhase, info: KeyInfo, ev: KeyboardEvent | null): boolean {
  let claimed = false
  const ctx: KeyCtx = {
    info,
    ev,
    claim() {
      claimed = true
      if (ev) { ev.preventDefault(); ev.stopPropagation() }
    },
  }
  // 同优先级 LIFO：注册时插到同组最前，正序遍历即后注册先执行
  for (const reg of chains[phase]) {
    if (reg.handler(ctx)) {
      ctx.claim()
      return true
    }
    if (claimed) return true
  }
  return false
}

function onCaptureKeyDown(e: KeyboardEvent) {
  metaSolo = isMetaLike(e) && !e.altKey && !e.ctrlKey && !e.shiftKey
  dispatch('capture', toInfo(e, 'down'), e)
}

function onBubbleKeyDown(e: KeyboardEvent) {
  const info = toInfo(e, 'down')
  dispatch('bubble', info, e)
  if (info.claimed || e.defaultPrevented) return
  // 输入框打字不进总线；全屏覆盖层打开时按键属于覆盖层（其搜索框等）也不外发
  if (modalOpen || isTextFieldEvent(e)) return
  bus.emit(SYS_KEY_TOPIC, info, { remote: false })
  if (info.claimed) e.preventDefault()
}

function onKeyUp(e: KeyboardEvent) {
  const info = toInfo(e, 'up')
  if (isMetaLike(e)) metaSolo = false
  dispatch('keyup', info, e)
}

export function installKeys() {
  if (installed) return
  installed = true
  window.addEventListener('keydown', onCaptureKeyDown, true)
  window.addEventListener('keydown', onBubbleKeyDown)
  window.addEventListener('keyup', onKeyUp)
  // OS 级切换（macOS ⌘Tab 浏览器收不到后续键）会导致 metaSolo 悬挂，失焦即清零
  window.addEventListener('blur', () => { metaSolo = false })
  document.addEventListener('visibilitychange', () => { if (document.hidden) metaSolo = false })
}

/** 注册按键处理器（capture/bubble/keyup 三链之一），返回注销函数 */
export function onKey(phase: KeyPhase, priority: number, handler: KeyHandler): () => void {
  installKeys()
  const list = chains[phase]
  const reg: Reg = { prio: priority, handler }
  let i = 0
  while (i < list.length && list[i].prio >= priority) i++
  list.splice(i, 0, reg)
  return () => {
    const idx = list.indexOf(reg)
    if (idx >= 0) list.splice(idx, 1)
  }
}

/** 启动台 / 聚焦搜索 / 切换器等全屏覆盖层开合时声明（挂起 desktop 层与总线发布） */
export function setModal(open: boolean) { modalOpen = open }

/** 可编辑元素判定（Alt+字母等窗口管理键在字段内让位）。composedPath 穿透
 *  shadow DOM（goder 等 web component 的内部输入框也能识别）。 */
export function isTextFieldEvent(e: Event): boolean {
  const path = e.composedPath?.() as EventTarget[] | undefined
  const t = (path?.[0] ?? e.target) as HTMLElement | null
  return !!t?.closest?.('input, textarea, [contenteditable="true"]')
}

/** iframe 转发入口：WebApp 收到子帧 postMessage 的全局组合键（F4 / ⌘Space /
 *  Win·⌘ 单击）后走同一条 bubble / keyup 链。仅系统层消费，不携带真实事件。 */
export function handleExternalKey(info: Partial<KeyInfo> & { type?: 'down' | 'up' }) {
  const full: KeyInfo = {
    type: info.type || 'down',
    key: info.key || '',
    code: info.code || '',
    alt: !!info.alt, ctrl: !!info.ctrl, meta: !!info.meta, shift: !!info.shift,
    repeat: false,
    soloMeta: info.soloMeta,
  }
  if (full.type === 'up') dispatch('keyup', full, null)
  else dispatch('bubble', full, null)
}
