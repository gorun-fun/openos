<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useSession } from '../stores/session'
import { useWindows } from '../stores/windows'
import { useDesktop } from '../stores/desktop'
import { useApps } from '../stores/apps'
import { api } from '../api/client'
import { bus } from '../bus/bus'
import { OsModal, OsButton } from '../ui'

const emit = defineEmits<{ launchpad: [] }>()
const session = useSession()
const windows = useWindows()
const desktop = useDesktop()
const apps = useApps()

const now = ref(new Date())
let timer = 0
onMounted(() => { timer = window.setInterval(() => (now.value = new Date()), 1000) })
onBeforeUnmount(() => clearInterval(timer))

const timeText = computed(() =>
  `${now.value.getMonth() + 1}月${now.value.getDate()}日 周${'日一二三四五六'[now.value.getDay()]} ${String(now.value.getHours()).padStart(2, '0')}:${String(now.value.getMinutes()).padStart(2, '0')}`,
)

const activeWin = computed(() => (windows.active?.minimized ? undefined : windows.active))
const activeApp = computed(() => {
  const w = activeWin.value
  if (!w) return null
  return { id: w.appId, name: apps.byId(w.appId)?.name || w.title || w.appId }
})

/** 各应用的专属菜单项（只放真正可用的动作；未登记的应用走通用项）。 */
const APP_MENU_ITEMS: Record<string, { label: string; run: () => void }[]> = {
  terminal: [{ label: '新建终端窗口', run: () => newWin('terminal') }],
  files: [{ label: '新建文件窗口', run: () => newWin('files') }],
  docs: [{ label: '新建文档窗口', run: () => newWin('docs') }],
  sheets: [{ label: '新建表格窗口', run: () => newWin('sheets') }],
  slides: [{ label: '新建幻灯片窗口', run: () => newWin('slides') }],
  goder: [{ label: '新建工作台窗口', run: () => newWin('goder') }],
  notes: [{ label: '新建便签窗口', run: () => newWin('notes') }],
}
/** 打开同应用的新窗口（携带时间戳参数避开“同参数去重”）。 */
function newWin(appId: string) { windows.open(appId, { n: Date.now() }) }

const menuOpen = ref('')
function toggle(m: string) { menuOpen.value = menuOpen.value === m ? '' : m }
function closeMenu() { menuOpen.value = '' }

// 点击菜单栏以外任意处关闭下拉（capture 阶段监听 document）
function onDocPointerDown(e: PointerEvent) {
  if (!(e.target as HTMLElement)?.closest?.('.menubar')) closeMenu()
}
onMounted(() => document.addEventListener('pointerdown', onDocPointerDown, true))
onBeforeUnmount(() => document.removeEventListener('pointerdown', onDocPointerDown, true))

const showAbout = ref(false)
const version = ref('')
const uptime = ref(0)
onMounted(async () => {
  try {
    const h = await api.get<{ version: string; uptimeSec: number }>('/api/health')
    version.value = h.version
    uptime.value = h.uptimeSec
  } catch { /* ignore */ }
})
const uptimeText = computed(() => {
  const s = uptime.value
  if (s < 60) return s + ' 秒'
  if (s < 3600) return Math.floor(s / 60) + ' 分钟'
  return Math.floor(s / 3600) + ' 小时 ' + Math.floor((s % 3600) / 60) + ' 分'
})

const online = ref<string[]>([])
let onlineTimer = 0
async function loadOnline() {
  try {
    const h = await api.get<{ online: string[] }>('/api/health')
    online.value = h.online || []
  } catch { /* ignore */ }
}
const aiOn = ref(false)
onMounted(async () => {
  loadOnline()
  onlineTimer = window.setInterval(loadOnline, 30000)
  bus.on('sys:presence', loadOnline)
  try {
    const h = await api.get<{ ai: boolean }>('/api/health')
    aiOn.value = h.ai
  } catch { /* ignore */ }
})
onBeforeUnmount(() => clearInterval(onlineTimer))

function logout() {
  session.logout()
  location.reload()
}

defineExpose({ closeMenu })
</script>

