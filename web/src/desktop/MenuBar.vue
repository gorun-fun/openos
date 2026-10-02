<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch, type Component } from 'vue'
import { useSession } from '../stores/session'
import { useWindows } from '../stores/windows'
import { useDesktop } from '../stores/desktop'
import { useApps } from '../stores/apps'
import { useNotifications } from '../stores/notifications'
import { api } from '../api/client'
import { bus } from '../bus/bus'
import { shell } from '../shell'
import { OsModal, OsButton } from '../ui'
import { useMenu } from '../ui/menu'

const emit = defineEmits<{ launchpad: []; spotlight: [] }>()
const session = useSession()
const windows = useWindows()
const desktop = useDesktop()
const apps = useApps()
const notifications = useNotifications()

const now = ref(new Date())
let timer = 0
onMounted(() => { timer = window.setInterval(() => (now.value = new Date()), 1000) })
onBeforeUnmount(() => clearInterval(timer))

// macOS 菜单栏时间格式：周三 10月1日 09:41（星期在前）
const timeText = computed(() =>
  `周${'日一二三四五六'[now.value.getDay()]} ${now.value.getMonth() + 1}月${now.value.getDate()}日 ${String(now.value.getHours()).padStart(2, '0')}:${String(now.value.getMinutes()).padStart(2, '0')}`,
)

const activeWin = computed(() => (windows.active?.minimized ? undefined : windows.active))
const activeApp = computed(() => {
  const w = activeWin.value
  if (!w) return null
  return { id: w.appId, name: apps.byId(w.appId)?.name || w.title || w.appId }
})

/** 各应用的专属菜单项（只放真正可用的动作；未登记的应用走通用项）。
 *  宿主可经 createOpenOS({ appMenus }) 注入/覆盖（如业务应用的快捷动作）。 */
const APP_MENU_ITEMS: Record<string, { label: string; run: () => void }[]> = {
  terminal: [{ label: '新建终端窗口', run: () => newWin('terminal') }],
  files: [{ label: '新建文件窗口', run: () => newWin('files') }],
  docs: [{ label: '新建文档窗口', run: () => newWin('docs') }],
  sheets: [{ label: '新建表格窗口', run: () => newWin('sheets') }],
  slides: [{ label: '新建幻灯片窗口', run: () => newWin('slides') }],
  goder: [{ label: '新建工作台窗口', run: () => newWin('goder') }],
  notes: [{ label: '新建便签窗口', run: () => newWin('notes') }],
}
/** 当前生效的应用菜单 = 内置 ∪ 宿主注入（宿主可覆盖同名内置应用）。 */
const appMenuOf = (appId: string) => shell.appMenus[appId] || APP_MENU_ITEMS[appId] || []
/** 菜单栏右侧扩展（单个或数组多槽）。 */
const extras = computed(() => (shell.menubarExtras ? ([] as Component[]).concat(shell.menubarExtras) : []))
/** 打开同应用的新窗口（携带时间戳参数避开“同参数去重”）。 */
function newWin(appId: string) { windows.open(appId, { n: Date.now() }) }

