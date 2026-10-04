<script setup lang="ts">
// 启动台：全部可见应用 + 用户文件夹分组。拖动应用到另一应用/文件夹图标上释放
// 自动合并成文件夹（macOS 行为）；点击文件夹展开面板（打开应用 / 改名 / 拖出或
// 右键移出）。文件夹持久化到服务端 user_settings 的 launchpadFolders 键
// （localStorage 仅缓存，与 Dock 同一模式）；应用卸载后成员自动失效隐藏，
// 文件夹空了不显示（下次保存时落盘清理）。搜索时忽略文件夹结构直接命中应用。
import { computed, onMounted, ref, watch } from 'vue'
import { useApps } from '../stores/apps'
import { useWindows } from '../stores/windows'
import { useDock } from '../stores/dock'
import { useSession } from '../stores/session'
import { api } from '../api/client'
import { toast } from '../ui/toast'
import { useMenu, menuPos } from '../ui/menu'
import AppIcon from '../ui/AppIcon.vue'
import FolderIcon from '../ui/FolderIcon.vue'

interface LpFolder { id: string; name: string; appIds: string[] }

const emit = defineEmits<{ close: [] }>()
const apps = useApps()
const windows = useWindows()
const dockStore = useDock()
const session = useSession()
const q = ref('')

const SETTINGS_KEY = 'launchpadFolders'
const CACHE_KEY = () => `openos_launchpad_folders_${session.user!.username}`

const folders = ref<LpFolder[]>([])
/** 拖动中的源（同组件内判定即可，无需跨 dataTransfer）：id + 来源文件夹 id（空 = 平铺）。 */
let dragSrc = { id: '', from: '' }
/** 合并目标高亮（dragover 持续续期，离开后短延时自灭）。 */
const hoverKey = ref('')
let hoverClr = 0
/** 展开面板的文件夹 id。 */
const openId = ref('')

/** 结构清洗：只信合法字段，appIds 去重。 */
function sanitize(raw: any): LpFolder[] {
  if (!Array.isArray(raw)) return []
  return raw
    .filter((f) => f && typeof f.id === 'string' && Array.isArray(f.appIds))
    .map((f): LpFolder => ({
      id: f.id,
      name: typeof f.name === 'string' && f.name ? f.name : '文件夹',
      appIds: [...new Set<string>(f.appIds.filter((x: any): x is string => typeof x === 'string'))],
    }))
}

onMounted(async () => {
  // 本地缓存先行，避免已建文件夹的用户先看到平铺闪烁
  try { folders.value = sanitize(JSON.parse(localStorage.getItem(CACHE_KEY()) || '[]')) } catch { /* ignore */ }
  try {
    const s = await api.get<Record<string, string>>('/api/settings')
    folders.value = sanitize(s[SETTINGS_KEY] ? JSON.parse(s[SETTINGS_KEY]) : [])
  } catch { /* 离线容忍：保留本地缓存 */ }
})

function save() {
  try { localStorage.setItem(CACHE_KEY(), JSON.stringify(folders.value)) } catch { /* 存储满静默 */ }
  api.put('/api/settings', { key: SETTINGS_KEY, value: JSON.stringify(folders.value) }).catch(() => {})
}

const inFolder = computed(() => new Set(folders.value.flatMap((f) => f.appIds)))
/** 未被收纳的平铺应用。 */
const flatApps = computed(() => apps.visible.filter((a) => !inFolder.value.has(a.id)))
/** 文件夹的有效成员（按 apps store 过滤失效项）与空文件夹剔除后的展示列表。 */
const gridFolders = computed(() =>
  folders.value
    .map((f) => ({ f, apps: f.appIds.map((id) => apps.byId(id)).filter(Boolean) }))
    .filter((x) => x.apps.length),
)

const searching = computed(() => !!q.value.trim())
const filtered = computed(() => {
  const s = q.value.trim().toLowerCase()
  const list = apps.visible // 搜索时忽略文件夹结构，直接命中应用本体（macOS 行为）
  if (!s) return list
  return list.filter(
    (a) => a.name.toLowerCase().includes(s) || a.id.toLowerCase().includes(s) || a.category.toLowerCase().includes(s),
  )
})

function open(id: string) {
  windows.open(id)
  emit('close')
}

// ---- 拖拽合并 ----
function onDragStart(e: DragEvent, appId: string, from = '') {
  dragSrc = { id: appId, from }
  // 拖出启动台 → 桌面创建快捷方式 / 拖到 Dock 固定（文件夹内应用同样支持）
  e.dataTransfer?.setData('application/x-openos-app', appId)
  e.dataTransfer!.effectAllowed = 'copy'
}

