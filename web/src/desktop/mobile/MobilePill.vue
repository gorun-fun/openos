<script setup lang="ts">
// 移动形态悬浮胶囊（系统导航）：底部居中胶囊承载 回桌面/多任务/更多，
// 拖动把手可拖离底部 → 变为自由悬浮球（点击弹出操作面板）。位置记忆在 localStorage。
// 多任务 = 全部窗口的横滑卡片（上滑关闭、点击恢复）；回桌面语义由 Desktop 统一处理
// （经 history.back() 走 popstate，Android 系统返回键同路径）。
// 仅在手机形态且有前台窗口时渲染（见 Desktop.vue）。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useWindows } from '../../stores/windows'
import { useApps } from '../../stores/apps'
import AppIcon from '../../ui/AppIcon.vue'
import { mobileViewportH } from '../../ui/formfactor'

const emit = defineEmits<{ home: [] }>()

const windows = useWindows()
const apps = useApps()

const PILL_H = 54
const EDGE = 8

type Mode = 'pill' | 'ball'
const mode = ref<Mode>('pill')
const ball = ref({ x: 0, y: 0 }) // 球位置（视口左上原点）
const KEY = 'openos_pill_pos'
try {
  const raw = localStorage.getItem(KEY)
  if (raw) {
    const v = JSON.parse(raw)
    if (v.mode === 'ball' && typeof v.x === 'number') {
      mode.value = 'ball'
      ball.value = { x: v.x, y: v.y }
    }
  }
} catch { /* 忽略坏数据 */ }

// ---- visualViewport 跟随：软键盘弹出时胶囊/球升到键盘上方，不被遮挡 ----
const vvTop = ref(0)
const vvH = ref(mobileViewportH())
function onVV() {
  const vv = window.visualViewport
  if (!vv) return
  vvTop.value = vv.offsetTop
  vvH.value = Math.round(vv.height)
}
onMounted(() => {
  window.visualViewport?.addEventListener('resize', onVV)
  window.visualViewport?.addEventListener('scroll', onVV)
  onVV()
})
onBeforeUnmount(() => {
  window.visualViewport?.removeEventListener('resize', onVV)
  window.visualViewport?.removeEventListener('scroll', onVV)
})

const ballR = 27 // 球半径（54px 直径）
// 球渲染位置：clamp 进可视区（visualViewport 区域内），旋转/键盘弹出后不丢出屏幕
const ballPos = computed(() => {
  const vw = window.innerWidth
  const x = Math.max(EDGE, Math.min(ball.value.x, vw - ballR * 2 - EDGE))
  const y = Math.max(vvTop.value + EDGE, Math.min(ball.value.y, vvTop.value + vvH.value - ballR * 2 - EDGE))
  return { x, y }
})
// 胶囊渲染位置：底部居中，键盘弹出时抬升
const pillTop = computed(() => vvTop.value + vvH.value - PILL_H - 12 - getSafeBottom())

function getSafeBottom(): number {
  // env() 值 JS 取不到，经 base.css 注入的 --os-safe-b 变量读取（缓存一次）
  if (safeBottom < 0) {
    const v = getComputedStyle(document.documentElement).getPropertyValue('--os-safe-b')
    safeBottom = parseFloat(v) || 0
  }
  return safeBottom
}
let safeBottom = -1

function save() {
  try {
    localStorage.setItem(KEY, JSON.stringify({ mode: mode.value, ...ball.value }))
  } catch { /* 私隐模式等存储失败可忽略 */ }
}

// ---- 拖动（胶囊把手 / 球本体）：pointer events，松手吸附 ----
const dragPos = ref<{ x: number; y: number } | null>(null) // 拖动中的临时位置
let lastDragMoved = false // 拖动过的松手不吃 click（球点击弹面板用）

