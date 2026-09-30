<script setup lang="ts">
import { computed, defineAsyncComponent, h, onErrorCaptured, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import type { WinState } from '../stores/windows'
import { MENUBAR_H, useWindows, viewport, type SnapZone } from '../stores/windows'
import { builtinComponents, runtimeComponents, appChrome, useApps } from '../stores/apps'
import SchemaApp from '../sdk/SchemaApp.vue'
import { snapPreview } from './snapState'

const props = defineProps<{ win: WinState }>()
const store = useWindows()

// ---- 窗口级错误边界：单个应用崩溃不拖垮桌面 ----
const crashed = ref(false)
const reloadSeq = ref(0)
onErrorCaptured((err) => {
  console.error(`[OpenOS] 应用 ${props.win.appId} 渲染出错:`, err)
  crashed.value = true
  return false
})

// 解析窗口内容组件：内置 → 运行时注册 → 声明式 Schema → 占位
const Placeholder = {
  render: () => h('div', { style: 'padding:24px;color:var(--os-text-secondary)' }, '未知应用：' + props.win.appId),
}
const ErrorPanel = {
  render: () => h('div', { style: 'padding:32px;display:flex;flex-direction:column;gap:14px;align-items:flex-start' }, [
    h('h3', { style: 'font-size:15px' }, '应用遇到错误'),
    h('p', { style: 'font-size:13px;color:var(--os-text-secondary)' }, '该窗口的应用发生异常，可重试或关闭窗口；其他窗口不受影响。'),
    h('div', { style: 'display:flex;gap:8px' }, [
      h('button', {
        style: 'padding:6px 16px;border:none;border-radius:8px;background:var(--os-accent);color:#fff;cursor:pointer',
        onClick: () => { crashed.value = false; reloadSeq.value++ },
      }, '重试'),
      h('button', {
        style: 'padding:6px 16px;border:1px solid var(--os-border-strong);border-radius:8px;background:transparent;color:var(--os-text);cursor:pointer',
        onClick: () => store.close(props.win.id),
      }, '关闭窗口'),
    ]),
  ]),
}

const comp = shallowRef<any>(null)
const apps = useApps()
async function resolveComp() {
  const appId = props.win.appId
  if (builtinComponents[appId]) {
    comp.value = defineAsyncComponent(builtinComponents[appId])
  } else if (runtimeComponents[appId]) {
    comp.value = runtimeComponents[appId]
  } else {
    const def = apps.byId(appId)
    if (def?.config) {
      try { comp.value = { render: () => h(SchemaApp, { schema: JSON.parse(def.config!), win: props.win }) } }
      catch { comp.value = Placeholder }
    } else {
      comp.value = Placeholder
    }
  }
}
resolveComp()
watch(() => props.win.appId, resolveComp)

const focused = computed(() => store.activeId === props.win.id)
// 融合工具栏窗体：应用自绘标题行（红绿灯悬浮其上，可拖拽/双击最大化），窗口内容占满整高
const merged = computed(() => appChrome[props.win.appId] === 'merged')

// ---- 拖动（含边缘吸附）与缩放 ----
const MIN_W = 300, MIN_H = 200
let snapHint: SnapZone | 'top' | null = null
let rafId = 0

function updateSnapHint(px: number, py: number) {
  const vp = viewport()
  const edge = 8
  let next: SnapZone | 'top' | null = null
  if (py <= MENUBAR_H + 4) next = 'top'
  else if (px <= edge) next = 'left'
  else if (px >= vp.w - edge) next = 'right'
  snapHint = next
  if (!next) { snapPreview.value = null; return }
  if (next === 'top') snapPreview.value = { x: 0, y: vp.y, w: vp.w, h: vp.h }
  else snapPreview.value = { x: next === 'left' ? 0 : Math.round(vp.w / 2), y: vp.y, w: Math.round(vp.w / 2), h: vp.h }
}

function finishSnap() {
  if (!snapHint) return
  if (snapHint === 'top') {
    const w = props.win
    if (!w.maximized) store.toggleMaximize(w.id)
  } else {
    store.snap(props.win.id, snapHint)
  }
  snapHint = null
  snapPreview.value = null
}

function onTitlePointerDown(e: PointerEvent) {
  if ((e.target as HTMLElement).closest('.traffic')) return
  store.focus(props.win.id)
  const win = props.win
  const startX = e.clientX, startY = e.clientY
  const origX = win.x, origY = win.y
  const el = e.currentTarget as HTMLElement
  el.setPointerCapture(e.pointerId)
  const wasSnapped = win.maximized || !!win.prev

  const move = (ev: PointerEvent) => {
    const dx = ev.clientX - startX, dy = ev.clientY - startY
    if (wasSnapped && (Math.abs(dx) > 6 || Math.abs(dy) > 6)) {
      // 从最大化/吸附状态拖出：先还原尺寸，并把窗口中心贴到指针
      store.unsnap(props.win.id)
      updateSnapHint(ev.clientX, ev.clientY)
      const w = props.win
      store.setBounds(props.win.id, { x: ev.clientX - w.w / 2, y: Math.max(MENUBAR_H, ev.clientY - 19) })
      return
    }
    updateSnapHint(ev.clientX, ev.clientY)
    if (rafId) return
    rafId = requestAnimationFrame(() => {
      rafId = 0
      store.setBounds(props.win.id, {
        x: origX + ev.clientX - startX,
        y: Math.max(MENUBAR_H, origY + ev.clientY - startY),
      })
    })
  }
  const up = () => {
    if (rafId) { cancelAnimationFrame(rafId); rafId = 0 }
    finishSnap()
    el.removeEventListener('pointermove', move)
    el.removeEventListener('pointerup', up)
  }
  el.addEventListener('pointermove', move)
  el.addEventListener('pointerup', up)
}

// 融合窗体：应用内容里标记 data-os-drag 的区域（标题/留白条）可拖动窗口；控件自身不参与
function draggableTarget(t: HTMLElement): boolean {
  if (!t.closest('[data-os-drag]')) return false
  return !t.closest('button, input, select, textarea, a, label, [data-os-no-drag]')
}
function onWinPointerDown(e: PointerEvent) {
  store.focus(props.win.id)
  if (merged.value && draggableTarget(e.target as HTMLElement)) onTitlePointerDown(e)
}
// 双击工具栏标题区最大化（与标准标题栏双击行为一致）
function onWinDblClick(e: MouseEvent) {
  if (merged.value && draggableTarget(e.target as HTMLElement)) store.toggleMaximize(props.win.id)
}

function onResizePointerDown(e: PointerEvent, dir: string) {
  e.stopPropagation()
  store.focus(props.win.id)
  const win = props.win
  const startX = e.clientX, startY = e.clientY
  const o = { x: win.x, y: win.y, w: win.w, h: win.h }
  const el = e.currentTarget as HTMLElement
  el.setPointerCapture(e.pointerId)
  const move = (ev: PointerEvent) => {
    const dx = ev.clientX - startX, dy = ev.clientY - startY
    const b: any = {}
    if (dir.includes('e')) b.w = Math.max(MIN_W, o.w + dx)
    if (dir.includes('s')) b.h = Math.max(MIN_H, o.h + dy)
    if (dir.includes('w')) {
      b.w = Math.max(MIN_W, o.w - dx)
      b.x = o.x + (o.w - b.w)
    }
    if (dir.includes('n')) {
      b.h = Math.max(MIN_H, o.h - dy)
      b.y = Math.max(MENUBAR_H, o.y + (o.h - b.h))
      if (b.y <= MENUBAR_H) b.h = o.y + o.h - MENUBAR_H
    }
    if (win.prev) { win.prev = undefined; win.maximized = false } // 手动缩放解除吸附记忆
    store.setBounds(props.win.id, b)
  }
  const up = () => {
    el.removeEventListener('pointermove', move)
    el.removeEventListener('pointerup', up)
  }
  el.addEventListener('pointermove', move)
  el.addEventListener('pointerup', up)
}

const resizeDirs = ['n', 's', 'e', 'w', 'ne', 'nw', 'se', 'sw']

// 最小化飞向 Dock：以 Dock 中心为目标做位移/缩放（CSS 过渡）
const dockFly = ref('')
function flyToDock() {
  const vp = viewport()
  const tx = vp.w / 2 - (props.win.x + props.win.w / 2)
  const ty = window.innerHeight - 40 - (props.win.y + props.win.h / 2)
  dockFly.value = `translate(${Math.round(tx)}px, ${Math.round(ty)}px) scale(.06)`
}
function onMinimizeClick() {
  flyToDock()
  setTimeout(() => {
    store.minimize(props.win.id)
    dockFly.value = ''
  }, 280)
}

// Esc 键快速关闭吸附预览（全局由 Desktop 处理，这里只做兜底清理）
onMounted(() => { snapPreview.value = null })
onBeforeUnmount(() => { if (rafId) cancelAnimationFrame(rafId) })
</script>

<template>
  <section
    class="window"
    :class="{ focused, minimized: win.minimized, maximized: win.maximized }"
    :data-app="win.appId"
    :style="{
      left: win.x + 'px', top: win.y + 'px', width: win.w + 'px', height: win.h + 'px', zIndex: win.z,
      ...(dockFly ? { transform: dockFly, opacity: 0 } : {}),
    }"
    @pointerdown="onWinPointerDown" @dblclick="onWinDblClick"
  >
    <header
      class="titlebar" :class="{ merged }"
      @pointerdown="onTitlePointerDown" @dblclick="store.toggleMaximize(win.id)"
    >
      <div class="traffic">
        <button class="t close" title="关闭" @click="store.close(win.id)"><span>✕</span></button>
        <button class="t min" title="最小化" @click="onMinimizeClick"><span>−</span></button>
        <button class="t max" title="最大化" @click="store.toggleMaximize(win.id)"><span>⤢</span></button>
      </div>
      <template v-if="!merged">
        <div class="title">{{ win.title }}</div>
        <div class="titlebar-space" />
      </template>
    </header>
    <div class="body">
      <ErrorPanel v-if="crashed" />
      <component :is="comp" :win="win" :key="reloadSeq" v-else-if="comp" />
    </div>
    <div
      v-for="d in resizeDirs" :key="d"
      class="resize" :data-dir="d"
      @pointerdown="onResizePointerDown($event, d)"
    />
  </section>
