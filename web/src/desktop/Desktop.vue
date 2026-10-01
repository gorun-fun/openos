<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useSession } from '../stores/session'
import { useDesktop } from '../stores/desktop'
import { useApps } from '../stores/apps'
import { useWindows } from '../stores/windows'
import { bus } from '../bus/bus'
import MenuBar from './MenuBar.vue'
import Dock from './Dock.vue'
import Launchpad from './Launchpad.vue'
import Spotlight from './Spotlight.vue'
import WindowFrame from './WindowFrame.vue'
import Shortcuts from './Shortcuts.vue'
import Widgets from './Widgets.vue'
import { snapPreview } from './snapState'
import { fsApi } from '../api/client'
import { toast } from '../ui/toast'
import { useMenu, menuPos } from '../ui/menu'
import { widgetDefs, widgetDef } from './widgetRegistry'
import { appForPath } from '../apps/openWith'
import { installAgentBridge } from './agentBridge'

const session = useSession()
const desktop = useDesktop()
const apps = useApps()
const windows = useWindows()

// ---- 全局事件总线路由：任何入口（终端 / goder / 其他窗口）都可操控桌面 ----
const offs: (() => void)[] = []

onMounted(async () => {
  desktop.load()
  await apps.refresh()
  bus.connect(session.user!.username)

  offs.push(
    bus.on('sys:open', (p: any) => { if (p?.app) windows.open(p.app) }),
    bus.on('sys:cmd', (p: any) => runCmd(p)),
    bus.on('sys:apps-changed', () => apps.refresh()),
    bus.on('sys:presence', (p: any) => {
      if (p.online && p.user !== session.user!.username) toast(`${p.user} 上线了`)
    }),
    // goder 桌面控制插件（openos MCP）的前端桥：执行 os_* 工具下发的动作
    installAgentBridge(),
  )

  // 恢复上次会话的窗口（按用户持久化），首次进入打开欢迎窗口
  const restored = restoreWindows()
  if (!restored && windows.list.length === 0) windows.open('welcome')

  // 快捷方式/小组件都已挂载登记，统一重排一次：吸附网格、解决重叠
  await nextTick()
  widgetsRef.value?.relayout()
  shortcutsRef.value?.relayout()

  window.addEventListener('resize', onResize)
  window.addEventListener('keydown', onShortcut)
})

onBeforeUnmount(() => {
  offs.forEach((off) => off())
  window.removeEventListener('resize', onResize)
  window.removeEventListener('keydown', onShortcut)
})

function onResize() {
  windows.onViewportResize()
  widgetsRef.value?.relayout()
  shortcutsRef.value?.relayout()
}

// 「最大化自动隐藏 Dock」开/关、或最大化窗口出现/消失时，可用区域变化 → 重排窗口
watch(() => desktop.dockAutoHide, () => windows.onViewportResize())
watch(() => windows.list.some((w) => w.maximized), () => windows.onViewportResize())

// ---- 桌面拖放：启动台应用拖出 → 快捷方式；系统文件拖入 → 上传 + 快捷方式 ----
const shortcutsRef = ref<InstanceType<typeof Shortcuts>>()
const widgetsRef = ref<InstanceType<typeof Widgets>>()

function onDrop(e: DragEvent) {
  e.preventDefault()
  const x = e.clientX, y = e.clientY
  const dt = e.dataTransfer
  if (!dt) return
  // 1. 快捷方式自身拖动换位
  if (dt.types.includes('application/x-openos-shortcut')) {
    shortcutsRef.value?.dropShortcut(e)
    return
  }
  // 2. 启动台拖出的应用
  const appId = dt.getData('application/x-openos-app')
  if (appId && apps.byId(appId)) {
    shortcutsRef.value?.addAt('app', appId, apps.byId(appId)!.name, x, y)
    toast(`已创建「${apps.byId(appId)!.name}」快捷方式`)
    return
  }
  // 3. 操作系统文件 → 上传到「桌面」目录并生成快捷方式
  const files = [...(dt.files || [])]
  if (files.length) {
    void (async () => {
      try {
        const dir = `/home/${session.user!.username}/桌面`
        await fsApi.mkdir(dir).catch(() => {})
        const up = await fsApi.upload(dir, files)
        for (const p of up.saved || []) {
          shortcutsRef.value?.addAt('file', p, p.split('/').pop() || '文件', x, y)
        }
        toast(`已上传 ${up.saved?.length || 0} 个文件到「桌面」`)
      } catch (err: any) {
        toast('上传失败：' + err.message)
      }
    })()
  }
}

