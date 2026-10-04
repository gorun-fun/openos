<script setup lang="ts">
// 桌面快捷方式：应用/文件/文件夹图标，双击打开（文件夹弹出内容面板）；可从启动台
// 拖入创建，也可拖入操作系统文件（自动上传到个人空间并生成快捷方式）。
// 位置为全桌面网格（左上角锚定），释放后吸附格子并与其他快捷方式/小组件互不覆盖。
// 拖动图标到另一图标上释放 = 自动创建文件夹收纳两者（macOS 行为）；文件夹成员可
// 拖出到桌面空白处移出、拖到其他文件夹图标上移动；右键可重命名 / 解散（成员散回桌面）。
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useWindows } from '../stores/windows'
import { useApps } from '../stores/apps'
import { useSession } from '../stores/session'
import { openPath, fileEmoji } from '../apps/openWith'
import AppIcon from '../ui/AppIcon.vue'
import FolderIcon from '../ui/FolderIcon.vue'
import { toast } from '../ui/toast'
import { GRID, cellUnder, findFree, isFree, posOf, registerItem, unregisterItem } from './grid'

export interface FolderItem { kind: 'app' | 'file'; id: string; name: string }
export interface Shortcut {
  kind: 'app' | 'file' | 'folder'
  id: string        // app id / 文件绝对路径 / 文件夹随机 id（f 前缀）
  name: string
  col: number
  row: number
  items?: FolderItem[] // 文件夹专属：成员列表
}

const windows = useWindows()
const apps = useApps()
const session = useSession()
/** v2：全桌面网格（左上角锚定）。旧版以右上角为原点、仅右侧 5 列，首次加载时迁移。 */
const KEY = () => `openos_desktop_v2_${session.user!.username}`
const OLD_KEY = () => `openos_desktop_${session.user!.username}`

const list = ref<Shortcut[]>([])
const selected = ref('')
/** 拖动悬停的合并目标（图标上高亮圆圈；dragover 持续续期，离开后短延时自灭）。 */
const hoverKey = ref('')
let hoverClr = 0
/** 打开内容面板（peek）的文件夹 id。 */
const peekId = ref('')
const peekNameRef = ref<HTMLInputElement>()

const keyOf = (s: { kind: string; id: string }) => `s:${s.kind}:${s.id}`

onMounted(() => {
  try {
    const v2 = localStorage.getItem(KEY())
    if (v2) {
      const arr = JSON.parse(v2)
      // folder 项兜底 items（旧数据不含文件夹，防手改数据缺键）
      list.value = (Array.isArray(arr) ? arr : []).map((s: Shortcut) =>
        s.kind === 'folder' ? { ...s, items: s.items || [] } : s)
    } else {
      // 旧格式（右上角锚定）→ 左上角锚定列
      const old: any[] = JSON.parse(localStorage.getItem(OLD_KEY()) || '[]')
      list.value = old.map((s) => {
        const oldLeft = window.innerWidth - 16 - s.col * 92 - 82
        return { ...s, col: Math.max(0, Math.round((oldLeft - GRID.left) / 92)) }
      })
    }
  } catch { list.value = [] }
  for (const s of list.value) registerItem(keyOf(s), { c: s.col, r: s.row, sc: 1, sr: 1, kind: 'shortcut' })
  save()
})

// 默认桌面：应用列表就绪后，为每个用户一次性种入 goder 快捷方式（首个空位）。
// 子组件挂载早于 Desktop 的 apps.refresh 完成，故 watch 等列表就绪再种；
// 未授权角色（如 guest）的 /api/apps 不含 goder，byId 为空自动跳过；
// 以 localStorage 标记保证只种一次——用户主动移除后不会自动恢复。
// 注意 stopSeed 用 let + 可选调用：immediate 回调同步执行时若 apps 已就绪（如
// 移动↔桌面形态切换后的二次挂载）会立刻走到自停逻辑，const 赋值尚未完成会触发
// TDZ ReferenceError 中断整个 Desktop 的 patch。
const SEEDED_KEY = () => `openos_desktop_goder_seeded_${session.user!.username}`
let stopSeed: (() => void) | undefined
stopSeed = watch(() => apps.apps.length, (n) => {
  if (!n) return
  stopSeed?.()
  if (localStorage.getItem(SEEDED_KEY())) return
  localStorage.setItem(SEEDED_KEY(), '1')
  const app = apps.byId('goder')
  if (app && !list.value.some((s) => s.kind === 'app' && s.id === 'goder')) {
    const s: Shortcut = { kind: 'app', id: 'goder', name: app.name, col: 0, row: 0 }
    list.value.push(s)
    place(s, 0, 0)
    save()
  }
}, { immediate: true })

function save() { localStorage.setItem(KEY(), JSON.stringify(list.value)) }

