<script setup lang="ts">
// 桌面快捷方式：应用/文件/文件夹图标，双击打开（文件夹弹出内容面板）；可从启动台
// 拖入创建、文件应用拖出文件/目录创建（引用不移动原文件），也可拖入操作系统文件
// （自动上传到个人空间并生成快捷方式）。文件快捷方式的 id 是 VFS 绝对路径，原文件
// 之后被移动/删除快捷方式即失效（openPath 报错提示），与启动台引用应用同一模型。
// 位置为全桌面网格（左上角锚定），释放后吸附格子并与其他快捷方式/小组件互不覆盖。
// 拖动图标到另一图标上释放 = 自动创建文件夹收纳两者（macOS 行为）；文件夹成员可
// 拖出到桌面空白处移出、拖到其他文件夹图标上移动；右键可重命名 / 解散（成员散回桌面）。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useWindows } from '../stores/windows'
import { useApps } from '../stores/apps'
import { useSession } from '../stores/session'
import { api } from '../api/client'
import { openPath, fileEmoji } from '../apps/openWith'
import AppIcon from '../ui/AppIcon.vue'
import FolderIcon from '../ui/FolderIcon.vue'
import { toast } from '../ui/toast'
import { GRID, cellUnder, findFree, isFree, posOf, registerItem, unregisterItem } from './grid'
import { onKey, KEY_PRIORITY, isTextFieldEvent, type KeyCtx } from '../ui/keys'

export interface FolderItem { kind: 'app' | 'file'; id: string; name: string; dir?: boolean }
export interface Shortcut {
  kind: 'app' | 'file' | 'folder'
  id: string        // app id / 文件绝对路径 / 文件夹随机 id（f 前缀）
  name: string
  col: number
  row: number
  dir?: boolean     // 文件快捷方式专属：目标是目录（双击用文件应用定位而非关联应用打开）
  items?: FolderItem[] // 文件夹专属：成员列表
}

const windows = useWindows()
const apps = useApps()
const session = useSession()
/** v2：全桌面网格（左上角锚定）。旧版以右上角为原点、仅右侧 5 列，首次加载时迁移。
 *  权威存储在服务端 user_settings 的 desktop.shortcuts 键（同一账号换设备布局一致），
 *  localStorage 仅作即时缓存与离线兜底；服务端无数据时把本地布局一次性迁移上云。 */
const SETTINGS_KEY = 'desktop.shortcuts'
const KEY = () => `openos_desktop_v2_${session.user!.username}`
const OLD_KEY = () => `openos_desktop_${session.user!.username}`
let serverReady = false // 服务端数据已就位（就位后的变更才回写服务端）
let saveTimer = 0 // 服务端写回防抖
let lastSynced = '' // 最近一次已知服务端内容（无变化不回写，防访问即改库）

const list = ref<Shortcut[]>([])
const selected = ref('')
/** 拖动悬停的合并目标（图标上高亮圆圈；dragover 持续续期，离开后短延时自灭）。 */
const hoverKey = ref('')
let hoverClr = 0
/** 本组件发起的拖动源（顶层快捷方式或文件夹面板成员），落点预览克隆与互换判据用。 */
interface DragPrev { kind: 'app' | 'file' | 'folder'; id: string; name: string; dir?: boolean; items?: FolderItem[]; col?: number; row?: number; fromFolder?: string }
const dragPrev = ref<DragPrev | null>(null)
/** 拖动中的源图标（keyOf 形态），图标本体压暗提示已离位。 */
const draggingKey = ref('')
/** 落点预览：松手后图标将吸附到的格子（虚拟对齐位置；dragover 持续续期，超时自灭）。 */
const ghost = ref<{ c: number; r: number } | null>(null)
let ghostClr = 0
/** 打开内容面板（peek）的文件夹 id。 */
const peekId = ref('')
const peekNameRef = ref<HTMLInputElement>()

const keyOf = (s: { kind: string; id: string }) => `s:${s.kind}:${s.id}`

/** 解析布局数据（folder 项兜底 items，旧数据不含文件夹，防手改数据缺键）；
 *  解析失败返回 null（服务端数据损坏时保留本地缓存并回写覆盖）。 */