function startDrag(e: PointerEvent) {
  if (e.button !== undefined && e.button !== 0) return
  lastDragMoved = false
  const startX = e.clientX, startY = e.clientY
  const orig = mode.value === 'ball' ? { ...ballPos.value } : pillCenter()
  let moved = false
  const move = (ev: PointerEvent) => {
    const dx = ev.clientX - startX, dy = ev.clientY - startY
    if (!moved && Math.hypot(dx, dy) < 6) return
    moved = true
    dragPos.value = {
      x: Math.max(EDGE, Math.min(orig.x + dx, window.innerWidth - ballR * 2 - EDGE)),
      y: Math.max(vvTop.value + EDGE, Math.min(orig.y + dy, vvTop.value + vvH.value - PILL_H - EDGE)),
    }
  }
  const up = () => {
    window.removeEventListener('pointermove', move)
    window.removeEventListener('pointerup', up)
    const p = dragPos.value
    dragPos.value = null
    lastDragMoved = moved
    if (!moved || !p) return
    // 松手吸附：位于屏幕下沿带 → 胶囊回底部；否则 → 球形态吸左右边
    const vw = window.innerWidth
    if (p.y > (vvTop.value + vvH.value) * 0.62) {
      mode.value = 'pill'
    } else {
      mode.value = 'ball'
      const toLeft = p.x + ballR < vw / 2
      ball.value = { x: toLeft ? EDGE : vw - ballR * 2 - EDGE, y: p.y }
    }
    save()
  }
  // 监听挂 window 而非指针捕获元素：拖动一开始 pill 即被 v-if 卸载，
  // 捕获元素从 DOM 移除后 capture 失效、up 再也收不到（球会卡在拖动态）
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', up)
}

function pillCenter(): { x: number; y: number } {
  const vw = window.innerWidth
  const w = 200 // 胶囊近似宽度，仅作拖动起点
  return { x: (vw - w) / 2, y: pillTop.value }
}

/** 球点击（拖动松手不触发）：弹操作面板。 */
function onBallClick() {
  if (mode.value === 'ball' && !lastDragMoved) actionsOpen.value = true
}

// ---- 弹层：多任务卡片 / 操作面板 ----
const tasksOpen = ref(false)
const actionsOpen = ref(false)
const active = computed(() => windows.active)
const tasks = computed(() => [...windows.list].sort((a, b) => b.z - a.z))

function openTask(id: string) {
  windows.focus(id)
  tasksOpen.value = false
}
function closeTask(id: string) {
  windows.close(id)
  if (!tasks.value.length) tasksOpen.value = false
}

// 卡片上滑关闭：touch-action: pan-x 把纵向手势留给这里（横向滚动归浏览器）
function onCardPointerDown(e: PointerEvent, id: string) {
  const el = e.currentTarget as HTMLElement
  el.setPointerCapture(e.pointerId)
  const startY = e.clientY
  let dy = 0
  const card = el as HTMLElement
  const move = (ev: PointerEvent) => {
    dy = Math.min(0, ev.clientY - startY)
    card.style.transform = `translateY(${dy}px)`
    card.style.opacity = String(1 + dy / 220)
  }
  const up = () => {
    el.removeEventListener('pointermove', move)
    el.removeEventListener('pointerup', up)
    card.style.transform = ''
    card.style.opacity = ''
    if (dy < -64) closeTask(id)
  }
  el.addEventListener('pointermove', move)
  el.addEventListener('pointerup', up)
}
</script>

