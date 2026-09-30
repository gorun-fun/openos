<script setup lang="ts">
// 聚焦搜索（Spotlight 拟合）：⌘/Alt+Space 或菜单栏放大镜唤起。
// 内容源 = 应用 / 文件（递归）/ 计算器即时运算 / 系统操作；文件类结果带右侧预览。
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useApps } from '../stores/apps'
import { useWindows } from '../stores/windows'
import { useDesktop } from '../stores/desktop'
import { fsApi, fetchRawBlob, type VFile } from '../api/client'
import { openPath, isImage, kindOf, fileEmoji, fmtSize, IMAGE_EXTS } from '../apps/openWith'
import AppIcon from '../ui/AppIcon.vue'

const emit = defineEmits<{ close: [] }>()
const apps = useApps()
const windows = useWindows()
const desktop = useDesktop()

const q = ref('')
const inputEl = ref<HTMLInputElement>()
nextTick(() => inputEl.value?.focus())

// ---- 结果模型：分组固定顺序渲染，每组内保持相关性顺序 ----
type Item =
  | { type: 'calc'; expr: string; value: string }
  | { type: 'app'; id: string; name: string }
  | { type: 'file'; file: VFile }
  | { type: 'action'; label: string; icon: string; run: () => void }

const items = ref<Item[]>([])
const active = ref(0)

const GROUPS: { key: string; label: string; match: (it: Item) => boolean }[] = [
  { key: 'calc', label: '计算器', match: (it) => it.type === 'calc' },
  { key: 'app', label: '应用程序', match: (it) => it.type === 'app' },
  { key: 'dir', label: '文件夹', match: (it) => it.type === 'file' && !!it.file.isDir },
  { key: 'doc', label: '文稿', match: (it) => it.type === 'file' && !it.file.isDir && ['md', 'markdown', 'txt', 'log', 'json', 'pdf'].includes(kindExt(it)) },
  { key: 'sheet', label: '表格', match: (it) => it.type === 'file' && ['csv', 'tsv'].includes(kindExt(it)) },
  { key: 'slide', label: '幻灯片', match: (it) => it.type === 'file' && ['html', 'htm'].includes(kindExt(it)) },
  { key: 'img', label: '图像', match: (it) => it.type === 'file' && !it.file.isDir && IMAGE_EXTS.includes(kindExt(it)) },
  { key: 'other', label: '其他文件', match: (it) => it.type === 'file' && !it.file.isDir },
  { key: 'act', label: '操作', match: (it) => it.type === 'action' },
]
function kindExt(it: Item): string {
  if (it.type !== 'file') return ''
  return (it.file.path.match(/\.([a-z0-9]+)$/i)?.[1] || '').toLowerCase()
}
const grouped = computed(() => {
  const used = new Set<number>()
  const out: { label: string; list: { it: Item; i: number }[] }[] = []
  for (const g of GROUPS) {
    const list: { it: Item; i: number }[] = []
    items.value.forEach((it, i) => {
      if (!used.has(i) && g.match(it)) { used.add(i); list.push({ it, i }) }
    })
    if (list.length) out.push({ label: g.label, list })
  }
  return out
})

// ---- 计算器：纯数字运算式的即时求值 ----
function tryCalc(s: string): string | null {
  const t = s.replace(/（/g, '(').replace(/）/g, ')').replace(/，/g, '').trim()
  if (!/^[\d+\-*/().\s%^]+$/.test(t) || !/\d/.test(t) || !/[+\-*/%^]/.test(t)) return null
  try {
    // 词法受限（仅数字与运算符），Function 求值安全
    const v = Function(`"use strict";return (${t.replace(/\^/g, '**')})`)()
    if (typeof v !== 'number' || !isFinite(v)) return null
    return String(Math.round(v * 1e10) / 1e10)
  } catch { return null }
}

// ---- 系统操作 ----
const ACTIONS: { label: string; keys: string[]; icon: string; run: () => void }[] = [
  { label: '打开系统设置…', keys: ['设置', '偏好', 'settings'], icon: '⚙️', run: () => windows.open('settings') },
  { label: '切换深浅色外观', keys: ['深色', '浅色', '外观', 'dark', 'light'], icon: '🌗', run: () => desktop.setDark(!desktop.dark) },
  { label: '更换壁纸', keys: ['壁纸', 'wallpaper'], icon: '🖼️', run: () => desktop.setWallpaper(desktop.wallpaper + 1) },
  { label: '锁定屏幕 / 退出登录', keys: ['锁屏', '锁定', '退出', 'logout'], icon: '🔒', run: () => { localStorage.removeItem('openos_token'); location.reload() } },
]