function parseList(raw: string | null): Shortcut[] | null {
  if (raw === null) return null
  try {
    const arr = JSON.parse(raw)
    if (!Array.isArray(arr)) return null
    return arr.map((s: Shortcut) =>
      s.kind === 'folder' ? { ...s, items: s.items || [] } : s)
  } catch { return null }
}

/** 全量重建网格占位注册（服务端数据替换本地缓存后调用）。 */
function registerAll() {
  for (const s of list.value) registerItem(keyOf(s), { c: s.col, r: s.row, sc: 1, sr: 1, kind: 'shortcut' })
}
function unregisterAll() {
  for (const s of list.value) unregisterItem(keyOf(s))
}

/** 服务端同步：本地缓存先上屏，服务端数据到达后校正；服务端无数据时迁移上云。 */
async function syncServer() {
  try {
    const s = await api.get<Record<string, string>>('/api/settings')
    serverReady = true // 同步判定已定，后续变更可回写
    if (s[SETTINGS_KEY] !== undefined) {
      const next = parseList(s[SETTINGS_KEY])
      if (next === null) {
        persistServer() // 服务端数据损坏：保留本地缓存并回写覆盖
      } else {
        lastSynced = JSON.stringify(next)
        if (lastSynced !== JSON.stringify(list.value)) {
          unregisterAll()
          list.value = next
          registerAll()
          localStorage.setItem(KEY(), JSON.stringify(list.value)) // 刷新本地缓存
        }
        // 其他设备已用过的桌面不再种 goder 快捷方式（用户可能主动移除过）
        localStorage.setItem(SEEDED_KEY(), '1')
      }
    } else if (list.value.length) {
      persistServer() // 旧版本布局只在 localStorage：一次性迁移到服务端
    }
  } catch { /* 离线容忍：继续用本地缓存 */ }
  serverReady = true
  syncResolve()
}

onMounted(() => {
  const parsed = parseList(localStorage.getItem(KEY()))
  if (parsed !== null) {
    list.value = parsed
  } else {
    // 旧格式（右上角锚定）→ 左上角锚定列
    const old: any[] = JSON.parse(localStorage.getItem(OLD_KEY()) || '[]')
    list.value = old.map((s) => {
      const oldLeft = window.innerWidth - 16 - s.col * 92 - 82
      return { ...s, col: Math.max(0, Math.round((oldLeft - GRID.left) / 92)) }
    })
  }
  registerAll()
  save()
  void syncServer()
})

// 默认桌面：应用列表就绪后，为每个用户一次性种入 goder 快捷方式（首个空位）。
// 子组件挂载早于 Desktop 的 apps.refresh 完成，故 watch 等列表就绪再种；
// 未授权角色（如 guest）的 /api/apps 不含 goder，byId 为空自动跳过；
// 以 localStorage 标记保证只种一次——用户主动移除后不会自动恢复。
// 注意 stopSeed 用 let + 可选调用：immediate 回调同步执行时若 apps 已就绪（如
// 移动↔桌面形态切换后的二次挂载）会立刻走到自停逻辑，const 赋值尚未完成会触发
// TDZ ReferenceError 中断整个 Desktop 的 patch。
const SEEDED_KEY = () => `openos_desktop_goder_seeded_${session.user!.username}`
/** 服务端同步完成信号：种入决策须等服务端布局落地（其他设备的桌面可能已移除 goder）。 */
let syncResolve: () => void = () => {}
const syncDone = new Promise<void>((r) => { syncResolve = r })
let stopSeed: (() => void) | undefined
stopSeed = watch(() => apps.apps.length, (n) => {
  if (!n) return
  stopSeed?.()
  void (async () => {
    await syncDone
    if (localStorage.getItem(SEEDED_KEY())) return
    localStorage.setItem(SEEDED_KEY(), '1')
    const app = apps.byId('goder')
    if (app && !list.value.some((s) => s.kind === 'app' && s.id === 'goder')) {
      const s: Shortcut = { kind: 'app', id: 'goder', name: app.name, col: 0, row: 0 }
      list.value.push(s)
      place(s, 0, 0)
      save()
    }
  })()
}, { immediate: true })

