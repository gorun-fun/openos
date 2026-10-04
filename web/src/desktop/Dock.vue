<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useApps } from '../stores/apps'
import { useWindows } from '../stores/windows'
import { useDesktop } from '../stores/desktop'
import { useDock } from '../stores/dock'
import { useSession } from '../stores/session'
import { fsApi, type AppDef } from '../api/client'
import { bus } from '../bus/bus'
import { toast } from '../ui/toast'
import { useMenu, menuPos } from '../ui/menu'
import AppIcon from '../ui/AppIcon.vue'

const emit = defineEmits<{ launchpad: [] }>()
const apps = useApps()
const windows = useWindows()
const desktop = useDesktop()
const dockStore = useDock()
const session = useSession()

onMounted(() => dockStore.load())

/** Dock 应用：固定应用按用户顺序 + 未固定的运行中应用临时驻留末尾（macOS 行为）。 */
const dockApps = computed<AppDef[]>(() => {
  const map = new Map(apps.visible.map((a) => [a.id, a]))
  const list: AppDef[] = []
  for (const id of dockStore.effective) {
    const a = map.get(id)
    if (a) list.push(a)
  }
  if (dockStore.pinned != null) {
    for (const a of apps.visible) {
      if (windows.runningAppIds.includes(a.id) && !dockStore.effectiveSet.has(a.id)) list.push(a)
    }
  }
  return list
})

function dockClick(appId: string) {
  const wins = windows.list.filter((w) => w.appId === appId)
  if (wins.length === 0) {
    windows.open(appId)
  } else {
    // 始终聚焦最前窗口（最小化的同时还原）；已在前台则保持不动，不再触发最小化
    windows.focus(wins.sort((a, b) => b.z - a.z)[0].id)
  }
}

// ---- 拖拽定制：拖动排序 / 拖出取消固定 / 从启动台或桌面快捷方式拖入 ----
// 内部拖动：dragover 实时重排 preview（macOS 跟手），松手提交；拖出 Dock 一段
// 距离释放 = 取消固定（Esc 取消时 dropEffect 为 none，不会误删）。外部拖入：
// dragover 阶段读不到数据（保护模式），以半透明插槽指示落点，drop 时读取固定。
const DOCK_MIME = 'application/x-openos-dock'
const APP_MIME = 'application/x-openos-app'
const SHORTCUT_MIME = 'application/x-openos-shortcut'

const drag = ref<{ id: string; internal: boolean } | null>(null)
const preview = ref<string[] | null>(null) // 内部拖动的实时顺序（松手才提交）
const slotIdx = ref(-1)                    // 外部拖入的落点插槽（显示列表下标）
let droppedInside = false                  // 本轮拖动是否落在了 Dock 内

const displayApps = computed<AppDef[]>(() => {
  const ids = preview.value ?? dockApps.value.map((a) => a.id)
  return ids.map((id) => apps.byId(id)).filter((a): a is AppDef => !!a)
})

function wantedDrag(dt: DataTransfer | null): boolean {
  return !!dt && [DOCK_MIME, APP_MIME, SHORTCUT_MIME].some((m) => dt.types.includes(m))
}

function itemDragStart(e: DragEvent, appId: string) {
  e.dataTransfer?.setData(DOCK_MIME, appId)
  if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
  drag.value = { id: appId, internal: true }
  preview.value = null
  droppedInside = false
  mouseX = null
  tip.value = null
  requestAnimationFrame(applyMagnify) // 收起放大与提示，避免拖拽过程中残留
}

/** 指针 x 应插入到哪个应用项之前（应用项 DOM 顺序 = 显示顺序）。 */
function indexAt(x: number): number {
  const els = dockEl.value?.querySelectorAll<HTMLElement>('.dock-item[data-app]') || []
  for (let i = 0; i < els.length; i++) {
    const r = els[i].getBoundingClientRect()
    if (x < r.left + r.width / 2) return i
  }
  return els.length
}

