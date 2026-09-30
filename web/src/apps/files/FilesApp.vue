<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { fsApi, downloadFile, fetchRawBlob, type VFile } from '../../api/client'
import { bus } from '../../bus/bus'
import { OsInput, OsSelect, OsEmpty, OsModal } from '../../ui'

const props = defineProps<{ win: WinState }>()
const windows = useWindows()
const session = useSession()
const home = `/home/${session.user!.username}`

const path = ref(props.win.props?.path || home)
const entries = ref<VFile[]>([])
const loading = ref(true)
const selected = ref('')
const renaming = ref('')
const renameTo = ref('')
const creating = ref<'dir' | 'file' | null>(null)
const newName = ref('')
const query = ref('')
const sortBy = ref('name')
const sortDir = ref<1 | -1>(1)
// 四种视图：图标 / 列表 / 分栏 / 画廊（仿 macOS Finder）
const view = ref<'icon' | 'list' | 'column' | 'gallery'>('icon')
const usage = ref({ used: 0, quota: 100 * 1024 * 1024 })
const preview = ref<{ url: string; name: string } | null>(null)
const uploadInput = ref<HTMLInputElement>()
const menuOpen = ref(false) // 工具栏「更多操作」下拉菜单

// 前进/后退历史
const history = ref<string[]>([path.value])
const hIdx = ref(0)
const canBack = computed(() => hIdx.value > 0)
const canForward = computed(() => hIdx.value < history.value.length - 1)

// 侧边栏：主页下一级目录（收藏区按语义名识别，其余归入"位置"）
const homeDirs = ref<VFile[]>([])
const FAV_PATTERNS: [RegExp, string][] = [
  [/^(desktop|桌面)$/i, '🖥'], [/^(docs?|documents|文稿|文档)$/i, '📄'],
  [/^(downloads?|下载)$/i, '⬇️'], [/^(pictures?|images?|photos?|图片|照片|相册)$/i, '🖼'],
  [/^(music|音乐)$/i, '🎵'], [/^(movies?|videos?|影片|视频)$/i, '🎬'],
]
const favDirs = computed(() => {
  const out: VFile[] = []
  const used = new Set<string>()
  for (const [re] of FAV_PATTERNS) {
    const hit = homeDirs.value.find((d) => !used.has(d.path) && re.test(baseName(d.path)))
    if (hit) { out.push(hit); used.add(hit.path) }
  }
  return out
})
const placeDirs = computed(() => homeDirs.value.filter((d) => !favDirs.value.includes(d)))
function sideIcon(name: string): string {
  for (const [re, ic] of FAV_PATTERNS) if (re.test(name)) return ic
  return '📁'
}

// 分栏视图：各列目录内容缓存（key 为目录绝对路径）
const colCache = reactive(new Map<string, VFile[]>())
const colInflight = new Set<string>()
async function ensureCol(p: string) {
  if (colCache.has(p) || colInflight.has(p)) return
  colInflight.add(p)
  try { colCache.set(p, await fsApi.list(p)) } catch { colCache.set(p, []) } finally { colInflight.delete(p) }
}
// 当前路径在 home 下的逐级链（分栏视图从左到右的各列）
const columnPaths = computed(() => {
  const chain: string[] = []
  let p: string | undefined = path.value
  while (p && p.startsWith(home)) {
    chain.unshift(p)
    if (p === home) break
    p = p.split('/').slice(0, -1).join('/')
  }
  return chain
})
const colsEl = ref<HTMLElement>()

// 图片 blob 缓存（画廊大图 / 缩略胶片 / 分栏预览列共用）
const blobCache = reactive(new Map<string, string>())
async function ensureBlob(p: string) {
  if (blobCache.has(p)) return
  blobCache.set(p, '') // 占位防止重复请求；失败也保持空串
  try { blobCache.set(p, URL.createObjectURL(await fetchRawBlob(p))) } catch { /* 忽略 */ }
}
const blobUrl = (p: string) => blobCache.get(p) || ''

async function load() {
  loading.value = true
  try {
    const [list, u] = await Promise.all([fsApi.list(path.value), fsApi.usage()])
    entries.value = list
    colCache.set(path.value, list)
    usage.value = u
  } finally {
    loading.value = false
  }
}
async function loadSidebar() {
  try { homeDirs.value = (await fsApi.list(home)).filter((f) => f.isDir) } catch { homeDirs.value = [] }
}
onMounted(() => { load(); loadSidebar() })