function save() {
  localStorage.setItem(KEY(), JSON.stringify(list.value))
  persistServer()
}
/** 服务端写回（400ms 防抖；服务端数据就位前不写，防本地空态覆盖云端布局）。 */
function persistServer() {
  if (!serverReady) return
  const v = JSON.stringify(list.value)
  if (v === lastSynced) return
  clearTimeout(saveTimer)
  saveTimer = window.setTimeout(() => {
    lastSynced = v
    api.put('/api/settings', { key: SETTINGS_KEY, value: v }).catch(() => { lastSynced = '' })
  }, 400)
}

function place(s: Shortcut, c: number, r: number) {
  const rect = findFree(c, r, 1, 1, 'shortcut', keyOf(s))
  s.col = rect.c
  s.row = rect.r
  registerItem(keyOf(s), { c: s.col, r: s.row, sc: 1, sr: 1, kind: 'shortcut' })
}

/** 在桌面坐标处添加快捷方式（网格吸附，冲突时顺延到最近空位）。返回是否真正创建（重复项跳过）。 */
function addAt(kind: Shortcut['kind'], id: string, name: string, x: number, y: number, dir?: boolean): boolean {
  if (list.value.some((s) => s.kind === kind && s.id === id)) return false
  const cell = cellUnder(x, y)
  const s: Shortcut = { kind, id, name, col: 0, row: 0, dir }
  list.value.push(s)
  place(s, cell.c, cell.r)
  save()
  return true
}

/** 供外部（桌面拖放 / 右键菜单）调用。 */
defineExpose({ addAt, addAppShortcut, dropShortcut, dragOver, endPreview, openByKey, removeByKey, relayout, renameFolder })

function style(s: Shortcut) {
  const p = posOf({ c: s.col, r: s.row })
  return { left: p.x + 5 + 'px', top: p.y + 6 + 'px', width: GRID.cw - 10 + 'px' }
}

/** 落点预览定位：与图标同一套格子坐标；跨源拖入（拿不到源数据）固定一格高仅显示虚线框。 */
const ghostStyle = computed(() => {
  if (!ghost.value) return {}
  const p = posOf(ghost.value)
  const st: Record<string, string> = { left: p.x + 5 + 'px', top: p.y + 6 + 'px', width: GRID.cw - 10 + 'px' }
  if (!dragPrev.value) st.height = GRID.ch - 12 + 'px'
  return st
})

// ---- 打开 / 移除 ----
function open(s: Shortcut) {
  if (s.kind === 'folder') { peekId.value = s.id; return }
  if (s.kind === 'app') windows.open(s.id)
  else openItem({ kind: 'file', id: s.id, name: s.name, dir: s.dir })
}

function openItem(it: FolderItem) {
  if (it.kind === 'app') windows.open(it.id)
  // 目录 → 文件应用定位（Spotlight 同款语义）；文件 → 统一关联表：md→文档、csv→表格、html→幻灯片、图片→预览
  else if (it.dir) windows.open('files', { path: it.id })
  else openPath(it.id)
  peekId.value = ''
}

/** 桌面右键菜单调用：按唯一键（kind:id，见 data-id）打开 / 移除。 */
function openByKey(key: string) {
  const s = list.value.find((x) => x.kind + ':' + x.id === key)
  if (s) open(s)
}
function removeByKey(key: string) {
  const s = list.value.find((x) => x.kind + ':' + x.id === key)
  if (!s) return
  const base = { c: s.col, r: s.row }
  unregisterItem(keyOf(s))
  list.value = list.value.filter((x) => x !== s)
  // 解散文件夹：成员散回桌面（从文件夹原格子起找空位顺延）
  if (s.kind === 'folder') for (const it of s.items || []) scatter(it, base.c, base.r)
  if (peekId.value === s.id) peekId.value = ''
  selected.value = ''
  save()
}
/** 成员落回桌面顶层（吸附到 c/r 附近空位）。 */
function scatter(it: FolderItem, c: number, r: number) {
  const ns: Shortcut = { kind: it.kind, id: it.id, name: it.name, col: 0, row: 0, dir: it.dir }
  list.value.push(ns)
  place(ns, c, r)
}

/** 右键菜单「重命名文件夹」：打开面板并聚焦选中名称。 */
function renameFolder(key: string) {
  const s = list.value.find((x) => x.kind + ':' + x.id === key)
  if (s?.kind !== 'folder') return
  peekId.value = s.id
  void nextTick(() => { peekNameRef.value?.focus(); peekNameRef.value?.select() })
}