function runCmd(p: any) {
  if (!p || !p.cmd) return
  switch (p.cmd) {
    case 'open': windows.open(p.arg); toast(`已打开：${apps.byId(p.arg)?.name || p.arg}`); break
    case 'close': windows.closeApp(p.arg); toast(`已关闭：${apps.byId(p.arg)?.name || p.arg}`); break
    case 'file': {
      const app = appForPath(String(p.arg))
      windows.open(app, { path: p.arg })
      toast('已打开文件')
      break
    }
    case 'wallpaper': desktop.setWallpaper(Number(p.arg) || 1); toast('已更换壁纸'); break
    case 'dark': desktop.setDark(p.arg === 'on'); toast(p.arg === 'on' ? '已切换深色模式' : '已切换浅色模式'); break
    default: toast(`未知指令：${p.cmd}`)
  }
}

// ---- 键盘快捷键 ----
const launchpadOpen = ref(false)
const spotOpen = ref(false)
function onShortcut(e: KeyboardEvent) {
  const inField = !!(e.target as HTMLElement)?.closest?.('input, textarea, [contenteditable="true"]')
  if (e.key === 'Escape') {
    launchpadOpen.value = false
    spotOpen.value = false
    return
  }
  // 聚焦搜索：⌘/Ctrl/Alt + Space（macOS 为 ⌘Space；浏览器内 Alt 最稳）。
  // 放在输入框判定之前——任何场景下都应可唤起。
  if (e.code === 'Space' && (e.metaKey || e.ctrlKey || e.altKey)) {
    spotOpen.value = !spotOpen.value
    e.preventDefault()
    return
  }
  if (e.key === 'F4') { launchpadOpen.value = !launchpadOpen.value; e.preventDefault(); return } // 启动台
  if (!e.altKey || inField) return
  const w = windows.active
  switch (e.key) {
    case 'w': case 'W': if (w) { windows.close(w.id); e.preventDefault(); } break
    case 'm': case 'M': if (w) { windows.minimize(w.id); e.preventDefault(); } break
    case 'Enter': if (w) { windows.toggleMaximize(w.id); e.preventDefault(); } break
    case '`': windows.cycle(); e.preventDefault(); break
  }
}

// ---- 窗口状态持久化（同用户刷新后恢复） ----
const STATE_KEY = () => `openos_wins_${session.user!.username}`
let saveTimer = 0
watch(
  () => windows.list.map((w) => `${w.appId}|${JSON.stringify(w.props)}|${w.title}`).join(';'),
  () => {
    clearTimeout(saveTimer)
    saveTimer = window.setTimeout(() => {
      const saved = windows.list.slice(0, 12).map((w) => ({ appId: w.appId, props: w.props, title: w.title }))
      localStorage.setItem(STATE_KEY(), JSON.stringify(saved))
    }, 600)
  },
)

function restoreWindows(): boolean {
  try {
    const raw = localStorage.getItem(STATE_KEY())
    if (!raw) return false
    const saved: { appId: string; props?: any; title?: string }[] = JSON.parse(raw)
    const allowed = new Set(apps.apps.map((a) => a.id))
    let n = 0
    for (const s of saved) {
      if (!allowed.has(s.appId)) continue // 权限变化后不再恢复
      windows.open(s.appId, s.props, s.title)
      n++
    }
    return n > 0
  } catch { return false }
}