// ---- 搜索（防抖 160ms）：本地应用/操作 + 服务端递归文件搜索 ----
let timer = 0
let seq = 0
watch(q, (s) => {
  clearTimeout(timer)
  const kw = s.trim().toLowerCase()
  if (!kw) { items.value = []; active.value = 0; return }
  timer = window.setTimeout(async () => {
    const out: Item[] = []
    const cv = tryCalc(kw)
    if (cv !== null) out.push({ type: 'calc', expr: kw, value: cv })
    for (const a of apps.visible) {
      if (a.name.toLowerCase().includes(kw) || a.id.includes(kw)) out.push({ type: 'app', id: a.id, name: a.name })
      if (out.length > 40) break
    }
    for (const act of ACTIONS) {
      if (kw.split(/\s+/).some((w) => act.label.toLowerCase().includes(w) || act.keys.some((k) => k.includes(w) || w.includes(k)))) {
        out.push({ type: 'action', label: act.label, icon: act.icon, run: act.run })
      }
    }
    items.value = out
    active.value = 0
    const my = ++seq
    try {
      const files = await fsApi.search(kw, 20)
      if (my !== seq) return // 过期响应丢弃
      for (const f of files) out.push({ type: 'file', file: f })
      items.value = [...out]
    } catch { /* 搜索失败忽略，保留本地结果 */ }
  }, 160)
})

const activeItem = computed(() => items.value[active.value] || null)

// ---- 预览（选中文件时右侧展示；图片拉取 blob 缩略） ----
const thumb = ref('')
watch(() => {
  const it = activeItem.value
  return it?.type === 'file' && isImage(it.file) ? it.file.path : ''
}, async (p) => {
  if (thumb.value) URL.revokeObjectURL(thumb.value)
  thumb.value = ''
  if (!p) return
  try { thumb.value = URL.createObjectURL(await fetchRawBlob(p)) } catch { /* 忽略 */ }
}, { immediate: true })
onBeforeUnmount(() => thumb.value && URL.revokeObjectURL(thumb.value))

// ---- 键盘：↑↓ 选择 / ⏎ 打开 / Esc 关闭 ----
function move(d: number) {
  const n = items.value.length
  if (!n) return
  active.value = (active.value + d + n) % n
  nextTick(() => document.querySelector('.spot-row.on')?.scrollIntoView({ block: 'nearest' }))
}
function activate(it: Item = activeItem.value!) {
  if (!it) return
  if (it.type === 'app') windows.open(it.id)
  else if (it.type === 'file') {
    if (it.file.isDir) windows.open('files', { path: it.file.path })
    else openPath(it.file.path)
  } else if (it.type === 'action') it.run()
  else if (it.type === 'calc') windows.open('calculator')
  emit('close')
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'ArrowDown') { e.preventDefault(); move(1) }
  else if (e.key === 'ArrowUp') { e.preventDefault(); move(-1) }
  else if (e.key === 'Enter') { e.preventDefault(); activate() }
  else if (e.key === 'Escape') { e.preventDefault(); emit('close') }
}
</script>