function overItem(e: DragEvent, key: string, targetFolder?: string) {
  if (!dragSrc.id) return
  if (targetFolder ? dragSrc.from === targetFolder : dragSrc.id === key) return // 已在目标内 / 拖到自己
  e.preventDefault()
  hoverKey.value = key
  clearTimeout(hoverClr)
  hoverClr = window.setTimeout(() => { hoverKey.value = '' }, 160)
}

/** 平铺应用上释放：与拖动源自动建新文件夹（占目标位）。 */
function dropOnApp(e: DragEvent, appId: string) {
  e.preventDefault()
  e.stopPropagation()
  hoverKey.value = ''
  if (!dragSrc.id || dragSrc.id === appId) return
  const f: LpFolder = {
    id: 'f' + Math.random().toString(36).slice(2, 10),
    name: folderNameOf(dragSrc.id, appId),
    appIds: [appId, takeOutSrc()],
  }
  folders.value.push(f)
  save()
}

/** 文件夹图标上释放：拖动源收入该文件夹。 */
function dropOnFolder(e: DragEvent, id: string) {
  e.preventDefault()
  e.stopPropagation()
  hoverKey.value = ''
  if (!dragSrc.id || dragSrc.from === id) return
  const f = folders.value.find((x) => x.id === id)
  if (!f) return
  if (f.appIds.includes(dragSrc.id)) { toast('该应用已在文件夹中'); return }
  f.appIds.push(takeOutSrc())
  save()
}

/** 摘除拖动源（从原文件夹移出，空壳顺带清理）；平铺来源无需处理。返回源 id。 */
function takeOutSrc(): string {
  if (dragSrc.from) {
    const src = folders.value.find((x) => x.id === dragSrc.from)
    if (src) {
      src.appIds = src.appIds.filter((x) => x !== dragSrc.id)
      if (!src.appIds.length) folders.value = folders.value.filter((x) => x !== src)
    }
  }
  return dragSrc.id
}

/** 新文件夹命名：两个应用同类目时取类目名，否则「文件夹」。 */
function folderNameOf(a: string, b: string): string {
  const ca = apps.byId(a)?.category
  const cb = apps.byId(b)?.category
  if (ca && ca === cb) return ca
  return '文件夹'
}

/** 启动台根层 drop：面板内拖出的应用落到面板外 = 移出文件夹（回到平铺）。 */
function onRootDrop(e: DragEvent) {
  if (!dragSrc.from) return
  if ((e.target as HTMLElement).closest?.('.peek')) return
  const src = folders.value.find((x) => x.id === dragSrc.from)
  if (src) {
    src.appIds = src.appIds.filter((x) => x !== dragSrc.id)
    if (!src.appIds.length) folders.value = folders.value.filter((x) => x !== src)
    save()
  }
}

// ---- 文件夹面板 ----
const openFolder = computed(() => {
  const src = folders.value.find((x) => x.id === openId.value)
  if (!src) return null
  const items = src.appIds.map((id) => apps.byId(id)).filter(Boolean) as { id: string; name: string }[]
  return { src, items }
})

// 面板成员右键菜单：移出文件夹（全局菜单单例，owner 区分于右键固定 Dock）
const menu = useMenu('launchpad')
const menuCtx = ref<{ fid: string; appId: string } | null>(null)
watch(() => menu.isOpen, (open) => { if (!open) menuCtx.value = null })
const menuStyle = computed(() => ({
  left: Math.max(6, Math.min(menuPos.value.x, window.innerWidth - 150)) + 'px',
  top: Math.max(6, Math.min(menuPos.value.y, window.innerHeight - 70)) + 'px',
}))
function itemCtx(e: MouseEvent, fid: string, appId: string) {
  e.preventDefault()
  e.stopPropagation()
  menuCtx.value = { fid, appId }
  menu.open(e.clientX, e.clientY)
}
function ctxRemove() {
  const c = menuCtx.value
  if (!c) return
  const f = folders.value.find((x) => x.id === c.fid)
  if (f) {
    f.appIds = f.appIds.filter((x) => x !== c.appId)
    if (!f.appIds.length) folders.value = folders.value.filter((x) => x !== f)
    save()
  }
}

/** 右键固定到 Dock 末尾（已在 Dock 中则提示）。 */
function pinToDock(e: MouseEvent, id: string) {
  e.preventDefault()
  e.stopPropagation()
  const app = apps.byId(id)
  if (!app) return
  if (dockStore.effectiveSet.has(id)) { toast(`「${app.name}」已在 Dock 中`); return }
  dockStore.pinAt(id, dockStore.effective.length)
  toast(`已固定「${app.name}」到 Dock`)
}
</script>