// 其他窗口（文档/表格/终端）保存后自动刷新
const off = bus.on('fs:changed', () => {
  load(); loadSidebar()
  colCache.clear()
  columnPaths.value.forEach(ensureCol)
})
onBeforeUnmount(() => {
  off()
  blobCache.forEach((u) => u && URL.revokeObjectURL(u))
})

const baseName = (p: string) => p.split('/').pop() || ''

// 融合窗体（chrome:'merged'）的标题行：显示当前文件夹名（Finder 行为），并同步窗口标题供 Dock/切换展示
const curTitle = computed(() => (path.value === home ? '主页' : baseName(path.value)))
watch(curTitle, (t) => windows.setTitle(props.win.id, t), { immediate: true })

// 排序比较器：名称/大小/时间，方向由 sortDir 决定；列表视图按 macOS 混排，其余视图目录置顶
const baseCmp = computed(() => {
  const cmp: Record<string, (a: VFile, b: VFile) => number> = {
    name: (a, b) => baseName(a.path).localeCompare(baseName(b.path), 'zh'),
    size: (a, b) => a.size - b.size,
    time: (a, b) => a.updatedAt.localeCompare(b.updatedAt),
  }
  const fn = cmp[sortBy.value] || cmp.name
  return (a: VFile, b: VFile) => fn(a, b) * sortDir.value
})
function sortedOf(list: VFile[]): VFile[] {
  const q = query.value.trim().toLowerCase()
  const filtered = q ? list.filter((f) => f.path.toLowerCase().includes(q)) : list
  const arr = [...filtered].sort(baseCmp.value)
  return view.value === 'list' ? arr : [...arr].sort((a, b) => Number(b.isDir) - Number(a.isDir))
}
const shown = computed(() => sortedOf(entries.value))
const colShown = (p: string) => sortedOf(colCache.get(p) || [])

// 选中项：优先当前目录，其次分栏缓存（中间列选中的文件也能预览）
const selectedFile = computed(() => {
  const cur = entries.value.find((f) => f.path === selected.value)
  if (cur) return cur
  for (const list of colCache.values()) {
    const f = list.find((x) => x.path === selected.value)
    if (f) return f
  }
  return null
})

function setSort(k: string) {
  if (sortBy.value === k) sortDir.value = sortDir.value === 1 ? -1 : 1
  else { sortBy.value = k; sortDir.value = 1 }
}

function enter(p: string, push = true) {
  if (p === path.value && !push) return
  path.value = p
  selected.value = ''
  query.value = ''
  if (push) {
    history.value = history.value.slice(0, hIdx.value + 1)
    history.value.push(p)
    hIdx.value = history.value.length - 1
  }
  load()
}
function enterAt(p: string) { // 历史跳转：不产生新记录
  path.value = p
  selected.value = ''
  query.value = ''
  load()
}
function goBack() { if (!canBack.value) return; hIdx.value--; enterAt(history.value[hIdx.value]) }
function goForward() { if (!canForward.value) return; hIdx.value++; enterAt(history.value[hIdx.value]) }

// 侧边栏高亮：目录项在其子路径内保持高亮；"主页"作为根仅精确匹配，避免子目录导航时恒亮
const isActive = (p: string) => (p === home ? path.value === p : path.value === p || path.value.startsWith(p + '/'))