function place(s: Shortcut, c: number, r: number) {
  const rect = findFree(c, r, 1, 1, 'shortcut', keyOf(s))
  s.col = rect.c
  s.row = rect.r
  registerItem(keyOf(s), { c: s.col, r: s.row, sc: 1, sr: 1, kind: 'shortcut' })
}

/** 在桌面坐标处添加快捷方式（网格吸附，冲突时顺延到最近空位）。 */
function addAt(kind: Shortcut['kind'], id: string, name: string, x: number, y: number) {
  if (list.value.some((s) => s.kind === kind && s.id === id)) return
  const cell = cellUnder(x, y)
  const s: Shortcut = { kind, id, name, col: 0, row: 0 }
  list.value.push(s)
  place(s, cell.c, cell.r)
  save()
}

/** 供外部（桌面拖放 / 右键菜单）调用。 */
defineExpose({ addAt, addAppShortcut, dropShortcut, openByKey, removeByKey, relayout, renameFolder })

function style(s: Shortcut) {
  const p = posOf({ c: s.col, r: s.row })
  return { left: p.x + 5 + 'px', top: p.y + 6 + 'px', width: GRID.cw - 10 + 'px' }
}

// ---- 打开 / 移除 ----
function open(s: Shortcut) {
  if (s.kind === 'folder') { peekId.value = s.id; return }
  if (s.kind === 'app') windows.open(s.id)
  else openPath(s.id) // 统一关联表：md→文档、csv→表格、html→幻灯片、图片→预览
}

function openItem(it: FolderItem) {
  if (it.kind === 'app') windows.open(it.id)
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
  const ns: Shortcut = { kind: it.kind, id: it.id, name: it.name, col: 0, row: 0 }
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

// ---- 拖拽 ----
function dragStart(e: DragEvent, s: Shortcut) {
  e.dataTransfer?.setData('application/x-openos-shortcut', JSON.stringify(s))
  // 文件夹额外标记：图标层 dragover 据此不高亮（文件夹不可收进别的文件夹）
  if (s.kind === 'folder') e.dataTransfer?.setData('application/x-openos-folder', s.id)
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
      if (s && (s.kind === 'app' || s.kind === 'file')) return { kind: s.kind, id: s.id, name: s.name, fromFolder: s.fromFolder }
    } catch { /* ignore */ }
    return null
  }
  const appId = dt.getData('application/x-openos-app')
  if (appId && apps.byId(appId)) return { kind: 'app', id: appId, name: apps.byId(appId)!.name }
  return null
}

/** 把源从原位置（顶层或某文件夹）摘除；源不在场（如来自启动台）时仅返回成员数据。 */
function takeOut(d: { kind: 'app' | 'file'; id: string; name: string; fromFolder?: string }): FolderItem {
  if (d.fromFolder) {
    const src = list.value.find((s) => s.kind === 'folder' && s.id === d.fromFolder)
    if (src?.items) {
      const it = src.items.find((x) => x.kind === d.kind && x.id === d.id)
      if (it) {
        src.items = src.items.filter((x) => x !== it)
        if (!src.items.length) removeFolderShell(src)
      }
      return { kind: d.kind, id: d.id, name: it?.name || d.name }
    }
  }
  const top = list.value.find((s) => s.kind === d.kind && s.id === d.id)
  if (top) {
    unregisterItem(keyOf(top))
    list.value = list.value.filter((x) => x !== top)
  }
  return { kind: d.kind, id: d.id, name: d.name }
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
  const d = readDragItem(e)
  if (!d) return
  if (d.fromFolder === target.id) return // 已在该文件夹内
  if (target.kind === 'folder') {
    if (target.items!.some((x) => x.kind === d.kind && x.id === d.id)) { toast('该应用已在文件夹中'); return }
    target.items!.push(takeOut(d))
  } else {
    if (d.kind === target.kind && d.id === target.id) return // 拖到自己身上
    const member = takeOut(d)
    const keep: FolderItem = { kind: target.kind, id: target.id, name: target.name }
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
  e.dataTransfer?.setData('application/x-openos-shortcut', JSON.stringify({ ...it, col: 0, row: 0, fromFolder: f.id }))
}

async function dropShortcut(e: DragEvent) {
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
      class="shortcut" :class="{ sel: selected === s.kind + ':' + s.id, 'drop-target': hoverKey === keyOf(s) }"
      :data-id="s.kind + ':' + s.id"
      :style="style(s)"
      draggable="true"
      @pointerdown="selected = s.kind + ':' + s.id"
      @dblclick="open(s)"
      @dragstart="dragStart($event, s)"
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
        <span class="file-icon">{{ fileEmoji(s.id) }}</span>
      </template>
      <span class="name">{{ s.name }}</span>
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
            @click="openItem(it)" @dragstart="peekDragStart($event, it)"
          >
            <AppIcon v-if="it.kind === 'app'" :app="it.id" :size="56" />
            <span v-else class="file-icon">{{ fileEmoji(it.id) }}</span>
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
