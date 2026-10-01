<script setup lang="ts">
// 通知中心：右上角横幅栈（自动消失、悬停暂停）+ 菜单栏时钟唤出的右侧面板
// （历史列表 / 声音与勿扰开关 / 一键清除），仿 macOS 通知中心交互。
// 数据与行为在 stores/notifications.ts；本组件只做展示与交互。
import { onBeforeUnmount, onMounted, watch } from 'vue'
import { useNotifications } from '../stores/notifications'
import { useWindows } from '../stores/windows'
import { useApps } from '../stores/apps'
import AppIcon from '../ui/AppIcon.vue'

const nc = useNotifications()
const windows = useWindows()
const apps = useApps()

const TOAST_MS = 5200

// ---- 横幅自动消失：每条一个定时器，悬停暂停、移出后重启剩余时间 ----
const timers = new Map<string, number>()
const deadlines = new Map<string, number>()

function armToast(id: string) {
  clearToastTimer(id)
  deadlines.set(id, Date.now() + TOAST_MS)
  timers.set(id, window.setTimeout(() => nc.toastDismiss(id), TOAST_MS))
}
function pauseToast(id: string) {
  const dl = deadlines.get(id)
  if (dl) deadlines.set(id, Math.max(400, dl - Date.now()) + Date.now()) // 记录剩余
  clearToastTimer(id)
}
function resumeToast(id: string) {
  const left = (deadlines.get(id) || 0) - Date.now()
  if (left > 0) {
    clearToastTimer(id)
    timers.set(id, window.setTimeout(() => nc.toastDismiss(id), left))
  } else {
    nc.toastDismiss(id)
  }
}
function clearToastTimer(id: string) {
  const t = timers.get(id)
  if (t) { clearTimeout(t); timers.delete(id) }
}
watch(
  () => nc.toasts.map((t) => t.id).join(','),
  (_, old) => {
    // 新出现的横幅起表；已消失的清表
    const ids = new Set(nc.toasts.map((t) => t.id))
    for (const id of [...timers.keys()]) if (!ids.has(id)) { clearToastTimer(id); deadlines.delete(id) }
    if (old !== undefined) {
      const oldIds = new Set(String(old).split(',').filter(Boolean))
      for (const id of ids) if (!oldIds.has(id)) armToast(id)
    }
  },
  { immediate: true },
)

// ---- 面板：点外部 / Esc 关闭（时钟按钮自身经 data-os-nc-clock 豁免） ----
function onDocDown(e: PointerEvent) {
  if (!nc.panelOpen) return
  const t = e.target as HTMLElement
  if (t.closest('.nc-panel') || t.closest('[data-os-nc-clock]')) return
  nc.closePanel()
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && nc.panelOpen) nc.closePanel()
}
onMounted(() => {
  document.addEventListener('pointerdown', onDocDown, true)
  document.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDocDown, true)
  document.removeEventListener('keydown', onKey)
  for (const id of [...timers.keys()]) clearToastTimer(id)
})

/** 点击横幅/通知项：打开来源应用（系统通知无可打开项则仅收起）。 */
function activate(id: string, appId?: string) {
  nc.toastDismiss(id)
  if (appId && apps.byId(appId)) windows.open(appId)
}

const appName = (appId?: string) => (appId ? apps.byId(appId)?.name || appId : '系统')