// 菜单栏下拉：menuOpen 记录"哪个"下拉，显示权归全局菜单单例（ui/menu.ts）。
// 点菜单外任意处 / Esc / 其他菜单打开时由单例统一关闭，这里不再自挂 document 监听。
const menuOpen = ref('')
const dropdown = useMenu('menubar')
function toggle(m: string) {
  if (menuOpen.value === m && dropdown.isOpen) { dropdown.close(); return }
  menuOpen.value = m
  dropdown.open()
}
function closeMenu() { dropdown.close() }
watch(() => dropdown.isOpen, (v) => { if (!v) menuOpen.value = '' })

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
        <button class="mb logo" data-os-menu-anchor :title="shell.brand.name" @click="toggle('os')">{{ shell.brand.logo }}</button>
        <div class="dropdown os-menu-root" v-if="menuOpen === 'os'">
          <button @click="showAbout = true; closeMenu()">关于 {{ shell.brand.name }}…</button>
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
          <button class="mb app-name" data-os-menu-anchor @click="toggle('app')">{{ activeApp.name }}</button>
          <div class="dropdown os-menu-root" v-if="menuOpen === 'app'">
            <button
              v-for="item in appMenuOf(activeApp.id)" :key="item.label"
              @click="item.run(); closeMenu()"
            >{{ item.label }}</button>
            <div v-if="appMenuOf(activeApp.id).length" class="sep" />
            <button :disabled="!activeWin" @click="activeWin && windows.minimize(activeWin.id); closeMenu()">隐藏<span class="hint">⌥M</span></button>
            <button :disabled="!activeWin" @click="activeWin && windows.toggleMaximize(activeWin.id); closeMenu()">缩放<span class="hint">⌥↩</span></button>
            <div class="sep" />
            <button :disabled="!activeWin" @click="activeWin && windows.close(activeWin.id); closeMenu()">退出 {{ activeApp.name }}<span class="hint">⌥W</span></button>
          </div>
        </div>

        <!-- 窗口菜单 -->
        <div class="menu-anchor">
          <button class="mb" data-os-menu-anchor @click="toggle('win')">窗口</button>
          <div class="dropdown os-menu-root" v-if="menuOpen === 'win'">
            <button :disabled="!windows.active" @click="windows.active && windows.minimize(windows.active.id); closeMenu()">最小化<span class="hint">⌥M</span></button>
            <button :disabled="!windows.active" @click="windows.active && windows.toggleMaximize(windows.active.id); closeMenu()">缩放<span class="hint">⌥↩</span></button>
            <button :disabled="!windows.active" @click="windows.active && windows.snap(windows.active.id, 'left'); closeMenu()">左半屏</button>
            <button :disabled="!windows.active" @click="windows.active && windows.snap(windows.active.id, 'right'); closeMenu()">右半屏</button>
            <div class="sep" />
            <button :disabled="windows.list.length < 2" @click="windows.cycle(); closeMenu()">循环切换<span class="hint">⌥`</span></button>
            <button :disabled="!windows.active" @click="windows.active && windows.close(windows.active.id); closeMenu()">关闭窗口<span class="hint">⌥W</span></button>
            <button :disabled="!windows.list.length" @click="windows.list.forEach(w => windows.close(w.id)); closeMenu()">关闭全部窗口</button>
          </div>
        </div>

        <div class="menu-anchor">
          <button class="mb" data-os-menu-anchor @click="toggle('help')">帮助</button>
          <div class="dropdown os-menu-root" v-if="menuOpen === 'help'">
            <button v-if="apps.byId('welcome')" @click="windows.open('welcome'); closeMenu()">使用指南</button>
            <button v-if="apps.byId('goder')" @click="windows.open('goder'); closeMenu()">Goder - AI助手</button>
          </div>
        </div>
      </template>
    </nav>

    <nav class="right">
      <!-- 宿主应用注入的菜单栏扩展（门店切换器/业务状态角标等），见 createOpenOS({menubarExtras})；支持数组多槽 -->
      <component :is="x" v-for="(x, i) in extras" :key="i" />
      <!-- 聚焦搜索（⌘/Alt+Space） -->
      <button class="mb icon-btn" title="聚焦搜索（⌘/Alt+Space）" @click="emit('spotlight')">
        <svg viewBox="0 0 16 16" class="mb-svg"><circle cx="6.8" cy="6.8" r="4.6" fill="none" stroke="currentColor" stroke-width="1.5" /><line x1="10.4" y1="10.4" x2="14.2" y2="14.2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
      </button>
      <span class="mb chip" :title="online.join('、')">👥 {{ online.length }} 在线</span>
      <span class="mb chip" :title="aiOn ? 'AI 已接入' : 'AI 演示模式（设置 → AI 服务 可配置）'">{{ aiOn ? '✦ AI' : '✦ AI·demo' }}</span>
      <button class="mb chip icon" title="切换深浅色" @click="desktop.setDark(!desktop.dark)">
        {{ desktop.dark ? '🌙' : '☀️' }}
      </button>
      <span class="mb user">{{ session.user?.displayName || session.user?.username }}</span>
      <!-- 时钟即通知中心入口（macOS 习惯）：点击唤出右侧通知面板，未读时显小红点 -->
      <button class="mb time" data-os-nc-clock title="通知中心" @click="notifications.togglePanel()">
        {{ timeText }}
        <i v-if="notifications.unread" class="nc-dot" />
      </button>
    </nav>

    <OsModal v-if="showAbout" title="关于 OpenOS" okText="好" :cancelText="''" @ok="showAbout = false" @cancel="showAbout = false">
      <p style="line-height:1.9">
        <b>{{ shell.brand.name }}</b><template v-if="shell.brand.version"> {{ shell.brand.version }}</template>（OpenOS v{{ version || '1.1' }}）<br />
        Go + Vite (Vue 3) · 事件总线通信 · SQLite 存储<br />
        已运行：{{ uptimeText }} · 在线：{{ online.length }} 人<br />
        快捷键：⌘/Alt+Space 聚焦搜索 · F4 启动台 · Alt+W 关闭 · Alt+M 最小化 · Alt+↩ 缩放 · Alt+` 切换窗口
      </p>
    </OsModal>
  </header>