function extOf(p: string) {
  return (p.match(/\.([a-z0-9]+)$/i)?.[1] || '').toLowerCase()
}
const APP_FOR_EXT: Record<string, string> = {
  txt: 'docs', md: 'docs', html: 'docs', json: 'docs',
  csv: 'sheets', tsv: 'sheets', pdf: 'pdf',
}
const IMAGE_EXT = ['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'ico', 'bmp']
const isImage = (f: VFile) => !f.isDir && IMAGE_EXT.includes(extOf(f.path))

async function openFile(f: VFile) {
  if (f.isDir) { enter(f.path); return }
  const ext = extOf(f.path)
  if (IMAGE_EXT.includes(ext)) {
    try {
      const blob = await fetchRawBlob(f.path)
      if (preview.value) URL.revokeObjectURL(preview.value.url)
      preview.value = { url: URL.createObjectURL(blob), name: baseName(f.path) }
    } catch { /* 打不开按文本走 */ }
    return
  }
  const app = APP_FOR_EXT[ext]
  if (app) windows.open(app, { path: f.path })
  else windows.open('docs', { path: f.path })
}

async function confirmCreate() {
  if (!creating.value || !newName.value.trim()) { creating.value = null; return }
  const p = path.value + '/' + newName.value.trim()
  try {
    if (creating.value === 'dir') await fsApi.mkdir(p)
    else await fsApi.touch(p, '')
  } catch (e: any) { alert(e.message) }
  creating.value = null; newName.value = ''
  load()
}

async function doRemove() {
  if (!selected.value) return
  if (!confirm(`删除 ${baseName(selected.value)} ？`)) return
  await fsApi.remove(selected.value)
  selected.value = ''
  load()
}

async function doRename() {
  if (!renaming.value || !renameTo.value.trim()) { renaming.value = ''; return }
  const to = path.value + '/' + renameTo.value.trim()
  try { await fsApi.rename(renaming.value, to) } catch (e: any) { alert(e.message) }
  renaming.value = ''
  load()
}

async function onUpload(e: Event) {
  const files = (e.target as HTMLInputElement).files
  if (!files?.length) return
  try {
    await fsApi.upload(path.value, [...files])
  } catch (err: any) {
    alert(err.message)
  }
  ;(e.target as HTMLInputElement).value = ''
  load()
}

// 「更多操作」菜单里的上传入口：选择文件后先收起菜单再走上传
function onUploadMenu(e: Event) {
  menuOpen.value = false
  onUpload(e)
}

async function onDrop(e: DragEvent) {
  e.preventDefault()
  const files = [...(e.dataTransfer?.files || [])]
  if (!files.length) return
  try { await fsApi.upload(path.value, files) } catch (e: any) { alert(e.message) }
  load()
}

async function doDownload() {
  if (!selected.value) return
  try { await downloadFile(selected.value) } catch (e: any) { alert(e.message) }
}

function fmtSize(n: number): string {
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / 1024 / 1024).toFixed(1) + ' MB'
}

function fileIcon(f: VFile): string {
  if (f.isDir) return '📁'
  const ext = extOf(f.path)
  if (IMAGE_EXT.includes(ext)) return '🖼️'
  switch (ext) {
    case 'md': case 'txt': return '📄'
    case 'csv': case 'tsv': return '📊'
    case 'pdf': return '📕'
    case 'html': return '🌐'
    default: return '📄'
  }
}

const usagePct = computed(() => Math.min(100, Math.round((usage.value.used / usage.value.quota) * 100)))

function closePreview() {
  if (preview.value) URL.revokeObjectURL(preview.value.url)
  preview.value = null
}

// —— 画廊视图：默认选中首项 + 左右切换 + 预加载邻近图片 ——
watch(view, (v) => {
  if (v === 'gallery' && !selected.value && shown.value.length) {
    const f = shown.value.find((x) => !x.isDir) || shown.value[0]
    selected.value = f.path
  }
})
function stepSel(d: number) {
  const list = shown.value
  if (!list.length) return
  let i = list.findIndex((f) => f.path === selected.value)
  i = i === -1 ? 0 : Math.min(list.length - 1, Math.max(0, i + d))
  selected.value = list[i].path
  nextTick(() => {
    const el = document.getElementById(`thumb-${encodeURIComponent(list[i].path)}`)
    el?.scrollIntoView({ block: 'nearest', inline: 'center', behavior: 'smooth' })
  })
}
watch([selected, view], () => {
  if (view.value !== 'gallery' && view.value !== 'column') return
  const list = shown.value
  const i = list.findIndex((f) => f.path === selected.value)
  if (i === -1) {
    const f = selectedFile.value
    if (f && isImage(f)) ensureBlob(f.path)
    return
  }
  for (const f of list.slice(Math.max(0, i - 8), i + 9)) if (isImage(f)) ensureBlob(f.path)
})

// —— 分栏视图：路径链就绪 + 新列自动滚动到最右 ——
watch([view, path], () => {
  if (view.value === 'column') columnPaths.value.forEach(ensureCol)
}, { immediate: true })
watch(() => columnPaths.value.length, async () => {
  if (view.value !== 'column') return
  await nextTick()
  if (colsEl.value) colsEl.value.scrollLeft = colsEl.value.scrollWidth
})
</script>