/** 落点之前固定应用的数量 = 固定列表中的目标下标（临时驻留的运行应用不入列）。 */
function pinnedIndexBefore(displayIdx: number, excludeId?: string): number {
  return displayApps.value
    .slice(0, Math.max(0, displayIdx))
    .filter((a) => a.id !== excludeId && dockStore.effectiveSet.has(a.id))
    .length
}

function onDockDragOver(e: DragEvent) {
  if (!wantedDrag(e.dataTransfer)) return
  e.preventDefault()
  const dt = e.dataTransfer!
  dt.dropEffect = dt.effectAllowed === 'copy' || dt.effectAllowed === 'copyLink' ? 'copy' : 'move'
  const idx = indexAt(e.clientX)
  if (drag.value?.internal) {
    const cur = preview.value ?? dockApps.value.map((a) => a.id)
    const from = cur.indexOf(drag.value.id)
    if (from >= 0 && from !== idx) {
      const arr = [...cur]
      arr.splice(from, 1)
      arr.splice(from < idx ? idx - 1 : idx, 0, drag.value.id)
      preview.value = arr
    }
  } else {
    drag.value = { id: '', internal: false } // 外部拖入标记：控制插槽渲染（全局 dragend 清理）
    slotIdx.value = idx
  }
}

function onDockDragLeave(e: DragEvent) {
  const rt = e.relatedTarget as Node | null
  if (!rt || !dockEl.value?.contains(rt)) slotIdx.value = -1
}

function onDockDrop(e: DragEvent) {
  const dt = e.dataTransfer
  if (!dt || !wantedDrag(dt)) return
  e.preventDefault()
  e.stopPropagation() // 阻断冒泡到桌面层（否则又被当作「拖到桌面创建快捷方式」）
  droppedInside = true
  if (drag.value?.internal) {
    const id = drag.value.id
    const wasPinned = dockStore.effectiveSet.has(id)
    dockStore.pinAt(id, pinnedIndexBefore(displayApps.value.findIndex((a) => a.id === id), id))
    if (!wasPinned) toast(`已在 Dock 中保留「${apps.byId(id)?.name || id}」`)
  } else {
    let appId = ''
    if (dt.types.includes(APP_MIME)) {
      appId = dt.getData(APP_MIME)
    } else if (dt.types.includes(SHORTCUT_MIME)) {
      try {
        const s = JSON.parse(dt.getData(SHORTCUT_MIME))
        if (s?.kind === 'app') appId = String(s.id)
        else toast('文件快捷方式暂不支持固定到 Dock')
      } catch { /* 数据损坏忽略 */ }
    }
    if (appId && apps.byId(appId)) {
      // 插入点之前的固定应用数 = 固定列表下标；slotIdx 异常时兜底追加到末尾
      const idx = slotIdx.value >= 0 ? pinnedIndexBefore(slotIdx.value) : dockStore.effective.length
      dockStore.pinAt(appId, idx)
      toast(`已固定「${apps.byId(appId)!.name}」到 Dock`)
    }
  }
  drag.value = null
  preview.value = null
  slotIdx.value = -1
}

function itemDragEnd(e: DragEvent) {
  const d = drag.value
  if (!d?.internal || droppedInside) return
  // 拖出 Dock 释放（且非 Esc 取消）→ 取消固定；Dock 边缘留缓冲带防误触
  const r = dockEl.value?.getBoundingClientRect()
  const outside = !!r && (e.clientY < r.top - 36 || e.clientX < r.left - 48 || e.clientX > r.right + 48)
  if (outside && e.dataTransfer?.dropEffect !== 'none') {
    const name = apps.byId(d.id)?.name || d.id
    dockStore.unpin(d.id)
    toast(`已从 Dock 移除「${name}」（启动台可重新固定）`)
  }
}

// 外部拖入（启动台/快捷方式）的 dragend 在源元素上触发，Dock 收不到，全局兜底清理
function onGlobalDragEnd() {
  drag.value = null
  preview.value = null
  slotIdx.value = -1
}
onMounted(() => window.addEventListener('dragend', onGlobalDragEnd))
onBeforeUnmount(() => window.removeEventListener('dragend', onGlobalDragEnd))