</template>

<style scoped>
.menubar {
  height: 28px; display: flex; align-items: center; justify-content: space-between;
  padding: 0 12px; position: relative; z-index: 9999;
  border-bottom: 1px solid var(--os-border);
  box-shadow: inset 0 -1px 0 rgba(255,255,255,.04), inset 0 1px 0 var(--os-highlight-edge);
  font-size: 13px;
}
nav { display: flex; align-items: center; gap: 4px; position: relative; }
.mb {
  padding: 2px 9px; border-radius: 6px; border: none; background: transparent;
  color: var(--os-text); font-size: 13px; transition: background .12s ease; cursor: default;
}
button.mb { cursor: pointer; }
.mb:hover { background: var(--os-hover); }
.mb:disabled { opacity: .4; }
.logo { font-weight: 700; font-size: 14px; }
.app-name { font-weight: 700; color: var(--os-text); max-width: 160px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.chip {
  font-size: 12px; color: var(--os-text-secondary);
  padding: 1px 10px; border-radius: 999px;
  background: var(--os-hover); border: 1px solid var(--os-border);
}
.chip.icon { background: transparent; border-color: transparent; padding: 1px 8px; }
.user { color: var(--os-text-secondary); }
.time { font-variant-numeric: tabular-nums; position: relative; padding: 2px 8px; }
.nc-dot {
  position: absolute; top: 1px; right: 1px; width: 5px; height: 5px;
  border-radius: 50%; background: #ff5f57; box-shadow: 0 0 0 1.5px rgba(0, 0, 0, .18);
}

.menu-anchor { position: relative; display: flex; }
.dropdown {
  position: absolute; top: 28px; left: 0; min-width: 230px; z-index: 10000;
  background: var(--os-panel); backdrop-filter: blur(30px) saturate(1.8);
  -webkit-backdrop-filter: blur(30px) saturate(1.8);
  border: 1px solid var(--os-border);
  border-radius: 12px; padding: 6px;
  box-shadow: var(--os-shadow-sm), inset 0 1px 0 var(--os-highlight-edge);
  animation: os-pop-in .14s var(--os-ease-out);
  transform-origin: top left;
}
.dropdown button {
  display: flex; align-items: center; width: 100%; text-align: left; padding: 6px 12px; border: none;
  background: transparent; border-radius: 7px; color: var(--os-text); font-size: 13px; cursor: pointer;
  transition: background .1s ease;
}
.dropdown button .hint { margin-left: auto; padding-left: 18px; font-size: 12px; color: var(--os-text-secondary); }
.dropdown button:hover:not(:disabled) .hint { color: rgba(255, 255, 255, .78); }
.icon-btn { display: inline-flex; align-items: center; justify-content: center; }
.mb-svg { width: 15px; height: 15px; }
.dropdown button:hover:not(:disabled) { background: var(--os-accent); color: #fff; }
.dropdown button:disabled { opacity: .35; cursor: default; }
.sep { height: 1px; background: var(--os-border); margin: 5px 10px; }
</style>