<template>
  <div class="files" @dragover.prevent @drop="onDrop">
    <!-- 工具栏（融合窗体标题行）：左侧留红绿灯区，标题即当前文件夹；操作收进 ⋯ 菜单 -->
    <div class="toolbar">
      <div class="nav-btns">
        <button class="tool-icon" :disabled="!canBack" title="后退" @click="goBack">‹</button>
        <button class="tool-icon" :disabled="!canForward" title="前进" @click="goForward">›</button>
      </div>
      <div class="tb-title" :title="path">{{ curTitle }}</div>
      <span class="grow" />
      <!-- 视图切换：仿 macOS 分段控件（图标/列表/分栏/画廊） -->
      <div class="seg">
        <button class="seg-btn" :class="{ on: view === 'icon' }" title="图标视图" @click="view = 'icon'">
          <svg viewBox="0 0 16 16"><rect x="2" y="2" width="5" height="5" rx="1.2" fill="currentColor" /><rect x="9" y="2" width="5" height="5" rx="1.2" fill="currentColor" /><rect x="2" y="9" width="5" height="5" rx="1.2" fill="currentColor" /><rect x="9" y="9" width="5" height="5" rx="1.2" fill="currentColor" /></svg>
        </button>
        <button class="seg-btn" :class="{ on: view === 'list' }" title="列表视图" @click="view = 'list'">
          <svg viewBox="0 0 16 16"><rect x="2" y="2.6" width="12" height="1.8" rx="0.9" fill="currentColor" /><rect x="2" y="7.1" width="12" height="1.8" rx="0.9" fill="currentColor" /><rect x="2" y="11.6" width="12" height="1.8" rx="0.9" fill="currentColor" /></svg>
        </button>
        <button class="seg-btn" :class="{ on: view === 'column' }" title="分栏视图" @click="view = 'column'">
          <svg viewBox="0 0 16 16"><rect x="1.5" y="2" width="13" height="12" rx="1.6" fill="none" stroke="currentColor" stroke-width="1.4" /><line x1="5.8" y1="2" x2="5.8" y2="14" stroke="currentColor" stroke-width="1.1" /><line x1="10.2" y1="2" x2="10.2" y2="14" stroke="currentColor" stroke-width="1.1" /></svg>
        </button>
        <button class="seg-btn" :class="{ on: view === 'gallery' }" title="画廊视图" @click="view = 'gallery'">
          <svg viewBox="0 0 16 16"><rect x="1.5" y="2.5" width="13" height="11" rx="1.6" fill="none" stroke="currentColor" stroke-width="1.4" /><circle cx="5.2" cy="6" r="1.2" fill="currentColor" /><path d="M3.5 12 L7 8.5 L9.5 11 L11.5 9 L14 11.5" fill="none" stroke="currentColor" stroke-width="1.2" /></svg>
        </button>
      </div>
      <OsSelect v-model="sortBy" :options="[
        { value: 'name', label: '按名称' }, { value: 'size', label: '按大小' }, { value: 'time', label: '按时间' },
      ]" />
      <OsInput v-model="query" placeholder="🔍 搜索" style="width: 118px" />
      <div class="tb-actions">
        <button class="tool-icon" title="更多操作" @click="menuOpen = !menuOpen">⋯</button>
        <template v-if="menuOpen">
          <div class="menu-mask" @click="menuOpen = false" />
          <div class="menu">
            <button class="mi" @click="menuOpen = false; creating = 'dir'; newName = ''">新建文件夹</button>
            <button class="mi" @click="menuOpen = false; creating = 'file'; newName = ''">新建文件</button>
            <label class="mi">上传文件…<input ref="uploadInput" type="file" multiple hidden @change="onUploadMenu" /></label>
            <div class="menu-sep" />
            <button class="mi" :disabled="!selected" @click="menuOpen = false; doDownload()">下载</button>
            <button class="mi" :disabled="!selected" @click="menuOpen = false; renaming = selected; renameTo = baseName(selected)">重命名</button>
            <button class="mi danger" :disabled="!selected" @click="menuOpen = false; doRemove()">删除</button>
          </div>
        </template>
      </div>
    </div>

    <div class="body">
      <!-- 左侧导航栏（仿 Finder 边栏）：收藏 + 位置 -->
      <aside class="sidebar">
        <div class="side-head">收藏</div>
        <button class="side-item" :class="{ active: isActive(home) }" @click="enter(home)">
          <span class="ic">🏠</span><span class="lb">主页</span>
        </button>
        <button
          v-for="d in favDirs" :key="d.path"
          class="side-item" :class="{ active: isActive(d.path) }" @click="enter(d.path)"
        >
          <span class="ic">{{ sideIcon(baseName(d.path)) }}</span><span class="lb">{{ baseName(d.path) }}</span>
        </button>
        <template v-if="placeDirs.length">
          <div class="side-head">位置</div>
          <button
            v-for="d in placeDirs" :key="d.path"
            class="side-item" :class="{ active: isActive(d.path) }" @click="enter(d.path)"
          >
            <span class="ic">📁</span><span class="lb">{{ baseName(d.path) }}</span>
          </button>
        </template>
      </aside>

      <main class="content" :class="'view-' + view">
        <div v-if="loading" class="state">加载中…</div>
        <OsEmpty v-else-if="view !== 'column' && !shown.length" :text="query ? '没有匹配的文件' : '此文件夹为空（可拖入文件上传）'" icon="📂" />

        <!-- 图标视图 -->
        <template v-else-if="view === 'icon'">
          <div
            v-for="f in shown" :key="f.path"
            class="item" :class="{ sel: selected === f.path }"
            @click="selected = f.path"
            @dblclick="openFile(f)"
          >
            <div class="ic">{{ fileIcon(f) }}</div>
            <div class="name">{{ baseName(f.path) }}</div>
            <div class="meta">{{ f.isDir ? '文件夹' : fmtSize(f.size) }}</div>
          </div>
        </template>

        <!-- 列表视图：点击表头排序 -->
        <table v-else-if="view === 'list'" class="list-table">
          <thead>
            <tr>
              <th class="sortable" @click="setSort('name')">名称<span class="arr" v-if="sortBy === 'name'">{{ sortDir === 1 ? '↑' : '↓' }}</span></th>
              <th class="sortable" @click="setSort('time')">修改时间<span class="arr" v-if="sortBy === 'time'">{{ sortDir === 1 ? '↑' : '↓' }}</span></th>
              <th class="sortable" @click="setSort('size')">大小<span class="arr" v-if="sortBy === 'size'">{{ sortDir === 1 ? '↑' : '↓' }}</span></th>
              <th>类型</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="f in shown" :key="f.path" :class="{ sel: selected === f.path }"
              @click="selected = f.path" @dblclick="openFile(f)"
            >
              <td class="tn">{{ fileIcon(f) }} {{ baseName(f.path) }}</td>
              <td class="dim">{{ f.updatedAt }}</td>
              <td>{{ f.isDir ? '—' : fmtSize(f.size) }}</td>
              <td class="dim">{{ f.isDir ? '文件夹' : (extOf(f.path) || '文件') }}</td>
            </tr>
          </tbody>
        </table>

        <!-- 分栏视图：逐级展开 + 最右预览列 -->
        <div v-else-if="view === 'column'" ref="colsEl" class="columns">
          <section v-for="(colPath, i) in columnPaths" :key="colPath" class="col">
            <template v-if="colCache.has(colPath)">
              <div
                v-for="f in colShown(colPath)" :key="f.path"
                class="col-item" :class="{ sel: columnPaths[i + 1] === f.path || selected === f.path }"
                @click="f.isDir ? enter(f.path) : (selected = f.path)"
                @dblclick="openFile(f)"
              >
                <span class="ci">{{ fileIcon(f) }}</span>
                <span class="cn">{{ baseName(f.path) }}</span>
                <span class="ct">{{ f.isDir ? '' : fmtSize(f.size) }}</span>
                <span v-if="f.isDir" class="chev">›</span>
              </div>
              <div v-if="!colShown(colPath).length" class="col-empty">空文件夹</div>
            </template>
            <div v-else class="col-loading">…</div>
          </section>
          <section v-if="selectedFile && !selectedFile.isDir" class="col col-preview">
            <img v-if="isImage(selectedFile) && blobUrl(selectedFile.path)" class="pv-img" :src="blobUrl(selectedFile.path)" alt="">
            <div v-else class="pv-emoji">{{ fileIcon(selectedFile) }}</div>
            <div class="pv-name">{{ baseName(selectedFile.path) }}</div>
            <div class="pv-meta">{{ fmtSize(selectedFile.size) }}<br>{{ selectedFile.updatedAt }}</div>
          </section>
        </div>

        <!-- 画廊视图：大图预览 + 底部缩略胶片 -->
        <div v-else-if="view === 'gallery'" class="gallery" tabindex="0" @keydown.left.prevent="stepSel(-1)" @keydown.right.prevent="stepSel(1)">
          <div v-if="selectedFile" class="g-main">
            <img
              v-if="isImage(selectedFile) && blobUrl(selectedFile.path)"
              :src="blobUrl(selectedFile.path)" :alt="baseName(selectedFile.path)" @dblclick="openFile(selectedFile)"
            >
            <div v-else class="g-emoji" @dblclick="openFile(selectedFile)">{{ fileIcon(selectedFile) }}</div>
            <div class="g-name">
              {{ baseName(selectedFile.path) }}
              <span class="g-meta">{{ selectedFile.isDir ? '文件夹' : fmtSize(selectedFile.size) }} · {{ selectedFile.updatedAt }}</span>
            </div>
          </div>
          <div v-else class="g-empty">选择一个项目以预览</div>
          <button class="g-nav left" title="上一个" @click="stepSel(-1)">‹</button>
          <button class="g-nav right" title="下一个" @click="stepSel(1)">›</button>
          <div class="film">
            <div
              v-for="f in shown" :id="'thumb-' + encodeURIComponent(f.path)" :key="f.path"
              class="thumb" :class="{ sel: selected === f.path }"
              @click="selected = f.path" @dblclick="openFile(f)"
            >
              <img v-if="isImage(f) && blobUrl(f.path)" :src="blobUrl(f.path)" alt="">
              <span v-else>{{ fileIcon(f) }}</span>
            </div>
          </div>
        </div>
      </main>
    </div>

    <footer class="status">
      <span>{{ shown.length }} 项 · {{ path }}</span>
      <span class="grow" />
      <span class="storage" :title="fmtSize(usage.used) + ' / ' + fmtSize(usage.quota)">
        空间 {{ usagePct }}%（{{ fmtSize(usage.used) }}）
      </span>
      <span class="storage-bar"><i :style="{ width: usagePct + '%' }" :class="{ warn: usagePct > 85 }" /></span>
    </footer>

    <OsModal v-if="creating" :title="creating === 'dir' ? '新建文件夹' : '新建文件'" okText="创建" @ok="confirmCreate" @cancel="creating = null">
      <OsInput v-model="newName" placeholder="名称（含扩展名，如 笔记.md）" @keydown.enter="confirmCreate" />
    </OsModal>

    <OsModal v-if="renaming" title="重命名" okText="确定" @ok="doRename" @cancel="renaming = ''">
      <OsInput v-model="renameTo" @keydown.enter="doRename" />
    </OsModal>

    <!-- 图片快速预览 -->
    <div v-if="preview" class="img-viewer" @pointerdown.self="closePreview">
      <header>
        <span>{{ preview.name }}</span>
        <button class="close" @click="closePreview">✕</button>
      </header>
      <img :src="preview.url" :alt="preview.name">
    </div>
  </div>