// ---- 桌面右键菜单（区分目标：桌面 / 快捷方式 / 小组件）----
// 显示权交给全局菜单单例（ui/menu.ts）：同一时刻全系统只有一个菜单，
// 点外部 / Esc / 滚动统一关闭；这里只保留"右键目标"的内容状态。
type CtxTarget =
  | { kind: 'shortcut'; key: string }
  | { kind: 'widget'; type: string; noteId?: string }
  | null
const ctx = ref<CtxTarget>(null)
const menu = useMenu('desktop')
const ctxPos = ref({ x: 0, y: 0 })
const ctxEl = ref<HTMLElement>()

function onContext(e: MouseEvent) {
  const t = e.target as HTMLElement
  // 应用窗口 / 菜单栏 / Dock / 启动台内部：交给应用自定义右键；
  // 未被应用接管的由全局守卫屏蔽原生菜单，桌面不弹菜单
  if (t.closest('.window, .menubar, .dock, .launchpad')) return
  // 输入框内右键保留系统默认（复制/粘贴等）；例外：桌面小组件（便签）内的
  // 输入框占满组件，右键是移除/换色的唯一入口，仍弹桌面菜单
  if (t.closest('input, textarea, [contenteditable="true"]') && !t.closest('.widget')) return

  let target: CtxTarget = null
  const widgetHost = t.closest<HTMLElement>('.widget')
  const shortcutHost = t.closest<HTMLElement>('.shortcut')
  const widgetType = widgetHost?.dataset.type
  if (widgetType && widgetDef(widgetType)) {
    target = { kind: 'widget', type: widgetType, noteId: widgetHost?.dataset.note || undefined }
  } else if (shortcutHost?.dataset.id) {
    target = { kind: 'shortcut', key: shortcutHost.dataset.id }
  }

  e.preventDefault()
  e.stopPropagation() // 桌面已接管，阻止全局守卫在冒泡末端又关掉刚打开的菜单
  ctx.value = target
  menu.open(e.clientX, e.clientY)
}

// 菜单关闭即清空右键目标；打开/重定位时按全局坐标渲染并夹在视口内（避免右下角弹出越界）。
// 同时盯 menuPos：菜单开着时在桌面另一处再右键 = 原地重定位（owner 不变，isOpen 不触发）。
watch([() => menu.isOpen, menuPos], ([open]) => {
  if (!open) { ctx.value = null; return }
  ctxPos.value = { ...menuPos.value }
  void nextTick(() => {
    const el = ctxEl.value
    if (!el) return
    const r = el.getBoundingClientRect()
    ctxPos.value = {
      x: Math.max(6, Math.min(menuPos.value.x, window.innerWidth - r.width - 6)),
      y: Math.max(32, Math.min(menuPos.value.y, window.innerHeight - r.height - 6)),
    }
  })
})

function ctxOpenShortcut() {
  const key = ctx.value
  if (key?.kind === 'shortcut') shortcutsRef.value?.openByKey(key.key)
}
function ctxRemoveShortcut() {
  const key = ctx.value
  if (key?.kind === 'shortcut') shortcutsRef.value?.removeByKey(key.key)
}
function ctxRemoveWidget() {
  const t = ctx.value
  if (t?.kind === 'widget') widgetsRef.value?.remove(t.type, t.noteId)
}
function ctxCycleNoteColor() {
  const t = ctx.value
  if (t?.kind === 'widget' && t.type === 'note') widgetsRef.value?.cycleNoteColor(t.noteId)
}
</script>