// ---- 右键菜单：打开 / 固定切换（显示权归全局菜单单例 ui/menu.ts） ----
const itemMenu = useMenu('dock-item')
const menuAppId = ref('')
const itemMenuPos = ref({ x: 0, y: 0 })
const itemMenuEl = ref<HTMLElement>()

function onItemContext(e: MouseEvent, appId: string) {
  e.preventDefault()
  e.stopPropagation()
  menuAppId.value = appId
  itemMenu.open(e.clientX, e.clientY)
}

function menuKeep() {
  const id = menuAppId.value
  itemMenu.close()
  if (id) dockStore.pinAt(id, dockStore.effective.length)
}

function menuUnpin() {
  const id = menuAppId.value
  itemMenu.close()
  if (id) dockStore.unpin(id)
}

// 菜单开着时再右键另一项 = 原地换目标（owner 不变，靠 menuPos 触发）；
// Dock 在屏幕底部，菜单位置在弹出点基础上向上展开并夹在视口内。
watch([() => itemMenu.isOpen, menuPos], ([open]) => {
  if (!open) { menuAppId.value = ''; return }
  itemMenuPos.value = { ...menuPos.value }
  void nextTick(() => {
    const el = itemMenuEl.value
    if (!el) return
    const r = el.getBoundingClientRect()
    itemMenuPos.value = {
      x: Math.max(6, Math.min(menuPos.value.x, window.innerWidth - r.width - 6)),
      y: Math.max(32, menuPos.value.y - r.height),
    }
  })
})

// ---- 自适应缩小：图标过多时等比缩小图标与间距，保证 Dock 恒为一排（macOS 行为）----
// 缩放因子 k 写入根元素 --dock-k（图标/内边距/gap 统一乘 k，总宽随 k 线性变化），
// AppIcon 尺寸是 prop 故单独用 iconSize 跟随。宽度测量走离屏克隆（强制基准尺寸），
// 避免重置真实 DOM 再测量造成的视觉闪烁。
const dockEl = ref<HTMLElement>()
const BASE_ICON = 42
const iconSize = ref(BASE_ICON)

function measureNatural(): number {
  const root = dockEl.value
  if (!root) return 0
  const clone = root.cloneNode(true) as HTMLElement
  clone.style.cssText += 'position:fixed;left:-9999px;top:0;visibility:hidden;max-width:none;'
  clone.style.setProperty('--dock-k', '1')
  // 克隆里 AppIcon 仍是当前 iconSize 的内联 px，归位基准；清掉邻居放大的 transform 残留
  for (const el of clone.querySelectorAll<HTMLElement>('.app-icon')) {
    el.style.width = el.style.height = BASE_ICON + 'px'
  }
  for (const el of clone.querySelectorAll<HTMLElement>('.dock-item')) el.style.transform = ''
  document.body.appendChild(clone)
  const w = clone.getBoundingClientRect().width
  clone.remove()
  return w
}

function refit() {
  const root = dockEl.value
  if (!root) return
  const natural = measureNatural()
  if (!natural) return
  const avail = window.innerWidth * 0.92 // 与 .dock 的 max-width 一致
  // Dock 总宽 = 常数项（border/分隔线）+ 线性项（图标/间距），解一元一次得 k，上限 1（只缩不放）
  const k = Math.min(1, (avail - 4) / Math.max(1, natural - 4))
  root.style.setProperty('--dock-k', String(k))
  iconSize.value = Math.round(BASE_ICON * k * 10) / 10
}

// ---- 邻居放大（macOS Dock）：指针距离连续衰减，直接写 DOM 避免 Vue 重渲抖动 ----
let rafId = 0
let mouseX: number | null = null

function applyMagnify() {
  rafId = 0
  const root = dockEl.value
  if (!root) return
  const items = root.querySelectorAll<HTMLElement>('.dock-item')
  const radius = Math.max(70, iconSize.value * 3.6) // 影响半径随图标缩小收敛，避免小图标一次放大一串
  for (const el of items) {
    if (mouseX == null) {
      el.style.transform = ''
      continue
    }
    const rect = el.getBoundingClientRect()
    // 用未放大时的基准位置计算（放大后的 rect 会漂移，需去抖：以 rect 中心近似 + 宽度还原）
    const base = rect.left + rect.width / 2
    const dist = Math.abs(base - mouseX)
    const influence = Math.max(0, 1 - dist / radius)
    const scale = 1 + 0.42 * Math.pow(influence, 1.7)
    const lift = -(scale - 1) * 24 // 放大同时向上抬起
    el.style.transform = `translateY(${lift.toFixed(1)}px) scale(${scale.toFixed(3)})`
  }
}