function fmtTime(ts: number) {
  const d = new Date(ts)
  const diff = Date.now() - ts
  const hm = `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  if (diff < 60_000) return '刚刚'
  if (diff < 3600_000) return Math.floor(diff / 60_000) + ' 分钟前'
  if (d.toDateString() === new Date().toDateString()) return hm
  return `${d.getMonth() + 1}月${d.getDate()}日 ${hm}`
}
</script>

<template>
  <!-- 右上角横幅栈（勿扰模式下不出现） -->
  <div class="nc-toasts">
    <div v-for="t in nc.toasts" :key="t.id" class="nc-toast" @click="activate(t.id, t.appId)"
      @mouseenter="pauseToast(t.id)" @mouseleave="resumeToast(t.id)">
      <AppIcon v-if="t.appId" :app="t.appId" :size="36" />
      <span v-else class="nc-sys-icon" aria-hidden="true">
        <svg viewBox="0 0 16 16" width="17" height="17"><path d="M8 1.5a4.2 4.2 0 0 0-4.2 4.2v2.6l-1.1 2.2a.7.7 0 0 0 .63 1H6a2 2 0 0 0 4 0h2.67a.7.7 0 0 0 .63-1L12.2 8.3V5.7A4.2 4.2 0 0 0 8 1.5Z" fill="currentColor"/></svg>
      </span>
      <div class="nc-toast-main">
        <div class="nc-toast-top"><b class="t">{{ t.title }}</b><span class="app">{{ appName(t.appId) }}</span></div>
        <p v-if="t.body">{{ t.body }}</p>
      </div>
      <button class="nc-x" title="关闭" @click.stop="nc.toastDismiss(t.id)">✕</button>
    </div>
  </div>

  <!-- 通知中心面板 -->
  <div v-if="nc.panelOpen" class="nc-panel">
    <header>
      <b>通知中心</b>
      <span class="grow" />
      <button class="hbtn" :class="{ on: nc.sound }" :title="nc.sound ? '提示声：开' : '提示声：关'"
        @click="nc.setSound(!nc.sound)">
        <svg viewBox="0 0 16 16" width="14" height="14"><path d="M8 2.2a3.8 3.8 0 0 0-3.8 3.8v2.3l-1 2a.64.64 0 0 0 .57.92h8.46a.64.64 0 0 0 .57-.93l-1-2V6A3.8 3.8 0 0 0 8 2.2Z" fill="currentColor"/><path v-if="nc.sound" d="M6.6 12.2a1.5 1.5 0 0 0 2.8 0" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/><path v-else d="M10.4 6.2l3 3M13.4 6.2l-3 3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/></svg>
      </button>
      <button class="hbtn" :class="{ on: nc.dnd }" :title="nc.dnd ? '勿扰模式：开（横幅静默，仅入列表）' : '勿扰模式：关'"
        @click="nc.setDnd(!nc.dnd)">
        <svg viewBox="0 0 16 16" width="14" height="14"><path d="M8 2.2a3.8 3.8 0 0 0-3.8 3.8v2.3l-1 2a.64.64 0 0 0 .57.92h8.46a.64.64 0 0 0 .57-.93l-1-2V6A3.8 3.8 0 0 0 8 2.2Z" fill="currentColor"/><path d="M8 2V1" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/><path v-if="!nc.dnd" d="M6.6 12.6a1.5 1.5 0 0 0 2.8 0" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/><circle v-if="nc.dnd" cx="8" cy="7.4" r="2.6" fill="var(--os-window, #2a2a30)"/><path v-if="nc.dnd" d="M6.2 9.2 9.8 5.6" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/></svg>
      </button>
      <button class="hbtn" title="清除全部" @click="nc.clear()">
        <svg viewBox="0 0 16 16" width="14" height="14"><path d="M3 4.5h10M6.5 4.5V3.2a.7.7 0 0 1 .7-.7h1.6a.7.7 0 0 1 .7.7v1.3M4.6 4.5l.5 8a1 1 0 0 0 1 .9h3.8a1 1 0 0 0 1-.9l.5-8" fill="none" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/></svg>
      </button>
    </header>

    <div class="nc-list">
      <div v-for="n in nc.list" :key="n.id" class="nc-item" :class="{ unread: !n.read }"
        @click="activate(n.id, n.appId)">
        <AppIcon v-if="n.appId" :app="n.appId" :size="34" />
        <span v-else class="nc-sys-icon small" aria-hidden="true">
          <svg viewBox="0 0 16 16" width="15" height="15"><path d="M8 1.5a4.2 4.2 0 0 0-4.2 4.2v2.6l-1.1 2.2a.7.7 0 0 0 .63 1H6a2 2 0 0 0 4 0h2.67a.7.7 0 0 0 .63-1L12.2 8.3V5.7A4.2 4.2 0 0 0 8 1.5Z" fill="currentColor"/></svg>
        </span>
        <div class="main">
          <div class="top"><b>{{ n.title }}</b><span class="time">{{ fmtTime(n.ts) }}</span></div>
          <p v-if="n.body">{{ n.body }}</p>
          <span class="app">{{ appName(n.appId) }}</span>
        </div>
        <button class="nc-x" title="删除" @click.stop="nc.dismiss(n.id)">✕</button>
      </div>
      <div v-if="!nc.list.length" class="nc-empty">暂无通知<br /><small>应用可经 notify() 或总线 sys:notify 发送</small></div>
    </div>
  </div>
</template>

<style scoped>
.nc-toasts {
  position: fixed; top: 36px; right: 12px; z-index: 10001;
  display: flex; flex-direction: column; gap: 10px; width: 344px;
  pointer-events: none;
}
.nc-toast {
  pointer-events: auto; position: relative; cursor: pointer;
  display: flex; gap: 10px; align-items: flex-start; padding: 11px 12px;
  border-radius: 15px; color: var(--os-text);
  background: var(--os-panel); backdrop-filter: blur(30px) saturate(1.8);
  -webkit-backdrop-filter: blur(30px) saturate(1.8);
  border: 1px solid var(--os-border);
  box-shadow: var(--os-shadow-sm), inset 0 1px 0 var(--os-highlight-edge);
  animation: nc-slide-in .28s var(--os-ease-out);
}
@keyframes nc-slide-in {
  from { transform: translateX(60px); opacity: 0; }
  to { transform: translateX(0); opacity: 1; }
}
.nc-toast-main { flex: 1; min-width: 0; }
.nc-toast-top { display: flex; align-items: baseline; gap: 8px; }
.nc-toast .t { font-size: 13.5px; font-weight: 650; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.nc-toast .app { margin-left: auto; font-size: 11px; color: var(--os-text-secondary); white-space: nowrap; }
.nc-toast p {
  margin: 3px 0 0; font-size: 12.5px; color: var(--os-text-secondary); line-height: 1.45;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
}
.nc-sys-icon {
  display: inline-flex; align-items: center; justify-content: center;
  width: 36px; height: 36px; border-radius: 9px; flex: none;
  background: linear-gradient(180deg, #6f7bff, #4a54e8); color: #fff;
}
.nc-sys-icon.small { width: 34px; height: 34px; }
.nc-x {
  position: absolute; top: 7px; right: 7px; width: 18px; height: 18px; border-radius: 50%;
  border: none; background: var(--os-hover); color: var(--os-text);
  font-size: 10px; line-height: 1; cursor: pointer; opacity: 0; transition: opacity .12s;
  display: flex; align-items: center; justify-content: center;
}
.nc-toast:hover .nc-x, .nc-item:hover .nc-x { opacity: 1; }
.nc-x:hover { background: rgba(255, 255, 255, .22); }

.nc-panel {
  position: fixed; top: 34px; right: 10px; z-index: 10001;
  width: 344px; max-height: calc(100vh - 140px);
  display: flex; flex-direction: column; border-radius: 16px; overflow: hidden;
  background: var(--os-panel); backdrop-filter: blur(34px) saturate(1.8);
  -webkit-backdrop-filter: blur(34px) saturate(1.8);
  border: 1px solid var(--os-border);
  box-shadow: var(--os-shadow-sm), inset 0 1px 0 var(--os-highlight-edge);
  animation: nc-pop .18s var(--os-ease-out);
  transform-origin: top right;
}
@keyframes nc-pop { from { transform: scale(.96) translateY(-6px); opacity: 0; } }
.nc-panel header {
  display: flex; align-items: center; gap: 4px; padding: 10px 12px 8px;
  border-bottom: 1px solid var(--os-border);
}
.nc-panel header b { font-size: 13px; }
.grow { flex: 1; }
.hbtn {
  display: inline-flex; align-items: center; justify-content: center;
  width: 26px; height: 26px; border-radius: 7px; border: none;
  background: transparent; color: var(--os-text-secondary); cursor: pointer;
}
.hbtn:hover { background: var(--os-hover); color: var(--os-text); }
.hbtn.on { color: var(--os-accent); }
.nc-list { overflow-y: auto; padding: 8px; }
.nc-item {
  position: relative; display: flex; gap: 10px; padding: 10px; border-radius: 12px;
  cursor: pointer; transition: background .1s;
}
.nc-item:hover { background: var(--os-hover); }
.nc-item.unread::before {
  content: ''; position: absolute; left: 3px; top: 50%; transform: translateY(-50%);
  width: 5px; height: 5px; border-radius: 50%; background: var(--os-accent);
}
.nc-item .main { flex: 1; min-width: 0; }
.nc-item .top { display: flex; align-items: baseline; gap: 8px; }
.nc-item b { font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.nc-item .time { margin-left: auto; font-size: 11px; color: var(--os-text-secondary); white-space: nowrap; }
.nc-item p { margin: 2px 0 3px; font-size: 12.5px; color: var(--os-text-secondary); line-height: 1.45; }
.nc-item .app { font-size: 11px; color: var(--os-text-tertiary, var(--os-text-secondary)); opacity: .8; }
.nc-empty {
  padding: 34px 16px; text-align: center; font-size: 13px; color: var(--os-text-secondary); line-height: 1.8;
}
.nc-empty small { font-size: 11px; opacity: .7; }
</style>
