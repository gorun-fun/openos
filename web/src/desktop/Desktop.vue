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
import { widgetDefs, widgetDef } from './widgetRegistry'
import { appForPath } from '../apps/openWith'

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
    ctx.value = null
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
type CtxTarget =
  | { kind: 'shortcut'; key: string }
  | { kind: 'widget'; type: string; noteId?: string }
  | null
const ctx = ref<{ x: number; y: number; target: CtxTarget } | null>(null)
const ctxPos = ref({ x: 0, y: 0 })
const ctxEl = ref<HTMLElement>()

function onContext(e: MouseEvent) {
  const t = e.target as HTMLElement
  // 应用窗口 / 菜单栏 / Dock / 启动台内部：交给应用或浏览器默认菜单
  if (t.closest('.window, .menubar, .dock, .launchpad, .ctxmenu')) return
  // 输入框内右键保留系统默认（复制/粘贴等）；例外：桌面小组件（便签）内的
  // 输入框占满组件，右键是移除/换色的唯一入口，仍弹系统菜单
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
  ctx.value = { x: e.clientX, y: e.clientY, target }
}

// 渲染后测量菜单尺寸，把菜单夹在视口内（避免右下角弹出越界）
watch(ctx, (c) => {
  if (!c) return
  ctxPos.value = { x: c.x, y: c.y }
  void nextTick(() => {
    const el = ctxEl.value
    if (!el) return
    const r = el.getBoundingClientRect()
    ctxPos.value = {
      x: Math.max(6, Math.min(c.x, window.innerWidth - r.width - 6)),
      y: Math.max(32, Math.min(c.y, window.innerHeight - r.height - 6)),
    }
  })
})

function ctxOpenShortcut() {
  const key = ctx.value?.target
  if (key?.kind === 'shortcut') shortcutsRef.value?.openByKey(key.key)
}
function ctxRemoveShortcut() {
  const key = ctx.value?.target
  if (key?.kind === 'shortcut') shortcutsRef.value?.removeByKey(key.key)
}
function ctxRemoveWidget() {
  const t = ctx.value?.target
  if (t?.kind === 'widget') widgetsRef.value?.remove(t.type, t.noteId)
}
function ctxCycleNoteColor() {
  const t = ctx.value?.target
  if (t?.kind === 'widget' && t.type === 'note') widgetsRef.value?.cycleNoteColor(t.noteId)
}
</script>

<template>
  <div class="desktop" :style="{ background: desktop.wallpapers[desktop.wallpaper] }" @pointerdown="ctx = null" @contextmenu="onContext" @dragover.prevent @drop="onDrop">
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

    <!-- 右键菜单（pointerdown 阻止冒泡，否则会被桌面层先清空导致点击失效） -->
    <div
      v-if="ctx" ref="ctxEl" class="ctxmenu"
      :style="{ left: ctxPos.x + 'px', top: ctxPos.y + 'px' }"
      @pointerdown.stop @contextmenu.prevent.stop
    >
      <!-- 右键快捷方式 -->
      <template v-if="ctx.target?.kind === 'shortcut'">
        <button @click="ctxOpenShortcut(); ctx = null">打开</button>
        <button @click="ctxRemoveShortcut(); ctx = null">移除快捷方式</button>
      </template>

      <!-- 右键小组件 -->
      <template v-else-if="ctx.target?.kind === 'widget'">
        <button v-if="ctx.target.type === 'note'" @click="ctxCycleNoteColor(); ctx = null">🎨 换个颜色</button>
        <button @click="ctxRemoveWidget(); ctx = null">移除小组件</button>
      </template>

      <!-- 右键桌面 -->
      <template v-else>
        <button @click="launchpadOpen = true; ctx = null">启动台</button>
        <button v-if="apps.byId('terminal')" @click="windows.open('terminal'); ctx = null">打开终端</button>
        <button v-if="apps.byId('files')" @click="windows.open('files'); ctx = null">打开文件</button>
        <button v-if="apps.byId('goder')" @click="windows.open('goder'); ctx = null">Goder - AI助手</button>
        <button v-if="apps.byId('notes')" @click="windows.open('notes'); ctx = null">便签</button>
        <div class="sep" />
        <div class="ctx-group">添加小组件</div>
        <button v-for="d in widgetDefs()" :key="d.type" @click="widgetsRef?.add(d.type); ctx = null">
          {{ d.icon ? d.icon + ' ' : '' }}{{ d.label }}
        </button>
        <div class="sep" />
        <button @click="desktop.setWallpaper(desktop.wallpaper + 1); ctx = null">更换壁纸</button>
        <button @click="desktop.setDark(!desktop.dark); ctx = null">切换深浅色</button>
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

.ctxmenu {
  position: fixed; z-index: 9500; min-width: 190px; padding: 6px;
  background: var(--os-panel); backdrop-filter: blur(30px) saturate(1.8);
  -webkit-backdrop-filter: blur(30px) saturate(1.8);
  border: 1px solid var(--os-border);
  border-radius: 12px; box-shadow: var(--os-shadow-sm), inset 0 1px 0 var(--os-highlight-edge);
  animation: os-pop-in .14s var(--os-ease-out);
  transform-origin: top left;
}
.ctxmenu button {
  display: block; width: 100%; text-align: left; padding: 6px 12px; border: none;
  background: transparent; border-radius: 7px; color: var(--os-text); font-size: 13px; cursor: pointer;
  transition: background .1s ease;
}
.ctxmenu button:hover { background: var(--os-accent); color: #fff; }
.ctx-group { padding: 5px 12px 3px; font-size: 11px; font-weight: 600; color: var(--os-text-secondary); }
.sep { height: 1px; background: var(--os-border); margin: 5px 10px; }
</style>