</template>

<style scoped>
.window {
  position: absolute; display: flex; flex-direction: column;
  /* 父层 pointer-events:none 穿透，窗口本体恢复接收事件 */
  pointer-events: auto;
  border-radius: var(--os-radius); overflow: hidden;
  background: var(--os-window);
  border: 1px solid var(--os-border-strong);
  box-shadow: var(--os-shadow);
  animation: os-window-open .22s var(--os-ease-spring);
  transition: opacity .26s ease, transform .26s cubic-bezier(.4,.1,.6,1), box-shadow .2s ease;
  /* 勿加 will-change: transform：它会常驻创建 fixed 包含块，令嵌入应用
     （goder 的模型/思考强度/策略等弹窗按视口坐标做 fixed 定位）整体漂移 */
}
.window.dragging { transition: none; }
.window.minimized { opacity: 0; transform: scale(.06) translateY(70vh); pointer-events: none; }
.window:not(.focused) { box-shadow: 0 10px 34px rgba(0,0,0,.2); }
/* 最大化 = macOS 全屏形态：直角、无边框 */
.window.maximized { border-radius: 0; border: none; }
/* 聚焦窗口：轻微提亮边框，强化前后层次 */
.window.focused { border-color: var(--os-border-strong); }

.titlebar {
  height: 38px; flex: none; display: flex; align-items: center; gap: 10px;
  background: var(--os-window-head);
  border-bottom: 1px solid var(--os-border);
  box-shadow: inset 0 1px 0 var(--os-highlight-edge);
  backdrop-filter: blur(16px); cursor: default; padding: 0 12px;
  user-select: none; touch-action: none;
}
.title {
  flex: 1; text-align: center; font-size: 13px; font-weight: 600;
  color: var(--os-text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.focused .title { color: var(--os-text); }
.titlebar-space { width: 66px; flex: none; }

/* 融合工具栏窗体：标题栏退化为悬浮的红绿灯拖拽区（应用工具栏自绘标题行，内容占满整窗） */
.titlebar.merged {
  position: absolute; top: 0; left: 0; width: 84px; height: 46px; z-index: 30;
  background: transparent; border-bottom: none; box-shadow: none; backdrop-filter: none;
  padding: 0 0 0 13px;
}

.traffic { display: flex; gap: 8px; flex: none; }
.t {
  width: 12px; height: 12px; border-radius: 50%; border: none; cursor: pointer;
  display: flex; align-items: center; justify-content: center; padding: 0;
  font-size: 8px; color: rgba(0,0,0,.55); line-height: 1;
  /* 内阴影 + 1px 深色描边：贴近 macOS 红绿灯的立体质感 */
  box-shadow: inset 0 0 0 0.5px rgba(0,0,0,.18), inset 0 1px 1px rgba(255,255,255,.25);
  transition: filter .12s ease;
}
.t span { opacity: 0; }
.window:hover .t span, .t:focus span { opacity: .9; }
.t.close { background: #ff5f57; }
.t.min { background: #febc2e; }
.t.max { background: #28c840; }
.t.close:hover { filter: brightness(.92); }
.t.min:hover { filter: brightness(.92); }
.t.max:hover { filter: brightness(.92); }
.t:active { filter: brightness(.82); }
/* 失焦且未悬停窗口的红绿灯退为灰色（macOS 行为） */
.window:not(.focused):not(:hover) .t { background: #cfcfd4; }
html.dark .window:not(.focused):not(:hover) .t { background: #55555c; }

.body { flex: 1; min-height: 0; position: relative; overflow: hidden; display: flex; }

/* 缩放手柄 */
.resize { position: absolute; z-index: 10; touch-action: none; }
.resize[data-dir='n']   { top: -3px; left: 8px; right: 8px; height: 6px; cursor: ns-resize; }
.resize[data-dir='s']   { bottom: -3px; left: 8px; right: 8px; height: 6px; cursor: ns-resize; }
.resize[data-dir='e']   { right: -3px; top: 8px; bottom: 8px; width: 6px; cursor: ew-resize; }
.resize[data-dir='w']   { left: -3px; top: 8px; bottom: 8px; width: 6px; cursor: ew-resize; }
.resize[data-dir='ne']  { top: -3px; right: -3px; width: 12px; height: 12px; cursor: nesw-resize; }
.resize[data-dir='nw']  { top: -3px; left: -3px; width: 12px; height: 12px; cursor: nwse-resize; }
.resize[data-dir='se']  { bottom: -3px; right: -3px; width: 12px; height: 12px; cursor: nwse-resize; }
.resize[data-dir='sw']  { bottom: -3px; left: -3px; width: 12px; height: 12px; cursor: nesw-resize; }
.maximized .resize { display: none; }

/* goder：深色工作台，标题栏配色与其界面融合（macOS 一体式外观） */
.window[data-app='goder'] {
  --os-window-head: #14161c;
  --os-border: rgba(255, 255, 255, 0.07);
}
.window[data-app='goder'] .title { color: #8b909c; }
.window[data-app='goder'].focused .title { color: #c7ccd6; }
</style>