function onMove(e: PointerEvent) {
  mouseX = e.clientX
  if (!rafId) rafId = requestAnimationFrame(applyMagnify)
}
function onLeave() {
  mouseX = null
  tip.value = null
  if (!rafId) rafId = requestAnimationFrame(applyMagnify)
}

// ---- 悬停提示：图标上方显示应用名称（替代原生 title，跟随放大动画不漂移） ----
const tip = ref<{ text: string; x: number } | null>(null)

function onOver(e: PointerEvent) {
  const item = (e.target as HTMLElement).closest<HTMLElement>('.dock-item')
  const name = item?.dataset.name
  if (!item || !name) { tip.value = null; return }
  const r = item.getBoundingClientRect() // 放大以 bottom center 为基准，中心 x 不漂移
  const dockRect = dockEl.value?.getBoundingClientRect()
  tip.value = { text: name, x: r.left + r.width / 2 - (dockRect?.left || 0) }
}

// ---- 废纸篓（Dock 右侧分区；文件系统关闭时隐藏） ----
const trashPath = computed(() => `/home/${session.user?.username || ''}/.Trash`)
const trashFull = ref(false)
const hasFilesApp = computed(() => !!apps.byId('files'))

async function loadTrash() {
  if (!hasFilesApp.value) return
  try { trashFull.value = (await fsApi.list(trashPath.value)).length > 0 } catch { trashFull.value = false }
}
const offTrash = bus.on('fs:changed', () => { loadTrash() })
onMounted(loadTrash)
onBeforeUnmount(offTrash)

function openTrash() {
  windows.open('files', { path: trashPath.value }, '废纸篓')
}
async function emptyTrash() {
  trashMenu.close()
  try {
    for (const k of await fsApi.list(trashPath.value)) await fsApi.remove(k.path)
    toast('废纸篓已清倒')
    loadTrash()
  } catch (e: any) { toast(e.message, { type: 'error' }) }
}
// 显示权归全局菜单单例（ui/menu.ts）：右键废纸篓弹出，点外部/Esc 统一关闭
const trashMenu = useMenu('dock-trash')

// ---- 自动隐藏（macOS「最大化时隐藏 Dock」）----
// 有最大化窗口且开启开关时，Dock 沉到屏幕外腾出全高空间；
// 仅当鼠标贴到屏幕最底边（3px 内）才滑出，离开 Dock 本体即收回，
// 不设上方热区，避免遮挡最大化窗口的底部操作区。
const reveal = ref(false)
const autoHidden = computed(() =>
  desktop.dockAutoHide && windows.list.some((w) => w.maximized) && !reveal.value,
)

function onWinPointerMove(e: PointerEvent) {
  if (!desktop.dockAutoHide || !windows.list.some((w) => w.maximized)) {
    reveal.value = false
    return
  }
  const nearBottom = e.clientY >= window.innerHeight - 3
  // 已滑出时：仅当指针仍在 Dock 本体（居中的 .dock，而非全宽容器）范围内才停留
  const r = dockEl.value?.getBoundingClientRect()
  const overDock = !!r && !autoHidden.value
    && e.clientY >= r.top - 8 && e.clientX >= r.left && e.clientX <= r.right
  reveal.value = nearBottom || overDock
}
onMounted(() => window.addEventListener('pointermove', onWinPointerMove))
onBeforeUnmount(() => window.removeEventListener('pointermove', onWinPointerMove))
onMounted(() => {
  // 应用列表变化后重算一次基准（如管理员新增应用）
  const unwatch = apps.$subscribe(() => requestAnimationFrame(applyMagnify))
  onBeforeUnmount(unwatch)
})
// 应用数量 / 废纸篓显隐变化后重算缩放（flush post 保证 DOM 已更新），窗口 resize 同步
watch(
  () => displayApps.value.length * 2 + (hasFilesApp.value ? 1 : 0),
  () => requestAnimationFrame(refit),
  { flush: 'post' },
)
onMounted(() => {
  refit()
  window.addEventListener('resize', refit)
})
onBeforeUnmount(() => {
  window.removeEventListener('resize', refit)
  if (rafId) cancelAnimationFrame(rafId)
})
</script>