</template>

<style scoped>
.files { width: 100%; display: flex; flex-direction: column; background: var(--os-window); }
/* 工具栏 = 融合窗体标题行：左侧 84px 为红绿灯悬浮区（WindowFrame 绘制），单行不换行 */
.toolbar {
  flex: none; height: 46px; display: flex; align-items: center; gap: 8px;
  padding: 0 12px 0 84px; overflow: hidden;
  background: var(--os-sidebar); border-bottom: 1px solid var(--os-border);
}
.nav-btns { display: flex; gap: 4px; }
.tool-icon {
  border: 1px solid var(--os-border-strong); background: var(--os-panel-solid); color: var(--os-text);
  border-radius: 7px; width: 26px; height: 28px; cursor: pointer; font-size: 16px; line-height: 1;
  display: flex; align-items: center; justify-content: center; padding-bottom: 2px;
}
.tool-icon:disabled { opacity: .35; cursor: default; }
.tb-title {
  font-size: 13.5px; font-weight: 600; white-space: nowrap;
  overflow: hidden; text-overflow: ellipsis; max-width: 42%;
}
.grow { flex: 1; }
/* 视图分段控件（仿 macOS segmented control） */
.seg { display: flex; gap: 1px; padding: 2px; border-radius: 8px; background: var(--os-active); }
.seg-btn {
  border: none; background: transparent; width: 32px; height: 24px; border-radius: 6px;
  cursor: pointer; color: var(--os-text-secondary); display: flex; align-items: center; justify-content: center;
}
.seg-btn svg { width: 13px; height: 13px; }
.seg-btn:hover { color: var(--os-text); }
.seg-btn.on { background: var(--os-panel-solid); color: var(--os-text); box-shadow: 0 1px 3px rgba(0, 0, 0, .18); }