/** 放到第一个空位（从左上角扫描）。 */
function addAppShortcut(appId: string) {
  const app = apps.byId(appId)
  if (!app) return
  if (list.value.some((s) => s.kind === 'app' && s.id === appId)) return
  const s: Shortcut = { kind: 'app', id: appId, name: app.name, col: 0, row: 0 }
  list.value.push(s)
  place(s, 0, 0)
  save()
}

/** 视口变化后重排：把越界的项收回网格内，并解决持久化数据可能的重叠。 */
function relayout() {
  for (const s of list.value) place(s, s.col, s.row)
  save()
}

// ---- 桌面选中层键盘（统一键盘中枢 ui/keys.ts 的 desktop 层） ----
// 焦点在桌面层级（不在应用窗口/弹窗/菜单/输入框内）时接管：方向键按网格最近邻
// 移动选中、Enter 打开选中项、Delete/Backspace 移除（与右键「移除快捷方式」一致，
// 文件夹照常解散散回桌面）。文件夹展开面板打开期间不接管（面板自有交互）。
let offKeys: (() => void) | undefined
onMounted(() => { offKeys = onKey('bubble', KEY_PRIORITY.desktop, onDesktopKey) })
onBeforeUnmount(() => { offKeys?.(); offKeys = undefined })

function onDesktopKey({ info, ev }: KeyCtx) {
  if (!ev || info.alt || info.ctrl || info.meta) return false
  if (peekId.value || isTextFieldEvent(ev)) return false
  const host = document.activeElement as HTMLElement | null
  if (host?.closest('.window, .os-modal-mask, .os-menu-root, [role="dialog"]')) return false
  switch (info.key) {
    case 'ArrowUp': case 'ArrowDown': case 'ArrowLeft': case 'ArrowRight': {
      if (!list.value.length) return false
      navigate(info.key.slice(5).toLowerCase() as 'up' | 'down' | 'left' | 'right')
      return true
    }
    case 'Enter': {
      const cur = selectedOf()
      if (!cur) return false
      open(cur)
      return true
    }
    case 'Delete': case 'Backspace': {
      const cur = selectedOf()
      if (!cur) return false
      removeByKey(cur.kind + ':' + cur.id)
      return true
    }
  }
  return false
}

const selectedOf = () => list.value.find((s) => s.kind + ':' + s.id === selected.value)

/** 方向键网格最近邻：主轴距离优先、副轴距离次之（Finder 桌面行为）；无选中时取首项。 */
function navigate(dir: 'up' | 'down' | 'left' | 'right') {
  const cur = selectedOf()
  if (!cur) { const first = list.value[0]; selected.value = first.kind + ':' + first.id; return }
  const horiz = dir === 'left' || dir === 'right'
  const sign = dir === 'left' || dir === 'up' ? -1 : 1
  let best: Shortcut | null = null
  let bestScore = Infinity
  for (const s of list.value) {
    if (s === cur) continue
    const main = horiz ? s.col - cur.col : s.row - cur.row
    if (main * sign <= 0) continue // 必须在目标方向上
    const score = Math.abs(main) * 1000 + Math.abs(horiz ? s.row - cur.row : s.col - cur.col)
    if (score < bestScore) { bestScore = score; best = s }
  }
  if (best) selected.value = best.kind + ':' + best.id
}

// ---- 拖拽 ----
function dragStart(e: DragEvent, s: Shortcut) {
  dragPrev.value = { ...s }
  draggingKey.value = keyOf(s)
  ghost.value = null
  e.dataTransfer?.setData('application/x-openos-shortcut', JSON.stringify(s))
  // 文件夹额外标记：图标层 dragover 据此不高亮（文件夹不可收进别的文件夹）
  if (s.kind === 'folder') e.dataTransfer?.setData('application/x-openos-folder', s.id)
}

/**
 * 桌面层 dragover（Desktop.vue 转发）：计算并显示松手后的吸附格预览。
 * 落点判据与 dropShortcut/addAt 严格一致——普通顶层项落格被其他快捷方式占用时互换
 * （落该格），其余（空闲/被小组件占用/越界/文件夹/新拖入源）走 findFree（clamp +
 * 螺旋顺延），保证预览即真实结果。悬停在图标上（合并/建夹语义）或文件夹面板内不显示。
 */