<template>
  <div class="dock-area" :class="{ autohidden: autoHidden }">
    <div
      ref="dockEl" class="dock os-glass"
      @pointermove="onMove" @pointerover="onOver" @pointerleave="onLeave"
      @dragover="onDockDragOver" @dragleave="onDockDragLeave" @drop="onDockDrop"
    >
      <div v-if="tip" class="dock-tip" :style="{ left: tip.x + 'px' }">{{ tip.text }}</div>
      <button class="dock-item launcher" data-name="启动台" aria-label="启动台" @click="emit('launchpad')">
        <span class="launcher-icon">
          <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" aria-hidden="true">
            <defs>
              <linearGradient id="lp-bg" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0" stop-color="#f2f4f8"/><stop offset="1" stop-color="#c4c9d4"/>
              </linearGradient>
              <linearGradient id="lp-gl" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0" stop-color="#fff" stop-opacity=".35"/><stop offset=".55" stop-color="#fff" stop-opacity="0"/>
              </linearGradient>
            </defs>
            <rect width="64" height="64" rx="15" fill="url(#lp-bg)"/>
            <rect width="64" height="64" rx="15" fill="url(#lp-gl)"/>
            <g fill="#828b99">
              <rect x="10.5" y="10.5" width="11" height="11" rx="3.5"/>
              <rect x="26.5" y="10.5" width="11" height="11" rx="3.5"/>
              <rect x="42.5" y="10.5" width="11" height="11" rx="3.5"/>
              <rect x="10.5" y="26.5" width="11" height="11" rx="3.5"/>
              <rect x="26.5" y="26.5" width="11" height="11" rx="3.5"/>
              <rect x="42.5" y="26.5" width="11" height="11" rx="3.5"/>
              <rect x="10.5" y="42.5" width="11" height="11" rx="3.5"/>
              <rect x="26.5" y="42.5" width="11" height="11" rx="3.5"/>
              <rect x="42.5" y="42.5" width="11" height="11" rx="3.5"/>
            </g>
          </svg>
        </span>
      </button>
      <div v-if="dockApps.length" class="dock-sep" />
      <template v-for="(a, i) in displayApps" :key="a.id">
        <div v-if="drag != null && !drag.internal && i === slotIdx" class="dock-slot" />
        <button
          class="dock-item" :class="{ dragging: drag?.id === a.id && drag.internal }"
          :data-app="a.id"
          :data-name="a.name"
          :aria-label="a.name"
          draggable="true"
          @click="dockClick(a.id)"
          @dragstart="itemDragStart($event, a.id)"
          @dragend="itemDragEnd"
          @contextmenu.prevent.stop="onItemContext($event, a.id)"
        >
          <AppIcon :app="a.id" :icon="a.icon" :size="iconSize" />
          <span class="dot" v-if="windows.runningAppIds.includes(a.id)" />
        </button>
      </template>
      <div v-if="drag != null && !drag.internal && slotIdx === displayApps.length" class="dock-slot" />

      <!-- 应用右键菜单：打开 / 固定切换 -->
      <div
        v-if="itemMenu.isOpen" ref="itemMenuEl"
        class="os-menu os-menu-root dock-item-menu"
        :style="{ left: itemMenuPos.x + 'px', top: itemMenuPos.y + 'px' }"
        @contextmenu.prevent.stop
      >
        <button @click="dockClick(menuAppId); itemMenu.close()">打开</button>
        <div class="sep" />
        <button v-if="dockStore.effectiveSet.has(menuAppId)" @click="menuUnpin">从 Dock 中移除</button>
        <button v-else @click="menuKeep">在 Dock 中保留</button>
      </div>

      <!-- 废纸篓（右侧分区） -->
      <template v-if="hasFilesApp">
        <div class="dock-sep" />
        <button
          class="dock-item trash" data-name="废纸篓" aria-label="废纸篓"
          @click="openTrash" @contextmenu.prevent.stop="trashMenu.toggle()"
        >
          <svg viewBox="0 0 64 64" class="trash-icon" :class="{ full: trashFull }" aria-hidden="true">
            <defs>
              <linearGradient id="trash-body" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0" stop-color="#eef1f6"/><stop offset="1" stop-color="#b9c0cd"/>
              </linearGradient>
              <linearGradient id="trash-body-full" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0" stop-color="#dfe3ea"/><stop offset="1" stop-color="#9aa2b1"/>
              </linearGradient>
            </defs>
            <!-- 纸团（满时露出桶口） -->
            <g v-if="trashFull">
              <circle cx="27" cy="13" r="7" fill="#f4f5f8" stroke="#c3c8d2" stroke-width="1.5"/>
              <circle cx="37" cy="12" r="8" fill="#fbfbfd" stroke="#c3c8d2" stroke-width="1.5"/>
              <path d="M30 9 l3 4 M36 8 l-2 5 M33 12 l4 3" stroke="#c9cdd6" stroke-width="1.4" stroke-linecap="round" fill="none"/>
            </g>
            <path d="M17 19 h30 l-3.2 35 a4 4 0 0 1-4 3.6 H24.2 a4 4 0 0 1-4-3.6 z"
              :fill="trashFull ? 'url(#trash-body-full)' : 'url(#trash-body)'" opacity=".95"/>
            <g stroke="#7d8494" stroke-width="2" stroke-linecap="round" opacity=".55">
              <line x1="26" y1="26" x2="26.8" y2="52"/>
              <line x1="32" y1="26" x2="32" y2="52"/>
              <line x1="38" y1="26" x2="37.2" y2="52"/>
            </g>
            <rect x="13.5" y="14.5" width="37" height="6.4" rx="3.2" fill="#cfd4de" stroke="#9aa1ae" stroke-width="1.2"/>
            <rect x="26.5" y="9.5" width="11" height="6" rx="3" fill="#cfd4de" stroke="#9aa1ae" stroke-width="1.2"/>
          </svg>
        </button>
        <div v-if="trashMenu.isOpen" class="os-menu os-menu-root dock-trash-menu" @contextmenu.prevent.stop>
          <button @click="openTrash(); trashMenu.close()">打开废纸篓</button>
          <div class="sep" />
          <button class="danger" :disabled="!trashFull" @click="emptyTrash">清倒废纸篓…</button>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.dock-area {
  position: fixed; bottom: 6px; left: 0; right: 0;
  display: flex; justify-content: center; z-index: 9650; pointer-events: none;
  transition: transform .32s cubic-bezier(.4, .1, .6, 1), opacity .24s ease;
}
/* 9650：高于启动台全屏层（9600）——启动台打开时 Dock 保持可见可交互
   （macOS 行为：可点击图标、可把应用拖入 Dock 固定）；低于 Spotlight 遮罩
   （9700），聚焦搜索期间点 Dock 区域仍是点遮罩收起搜索 */
