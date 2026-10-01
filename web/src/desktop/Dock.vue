<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useApps } from '../stores/apps'
import { useWindows } from '../stores/windows'
import { useDesktop } from '../stores/desktop'
import { useSession } from '../stores/session'
import { fsApi } from '../api/client'
import { bus } from '../bus/bus'
import { toast } from '../ui/toast'
import { useMenu } from '../ui/menu'
import { shell } from '../shell'
import AppIcon from '../ui/AppIcon.vue'

const emit = defineEmits<{ launchpad: [] }>()
const apps = useApps()
const windows = useWindows()
const desktop = useDesktop()
const session = useSession()

/** Dock 应用：宿主经 createOpenOS({ dockOrder }) 指定排序，未列出的排在其后按服务器顺序。 */
const dockApps = computed(() => {
  const order = shell.dockOrder
  if (!order.length) return apps.visible
  const rank = (id: string) => {
    const i = order.indexOf(id)
    return i === -1 ? order.length : i
  }
  return [...apps.visible].sort((a, b) => rank(a.id) - rank(b.id) || a.id.localeCompare(b.id))
})

function dockClick(appId: string) {
  const wins = windows.list.filter((w) => w.appId === appId)
  if (wins.length === 0) {
    windows.open(appId)
  } else {
    const anyVisible = wins.some((w) => !w.minimized)
    const top = wins.sort((a, b) => b.z - a.z)[0]
    if (anyVisible && windows.activeId === top.id) {
      windows.minimize(top.id) // 再次点击最小化最前窗口
    } else {
      windows.focus(top.id)
    }
  }
}

// ---- 邻居放大（macOS Dock）：指针距离连续衰减，直接写 DOM 避免 Vue 重渲抖动 ----
const dockEl = ref<HTMLElement>()
let rafId = 0
let mouseX: number | null = null

function applyMagnify() {
  rafId = 0
  const root = dockEl.value
  if (!root) return
  const items = root.querySelectorAll<HTMLElement>('.dock-item')
  for (const el of items) {
    if (mouseX == null) {
      el.style.transform = ''
      continue
    }
    const rect = el.getBoundingClientRect()
    // 用未放大时的基准位置计算（放大后的 rect 会漂移，需去抖：以 rect 中心近似 + 宽度还原）
    const base = rect.left + rect.width / 2
    const dist = Math.abs(base - mouseX)
    const influence = Math.max(0, 1 - dist / 150) // 影响半径 150px
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
onBeforeUnmount(() => { if (rafId) cancelAnimationFrame(rafId) })
</script>

<template>
  <div class="dock-area" :class="{ autohidden: autoHidden }">
    <div ref="dockEl" class="dock os-glass" @pointermove="onMove" @pointerover="onOver" @pointerleave="onLeave">
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
      <div class="dock-sep" />
      <button
        v-for="app in dockApps" :key="app.id"
        class="dock-item"
        :data-name="app.name"
        :aria-label="app.name"
        @click="dockClick(app.id)"
      >
        <AppIcon :app="app.id" :size="42" />
        <span class="dot" v-if="windows.runningAppIds.includes(app.id)" />
      </button>

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
  display: flex; justify-content: center; z-index: 9000; pointer-events: none;
  transition: transform .32s cubic-bezier(.4, .1, .6, 1), opacity .24s ease;
}
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
  display: flex; flex-wrap: wrap; justify-content: center; gap: 6px;
  padding: 8px 10px; border-radius: 22px;
  border: 1px solid var(--os-border);
  box-shadow: 0 14px 44px rgba(0,0,0,.28), inset 0 1px 0 var(--os-highlight-edge);
  pointer-events: auto; max-width: 92vw; overflow: visible;
  align-items: flex-end;
}
.dock-item {
  position: relative; border: none; background: transparent; padding: 2px; cursor: pointer;
  border-radius: 12px; flex: none;
  transition: transform .16s cubic-bezier(.25,.1,.25,1);
  display: flex; align-items: flex-end; transform-origin: bottom center;
  will-change: transform;
}
.dock-item:active { filter: brightness(.85); }
.launcher {
  display: flex; align-items: center; justify-content: center;
}
/* 启动台按钮：与新 AppIcon 同一套 squircle 语言（圆角裁切 + 投影） */
.launcher-icon {
  display: block; width: 42px; height: 42px; border-radius: 23%; overflow: hidden;
  box-shadow: 0 1px 2px rgba(0,0,0,.18), 0 4px 10px rgba(0,0,0,.22);
}
.launcher-icon svg { display: block; width: 100%; height: 100%; }
.dock-sep { width: 1px; align-self: stretch; background: var(--os-border-strong); margin: 4px 3px; flex: none; }
/* 废纸篓图标：与 AppIcon 同一套 squircle 投影语言 */
.trash-icon {
  display: block; width: 42px; height: 42px;
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