function dragOver(e: DragEvent) {
  const t = e.dataTransfer?.types
  if (!t || !(t.includes('application/x-openos-shortcut') || t.includes('application/x-openos-app')
    || t.includes('application/x-openos-entries') || t.includes('application/x-openos-paths') || t.includes('Files'))) return
  const tgt = e.target as HTMLElement | null
  // 用 target 即时判断（hoverKey 有 160ms 自灭残留，会误抑制离开图标后的预览恢复）
  if (tgt?.closest('.shortcut') || tgt?.closest('.peek-mask')) { ghost.value = null; return }
  const cell = cellUnder(e.clientX, e.clientY)
  const self = dragPrev.value
  const selfKey = self && !self.fromFolder ? `s:${self.kind}:${self.id}` : undefined
  const hit = self && self.kind !== 'folder' && !self.fromFolder
    ? list.value.find((x) => keyOf(x) !== selfKey && x.col === cell.c && x.row === cell.r)
    : null
  const fin = hit ? cell : findFree(cell.c, cell.r, 1, 1, 'shortcut', selfKey)
  // 预览格即源原位（拖回原地）：不显示
  if (self && self.col !== undefined && fin.c === self.col && fin.r === self.row) { ghost.value = null; return }
  ghost.value = fin
  clearTimeout(ghostClr)
  // 浏览器拖动静止时 dragover 约每 350ms 周期触发，超时须长于该间隔防预览闪烁
  ghostClr = window.setTimeout(() => { ghost.value = null }, 600)
}

/** 拖放会话结束（drop 路径）清理落点预览。 */
function endPreview() {
  ghost.value = null
  clearTimeout(ghostClr)
}

/** 源元素的 dragend（drop 完成或 Esc 取消都会触发）：清拖动源状态。 */
function dragEnd() {
  dragPrev.value = null
  draggingKey.value = ''
  endPreview()
}

/** 图标上的 dragover：普通源允许合并释放（preventDefault 后 drop 才会派发到此）；文件夹源放行冒泡给桌面层（走换位）。 */
function onItemDragOver(e: DragEvent, s: Shortcut) {
  if (e.dataTransfer?.types.includes('application/x-openos-folder')) return
  e.preventDefault()
  hoverKey.value = keyOf(s)
  clearTimeout(hoverClr)
  hoverClr = window.setTimeout(() => { hoverKey.value = '' }, 160)
}

/** 从拖放事件解析合并源：桌面项（可带 fromFolder）/ 启动台拖出的应用。文件夹与未知来源返回 null。 */
function readDragItem(e: DragEvent): (FolderItem & { fromFolder?: string }) | null {
  const dt = e.dataTransfer
  if (!dt) return null
  const raw = dt.getData('application/x-openos-shortcut')
  if (raw) {
    try {
      const s = JSON.parse(raw)
      if (s && (s.kind === 'app' || s.kind === 'file')) return { kind: s.kind, id: s.id, name: s.name, dir: s.dir, fromFolder: s.fromFolder }
    } catch { /* ignore */ }
    return null
  }
  const appId = dt.getData('application/x-openos-app')
  if (appId && apps.byId(appId)) return { kind: 'app', id: appId, name: apps.byId(appId)!.name }
  return null
}

/** 把源从原位置（顶层或某文件夹）摘除；源不在场（如来自启动台）时仅返回成员数据。 */
function takeOut(d: { kind: 'app' | 'file'; id: string; name: string; dir?: boolean; fromFolder?: string }): FolderItem {
  if (d.fromFolder) {
    const src = list.value.find((s) => s.kind === 'folder' && s.id === d.fromFolder)
    if (src?.items) {
      const it = src.items.find((x) => x.kind === d.kind && x.id === d.id)
      if (it) {
        src.items = src.items.filter((x) => x !== it)
        if (!src.items.length) removeFolderShell(src)
      }
      return { kind: d.kind, id: d.id, name: it?.name || d.name, dir: it?.dir ?? d.dir }
    }
  }
  const top = list.value.find((s) => s.kind === d.kind && s.id === d.id)
  if (top) {
    unregisterItem(keyOf(top))
    list.value = list.value.filter((x) => x !== top)
  }
  return { kind: d.kind, id: d.id, name: d.name, dir: d.dir }
}

