<script setup lang="ts">
// 启动台：居中悬浮玻璃弹窗（非全屏层）——头部内嵌搜索 + 「···」更多菜单，
// 下沿一排类目胶囊标签（选中白底、未选半透明，横向滚动），正文 7 列图标网格
// 超出面板滚动（右侧细滚动条）。拖动应用到另一应用/文件夹图标上释放
// 自动合并成文件夹（macOS 行为）；点击文件夹展开面板（打开应用 / 改名 / 拖出或
// 右键移出）。文件夹持久化到服务端 user_settings 的 launchpadFolders 键
// （localStorage 仅缓存，与 Dock 同一模式）；应用卸载后成员自动失效隐藏，
// 文件夹空了不显示（下次保存时落盘清理）。搜索/类目视图忽略文件夹结构直接
// 命中应用本体（视图内建文件夹前先从既有文件夹摘除，防重复收纳）。
import { computed, ref, watch } from 'vue'
import { useApps } from '../stores/apps'
import type { AppDef } from '../api/client'
import { useWindows } from '../stores/windows'
import { useDock } from '../stores/dock'
import { useSession } from '../stores/session'
import { useUserSetting } from '../composables/useUserSetting'
import { toast } from '../ui/toast'
import { osConfirm } from '../ui/confirm'
import { useMenu, menuPos } from '../ui/menu'
import AppIcon from '../ui/AppIcon.vue'
import FolderIcon from '../ui/FolderIcon.vue'

interface LpFolder { id: string; name: string; appIds: string[] }
/** 网格单元：平铺/搜索/类目三视图统一为一种循环模型。 */
type Entry =
  | { kind: 'app'; key: string; a: AppDef }
  | { kind: 'folder'; key: string; f: LpFolder }

const emit = defineEmits<{ close: [] }>()
const apps = useApps()
const windows = useWindows()
const dockStore = useDock()
const session = useSession()
const q = ref('')
/** 选中的类目筛选（空 = 全部）。 */
const cat = ref('')

const SETTINGS_KEY = 'launchpadFolders'
const CACHE_KEY = () => `openos_launchpad_folders_${session.user!.username}`

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

/** 文件夹持久化：值由 useUserSetting 持有（缓存先行回显 → 服务端校正 → save 提交）。
 *  服务端无记录 = 从未自定义过文件夹：以服务端为准清回默认（与 Dock 同语义）。 */
const lpSetting = useUserSetting<LpFolder[]>(SETTINGS_KEY, {
  cacheKey: CACHE_KEY,
  parse: (raw) => {
    try { return sanitize(JSON.parse(raw)) } catch { return undefined } // 损坏：保留缓存
  },
  serialize: (v) => JSON.stringify(v),
  readCache: () => {
    try { return sanitize(JSON.parse(localStorage.getItem(CACHE_KEY()) || '[]')) } catch { return [] }
  },
  absent: () => [],
})
const folders = computed({
  get: (): LpFolder[] => lpSetting.value.value ?? [],
  set: (v: LpFolder[]) => { lpSetting.value.value = v },
})

/** 拖动中的源（同组件内判定即可，无需跨 dataTransfer）：id + 来源文件夹 id（空 = 平铺）。 */
let dragSrc = { id: '', from: '' }
/** 合并目标高亮（dragover 持续续期，离开后短延时自灭）。 */
const hoverKey = ref('')
let hoverClr = 0
/** 展开面板的文件夹 id。 */
const openId = ref('')