<template>
  <header class="menubar os-glass" @pointerdown.stop>
    <nav class="left">
      <!-- 平时只保留一个系统图标；左侧其余菜单跟随当前应用（macOS 风格） -->
      <div class="menu-anchor">
        <button class="mb logo" title="OpenOS" @click="toggle('os')">🖥️</button>
        <div class="dropdown" v-if="menuOpen === 'os'">
          <button @click="showAbout = true; closeMenu()">关于 OpenOS…</button>
          <button @click="windows.open('settings'); closeMenu()">系统设置…</button>
          <button @click="emit('launchpad'); closeMenu()">启动台</button>
          <button @click="windows.open('welcome'); closeMenu()">使用指南…</button>
          <div class="sep" />
          <button @click="desktop.setDark(!desktop.dark); closeMenu()">
            切换深色模式（当前{{ desktop.dark ? '深' : '浅' }}）
          </button>
          <button @click="desktop.setWallpaper(desktop.wallpaper + 1); closeMenu()">更换壁纸</button>
          <div class="sep" />
          <button @click="logout">锁定屏幕 / 退出登录</button>
        </div>
      </div>

      <template v-if="activeApp">
        <!-- 当前应用菜单（粗体应用名） -->
        <div class="menu-anchor">
          <button class="mb app-name" @click="toggle('app')">{{ activeApp.name }}</button>
          <div class="dropdown" v-if="menuOpen === 'app'">
            <button
              v-for="item in APP_MENU_ITEMS[activeApp.id] || []" :key="item.label"
              @click="item.run(); closeMenu()"
            >{{ item.label }}</button>
            <div v-if="(APP_MENU_ITEMS[activeApp.id] || []).length" class="sep" />
            <button :disabled="!activeWin" @click="activeWin && windows.minimize(activeWin.id); closeMenu()">隐藏（Alt+M）</button>
            <button :disabled="!activeWin" @click="activeWin && windows.toggleMaximize(activeWin.id); closeMenu()">缩放（Alt+↩）</button>
            <div class="sep" />
            <button :disabled="!activeWin" @click="activeWin && windows.close(activeWin.id); closeMenu()">退出 {{ activeApp.name }}（Alt+W）</button>
          </div>
        </div>

        <!-- 窗口菜单 -->
        <div class="menu-anchor">
          <button class="mb" @click="toggle('win')">窗口</button>
          <div class="dropdown" v-if="menuOpen === 'win'">
            <button :disabled="!windows.active" @click="windows.active && windows.minimize(windows.active.id); closeMenu()">最小化（Alt+M）</button>
            <button :disabled="!windows.active" @click="windows.active && windows.toggleMaximize(windows.active.id); closeMenu()">缩放（Alt+↩）</button>
            <button :disabled="!windows.active" @click="windows.active && windows.snap(windows.active.id, 'left'); closeMenu()">左半屏</button>
            <button :disabled="!windows.active" @click="windows.active && windows.snap(windows.active.id, 'right'); closeMenu()">右半屏</button>
            <div class="sep" />
            <button :disabled="windows.list.length < 2" @click="windows.cycle(); closeMenu()">循环切换（Alt+`）</button>
            <button :disabled="!windows.active" @click="windows.active && windows.close(windows.active.id); closeMenu()">关闭窗口（Alt+W）</button>
            <button :disabled="!windows.list.length" @click="windows.list.forEach(w => windows.close(w.id)); closeMenu()">关闭全部窗口</button>
          </div>
        </div>

        <div class="menu-anchor">
          <button class="mb" @click="toggle('help')">帮助</button>
          <div class="dropdown" v-if="menuOpen === 'help'">
            <button @click="windows.open('welcome'); closeMenu()">使用指南</button>
            <button @click="windows.open('goder'); closeMenu()">Goder - AI助手</button>
          </div>
        </div>
      </template>
    </nav>

    <nav class="right">
      <span class="mb chip" :title="online.join('、')">👥 {{ online.length }} 在线</span>
      <span class="mb chip" :title="aiOn ? 'AI 已接入' : 'AI 演示模式（设置 → AI 服务 可配置）'">{{ aiOn ? '✦ AI' : '✦ AI·demo' }}</span>
      <button class="mb chip icon" title="切换深浅色" @click="desktop.setDark(!desktop.dark)">
        {{ desktop.dark ? '🌙' : '☀️' }}
      </button>
      <span class="mb user">{{ session.user?.displayName || session.user?.username }}</span>
      <span class="mb time">{{ timeText }}</span>
    </nav>

    <OsModal v-if="showAbout" title="关于 OpenOS" okText="好" :cancelText="''" @ok="showAbout = false" @cancel="showAbout = false">
      <p style="line-height:1.9">
        <b>OpenOS</b> v{{ version || '1.1' }} · 仿 macOS 网页桌面<br />
        前后端分离：Go + Vite (Vue 3) · 事件总线通信 · SQLite 存储<br />
        已运行：{{ uptimeText }} · 在线：{{ online.length }} 人<br />
        快捷键：Alt+W 关闭 · Alt+M 最小化 · Alt+↩ 缩放 · Alt+` 切换 · Alt+Space 启动台
      </p>
    </OsModal>
  </header>
</template>

<style scoped>
.menubar {
  height: 28px; display: flex; align-items: center; justify-content: space-between;
  padding: 0 12px; position: relative; z-index: 9999;
  border-bottom: 1px solid var(--os-border);
  font-size: 13px;
}
nav { display: flex; align-items: center; gap: 4px; position: relative; }
.mb { padding: 2px 8px; border-radius: 6px; border: none; background: transparent; color: var(--os-text); font-size: 13px; }
.mb:hover { background: var(--os-hover); }
.mb:disabled { opacity: .4; }
.logo { font-weight: 700; font-size: 14px; }
.app-name { font-weight: 700; color: var(--os-text); max-width: 160px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.chip { font-size: 12px; color: var(--os-text-secondary); }
.user { color: var(--os-text-secondary); }
.time { font-variant-numeric: tabular-nums; }

.menu-anchor { position: relative; display: flex; }
.dropdown {
  position: absolute; top: 26px; left: 0; min-width: 220px; z-index: 10000;
  background: var(--os-panel-solid); border: 1px solid var(--os-border);
  border-radius: 10px; box-shadow: var(--os-shadow-sm); padding: 5px;
  animation: os-fade-in .12s ease;
}
.dropdown button {
  display: block; width: 100%; text-align: left; padding: 7px 12px; border: none;
  background: transparent; border-radius: 6px; color: var(--os-text); font-size: 13px; cursor: pointer;
}
.dropdown button:hover:not(:disabled) { background: var(--os-accent); color: #fff; }
.dropdown button:disabled { opacity: .35; cursor: default; }
.sep { height: 1px; background: var(--os-border); margin: 5px 8px; }
</style>