/** 移除空文件夹壳（成员被全部拖走时）。 */
function removeFolderShell(f: Shortcut) {
  unregisterItem(keyOf(f))
  list.value = list.value.filter((x) => x !== f)
  if (peekId.value === f.id) peekId.value = ''
}

/** 图标上释放：目标是文件夹 = 加入；目标是应用/文件 = 自动创建文件夹收纳两者（占目标原格）。 */
function dropOnShortcut(e: DragEvent, target: Shortcut) {
  e.preventDefault()
  e.stopPropagation()
  hoverKey.value = ''
  endPreview()
  const d = readDragItem(e)
  if (!d) return
  if (d.fromFolder === target.id) return // 已在该文件夹内
  if (target.kind === 'folder') {
    if (target.items!.some((x) => x.kind === d.kind && x.id === d.id)) { toast('该应用已在文件夹中'); return }
    target.items!.push(takeOut(d))
  } else {
    if (d.kind === target.kind && d.id === target.id) return // 拖到自己身上
    const member = takeOut(d)
    const keep: FolderItem = { kind: target.kind, id: target.id, name: target.name, dir: target.dir }
    unregisterItem(keyOf(target))
    list.value = list.value.filter((x) => x !== target)
    const folder: Shortcut = {
      kind: 'folder',
      id: 'f' + Math.random().toString(36).slice(2, 10),
      name: folderNameOf(member, keep),
      col: target.col, row: target.row,
      items: [keep, member],
    }
    list.value.push(folder)
    registerItem(keyOf(folder), { c: folder.col, r: folder.row, sc: 1, sr: 1, kind: 'shortcut' })
  }
  save()
}

/** 新文件夹命名：两个成员都是应用且同类目时取类目名，否则「文件夹」。 */
function folderNameOf(a: FolderItem, b: FolderItem): string {
  if (a.kind === 'app' && b.kind === 'app') {
    const ca = apps.byId(a.id)?.category
    const cb = apps.byId(b.id)?.category
    if (ca && ca === cb) return ca
  }
  return '文件夹'
}

const peekFolder = computed(() => list.value.find((s) => s.kind === 'folder' && s.id === peekId.value))
/** 面板成员：应用须仍在应用列表（被卸载的隐藏）；文件不过滤（沿用顶层快捷方式语义）。 */
const peekItems = computed(() => (peekFolder.value?.items || []).filter((it) => it.kind === 'file' || apps.byId(it.id)))

/** 面板内成员拖出：与顶层快捷方式同一 dataTransfer 类型 + fromFolder 标记，桌面空白 drop 时移出。 */
function peekDragStart(e: DragEvent, it: FolderItem) {
  const f = peekFolder.value
  if (!f) return
  dragPrev.value = { ...it, fromFolder: f.id }
  ghost.value = null
  e.dataTransfer?.setData('application/x-openos-shortcut', JSON.stringify({ ...it, col: 0, row: 0, fromFolder: f.id }))
}

async function dropShortcut(e: DragEvent) {
  endPreview()
  const raw = e.dataTransfer?.getData('application/x-openos-shortcut')
  if (!raw) return
  try {
    const s = JSON.parse(raw) as Shortcut & { fromFolder?: string }
    const cell = cellUnder(e.clientX, e.clientY)
    if (s.kind === 'folder') {
      // 文件夹换位（文件夹只能落在桌面空白，图标上释放被 dropOnShortcut 吞掉）
      const item = list.value.find((x) => x.kind === 'folder' && x.id === s.id)
      if (item) { place(item, cell.c, cell.r); save() }
      return
    }
    if (s.fromFolder) {
      // 从文件夹面板拖到桌面空白：移出文件夹并吸附落点（folder 分支已 return，此处必为成员）
      const it = takeOut(s as FolderItem & { fromFolder?: string })
      scatter(it, cell.c, cell.r)
      save()
      return
    }
    const item = list.value.find((x) => x.kind === s.kind && x.id === s.id)
    if (!item) return
    // 目标格被其他快捷方式占用时互换；被小组件占用或越界时搜索最近空位
    const rect = { c: cell.c, r: cell.r, sc: 1, sr: 1 }
    if (isFree(rect, keyOf(item))) {
      place(item, cell.c, cell.r)
    } else {
      const hit = list.value.find((x) => x !== item && x.col === cell.c && x.row === cell.r)
      if (hit) {
        const from = { col: item.col, row: item.row }
        unregisterItem(keyOf(item))
        item.col = cell.c; item.row = cell.r
        registerItem(keyOf(item), { c: item.col, r: item.row, sc: 1, sr: 1, kind: 'shortcut' })
        unregisterItem(keyOf(hit))
        hit.col = from.col; hit.row = from.row
        registerItem(keyOf(hit), { c: hit.col, r: hit.row, sc: 1, sr: 1, kind: 'shortcut' })
      } else {
        place(item, cell.c, cell.r)
      }
    }
    save()
  } catch { /* ignore */ }
}
</script>