/* 「更多操作」下拉菜单（仿 macOS 菜单：hover 蓝底白字） */
.tb-actions { position: relative; display: flex; }
.menu-mask { position: fixed; inset: 0; z-index: 50; }
.menu {
  position: absolute; top: 34px; right: 0; z-index: 51; min-width: 156px;
  background: var(--os-panel-solid); border: 1px solid var(--os-border-strong);
  border-radius: 10px; box-shadow: var(--os-shadow-sm); padding: 5px;
  display: flex; flex-direction: column; animation: os-pop-in .12s var(--os-ease-out);
}
.mi {
  border: none; background: none; width: 100%; text-align: left; padding: 6px 10px;
  border-radius: 6px; font-size: 13px; color: var(--os-text); cursor: pointer; font-family: inherit;
}
.mi:hover:not(:disabled) { background: var(--os-accent); color: #fff; }
.mi:disabled { opacity: .4; cursor: default; }
.mi.danger:hover:not(:disabled) { background: #e5484d; }
.menu-sep { height: 1px; margin: 4px 8px; background: var(--os-border); }

/* 主体：侧边栏 + 内容区 */
.body { flex: 1; display: flex; min-height: 0; }
.sidebar {
  flex: none; width: 172px; overflow-y: auto; padding: 6px 8px 12px;
  background: var(--os-sidebar); border-right: 1px solid var(--os-border);
}
.side-head { font-size: 11px; font-weight: 600; color: var(--os-text-secondary); padding: 10px 8px 4px; }
.side-item {
  display: flex; align-items: center; gap: 7px; width: 100%; padding: 5px 8px;
  border: none; background: none; border-radius: 7px; cursor: pointer; text-align: left;
  font-size: 13px; color: var(--os-text); font-family: inherit;
}
.side-item .ic { flex: none; font-size: 14px; }
.side-item .lb { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.side-item:hover { background: var(--os-hover); }
.side-item.active { background: var(--os-selected); color: var(--os-accent); }

.content { flex: 1; min-width: 0; overflow-y: auto; }
.content.view-icon {
  padding: 14px; display: grid; gap: 6px; align-content: start;
  grid-template-columns: repeat(auto-fill, minmax(96px, 1fr));
}
.content.view-list { display: block; }
.content.view-column { display: flex; padding: 0; overflow-x: auto; overflow-y: hidden; }
.content.view-gallery { display: flex; flex-direction: column; padding: 0; overflow: hidden; }
.state { padding: 40px; text-align: center; color: var(--os-text-secondary); grid-column: 1 / -1; }

/* 图标视图 */
.item {
  display: flex; flex-direction: column; align-items: center; gap: 4px;
  padding: 10px 4px; border-radius: 10px; cursor: default; text-align: center;
}
.item:hover { background: var(--os-hover); }
.item.sel { background: var(--os-selected); }
.item .ic { font-size: 40px; }
.item .name { font-size: 12px; word-break: break-all; line-height: 1.3; border-radius: 4px; padding: 0 2px; }
.item.sel .name { background: var(--os-accent); color: #fff; }
.item .meta { font-size: 10.5px; color: var(--os-text-secondary); }

/* 列表视图 */
.list-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.list-table th {
  text-align: left; padding: 8px 12px; font-size: 12px; color: var(--os-text-secondary);
  border-bottom: 1px solid var(--os-border); position: sticky; top: 0; background: var(--os-sidebar);
  user-select: none; white-space: nowrap;
}
.list-table th.sortable { cursor: pointer; }
.list-table th.sortable:hover { color: var(--os-text); }
.list-table th .arr { margin-left: 3px; }
.list-table td { padding: 7px 12px; border-bottom: 1px solid var(--os-border); }
.list-table tr:hover { background: var(--os-hover); }
.list-table tr.sel { background: var(--os-selected); }
.tn { max-width: 320px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dim { color: var(--os-text-secondary); font-size: 12px; }

/* 分栏视图 */
.columns { display: flex; flex: 1; min-width: 0; align-items: stretch; }
.col {
  flex: none; width: 190px; overflow-y: auto; padding: 6px 4px;
  border-right: 1px solid var(--os-border); background: var(--os-window);
}
.col-item {
  display: flex; align-items: center; gap: 6px; padding: 4px 8px; border-radius: 6px;
  font-size: 13px; cursor: default;
}
.col-item .ci { flex: none; }
.col-item .cn { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.col-item .ct { flex: none; font-size: 11px; color: var(--os-text-secondary); }
.col-item .chev { flex: none; color: var(--os-text-secondary); }
.col-item:hover { background: var(--os-hover); }
.col-item.sel { background: var(--os-selected); }
.col-empty, .col-loading { padding: 14px 10px; font-size: 12px; color: var(--os-text-secondary); text-align: center; }
.col-preview {
  width: 230px; display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 8px; padding: 16px; text-align: center;
}
.pv-img { max-width: 100%; max-height: 150px; object-fit: contain; border-radius: 6px; box-shadow: var(--os-shadow-sm); }
.pv-emoji { font-size: 60px; }
.pv-name { font-size: 13px; word-break: break-all; }
.pv-meta { font-size: 11px; color: var(--os-text-secondary); line-height: 1.6; }

/* 画廊视图 */
.gallery { flex: 1; display: flex; flex-direction: column; min-height: 0; position: relative; outline: none; }
.g-main {
  flex: 1; min-height: 0; display: flex; flex-direction: column; align-items: center;
  justify-content: center; gap: 10px; padding: 20px 56px;
}
.g-main img { max-width: 100%; max-height: calc(100% - 40px); object-fit: contain; border-radius: 8px; box-shadow: var(--os-shadow-sm); }
.g-emoji { font-size: 84px; cursor: default; }
.g-name { font-size: 13px; text-align: center; word-break: break-all; }
.g-meta { color: var(--os-text-secondary); font-size: 11.5px; }
.g-empty { flex: 1; display: flex; align-items: center; justify-content: center; color: var(--os-text-secondary); }
.g-nav {
  position: absolute; top: 40%; width: 30px; height: 44px; border: 1px solid var(--os-border-strong);
  background: var(--os-panel-solid); color: var(--os-text); border-radius: 8px; font-size: 20px; cursor: pointer; z-index: 5;
}
.g-nav:hover { background: var(--os-hover); }
.g-nav.left { left: 10px; }
.g-nav.right { right: 10px; }
.film {
  flex: none; height: 70px; display: flex; gap: 8px; align-items: center;
  padding: 8px 12px; overflow-x: auto; border-top: 1px solid var(--os-border); background: var(--os-sidebar);
}
.thumb {
  flex: none; width: 52px; height: 52px; border-radius: 6px; overflow: hidden; cursor: pointer;
  display: flex; align-items: center; justify-content: center; font-size: 24px;
  background: var(--os-active); border: 2px solid transparent;
}
.thumb img { width: 100%; height: 100%; object-fit: cover; }
.thumb:hover { background: var(--os-hover); }
.thumb.sel { border-color: var(--os-accent); }

/* 状态栏 */
.status {
  flex: none; padding: 5px 12px; font-size: 11.5px; color: var(--os-text-secondary);
  border-top: 1px solid var(--os-border); background: var(--os-sidebar);
  display: flex; gap: 12px; align-items: center;
}
.storage-bar { width: 80px; height: 4px; border-radius: 2px; background: var(--os-active); overflow: hidden; }
.storage-bar i { display: block; height: 100%; background: var(--os-accent); }
.storage-bar i.warn { background: #ff9f0a; }

/* 图片快速预览 */
.img-viewer {
  position: absolute; inset: 0; z-index: 60; background: rgba(0,0,0,.82);
  display: flex; flex-direction: column; animation: os-fade-in .15s;
}
.img-viewer header {
  display: flex; justify-content: space-between; align-items: center; padding: 10px 16px; color: #fff; font-size: 13px;
}
.img-viewer .close {
  border: none; background: rgba(255,255,255,.15); color: #fff; border-radius: 50%;
  width: 26px; height: 26px; cursor: pointer;
}
.img-viewer img { flex: 1; min-height: 0; object-fit: contain; margin: 0 16px 16px; }
</style>