/* 自动隐藏：整体沉到屏幕外（留 10px 余量，连投影一起收干净） */
.dock-area.autohidden {
  transform: translateY(calc(100% + 12px));
  opacity: 0;
}
/* overflow 必须保持 visible：一旦某个轴是 auto/scroll，另一个轴的 visible 会被
   规范计算为 auto，放大抬升的图标就会被容器整体裁掉 */
.dock {
  /* relative：dock-tip 以 Dock 本体为定位基准（x 坐标按「项中心 - dock 左缘」计算，
     若基准退化为全宽的 dock-area，tip 会向左偏 dock 居中后的左侧留白宽度） */
  position: relative;
  /* nowrap：恒一排，图标过多时由 --dock-k 等比缩小（refit 离屏测量计算）；
     图标/内边距/gap 统一乘 k，总宽随 k 线性变化 */
  display: flex; flex-wrap: nowrap; justify-content: center;
  gap: calc(6px * var(--dock-k, 1));
  padding: calc(8px * var(--dock-k, 1)) calc(10px * var(--dock-k, 1));
  border-radius: 22px;
  border: 1px solid var(--os-border);
  box-shadow: 0 14px 44px rgba(0,0,0,.28), inset 0 1px 0 var(--os-highlight-edge);
  pointer-events: auto; max-width: 92vw; overflow: visible;
  align-items: flex-end;
}
.dock-item {
  position: relative; border: none; background: transparent;
  padding: calc(2px * var(--dock-k, 1)); cursor: pointer;
  border-radius: 12px; flex: none;
  transition: transform .16s cubic-bezier(.25,.1,.25,1);
  display: flex; align-items: flex-end; transform-origin: bottom center;
  will-change: transform;
}
.dock-item:active { filter: brightness(.85); }
/* 拖动中的项：原位半透明（图标随指针飞行的快照由浏览器渲染） */
.dock-item.dragging { opacity: .3; }
/* 外部拖入（启动台/桌面快捷方式）的落点插槽 */
.dock-slot {
  flex: none; width: calc(46px * var(--dock-k, 1)); height: calc(46px * var(--dock-k, 1));
  border-radius: 12px;
  border: 1.5px dashed rgba(10, 132, 255, .6);
  background: rgba(10, 132, 255, .14);
  pointer-events: none;
}
.dock-item-menu { position: fixed; min-width: 160px; transform-origin: bottom left; }
.launcher {
  display: flex; align-items: center; justify-content: center;
}
/* 启动台按钮：与新 AppIcon 同一套 squircle 语言（圆角裁切 + 投影） */
.launcher-icon {
  display: block; width: calc(42px * var(--dock-k, 1)); height: calc(42px * var(--dock-k, 1));
  border-radius: 23%; overflow: hidden;
  box-shadow: 0 1px 2px rgba(0,0,0,.18), 0 4px 10px rgba(0,0,0,.22);
}
.launcher-icon svg { display: block; width: 100%; height: 100%; }
.dock-sep { width: 1px; align-self: stretch; background: var(--os-border-strong); margin: calc(4px * var(--dock-k, 1)) calc(3px * var(--dock-k, 1)); flex: none; }
/* 废纸篓图标：与 AppIcon 同一套 squircle 投影语言 */
.trash-icon {
  display: block; width: calc(42px * var(--dock-k, 1)); height: calc(42px * var(--dock-k, 1));
  filter: drop-shadow(0 1px 2px rgba(0, 0, 0, .18)) drop-shadow(0 4px 10px rgba(0, 0, 0, .22));
}
.dock-trash-menu { position: fixed; bottom: 86px; right: 24px; min-width: 150px; transform-origin: bottom right; }
.dot {
  position: absolute; bottom: -5px; left: 50%; transform: translateX(-50%);
  width: 4px; height: 4px; border-radius: 50%; background: var(--os-text);
}
/* 悬停名称提示：锚定 Dock 顶部上方（避开邻居放大最大 ~29px 的抬升），不参与 flex 布局、不挡指针 */
.dock-tip {
  position: absolute; bottom: calc(100% + 25px); transform: translateX(-50%);
  padding: 4px 13px; border-radius: 9px; white-space: nowrap; z-index: 10;
  background: var(--os-panel); backdrop-filter: blur(24px) saturate(1.8);
  -webkit-backdrop-filter: blur(24px) saturate(1.8);
  border: 1px solid var(--os-border);
  color: var(--os-text); font-size: 12.5px; pointer-events: none;
  box-shadow: var(--os-shadow-sm), inset 0 1px 0 var(--os-highlight-edge);
  animation: dock-tip-in .14s var(--os-ease-out);
}
/* 提示弹入动画须保留 translateX(-50%) 居中（通用 pop 动画会覆盖 transform 导致横向跳动） */
@keyframes dock-tip-in {
  from { opacity: 0; transform: translateX(-50%) translateY(3px) scale(.96); }
  to { opacity: 1; transform: translateX(-50%) translateY(0) scale(1); }
}
</style>