<template>
  <div class="shortcuts" @pointerdown.self="selected = ''">
    <div
      v-for="s in list" :key="s.kind + ':' + s.id"
      class="shortcut" :class="{ sel: selected === s.kind + ':' + s.id, 'drop-target': hoverKey === keyOf(s), dragging: draggingKey === keyOf(s) }"
      :data-id="s.kind + ':' + s.id"
      :style="style(s)"
      draggable="true"
      @pointerdown="selected = s.kind + ':' + s.id"
      @dblclick="open(s)"
      @dragstart="dragStart($event, s)"
      @dragend="dragEnd"
      @dragover="onItemDragOver($event, s)"
      @drop="dropOnShortcut($event, s)"
      :title="s.kind === 'folder'
        ? `${s.name}（双击打开，拖图标到上面收入）`
        : `${s.name}（双击打开，拖到其他图标上合并成文件夹）`"
    >
      <template v-if="s.kind === 'app'">
        <AppIcon :app="s.id" :size="48" />
      </template>
      <FolderIcon v-else-if="s.kind === 'folder'" :entries="s.items || []" :size="48" />
      <template v-else>
        <span class="file-icon">{{ fileEmoji(s.id, s.dir) }}</span>
      </template>
      <span class="name">{{ s.name }}</span>
    </div>

    <!-- 拖动落点预览：松手后图标将吸附的格子（本组件源显示半透明克隆，跨源拖入仅虚线框） -->
    <div v-if="ghost" class="ghost" :class="{ 'has-src': !!dragPrev }" :style="ghostStyle" aria-hidden="true">
      <template v-if="dragPrev">
        <AppIcon v-if="dragPrev.kind === 'app'" :app="dragPrev.id" :size="48" />
        <FolderIcon v-else-if="dragPrev.kind === 'folder'" :entries="dragPrev.items || []" :size="48" />
        <span v-else class="file-icon">{{ fileEmoji(dragPrev.id, dragPrev.dir) }}</span>
        <span class="name">{{ dragPrev.name }}</span>
      </template>
    </div>

    <!-- 文件夹内容面板：改名 / 点击打开 / 拖出成员移出（遮罩点击关闭） -->
    <div v-if="peekFolder" class="peek-mask" @pointerdown.self="peekId = ''" @contextmenu.prevent.stop>
      <div class="peek" role="dialog" aria-modal="true" :aria-label="peekFolder.name">
        <input
          ref="peekNameRef" v-model="peekFolder.name" class="peek-name"
          aria-label="文件夹名称" spellcheck="false"
          @keydown.enter="($event.target as HTMLInputElement).blur()" @change="save()"
        >
        <div class="peek-grid">
          <button
            v-for="it in peekItems" :key="it.kind + ':' + it.id" class="peek-item"
            draggable="true" :title="`${it.name}（点击打开 · 拖出到桌面移出文件夹）`"
            @click="openItem(it)" @dragstart="peekDragStart($event, it)" @dragend="dragEnd"
          >
            <AppIcon v-if="it.kind === 'app'" :app="it.id" :size="56" />
            <span v-else class="file-icon">{{ fileEmoji(it.id, it.dir) }}</span>
            <span class="name">{{ it.name }}</span>
          </button>
          <div v-if="!peekItems.length" class="peek-empty">文件夹是空的，拖图标进来吧</div>
        </div>
        <p class="peek-hint">点击打开 · 拖出图标到桌面空白处移出 · 拖到其他文件夹上移动</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.shortcuts { position: absolute; inset: 0; pointer-events: none; }