<template>
  <!-- 悬浮胶囊：把手 + 回桌面 / 多任务 / 更多 -->
  <div
    v-if="mode === 'pill' && !dragPos"
    class="pill"
    :style="{ top: pillTop + 'px' }"
  >
    <button class="handle" title="拖动可改变位置" @pointerdown.prevent="startDrag">
      <svg viewBox="0 0 12 20" width="8" height="14"><path d="M2 3h8M2 8h8M2 13h8M2 18h8" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>
    </button>
    <button class="pbtn" title="回桌面" @click="emit('home')">
      <svg viewBox="0 0 20 20" width="21" height="21"><path d="M3 9.4 10 3.4l7 6V16a1.2 1.2 0 0 1-1.2 1.2h-3.3v-4.5h-5v4.5H4.2A1.2 1.2 0 0 1 3 16Z" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linejoin="round" stroke-linecap="round"/></svg>
    </button>
    <button class="pbtn" title="多任务" @click="tasksOpen = true">
      <svg viewBox="0 0 20 20" width="20" height="20"><rect x="2.6" y="4.4" width="6.4" height="6.4" rx="1.6" fill="none" stroke="currentColor" stroke-width="1.7"/><rect x="11" y="4.4" width="6.4" height="6.4" rx="1.6" fill="none" stroke="currentColor" stroke-width="1.7"/><rect x="2.6" y="12.8" width="6.4" height="6.4" rx="1.6" fill="none" stroke="currentColor" stroke-width="1.7"/></svg>
    </button>
    <button class="pbtn" title="更多" @click="actionsOpen = true">
      <svg viewBox="0 0 20 20" width="20" height="20"><circle cx="4.6" cy="10" r="1.7" fill="currentColor"/><circle cx="10" cy="10" r="1.7" fill="currentColor"/><circle cx="15.4" cy="10" r="1.7" fill="currentColor"/></svg>
    </button>
  </div>

  <!-- 悬浮球（拖动跟手态复用同一元素） -->
  <div
    v-if="mode === 'ball' || dragPos"
    class="ball"
    :class="{ dragging: !!dragPos }"
    :style="{ left: (dragPos ?? ballPos).x + 'px', top: (dragPos ?? ballPos).y + 'px' }"
    @pointerdown.prevent="startDrag"
    @click="onBallClick"
  >
    <svg viewBox="0 0 20 20" width="22" height="22"><rect x="2.6" y="4.4" width="6.4" height="6.4" rx="1.8" fill="none" stroke="currentColor" stroke-width="1.8"/><rect x="11" y="4.4" width="6.4" height="6.4" rx="1.8" fill="none" stroke="currentColor" stroke-width="1.8"/><rect x="2.6" y="12.8" width="6.4" height="6.4" rx="1.8" fill="none" stroke="currentColor" stroke-width="1.8"/></svg>
  </div>

  <!-- 多任务：横滑卡片，上滑关闭，点击恢复 -->
  <Teleport to="body">
    <div v-if="tasksOpen" class="sheet-mask" @click="tasksOpen = false" @pointerdown.stop>
      <div class="tasks" @click.stop>
        <header>
          <b>多任务</b>
          <span class="grow" />
          <button class="hbtn" @click="tasksOpen = false">完成</button>
        </header>
        <div v-if="tasks.length" class="task-row">
          <div
            v-for="w in tasks" :key="w.id" class="task-card" :class="{ on: w.id === active?.id }"
            @click="openTask(w.id)" @pointerdown="onCardPointerDown($event, w.id)"
          >
            <div class="card-head">
              <AppIcon :app="w.appId" :size="26" />
              <span class="t">{{ w.title || apps.byId(w.appId)?.name }}</span>
              <button class="x" title="关闭" @click.stop="closeTask(w.id)">✕</button>
            </div>
            <div class="card-body">
              <AppIcon :app="w.appId" :size="64" />
              <span class="app">{{ apps.byId(w.appId)?.name || w.appId }}</span>
            </div>
          </div>
        </div>
        <div v-else class="empty">没有后台应用<br /><small>回到桌面打开一个应用吧</small></div>
      </div>
    </div>

    <!-- 操作面板（胶囊「更多」/ 球点击） -->
    <div v-if="actionsOpen" class="sheet-mask" @click="actionsOpen = false" @pointerdown.stop>
      <div class="actions" @click.stop>
        <button v-if="active" class="abtn danger" @click="actionsOpen = false; windows.close(active.id)">
          关闭「{{ active.title }}」
        </button>
        <button class="abtn" @click="actionsOpen = false; tasksOpen = true">多任务</button>
        <button class="abtn" @click="actionsOpen = false; emit('home')">回桌面</button>
        <button v-if="mode === 'ball'" class="abtn" @click="mode = 'pill'; save(); actionsOpen = false">
          恢复底部胶囊
        </button>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.pill {
  position: fixed; left: 50%; transform: translateX(-50%); z-index: 9650;
  height: 54px; display: flex; align-items: center; gap: 2px;
  padding: 0 8px 0 4px; border-radius: 999px;
  background: var(--os-panel); backdrop-filter: blur(24px) saturate(1.8);
  -webkit-backdrop-filter: blur(24px) saturate(1.8);
  border: 1px solid var(--os-border);
  box-shadow: var(--os-shadow-sm), inset 0 1px 0 var(--os-highlight-edge);
  animation: pill-in .22s var(--os-ease-out);
}
@keyframes pill-in { from { transform: translateX(-50%) translateY(16px); opacity: 0; } }
.handle {
  width: 26px; height: 44px; border: none; background: none; cursor: grab;
  color: var(--os-text-tertiary, var(--os-text-secondary)); display: flex; align-items: center; justify-content: center;
  touch-action: none;
}
.handle:active { cursor: grabbing; }
.pbtn {
  width: 48px; height: 44px; border: none; border-radius: 14px; background: none;
  color: var(--os-text); cursor: pointer; display: flex; align-items: center; justify-content: center;
}
.pbtn:active { background: var(--os-hover); }

