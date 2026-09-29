<script setup lang="ts">
// 桌面快捷方式：应用/文件图标，双击打开，右键菜单移除；可从启动台拖入创建，
// 也可拖入操作系统文件（自动上传到个人空间并生成快捷方式）。
// 位置为全桌面网格（左上角锚定），释放后吸附格子并与其他快捷方式/小组件互不覆盖。
import { onMounted, ref } from 'vue'
import { useWindows } from '../stores/windows'
import { useApps } from '../stores/apps'
import { useSession } from '../stores/session'
import { fsApi } from '../api/client'
import AppIcon from '../ui/AppIcon.vue'
import { GRID, cellUnder, findFree, isFree, posOf, registerItem, unregisterItem } from './grid'

export interface Shortcut {
  kind: 'app' | 'file'
  id: string        // app id 或文件绝对路径
  name: string
  col: number
  row: number
}

const windows = useWindows()
const apps = useApps()
const session = useSession()
/** v2：全桌面网格（左上角锚定）。旧版以右上角为原点、仅右侧 5 列，首次加载时迁移。 */
const KEY = () => `openos_desktop_v2_${session.user!.username}`
const OLD_KEY = () => `openos_desktop_${session.user!.username}`

const list = ref<Shortcut[]>([])
const selected = ref('')

const keyOf = (s: { kind: string; id: string }) => `s:${s.kind}:${s.id}`

onMounted(() => {
  try {
    const v2 = localStorage.getItem(KEY())
    if (v2) {
      list.value = JSON.parse(v2)
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

function save() { localStorage.setItem(KEY(), JSON.stringify(list.value)) }

function extOf(p: string) { return (p.match(/\.([a-z0-9]+)$/i)?.[1] || '').toLowerCase() }
const APP_FOR_EXT: Record<string, string> = {
  txt: 'docs', md: 'docs', html: 'docs', json: 'docs',
  csv: 'sheets', tsv: 'sheets', pdf: 'pdf',
}
const FILE_EMOJI: Record<string, string> = {
  md: '📄', txt: '📄', pdf: '📕', csv: '📊', html: '🌐', png: '🖼️', jpg: '🖼️', jpeg: '🖼️', gif: '🖼️',
}

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
defineExpose({ addAt, addAppShortcut, dropShortcut, openByKey, removeByKey, relayout })

function style(s: Shortcut) {
  const p = posOf({ c: s.col, r: s.row })
  return { left: p.x + 5 + 'px', top: p.y + 6 + 'px', width: GRID.cw - 10 + 'px' }
}

function open(s: Shortcut) {
  if (s.kind === 'app') {
    windows.open(s.id)
  } else {
    windows.open(APP_FOR_EXT[extOf(s.id)] || 'docs', { path: s.id }, s.name)
  }
}

/** 桌面右键菜单调用：按唯一键（kind:id，见 data-id）打开 / 移除。 */
function openByKey(key: string) {
  const s = list.value.find((x) => x.kind + ':' + x.id === key)
  if (s) open(s)
}
function removeByKey(key: string) {
  const s = list.value.find((x) => x.kind + ':' + x.id === key)
  if (!s) return
  unregisterItem(keyOf(s))
  list.value = list.value.filter((x) => x !== s)
  selected.value = ''
  save()
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

function dragStart(e: DragEvent, s: Shortcut) {
  e.dataTransfer?.setData('application/x-openos-shortcut', JSON.stringify(s))
}

async function dropShortcut(e: DragEvent) {
  const raw = e.dataTransfer?.getData('application/x-openos-shortcut')
  if (!raw) return
  try {
    const s = JSON.parse(raw) as Shortcut
    const item = list.value.find((x) => x.kind === s.kind && x.id === s.id)
    if (!item) return
    const cell = cellUnder(e.clientX, e.clientY)
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
      class="shortcut" :class="{ sel: selected === s.kind + ':' + s.id }"
      :data-id="s.kind + ':' + s.id"
      :style="style(s)"
      draggable="true"
      @pointerdown="selected = s.kind + ':' + s.id"
      @dblclick="open(s)"
      @dragstart="dragStart($event, s)"
      :title="`${s.name}（双击打开，可拖动换位）`"
    >
      <template v-if="s.kind === 'app'">
        <AppIcon :app="s.id" :size="48" />
      </template>
      <template v-else>
        <span class="file-icon">{{ FILE_EMOJI[extOf(s.id)] || '📄' }}</span>
      </template>
      <span class="name">{{ s.name }}</span>
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
.file-icon {
  width: 48px; height: 48px; display: flex; align-items: center; justify-content: center;
  font-size: 40px; line-height: 1;
  filter: drop-shadow(0 2px 4px rgba(0,0,0,.35));
}
.name {
  font-size: 12px; color: #fff; text-align: center; line-height: 1.3; word-break: break-all;
  text-shadow: 0 1px 3px rgba(0,0,0,.65); max-height: 2.6em; overflow: hidden;
}
</style>