<template>
  <div class="launchpad" @pointerdown.self="emit('close')" @dragover.prevent @drop="onRootDrop">
    <div class="search-wrap">
      <svg viewBox="0 0 16 16" class="mg"><circle cx="6.8" cy="6.8" r="4.6" fill="none" stroke="currentColor" stroke-width="1.5" /><line x1="10.4" y1="10.4" x2="14.2" y2="14.2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
      <input v-model="q" placeholder="搜索" autofocus @keydown.esc="emit('close')" />
    </div>
    <div class="grid">
      <!-- 常规视图：平铺应用 + 文件夹 -->
      <template v-if="!searching">
        <button
          v-for="(a, i) in flatApps" :key="a.id"
          class="lp-item" draggable="true"
          :class="{ 'drop-target': hoverKey === a.id }"
          :style="{ '--i': i }"
          :title="`点击打开 · 拖到其他图标上合并成文件夹 / 拖到桌面创建快捷方式 / 拖到 Dock 固定`"
          @click="open(a.id)"
          @dragstart="onDragStart($event, a.id)"
          @dragover="overItem($event, a.id)"
          @drop="dropOnApp($event, a.id)"
          @contextmenu.prevent.stop="pinToDock($event, a.id)"
        >
          <AppIcon :app="a.id" :icon="a.icon" :size="68" />
          <span class="name">{{ a.name }}</span>
        </button>
        <button
          v-for="(g, j) in gridFolders" :key="g.f.id"
          class="lp-item" :class="{ 'drop-target': hoverKey === g.f.id }"
          :style="{ '--i': flatApps.length + j }"
          :title="`${g.f.name}（点击展开 · 拖应用到上面收入）`"
          @click="openId = g.f.id"
          @dragover="overItem($event, g.f.id, g.f.id)"
          @drop="dropOnFolder($event, g.f.id)"
        >
          <FolderIcon :entries="g.f.appIds.map((id) => ({ kind: 'app' as const, id }))" :size="68" />
          <span class="name">{{ g.f.name }}</span>
        </button>
        <div v-if="!flatApps.length && !gridFolders.length" class="empty">没有应用</div>
      </template>
      <!-- 搜索视图：忽略文件夹结构 -->
      <template v-else>
        <button
          v-for="(a, i) in filtered" :key="a.id"
          class="lp-item" draggable="true"
          :style="{ '--i': i }"
          :title="`点击打开 · 拖到桌面创建快捷方式 / 拖到 Dock 固定`"
          @click="open(a.id)"
          @dragstart="onDragStart($event, a.id)"
          @contextmenu.prevent.stop="pinToDock($event, a.id)"
        >
          <AppIcon :app="a.id" :icon="a.icon" :size="68" />
          <span class="name">{{ a.name }}</span>
        </button>
        <div v-if="!filtered.length" class="empty">没有匹配的应用</div>
      </template>
    </div>
    <p class="hint">点击打开 · 拖到图标上合并成文件夹 · 拖到桌面创建快捷方式 / 拖到 Dock 固定 · 右键固定到 Dock · Esc 关闭</p>

    <!-- 文件夹面板：遮罩点击关闭；成员点击打开、拖出面板移出 -->
    <div v-if="openFolder" class="peek-mask" @pointerdown.self="openId = ''" @contextmenu.prevent.stop>
      <div class="peek" role="dialog" aria-modal="true" :aria-label="openFolder.src.name">
        <input
          v-model="openFolder.src.name" class="peek-name"
          aria-label="文件夹名称" spellcheck="false"
          @keydown.enter="($event.target as HTMLInputElement).blur()" @change="save()"
        >
        <div class="peek-grid">
          <button
            v-for="a in openFolder.items" :key="a.id" class="lp-item" draggable="true"
            :title="`${a.name}（点击打开 · 拖出到面板外移出文件夹）`"
            @click="open(a.id)"
            @dragstart="onDragStart($event, a.id, openFolder.src.id)"
            @contextmenu.prevent.stop="itemCtx($event, openFolder.src.id, a.id)"
          >
            <AppIcon :app="a.id" :size="68" />
            <span class="name">{{ a.name }}</span>
          </button>
          <div v-if="!openFolder.items.length" class="peek-empty">文件夹是空的，拖应用进来吧</div>
        </div>
        <p class="peek-hint">点击打开 · 拖出面板移出文件夹 · 右键移出</p>
      </div>
    </div>

    <!-- 面板成员右键菜单（全局菜单单例渲染） -->
    <div
      v-if="menu.isOpen && menuCtx" class="os-menu os-menu-root"
      :style="menuStyle" @contextmenu.prevent.stop
    >
      <button @click="ctxRemove(); menu.close()">移出文件夹</button>
    </div>
  </div>
</template>