<template>
  <div class="spot-mask" @pointerdown.self="emit('close')">
    <div class="spot" :class="{ 'with-preview': activeItem?.type === 'file' }">
      <div class="spot-main">
        <div class="spot-field">
          <svg viewBox="0 0 20 20" class="mg"><circle cx="8.5" cy="8.5" r="5.6" fill="none" stroke="currentColor" stroke-width="1.8" /><line x1="13" y1="13" x2="17.4" y2="17.4" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
          <input
            ref="inputEl" v-model="q" placeholder="聚焦搜索" spellcheck="false"
            @keydown="onKey"
          >
        </div>

        <div v-if="items.length" class="spot-results">
          <template v-for="g in grouped" :key="g.label">
            <div class="spot-grp">{{ g.label }}</div>
            <button
              v-for="{ it, i } in g.list" :key="i" class="spot-row"
              :class="{ on: i === active }"
              @pointerdown.prevent="active = i"
              @click="activate(it)"
              @mousemove="active = i"
            >
              <span class="row-ic">
                <AppIcon v-if="it.type === 'app'" :app="it.id" :size="24" />
                <template v-else-if="it.type === 'calc'"><span class="calc-ic">🟠</span></template>
                <template v-else-if="it.type === 'action'"><span class="act-ic">{{ it.icon }}</span></template>
                <img v-else-if="it.type === 'file' && isImage(it.file) && i === active && thumb" class="thumb" :src="thumb" alt="">
                <template v-else-if="it.type === 'file'"><span class="file-ic">{{ it.file.isDir ? '📁' : fileEmoji(it.file.path) }}</span></template>
              </span>
              <span class="row-name">
                <template v-if="it.type === 'app'">{{ it.name }}</template>
                <template v-else-if="it.type === 'calc'">{{ it.expr }} = <b>{{ it.value }}</b></template>
                <template v-else-if="it.type === 'action'">{{ it.label }}</template>
                <template v-else>{{ it.file.path.split('/').pop() }}</template>
              </span>
              <span v-if="it.type === 'file'" class="row-sub">{{ it.file.path.replace(/^\/home\/[^/]+\//, '~/') }}</span>
              <span v-else-if="it.type === 'app'" class="row-sub">应用程序</span>
              <span v-else-if="it.type === 'calc'" class="row-sub">按 ⏎ 打开计算器</span>
            </button>
          </template>
        </div>
      </div>

      <!-- 文件预览面板（macOS 聚焦右侧详情） -->
      <aside v-if="activeItem?.type === 'file'" class="spot-preview">
        <template v-if="activeItem.file.isDir">
          <span class="pv-big">📁</span>
        </template>
        <img v-else-if="isImage(activeItem.file) && thumb" class="pv-img" :src="thumb" alt="">
        <span v-else class="pv-big">{{ fileEmoji(activeItem.file.path) }}</span>
        <div class="pv-name">{{ activeItem.file.path.split('/').pop() }}</div>
        <dl class="pv-meta">
          <div><dt>种类</dt><dd>{{ kindOf(activeItem.file) }}</dd></div>
          <div v-if="!activeItem.file.isDir"><dt>大小</dt><dd>{{ fmtSize(activeItem.file.size) }}</dd></div>
          <div><dt>修改时间</dt><dd>{{ activeItem.file.updatedAt }}</dd></div>
          <div class="pv-path"><dt>位置</dt><dd>{{ activeItem.file.path.replace(/^\/home\/[^/]+\//, '~/') }}</dd></div>
        </dl>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.spot-mask { position: fixed; inset: 0; z-index: 9700; animation: os-fade-in .12s; }
.spot {
  margin: 16vh auto 0; width: min(620px, 92vw); overflow: hidden;
  background: var(--os-panel); backdrop-filter: blur(42px) saturate(1.8);
  -webkit-backdrop-filter: blur(42px) saturate(1.8);
  border: 1px solid var(--os-border-strong);
  border-radius: 16px; box-shadow: var(--os-shadow);
  display: flex; animation: spot-in .2s var(--os-ease-spring);
}
@keyframes spot-in {
  from { opacity: 0; transform: scale(.97) translateY(-8px); }
  to { opacity: 1; transform: none; }
}
.spot.with-preview { width: min(880px, 94vw); }
.spot-main { flex: 1; min-width: 0; display: flex; flex-direction: column; }

.spot-field {
  display: flex; align-items: center; gap: 12px; padding: 13px 18px;
}
.spot-field .mg { width: 24px; height: 24px; color: var(--os-text-secondary); flex: none; }
.spot-field input {
  flex: 1; border: none; background: transparent; outline: none;
  font-size: 23px; font-weight: 300; color: var(--os-text); font-family: inherit;
}
.spot-field input::placeholder { color: var(--os-text-secondary); opacity: .7; }

.spot-results {
  max-height: 46vh; overflow-y: auto; border-top: 1px solid var(--os-border);
  padding: 6px; overscroll-behavior: contain;
}
.spot-grp { padding: 8px 12px 3px; font-size: 11px; font-weight: 700; color: var(--os-text-secondary); }
.spot-row {
  display: flex; align-items: center; gap: 10px; width: 100%;
  border: none; background: none; border-radius: 8px; padding: 5px 12px;
  font-size: 13.5px; color: var(--os-text); cursor: pointer; font-family: inherit; text-align: left;
}
.spot-row.on { background: var(--os-accent); color: #fff; }
.spot-row .row-ic { flex: none; width: 24px; height: 24px; display: flex; align-items: center; justify-content: center; }
.spot-row .row-ic .file-ic, .spot-row .row-ic .act-ic { font-size: 19px; }
.spot-row .thumb { width: 24px; height: 24px; object-fit: cover; border-radius: 4px; }
.spot-row .row-name { flex: none; max-width: 62%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.spot-row .row-name b { font-weight: 600; }
.spot-row .row-sub {
  flex: 1; min-width: 0; font-size: 11px; color: var(--os-text-secondary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; text-align: right;
}
.spot-row.on .row-sub { color: rgba(255, 255, 255, .8); }

.spot-preview {
  flex: none; width: 250px; border-left: 1px solid var(--os-border);
  display: flex; flex-direction: column; align-items: center; text-align: center;
  padding: 22px 16px; gap: 10px; background: rgba(127, 127, 127, 0.04);
}
.pv-big { font-size: 68px; line-height: 1; }
.pv-img { max-width: 100%; max-height: 140px; object-fit: contain; border-radius: 8px; box-shadow: var(--os-shadow-sm); }
.pv-name { font-size: 13.5px; font-weight: 600; word-break: break-all; }
.pv-meta { font-size: 11.5px; color: var(--os-text-secondary); width: 100%; }
.pv-meta div { display: flex; gap: 8px; padding: 2px 0; }
.pv-meta dt { flex: none; width: 48px; text-align: right; }
.pv-meta dd { flex: 1; min-width: 0; text-align: left; word-break: break-all; }
.pv-meta .pv-path dd { font-size: 10.5px; }
</style>