/** 保存：localStorage 即时写（缓存）+ 服务端 400ms 防抖回写（权威；内容未变不回写）。 */
function save() {
  lpSetting.save()
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

/** 现存类目（按应用顺序首见排列）。 */
const categories = computed(() => {
  const out: string[] = []
  for (const a of apps.visible) if (a.category && !out.includes(a.category)) out.push(a.category)
  return out
})
/** 有效选中类目：应用卸载等导致类目消失时回落「全部」。 */
const effCat = computed(() => (cat.value && categories.value.includes(cat.value) ? cat.value : ''))

/** 三视图统一网格模型：搜索 > 类目 > 平铺+文件夹。 */
const entries = computed<Entry[]>(() => {
  if (searching.value) return filtered.value.map((a) => ({ kind: 'app', key: a.id, a }))
  if (effCat.value) return apps.visible.filter((a) => a.category === effCat.value).map((a) => ({ kind: 'app', key: a.id, a }))
  const out: Entry[] = flatApps.value.map((a) => ({ kind: 'app', key: a.id, a }))
  for (const g of gridFolders.value) out.push({ kind: 'folder', key: g.f.id, f: g.f })
  return out
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

/** 从所有文件夹中摘除应用（空壳顺带清理）。 */
function sweepFromFolder(appId: string) {
  let changed = false
  for (const f of folders.value) {
    if (f.appIds.includes(appId)) { f.appIds = f.appIds.filter((x) => x !== appId); changed = true }
  }
  if (changed) folders.value = folders.value.filter((f) => f.appIds.length)
}

/** 平铺应用上释放：与拖动源自动建新文件夹（占目标位）。 */
function dropOnApp(e: DragEvent, appId: string) {
  e.preventDefault()
  e.stopPropagation()
  hoverKey.value = ''
  if (!dragSrc.id || dragSrc.id === appId) return
  // 搜索/类目视图展示的可能是文件夹内成员：建夹前先从既有文件夹摘除，防重复收纳
  sweepFromFolder(appId)
  sweepFromFolder(dragSrc.id)
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
  sweepFromFolder(dragSrc.id) // 搜索/类目视图拖出的源可能还挂在别的文件夹里
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

// ---- 「···」更多菜单（右对齐按钮下方弹出） ----
const more = useMenu('launchpad:more')
function toggleMore(e: MouseEvent) {
  const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
  more.toggle(r.right - 150, r.bottom + 8)
}
const moreStyle = computed(() => ({
  left: Math.max(6, Math.min(menuPos.value.x, window.innerWidth - 160)) + 'px',
  top: Math.max(6, Math.min(menuPos.value.y, window.innerHeight - 90)) + 'px',
}))
async function dissolveAll() {
  if (!folders.value.length) return
  const n = folders.value.length
  if (!(await osConfirm({
    title: '解散全部文件夹',
    body: `将解散 ${n} 个文件夹，成员应用回到启动台平铺。`,
    danger: true,
    confirmText: '解散',
  }))) return
  folders.value = []
  openId.value = ''
  save()
}
</script>

<template>
  <div class="launchpad" @pointerdown.self="emit('close')" @dragover.prevent @drop="onRootDrop">
    <div class="panel" role="dialog" aria-modal="true" aria-label="启动台">
      <header class="lp-head">
        <div class="search-wrap">
          <svg viewBox="0 0 16 16" class="mg"><circle cx="6.8" cy="6.8" r="4.6" fill="none" stroke="currentColor" stroke-width="1.5" /><line x1="10.4" y1="10.4" x2="14.2" y2="14.2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
          <input v-model="q" placeholder="搜索" autofocus @keydown.esc="emit('close')" />
        </div>
        <button class="more" data-os-menu-anchor aria-label="更多操作" title="更多" @click="toggleMore">
          <svg viewBox="0 0 16 16" width="15" height="15"><circle cx="3" cy="8" r="1.7" fill="currentColor" /><circle cx="8" cy="8" r="1.7" fill="currentColor" /><circle cx="13" cy="8" r="1.7" fill="currentColor" /></svg>
        </button>
      </header>
      <nav v-if="!searching" class="cats" aria-label="应用类目">
        <button class="cat" :class="{ on: !effCat }" @click="cat = ''">全部</button>
        <button v-for="c in categories" :key="c" class="cat" :class="{ on: effCat === c }" @click="cat = c">{{ c }}</button>
      </nav>
      <div v-if="!searching" class="sep"></div>
      <div class="grid">
        <button
          v-for="(it, i) in entries" :key="it.key"
          class="lp-item" :draggable="it.kind === 'app'"
          :class="{ 'drop-target': hoverKey === it.key }"
          :style="{ '--i': i }"
          :title="it.kind === 'app'
            ? '点击打开 · 拖到其他图标上合并成文件夹 / 拖到桌面创建快捷方式 / 拖到 Dock 固定'
            : `${it.f.name}（点击展开 · 拖应用到上面收入）`"
          @click="it.kind === 'app' ? open(it.a.id) : (openId = it.f.id)"
          @dragstart="it.kind === 'app' && onDragStart($event, it.a.id)"
          @dragover="it.kind === 'app' ? overItem($event, it.a.id) : overItem($event, it.f.id, it.f.id)"
          @drop="it.kind === 'app' ? dropOnApp($event, it.a.id) : dropOnFolder($event, it.f.id)"
          @contextmenu.prevent.stop="it.kind === 'app' && pinToDock($event, it.a.id)"
        >
          <AppIcon v-if="it.kind === 'app'" :app="it.a.id" :icon="it.a.icon" :size="72" />
          <FolderIcon v-else :entries="it.f.appIds.map((id) => ({ kind: 'app' as const, id }))" :size="72" />
          <span class="name">{{ it.kind === 'app' ? it.a.name : it.f.name }}</span>
        </button>
        <div v-if="!entries.length" class="empty">{{ searching ? '没有匹配的应用' : '没有应用' }}</div>
      </div>
      <p class="lp-foot">点击打开 · 拖到图标上合并成文件夹 · 拖到桌面创建快捷方式 / 拖到 Dock 固定 · 右键固定到 Dock · Esc 关闭</p>
    </div>

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

    <!-- 「···」更多菜单（全局菜单单例渲染） -->
    <div v-if="more.isOpen" class="os-menu os-menu-root" :style="moreStyle" @contextmenu.prevent.stop>
      <button :disabled="!folders.length" @click="dissolveAll(); more.close()">解散全部文件夹</button>
    </div>
  </div>
</template>

<style scoped>
/* 外层：轻压暗背景（桌面壁纸清晰可见），面板整体略偏上 */
.launchpad {
  position: fixed; inset: 0; z-index: 9600;
  background: rgba(0, 0, 0, 0.24);
  display: flex; align-items: center; justify-content: center;
  padding-bottom: 5vh;
  animation: os-fade-in .16s ease;
}

/* 居中悬浮玻璃面板 */
.panel {
  width: min(980px, 72vw); height: min(660px, 72vh); min-height: 420px;
  display: flex; flex-direction: column; overflow: hidden;
  border-radius: 26px;
  border: 1px solid rgba(255, 255, 255, 0.14);
  background: rgba(40, 41, 46, 0.62);
  backdrop-filter: blur(40px) saturate(1.45);
  -webkit-backdrop-filter: blur(40px) saturate(1.45);
  box-shadow: 0 30px 90px rgba(0, 0, 0, 0.5), inset 0 1px 0 rgba(255, 255, 255, 0.08);
  animation: lp-panel-in .22s var(--os-ease-spring);
}
@keyframes lp-panel-in {
  from { opacity: 0; transform: scale(0.96) translateY(12px); }
  to { opacity: 1; transform: scale(1) translateY(0); }
}

/* 头部：内嵌搜索框 + 「···」 */
.lp-head { flex: none; display: flex; align-items: center; gap: 10px; padding: 16px 18px 10px; }
.search-wrap { flex: 1; max-width: 300px; position: relative; animation: lp-item-in .3s var(--os-ease-out) both; }
.mg { position: absolute; left: 11px; top: 50%; transform: translateY(-50%); width: 13px; height: 13px; color: rgba(255, 255, 255, 0.55); pointer-events: none; }
.lp-head input {
  width: 100%; padding: 7px 12px 7px 32px; border-radius: 10px;
  border: none; background: rgba(255, 255, 255, 0.1); color: #fff; font-size: 13px;
  transition: background .15s, box-shadow .15s;
}
.lp-head input:focus { background: rgba(255, 255, 255, 0.16); outline: none; box-shadow: 0 0 0 2px rgba(255, 255, 255, 0.18); }
.lp-head input::placeholder { color: rgba(255, 255, 255, 0.5); }
.more {
  flex: none; width: 30px; height: 30px; display: flex; align-items: center; justify-content: center;
  border-radius: 50%; border: none; cursor: pointer; color: rgba(255, 255, 255, 0.75);
  background: rgba(255, 255, 255, 0.08); transition: background .15s;
}
.more:hover { background: rgba(255, 255, 255, 0.16); }

/* 类目胶囊标签：单行横向滚动（滚动条隐藏），选中白底反色 */
.cats { flex: none; display: flex; gap: 8px; padding: 4px 18px 12px; overflow-x: auto; scrollbar-width: none; }
.cats::-webkit-scrollbar { display: none; }
.cat {
  flex: none; padding: 5px 14px; border-radius: 999px; border: none; cursor: pointer;
  background: rgba(255, 255, 255, 0.08); color: rgba(255, 255, 255, 0.82);
  font-size: 12.5px; line-height: 1.4; white-space: nowrap;
  transition: background .15s, color .15s;
}
.cat:hover { background: rgba(255, 255, 255, 0.14); }
.cat.on { background: #fff; color: #1d1d1f; font-weight: 500; }
.sep { flex: none; height: 1px; margin: 0 14px; background: rgba(255, 255, 255, 0.12); }

/* 正文网格：7 列，超出面板滚动（细滚动条） */
.grid {
  flex: 1; min-height: 0; overflow-y: auto;
  display: grid; grid-template-columns: repeat(7, 1fr);
  gap: 20px 8px; align-content: start;
  padding: 16px 22px 10px;
  scrollbar-width: thin; scrollbar-color: rgba(255, 255, 255, 0.25) transparent;
}
.grid::-webkit-scrollbar { width: 5px; }
.grid::-webkit-scrollbar-thumb { background: rgba(255, 255, 255, 0.22); border-radius: 3px; }
.grid::-webkit-scrollbar-thumb:hover { background: rgba(255, 255, 255, 0.35); }
.grid::-webkit-scrollbar-track { background: transparent; }
@media (max-width: 1020px) {
  .panel { width: 92vw; }
  .grid { grid-template-columns: repeat(auto-fill, minmax(104px, 1fr)); }
}

.lp-item {
  position: relative;
  display: flex; flex-direction: column; align-items: center; gap: 8px;
  background: transparent; border: none; cursor: pointer; padding: 10px 4px; border-radius: 16px;
  transition: transform .16s var(--os-ease-spring), background .15s;
  /* 图标依次弹入（基于网格内序号的级联延迟，封顶防长列表拖沓） */
  animation: lp-item-in .34s var(--os-ease-out) both;
  animation-delay: min(calc(var(--i, 0) * 18ms), 300ms);
}
@keyframes lp-item-in {
  from { opacity: 0; transform: translateY(14px) scale(.92); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}
.lp-item:hover { transform: scale(1.08); background: rgba(255, 255, 255, 0.08); }
.lp-item:active { transform: scale(1.02); }
.name {
  max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  color: #fff; font-size: 12.5px; text-shadow: 0 1px 4px rgba(0, 0, 0, 0.45);
}
.empty { color: rgba(255, 255, 255, 0.7); grid-column: 1 / -1; text-align: center; padding: 40px; }
.lp-foot { flex: none; margin: 0; padding: 8px 18px 14px; text-align: center; font-size: 11px; color: rgba(255, 255, 255, 0.35); user-select: none; }

/* 合并目标高亮：半透明圆圈罩住目标图标（macOS 式） */
.lp-item.drop-target { transform: scale(1.1); }
.lp-item.drop-target::before {
  content: ''; position: absolute; left: 50%; top: 46px;
  width: 96px; height: 96px; transform: translate(-50%, -50%);
  border-radius: 50%; pointer-events: none; z-index: 1;
  background: rgba(255, 255, 255, 0.18); border: 1.5px solid rgba(255, 255, 255, 0.42);
  box-shadow: 0 0 0 7px rgba(255, 255, 255, 0.07);
}
.lp-item.drop-target > :deep(*) { position: relative; z-index: 2; }

/* 文件夹面板（覆盖全屏遮罩上居中的二级玻璃弹窗） */
.peek-mask {
  position: absolute; inset: 0; z-index: 2;
  display: flex; align-items: center; justify-content: center;
  background: rgba(0, 0, 0, 0.25); animation: os-fade-in .14s ease;
}
.peek {
  width: 460px; max-width: 88vw; max-height: 70vh; overflow: visible; padding: 18px 22px 14px;
  border-radius: 26px; border: 1px solid rgba(255, 255, 255, 0.14);
  background: rgba(38, 40, 46, 0.6);
  backdrop-filter: blur(30px) saturate(1.4);
  -webkit-backdrop-filter: blur(30px) saturate(1.4);
  box-shadow: 0 24px 70px rgba(0, 0, 0, 0.45);
  animation: os-pop-in .18s var(--os-ease-spring);
}
.peek-name {
  display: block; width: 240px; margin: 0 auto 14px; padding: 4px 10px;
  border-radius: 8px; border: 1px solid transparent;
  background: transparent; color: #fff; font-size: 15px; text-align: center;
  transition: background .15s, border-color .15s;
}
.peek-name:hover { background: rgba(255, 255, 255, 0.1); }
.peek-name:focus { background: rgba(255, 255, 255, 0.16); border-color: rgba(10, 132, 255, 0.6); outline: none; }
.peek-grid {
  display: grid; grid-template-columns: repeat(4, 1fr); gap: 16px 8px;
  min-height: 110px; align-content: start; max-height: 46vh; overflow-y: auto; padding: 2px;
  scrollbar-width: thin; scrollbar-color: rgba(255, 255, 255, 0.25) transparent;
}
.peek-grid::-webkit-scrollbar { width: 5px; }
.peek-grid::-webkit-scrollbar-thumb { background: rgba(255, 255, 255, 0.22); border-radius: 3px; }
.peek-empty { grid-column: 1 / -1; padding: 30px 0; text-align: center; color: rgba(255, 255, 255, 0.55); font-size: 13px; }
.peek-hint { margin: 14px 0 0; text-align: center; font-size: 11px; color: rgba(255, 255, 255, 0.45); }
</style>