.ball {
  position: fixed; z-index: 9650; width: 54px; height: 54px; border-radius: 50%;
  display: flex; align-items: center; justify-content: center; color: var(--os-text);
  background: var(--os-panel); backdrop-filter: blur(24px) saturate(1.8);
  -webkit-backdrop-filter: blur(24px) saturate(1.8);
  border: 1px solid var(--os-border);
  box-shadow: var(--os-shadow-sm), inset 0 1px 0 var(--os-highlight-edge);
  transition: left .22s var(--os-ease-out), top .22s var(--os-ease-out);
  cursor: pointer; touch-action: none;
}
.ball.dragging { transition: none; }

/* —— 底部弹层通用（遮罩 + 上滑面板） —— */
.sheet-mask {
  position: fixed; inset: 0; z-index: 9680; background: rgba(0, 0, 0, .32);
  animation: os-fade-in .16s ease; display: flex; align-items: flex-end;
}
.tasks, .actions {
  width: 100%; background: var(--os-panel-solid); color: var(--os-text);
  border-radius: 20px 20px 0 0; padding-bottom: calc(env(safe-area-inset-bottom, 0px) + 12px);
  animation: sheet-up .26s var(--os-ease-out);
  box-shadow: 0 -12px 40px rgba(0, 0, 0, .22);
}
@keyframes sheet-up { from { transform: translateY(60%); opacity: .4; } }
.tasks header {
  display: flex; align-items: center; padding: 14px 18px 8px;
}
.tasks header b { font-size: 15px; }
.grow { flex: 1; }
.hbtn {
  border: none; background: var(--os-hover); color: var(--os-text);
  border-radius: 999px; padding: 6px 16px; font-size: 13px; cursor: pointer; font-family: inherit;
}
.task-row {
  display: flex; gap: 12px; overflow-x: auto; touch-action: pan-x;
  -webkit-overflow-scrolling: touch; padding: 6px 16px 14px;
  scroll-snap-type: x mandatory;
}
.task-card {
  flex: none; width: 62vw; max-width: 280px; border-radius: 16px; overflow: hidden;
  background: var(--os-bg); border: 1px solid var(--os-border);
  scroll-snap-align: start; cursor: pointer; touch-action: pan-x;
  transition: transform .18s var(--os-ease-out), opacity .18s;
}
.task-card.on { border-color: var(--os-accent); }
.card-head {
  display: flex; align-items: center; gap: 8px; padding: 8px 10px;
  border-bottom: 1px solid var(--os-border); background: var(--os-sidebar);
}
.card-head .t { flex: 1; min-width: 0; font-size: 13px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.x {
  width: 24px; height: 24px; border-radius: 50%; border: none; cursor: pointer;
  background: var(--os-hover); color: var(--os-text); font-size: 11px; flex: none;
  display: flex; align-items: center; justify-content: center;
}
.card-body {
  height: 150px; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 12px;
}
.card-body .app { font-size: 12px; color: var(--os-text-secondary); }
.empty { padding: 40px 16px 30px; text-align: center; font-size: 14px; color: var(--os-text-secondary); line-height: 1.8; }
.empty small { font-size: 12px; opacity: .7; }

.actions { display: flex; flex-direction: column; gap: 8px; padding: 14px 14px 6px; }
.abtn {
  height: 50px; border: none; border-radius: 13px; background: var(--os-bg); color: var(--os-text);
  font-size: 14.5px; cursor: pointer; font-family: inherit;
}
.abtn:active { background: var(--os-hover); }
.abtn.danger { color: #e5484d; }
</style>