<template>
  <div class="desktop" :style="{ background: desktop.wallpapers[desktop.wallpaper] }" @contextmenu="onContext" @dragover.prevent @drop="onDrop">
    <MenuBar @launchpad="launchpadOpen = true" @spotlight="spotOpen = true" />

    <Widgets ref="widgetsRef" />
    <Shortcuts ref="shortcutsRef" />

    <div class="windows-layer">
      <WindowFrame v-for="w in windows.list" :key="w.id" :win="w" />
    </div>

    <Dock @launchpad="launchpadOpen = true" />

    <Launchpad v-if="launchpadOpen" @close="launchpadOpen = false" />
    <Spotlight v-if="spotOpen" @close="spotOpen = false" />

    <!-- 窗口吸附预览（桌面层渲染，避免被窗口裁剪） -->
    <div v-if="snapPreview" class="snap-preview" :style="{
      left: snapPreview.x + 'px', top: snapPreview.y + 'px',
      width: snapPreview.w + 'px', height: snapPreview.h + 'px',
    }" />

    <!-- 右键菜单：显示权归全局菜单单例（点外部/Esc/滚动统一关闭），根节点标记 os-menu-root。
         注意空白桌面的右键目标为 null，不能拿 ctx 做渲染条件 -->
    <div
      v-if="menu.isOpen" ref="ctxEl" class="os-menu os-menu-root ctxmenu"
      :style="{ left: ctxPos.x + 'px', top: ctxPos.y + 'px' }"
      @contextmenu.prevent.stop
    >
      <!-- 右键快捷方式 -->
      <template v-if="ctx?.kind === 'shortcut'">
        <button @click="ctxOpenShortcut(); menu.close()">打开</button>
        <button @click="ctxRemoveShortcut(); menu.close()">移除快捷方式</button>
      </template>

      <!-- 右键小组件 -->
      <template v-else-if="ctx?.kind === 'widget'">
        <button v-if="ctx.type === 'note'" @click="ctxCycleNoteColor(); menu.close()">🎨 换个颜色</button>
        <button @click="ctxRemoveWidget(); menu.close()">移除小组件</button>
      </template>

      <!-- 右键桌面 -->
      <template v-else>
        <button @click="launchpadOpen = true; menu.close()">启动台</button>
        <button v-if="apps.byId('terminal')" @click="windows.open('terminal'); menu.close()">打开终端</button>
        <button v-if="apps.byId('files')" @click="windows.open('files'); menu.close()">打开文件</button>
        <button v-if="apps.byId('goder')" @click="windows.open('goder'); menu.close()">Goder - AI助手</button>
        <button v-if="apps.byId('notes')" @click="windows.open('notes'); menu.close()">便签</button>
        <div class="sep" />
        <div class="ctx-group">添加小组件</div>
        <button v-for="d in widgetDefs()" :key="d.type" @click="widgetsRef?.add(d.type); menu.close()">
          {{ d.icon ? d.icon + ' ' : '' }}{{ d.label }}
        </button>
        <div class="sep" />
        <button @click="desktop.setWallpaper(desktop.wallpaper + 1); menu.close()">更换壁纸</button>
        <button @click="desktop.setDark(!desktop.dark); menu.close()">切换深浅色</button>
      </template>
    </div>
  </div>
</template>

<style scoped>
.desktop { position: fixed; inset: 0; overflow: hidden; }
/* 窗口层仅接收窗口本体的事件，空白区域穿透给小组件/快捷方式（可拖动/拖放） */
.windows-layer { position: absolute; inset: 0; pointer-events: none; }

.snap-preview {
  position: fixed; z-index: 20000; border-radius: 14px; pointer-events: none;
  background: rgba(10, 132, 255, .16); border: 1.5px solid rgba(10, 132, 255, .5);
  box-shadow: inset 0 0 0 1px rgba(255,255,255,.12), 0 8px 30px rgba(10,132,255,.18);
  backdrop-filter: blur(6px);
  transition: all .16s var(--os-ease-out);
}

/* 菜单卡片/按钮/分隔线统一用全局 .os-menu 样式（base.css），这里只保留桌面菜单特有的分组标题 */
.ctx-group { padding: 5px 10px 3px; font-size: 11px; font-weight: 600; color: var(--os-text-secondary); }
</style>