<style scoped>
.launchpad {
  position: fixed; inset: 0; z-index: 9600;
  background: rgba(0, 0, 0, 0.3);
  backdrop-filter: blur(46px) saturate(1.5) brightness(1.08);
  -webkit-backdrop-filter: blur(46px) saturate(1.5) brightness(1.08);
  display: flex; flex-direction: column; align-items: center;
  padding-top: 8vh; animation: os-fade-in .18s ease;
}
.search-wrap { margin-bottom: 4vh; animation: lp-item-in .3s var(--os-ease-out) both; position: relative; }
.mg { position: absolute; left: 12px; top: 50%; transform: translateY(-50%); width: 14px; height: 14px; color: rgba(255,255,255,.6); pointer-events: none; }
input {
  width: 280px; padding: 8px 16px 8px 34px; border-radius: 999px;
  border: 1px solid rgba(255,255,255,.28);
  background: rgba(255,255,255,.2); color: #fff; font-size: 14px;
  backdrop-filter: blur(10px); transition: background .15s, border-color .15s;
}
input:focus { background: rgba(255,255,255,.3); border-color: rgba(255,255,255,.5); }
input::placeholder { color: rgba(255,255,255,.6); }
.grid {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
  gap: 22px 12px; width: min(880px, 88vw); overflow-y: auto; padding-bottom: 20px;
}
.lp-item {
  position: relative;
  display: flex; flex-direction: column; align-items: center; gap: 9px;
  background: transparent; border: none; cursor: pointer; padding: 10px 4px; border-radius: 16px;
  transition: transform .16s var(--os-ease-spring), background .15s;
  /* 图标依次弹入（基于网格内序号的级联延迟） */
  animation: lp-item-in .34s var(--os-ease-out) both;
  animation-delay: calc(var(--i, 0) * 22ms);
}
@keyframes lp-item-in {
  from { opacity: 0; transform: translateY(14px) scale(.92); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}
.lp-item:hover { transform: scale(1.08); background: rgba(255,255,255,.08); }
.lp-item:active { transform: scale(1.02); }
.name { color: #fff; font-size: 12.5px; text-shadow: 0 1px 4px rgba(0,0,0,.45); }
.empty { color: rgba(255,255,255,.7); grid-column: 1 / -1; text-align: center; padding: 40px; }
.hint { position: absolute; bottom: 100px; color: rgba(255,255,255,.55); font-size: 12px; }

/* 合并目标高亮：半透明圆圈罩住目标图标（macOS 式） */
.lp-item.drop-target { transform: scale(1.1); }
.lp-item.drop-target::before {
  content: ''; position: absolute; left: 50%; top: 44px;
  width: 92px; height: 92px; transform: translate(-50%, -50%);
  border-radius: 50%; pointer-events: none; z-index: 1;
  background: rgba(255,255,255,.18); border: 1.5px solid rgba(255,255,255,.42);
  box-shadow: 0 0 0 7px rgba(255,255,255,.07);
}
.lp-item.drop-target > :deep(*) { position: relative; z-index: 2; }

/* 文件夹面板 */
.peek-mask {
  position: absolute; inset: 0; z-index: 2;
  display: flex; align-items: center; justify-content: center;
  background: rgba(0, 0, 0, .25); animation: os-fade-in .14s ease;
}
.peek {
  width: 460px; max-width: 88vw; max-height: 70vh; overflow: visible; padding: 18px 22px 14px;
  border-radius: 26px; border: 1px solid rgba(255,255,255,.14);
  background: rgba(38, 40, 46, .6);
  backdrop-filter: blur(30px) saturate(1.4);
  -webkit-backdrop-filter: blur(30px) saturate(1.4);
  box-shadow: 0 24px 70px rgba(0,0,0,.45);
  animation: os-pop-in .18s var(--os-ease-spring);
}
.peek-name {
  display: block; width: 240px; margin: 0 auto 14px; padding: 4px 10px;
  border-radius: 8px; border: 1px solid transparent;
  background: transparent; color: #fff; font-size: 15px; text-align: center;
  transition: background .15s, border-color .15s;
}
.peek-name:hover { background: rgba(255,255,255,.1); }
.peek-name:focus { background: rgba(255,255,255,.16); border-color: rgba(10,132,255,.6); outline: none; }
.peek-grid {
  display: grid; grid-template-columns: repeat(4, 1fr); gap: 16px 8px;
  min-height: 110px; align-content: start; max-height: 46vh; overflow-y: auto; padding: 2px;
}
.peek-empty { grid-column: 1 / -1; padding: 30px 0; text-align: center; color: rgba(255,255,255,.55); font-size: 13px; }
.peek-hint { margin: 14px 0 0; text-align: center; font-size: 11px; color: rgba(255,255,255,.45); }
</style>