.shortcut {
  position: absolute; display: flex; flex-direction: column; align-items: center; gap: 5px;
  padding: 8px 4px; border-radius: 10px; pointer-events: auto; cursor: default;
  border: 1px solid transparent;
}
.shortcut:hover { background: rgba(255,255,255,.09); }
.shortcut.sel { background: rgba(10,132,255,.3); border-color: rgba(10,132,255,.55); }
/* 拖动中的源图标本体：压暗提示已离位（落点由 .ghost 预览指示） */
.shortcut.dragging { opacity: .35; }
/* 拖动落点预览（虚拟对齐位置）：虚线格框；有源数据时叠加半透明图标克隆 */
.ghost {
  position: absolute; display: flex; flex-direction: column; align-items: center; gap: 5px;
  padding: 8px 4px; border-radius: 10px; pointer-events: none; z-index: 4;
  border: 1.5px dashed rgba(255,255,255,.5); background: rgba(255,255,255,.07);
}
.ghost.has-src { opacity: .55; border-color: rgba(255,255,255,.32); }
/* 合并目标高亮：macOS 式半透明圆圈罩住图标 */
.shortcut.drop-target { z-index: 5; }
.shortcut.drop-target::after {
  content: ''; position: absolute; left: 50%; top: 32px;
  width: 68px; height: 68px; transform: translate(-50%, -50%);
  border-radius: 50%; pointer-events: none;
  background: rgba(255,255,255,.22); border: 1.5px solid rgba(255,255,255,.5);
  box-shadow: 0 0 0 6px rgba(255,255,255,.08);
}
.file-icon {
  width: 48px; height: 48px; display: flex; align-items: center; justify-content: center;
  font-size: 40px; line-height: 1;
  filter: drop-shadow(0 2px 4px rgba(0,0,0,.35));
}
.name {
  font-size: 12px; color: #fff; text-align: center; line-height: 1.3; word-break: break-all;
  text-shadow: 0 1px 3px rgba(0,0,0,.65); max-height: 2.6em; overflow: hidden;
}

/* 文件夹内容面板（遮罩层级高于窗口层、低于系统级 DialogHost） */
.peek-mask {
  position: absolute; inset: 0; z-index: 20000; pointer-events: auto;
  display: flex; align-items: center; justify-content: center;
  background: rgba(0, 0, 0, .22); backdrop-filter: blur(3px);
  animation: os-fade-in .14s ease;
}
.peek {
  width: 400px; max-width: 88vw; padding: 16px 20px 12px;
  border-radius: 22px; border: 1px solid rgba(255,255,255,.14);
  background: rgba(38, 40, 46, .78);
  backdrop-filter: blur(30px) saturate(1.4);
  -webkit-backdrop-filter: blur(30px) saturate(1.4);
  box-shadow: 0 24px 70px rgba(0,0,0,.45);
  animation: os-pop-in .18s var(--os-ease-spring);
}
.peek-name {
  display: block; width: 220px; margin: 0 auto 12px; padding: 4px 10px;
  border-radius: 8px; border: 1px solid transparent;
  background: transparent; color: #fff; font-size: 14px; text-align: center;
  transition: background .15s, border-color .15s;
}
.peek-name:hover { background: rgba(255,255,255,.1); }
.peek-name:focus { background: rgba(255,255,255,.16); border-color: rgba(10,132,255,.6); outline: none; }
.peek-grid {
  display: grid; grid-template-columns: repeat(5, 1fr); gap: 14px 6px;
  min-height: 100px; align-content: start;
}
.peek-item {
  display: flex; flex-direction: column; align-items: center; gap: 7px;
  padding: 8px 2px; border: none; border-radius: 12px; background: transparent; cursor: pointer;
  transition: background .15s, transform .16s var(--os-ease-spring);
}
.peek-item:hover { background: rgba(255,255,255,.1); transform: scale(1.05); }
.peek-item .file-icon { width: 56px; height: 56px; font-size: 44px; }
.peek-item .name {
  font-size: 12px; max-height: 2.6em; overflow: hidden;
}
.peek-empty {
  grid-column: 1 / -1; padding: 26px 0; text-align: center;
  color: var(--os-text-secondary); font-size: 13px;
}
.peek-hint { margin: 12px 0 0; text-align: center; font-size: 11px; color: rgba(255,255,255,.45); }
</style>
