<script setup lang="ts">
// 文件应用（访达拟合）：四视图 + 侧栏收藏/位置/废纸篓 + 右键菜单 + 多选 + 键盘导航
// + 拖拽移动/复制 + 废纸篓 + 快速预览（空格）+ 路径栏 + 原位重命名。
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { useApps } from '../../stores/apps'
import { useSession } from '../../stores/session'
import { fsApi, downloadFile, fetchRawBlob, type VFile } from '../../api/client'
import { bus } from '../../bus/bus'
import { OsInput, OsButton, OsEmpty, OsModal, toast, useMenu, menuPos, icons, resolveFileDialog, type FileDialogRequest } from '../../ui'
import { openPath, openWithApps, isImage, fileEmoji, kindOf, extOf, fmtSize, IMAGE_EXTS, parentOf } from '../openWith'
import { renderMarkdown } from '../../lib/md'
import { parseCSV } from '../sheets/engine'
import { blankDeck } from '../slides/format'

const props = defineProps<{ win: WinState }>()
const windows = useWindows()
const apps = useApps()
const session = useSession()
const home = `/home/${session.user!.username}`
const trashPath = home + '/.Trash'

// —— 文件选择模式（其他应用经 ui/fileDialog 服务唤起本窗口充当「打开/存储」对话框） ——
// 本窗口仍是完整可拖动的文件管理窗，仅底部状态栏换成选择栏、双击文件=确认。
const pickerReq: FileDialogRequest | undefined = props.win.props?.picker
const path = ref(props.win.props?.path || pickerReq?.defaultDir || home)
const pickName = ref(pickerReq?.defaultName || '')
const pickOverwrite = ref(false) // save 模式重名：第一次点变「替换」，再点才覆盖
watch([pickName, path], () => { pickOverwrite.value = false })
onBeforeUnmount(() => { if (pickerReq) resolveFileDialog(pickerReq.reqId, null) }) // 关窗=取消（幂等）
const entries = ref<VFile[]>([])
const loading = ref(true)
const query = ref('')
const sortBy = ref(localStorage.getItem('openos_files_sort') || 'name')
const sortDir = ref<1 | -1>(1)
// 四种视图：图标 / 列表 / 分栏 / 画廊（仿 macOS Finder，全局记忆）
type View = 'icon' | 'list' | 'column' | 'gallery'
const view = ref<View>((localStorage.getItem('openos_files_view') as View) || 'icon')
watch(view, (v) => localStorage.setItem('openos_files_view', v))
watch(sortBy, (v) => localStorage.setItem('openos_files_sort', v))
const usage = ref({ used: 0, quota: 100 * 1024 * 1024 })
const uploadInput = ref<HTMLInputElement>()
const contentEl = ref<HTMLElement>()
const colsEl = ref<HTMLElement>()

// ---- 多选（Cmd/⌃ 点选、Shift 范围选、⌘A 全选） ----
const selSet = ref<Set<string>>(new Set())
const anchorPath = ref('')
const hasSel = (p: string) => selSet.value.has(p)
function setSelection(paths: string[]) {
  selSet.value = new Set(paths)
  anchorPath.value = paths[paths.length - 1] || ''
}
function toggleSelection(p: string) {
  const s = new Set(selSet.value)
  if (s.has(p)) s.delete(p)
  else s.add(p)
  selSet.value = s
  anchorPath.value = p
}
function extendSelection(p: string) {
  const list = shown.value
  const a = list.findIndex((f) => f.path === anchorPath.value)
  const b = list.findIndex((f) => f.path === p)
  if (a === -1 || b === -1) { setSelection([p]); return }
  const [lo, hi] = a < b ? [a, b] : [b, a]
  const s = new Set(selSet.value)
  for (let k = lo; k <= hi; k++) s.add(list[k].path)
  selSet.value = s
}
function onItemClick(e: MouseEvent, f: VFile) {
  if (e.metaKey || e.ctrlKey) toggleSelection(f.path)
  else if (e.shiftKey) extendSelection(f.path)
  else setSelection([f.path])
}
const selPaths = computed(() => [...selSet.value])
const primary = computed(() => selPaths.value[selPaths.value.length - 1] || '')
const inTrash = computed(() => path.value === trashPath || path.value.startsWith(trashPath + '/'))

// 剪贴板（模块级：跨文件窗口共享拷贝/剪切）
let clip: { mode: 'copy' | 'move'; paths: string[] } | null = null

// 前进/后退历史
const history = ref<string[]>([path.value])
const hIdx = ref(0)
const canBack = computed(() => hIdx.value > 0)
const canForward = computed(() => hIdx.value < history.value.length - 1)

// 侧栏：收藏（语义名识别）+ 位置 + 废纸篓
const homeDirs = ref<VFile[]>([])
const trashFull = ref(false)
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

// 分栏视图列缓存
const colCache = reactive(new Map<string, VFile[]>())
const colInflight = new Set<string>()
async function ensureCol(p: string) {
  if (colCache.has(p) || colInflight.has(p)) return
  colInflight.add(p)
  try { colCache.set(p, hideDots(await fsApi.list(p))) } catch { colCache.set(p, []) } finally { colInflight.delete(p) }
}
const columnPaths = computed(() => {
  const chain: string[] = []
  let p: string | undefined = path.value
  while (p && (p.startsWith(home))) {
    chain.unshift(p)
    if (p === home) break
    p = p.split('/').slice(0, -1).join('/')
  }
  return chain
})

// 图片缩略 blob 缓存（图标/列表/分栏缩略 + 画廊大图共用）
const blobCache = reactive(new Map<string, string>())
async function ensureBlob(p: string) {
  if (blobCache.has(p)) return
  blobCache.set(p, '')
  try { blobCache.set(p, URL.createObjectURL(await fetchRawBlob(p))) } catch { /* 忽略 */ }
}
const blobUrl = (p: string) => blobCache.get(p) || ''

const baseName = (p: string) => p.split('/').pop() || ''
// 隐藏点开头的文件/目录（macOS 行为；.Trash 只经侧栏进入）
const hideDots = (list: VFile[]) => list.filter((f) => !baseName(f.path).startsWith('.'))

async function load() {
  loading.value = true
  try {
    const [list, u] = await Promise.all([fsApi.list(path.value), fsApi.usage()])
    entries.value = hideDots(list)
    colCache.set(path.value, entries.value)
    usage.value = u
    // 清掉已不存在的选中项
    const alive = new Set(entries.value.map((f) => f.path))
    selSet.value = new Set([...selSet.value].filter((p) => alive.has(p)))
    // 图标/列表/分栏的图片缩略（上限 80 张防大目录轰炸）
    let n = 0
    for (const f of entries.value) {
      if (isImage(f) && n++ < 80) ensureBlob(f.path)
      if (n >= 80) break
    }
  } finally {
    loading.value = false
  }
}
async function refreshAll() {
  try {
    homeDirs.value = hideDots(await fsApi.list(home)).filter((f) => f.isDir && f.path !== trashPath)
  } catch { homeDirs.value = [] }
  try { trashFull.value = (await fsApi.list(trashPath)).length > 0 } catch { trashFull.value = false }
  load()
  colCache.clear()
  columnPaths.value.forEach(ensureCol)
}
onMounted(() => {
  refreshAll()
  // 打开即快速预览（聚焦搜索/打开方式把图片交给文件应用时）
  const pv = props.win.props?.preview
  if (pv) quickLook({ path: pv, isDir: false, size: 0, updatedAt: '' })
})

// 其他窗口保存/删除后自动刷新
const off = bus.on('fs:changed', refreshAll)
onBeforeUnmount(() => {
  off()
  blobCache.forEach((u) => u && URL.revokeObjectURL(u))
})

// 融合窗体标题行：当前文件夹名 + 已编辑标记，同步给 Dock/切换
// （选择模式保持 fileDialog 传入的对话框标题，不随目录切换变化）
const curTitle = computed(() => (path.value === home ? '主页' : path.value === trashPath ? '废纸篓' : baseName(path.value)))
watch(curTitle, (t) => { if (!pickerReq) windows.setTitle(props.win.id, t) }, { immediate: true })

// ---- 排序与过滤 ----
const baseCmp = computed(() => {
  const cmp: Record<string, (a: VFile, b: VFile) => number> = {
    name: (a, b) => baseName(a.path).localeCompare(baseName(b.path), 'zh', { numeric: true }),
    size: (a, b) => a.size - b.size,
    time: (a, b) => a.updatedAt.localeCompare(b.updatedAt),
  }
  const fn = cmp[sortBy.value] || cmp.name
  return (a: VFile, b: VFile) => fn(a, b) * sortDir.value
})
function sortedOf(list: VFile[]): VFile[] {
  const q = query.value.trim().toLowerCase()
  let filtered = q ? list.filter((f) => f.path.toLowerCase().includes(q)) : list
  // 选择模式 open + 扩展名过滤：只留目录与匹配后缀的文件
  if (pickerReq?.mode === 'open' && pickerReq.exts?.length) {
    filtered = filtered.filter((f) => f.isDir || pickerReq.exts!.includes(extOf(f.path)))
  }
  return [...filtered].sort((a, b) => Number(b.isDir) - Number(a.isDir) || baseCmp.value(a, b))
}
const shown = computed(() => sortedOf(entries.value))
const colShown = (p: string) => sortedOf(colCache.get(p) || [])
// 空目录/无匹配时切到 flex 居中布局，空状态文案垂直居中（网格/列表会把内容顶在上方）
const contentClass = computed(() => (!shown.value.length && view.value !== 'column' ? 'view-empty' : 'view-' + view.value))

// 选中项（跨分栏缓存查找）
const selectedFile = computed(() => {
  const cur = entries.value.find((f) => f.path === primary.value)
  if (cur) return cur
  for (const list of colCache.values()) {
    const f = list.find((x) => x.path === primary.value)
    if (f) return f
  }
  return null
})

// ---- 导航 ----
function enter(p: string, push = true) {
  if (p === path.value && !push) return
  path.value = p
  setSelection([])
  query.value = ''
  if (push) {
    history.value = history.value.slice(0, hIdx.value + 1)
    history.value.push(p)
    hIdx.value = history.value.length - 1
  }
  load()
}
function enterAt(p: string) {
  path.value = p
  setSelection([])
  query.value = ''
  load()
}
function goBack() { if (!canBack.value) return; hIdx.value--; enterAt(history.value[hIdx.value]) }
function goForward() { if (!canForward.value) return; hIdx.value++; enterAt(history.value[hIdx.value]) }
const isActive = (p: string) => (p === home ? path.value === p : path.value === p || path.value.startsWith(p + '/'))

// 底部路径栏面包屑
const crumbs = computed(() => {
  const out: { label: string; path: string }[] = [{ label: '主页', path: home }]
  if (path.value.startsWith(home + '/')) {
    let acc = home
    for (const seg of path.value.slice(home.length + 1).split('/')) {
      acc += '/' + seg
      out.push({ label: seg === '.Trash' ? '废纸篓' : seg, path: acc })
    }
  }
  return out
})

// ---- 打开 / 快速预览 ----
async function openFile(f: VFile) {
  if (f.isDir) { enter(f.path); return }
  if (pickerReq) {
    // 选择模式：open 双击文件=确认选择；save 双击=带出文件名
    if (pickerReq.mode === 'open') confirmPick()
    else pickName.value = baseName(f.path)
    return
  }
  if (IMAGE_EXTS.includes(extOf(f.path))) { quickLook(f); return }
  openPath(f.path)
}

// ---- 选择模式（fileDialog）：目标路径 / 重名确认 / 回执 ----
const pickSel = computed(() => (selectedFile.value && !selectedFile.value.isDir ? selectedFile.value : null))
const pickTarget = computed(() => {
  if (!pickerReq) return ''
  if (pickerReq.mode === 'open') return pickSel.value?.path || ''
  const n = pickName.value.trim()
  return n && n !== '.' && n !== '..' ? path.value.replace(/\/$/, '') + '/' + n : ''
})
const pickExists = computed(() => {
  if (pickerReq?.mode !== 'save' || !pickTarget.value) return false
  return entries.value.some((f) => !f.isDir && f.path === pickTarget.value)
})
const canPick = computed(() => !!pickTarget.value)
function confirmPick() {
  if (!pickerReq || !pickTarget.value) return
  // save 模式重名：第一次点变更为「替换」，再点才真正覆盖（macOS 行为）
  if (pickerReq.mode === 'save' && pickExists.value && !pickOverwrite.value) { pickOverwrite.value = true; return }
  resolveFileDialog(pickerReq.reqId, pickTarget.value)
  windows.close(props.win.id)
}
function cancelPick() {
  if (pickerReq) resolveFileDialog(pickerReq.reqId, null)
  windows.close(props.win.id)
}
function openSelection() {
  for (const p of selPaths.value) {
    const f = entries.value.find((x) => x.path === p)
    if (f) openFile(f)
  }
}

const ql = ref<{ f: VFile; url?: string; html?: string; text?: string; loading: boolean } | null>(null)
async function quickLook(f: VFile) {
  closeQl()
  ql.value = { f, loading: true }
  const ext = extOf(f.path)
  try {
    if (IMAGE_EXTS.includes(ext) || ext === 'pdf') {
      ql.value = { f, url: URL.createObjectURL(await fetchRawBlob(f.path)), loading: false }
    } else if (ext === 'md' || ext === 'markdown') {
      const d = await fsApi.read(f.path)
      ql.value = { f, html: renderMarkdown(d.content), loading: false }
    } else if (ext === 'csv' || ext === 'tsv') {
      const d = await fsApi.read(f.path)
      const rows = ext === 'tsv' ? d.content.split(/\r?\n/).map((l) => l.split('\t')) : parseCSV(d.content)
      const body = rows.slice(0, 60).map((r) => '<tr>' + r.slice(0, 12).map((c) => `<td>${c.replace(/&/g, '&amp;').replace(/</g, '&lt;')}</td>`).join('') + '</tr>').join('')
      ql.value = { f, html: `<table class="ql-table">${body}</table>`, loading: false }
    } else if (ext === 'html' || ext === 'htm') {
      const d = await fsApi.read(f.path)
      ql.value = { f, html: d.content, loading: false }
    } else {
      const d = await fsApi.read(f.path)
      ql.value = { f, text: d.isBinary ? '（二进制文件，请下载查看）' : d.content, loading: false }
    }
  } catch {
    ql.value = { f, text: '无法读取文件', loading: false }
  }
}
function closeQl() {
  if (ql.value?.url) URL.revokeObjectURL(ql.value.url)
  ql.value = null
}
function onQlKey(e: KeyboardEvent) {
  if (e.key === 'Escape' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); closeQl() }
}

// ---- 命名工具：冲突时生成「名称 副本」「名称 副本 2」 ----
async function uniqueName(dir: string, wanted: string): Promise<string> {
  let list: VFile[]
  if (dir === path.value) list = entries.value
  else { try { list = await fsApi.list(dir) } catch { list = [] } }
  const taken = new Set(list.map((f) => baseName(f.path)))
  if (!taken.has(wanted)) return wanted
  const dot = wanted.lastIndexOf('.')
  const stem = dot > 0 ? wanted.slice(0, dot) : wanted
  const ext = dot > 0 ? wanted.slice(dot) : ''
  if (!taken.has(`${stem} 副本${ext}`)) return `${stem} 副本${ext}`
  let n = 2
  while (taken.has(`${stem} 副本 ${n}${ext}`)) n++
  return `${stem} 副本 ${n}${ext}`
}

// ---- 移动 / 复制 / 废纸篓 ----
async function transferTo(dir: string, paths: string[], copy: boolean) {
  if (!paths.length) return
  await fsApi.mkdir(dir).catch(() => {})
  const errors: string[] = []
  for (const p of paths) {
    if (parentOf(p) === dir && !copy) continue // 原地跳过
    if (dir === p || dir.startsWith(p + '/')) continue // 不能移进自身
    const base = baseName(p)
    const target = dir + '/' + await uniqueName(dir, base)
    try {
      if (copy) await fsApi.copy(p, target)
      else await fsApi.rename(p, target)
    } catch (e: any) { errors.push(`${base}：${e.message}`) }
  }
  if (errors.length) toast(errors.slice(0, 3).join('；'), { type: 'error' })
  else toast(copy ? `已复制 ${paths.length} 个项目` : `已移动 ${paths.length} 个项目`)
  refreshAll()
}

async function moveToTrash(paths: string[]) {
  if (!paths.length) return
  if (inTrash.value) {
    // 废纸篓内删除 = 立即删除（二次确认由调用方决定）
    for (const p of paths) await fsApi.remove(p).catch(() => {})
    toast('已永久删除')
    refreshAll()
    return
  }
  await fsApi.mkdir(trashPath).catch(() => {})
  await transferTo(trashPath, paths, false)
}

const confirmEmpty = ref(false)
async function emptyTrash() {
  confirmEmpty.value = false
  try {
    for (const k of await fsApi.list(trashPath)) await fsApi.remove(k.path)
    toast('废纸篓已清倒')
  } catch (e: any) { toast(e.message, { type: 'error' }) }
  refreshAll()
}

const confirmDelete = ref(false)
function deleteSelection() {
  if (!inTrash.value) { moveToTrash(selPaths.value); return }
  confirmDelete.value = false
  moveToTrash(selPaths.value) // inTrash 分支 = 永久删除
}

async function duplicateSelection() {
  const errors: string[] = []
  for (const p of selPaths.value) {
    const target = path.value + '/' + await uniqueName(path.value, baseName(p))
    try { await fsApi.copy(p, target) } catch (e: any) { errors.push(e.message) }
  }
  if (errors.length) toast(errors.slice(0, 2).join('；'), { type: 'error' })
  refreshAll()
}

function doCopy() { if (selPaths.value.length) clip = { mode: 'copy', paths: selPaths.value } }
function doCut() { if (selPaths.value.length) clip = { mode: 'move', paths: selPaths.value } }
function canPaste() { return !!clip && clip.paths.length > 0 && !inTrash.value }
function doPaste() { if (clip) transferTo(path.value, clip.paths, clip.mode === 'copy') }

async function doDownload() {
  for (const p of selPaths.value) {
    try { await downloadFile(p) } catch (e: any) { toast(e.message, { type: 'error' }) }
  }
}

// ---- 新建 / 重命名 ----
const creating = ref<'dir' | null>(null)
const newName = ref('')
async function confirmCreate() {
  if (!creating.value || !newName.value.trim()) { creating.value = null; return }
  const p = path.value + '/' + newName.value.trim()
  try {
    if (creating.value === 'dir') await fsApi.mkdir(p)
  } catch (e: any) { toast(e.message, { type: 'error' }) }
  creating.value = null; newName.value = ''
  refreshAll()
}

async function newFile(kind: 'md' | 'csv' | 'html') {
  ctxMenu.close()
  const def = {
    md: { name: '未命名文稿.md', content: '# 未命名文稿\n\n' },
    csv: { name: '未命名表格.csv', content: '列 1,列 2,列 3\n,,\n' },
    html: { name: '未命名演示.html', content: blankDeck('未命名演示') },
  }[kind]
  const p = path.value + '/' + await uniqueName(path.value, def.name)
  try { await fsApi.touch(p, def.content) } catch (e: any) { toast(e.message, { type: 'error' }) }
  refreshAll()
}

// 原位重命名（Finder：回车或点选中名）
const renaming = ref('')
const renameTo = ref('')
const renameEl = ref<HTMLInputElement>()
function startRename(p: string) {
  renaming.value = p
  renameTo.value = baseName(p)
  nextTick(() => {
    // v-for 内的模板 ref 是数组（三种视图共用一个名字，取实际挂载的那个）
    const el = (Array.isArray(renameEl.value) ? renameEl.value[0] : renameEl.value) as HTMLInputElement | undefined
    el?.focus()
    el?.select()
  })
}
async function commitRename() {
  const p = renaming.value
  const nv = renameTo.value.trim()
  renaming.value = ''
  if (!p || !nv || nv === baseName(p) || nv === '.' || nv === '..') return
  try {
    await fsApi.rename(p, parentOf(p) + '/' + nv)
    setSelection([parentOf(p) + '/' + nv])
  } catch (e: any) { toast(e.message, { type: 'error' }) }
  refreshAll()
}

async function onUpload(e: Event) {
  const files = (e.target as HTMLInputElement).files
  if (!files?.length) return
  try { await fsApi.upload(path.value, [...files]) } catch (err: any) { toast(err.message, { type: 'error' }) }
  ;(e.target as HTMLInputElement).value = ''
  refreshAll()
}
async function onDrop(e: DragEvent) {
  e.preventDefault()
  dragOverDir.value = ''
  // 1. 内部拖拽（移动 / Alt 复制）
  const raw = e.dataTransfer?.getData('application/x-openos-paths')
  if (raw) {
    try {
      const paths = JSON.parse(raw) as string[]
      const copy = e.altKey || e.dataTransfer?.dropEffect === 'copy'
      transferTo(path.value, paths, !!copy)
    } catch { /* 忽略 */ }
    return
  }
  // 2. 操作系统文件 → 上传
  const files = [...(e.dataTransfer?.files || [])]
  if (!files.length) return
  try { await fsApi.upload(path.value, files) } catch (err: any) { toast(err.message, { type: 'error' }) }
  refreshAll()
}

// ---- 拖拽（移动到文件夹 / 侧栏目录 / 废纸篓） ----
const dragOverDir = ref('')
function onDragStart(e: DragEvent, f: VFile) {
  if (!hasSel(f.path)) setSelection([f.path])
  const paths = hasSel(f.path) ? selPaths.value : [f.path]
  e.dataTransfer?.setData('application/x-openos-paths', JSON.stringify(paths))
  if (e.dataTransfer) e.dataTransfer.effectAllowed = 'copyMove'
}
function onItemDragOver(e: DragEvent, f: VFile) {
  if (!f.isDir) return
  e.preventDefault()
  e.stopPropagation()
  if (e.dataTransfer) e.dataTransfer.dropEffect = e.altKey ? 'copy' : 'move'
  dragOverDir.value = f.path
}
function onItemDragLeave(f: VFile) { if (dragOverDir.value === f.path) dragOverDir.value = '' }
function onItemDrop(e: DragEvent, f: VFile) {
  if (!f.isDir) return
  e.preventDefault()
  e.stopPropagation()
  dragOverDir.value = ''
  const raw = e.dataTransfer?.getData('application/x-openos-paths')
  if (!raw) return
  try { transferTo(f.path, JSON.parse(raw), e.altKey) } catch { /* 忽略 */ }
}
function onSideDragOver(e: DragEvent, dir: string) {
  e.preventDefault()
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
  dragOverDir.value = dir
}
function onSideDrop(e: DragEvent, dir: string) {
  dragOverDir.value = ''
  const raw = e.dataTransfer?.getData('application/x-openos-paths')
  if (!raw) return
  e.preventDefault()
  try {
    const paths = JSON.parse(raw) as string[]
    if (dir === trashPath) moveToTrash(paths)
    else transferTo(dir, paths, false)
  } catch { /* 忽略 */ }
}

// ---- 键盘导航（Finder：方向键移动、⏎重命名、⌘O 打开、空格快速预览、⌫ 移到废纸篓） ----
function navList(): VFile[] {
  if (view.value === 'column') {
    const lastCol = columnPaths.value[columnPaths.value.length - 1]
    return colShown(lastCol || path.value)
  }
  return shown.value
}
function iconCols(): number {
  const w = contentEl.value?.clientWidth || 900
  return Math.max(1, Math.floor((w - 28) / 102))
}
function scrollSelIntoView() {
  nextTick(() => {
    document.getElementById('f-' + encodeURIComponent(primary.value))?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  })
}
function onKeydown(e: KeyboardEvent) {
  if (renaming.value || creating.value) return
  const meta = e.metaKey || e.ctrlKey
  if (meta && e.key.toLowerCase() === 'a') { e.preventDefault(); setSelection(shown.value.map((f) => f.path)); return }
  if (meta && e.key.toLowerCase() === 'c') { e.preventDefault(); doCopy(); return }
  if (meta && e.key.toLowerCase() === 'x') { e.preventDefault(); doCut(); return }
  if (meta && e.key.toLowerCase() === 'v') { e.preventDefault(); doPaste(); return }
  if (meta && e.key.toLowerCase() === 'd') { e.preventDefault(); duplicateSelection(); return }
  if (meta && e.key.toLowerCase() === 'o') { e.preventDefault(); openSelection(); return }
  if (e.key === 'Escape') { setSelection([]); return }
  if (e.key === 'Enter') {
    e.preventDefault()
    if (pickerReq) { if (canPick.value) confirmPick(); return } // 选择模式：回车=确认
    if (selPaths.value.length === 1) startRename(primary.value)
    else openSelection()
    return
  }
  if (e.key === ' ') { e.preventDefault(); if (selectedFile.value) quickLook(selectedFile.value); return }
  if (e.key === 'Delete' || e.key === 'Backspace') {
    e.preventDefault()
    if (inTrash.value) { confirmDelete.value = true; return }
    moveToTrash(selPaths.value)
    return
  }
  const list = navList()
  if (!list.length) return
  let i = list.findIndex((f) => f.path === primary.value)
  const cols = view.value === 'icon' ? iconCols() : 1
  let d = 0
  if (e.key === 'ArrowRight') d = 1
  else if (e.key === 'ArrowLeft') d = -1
  else if (e.key === 'ArrowDown') d = cols
  else if (e.key === 'ArrowUp') d = -cols
  else return
  e.preventDefault()
  if (i === -1) i = 0
  const j = Math.max(0, Math.min(list.length - 1, i + d))
  const target = list[j]
  if (e.shiftKey && anchorPath.value) {
    const a = list.findIndex((f) => f.path === anchorPath.value)
    if (a !== -1) {
      const [lo, hi] = a < j ? [a, j] : [j, a]
      const s = new Set(selSet.value)
      for (let k = lo; k <= hi; k++) s.add(list[k].path)
      selSet.value = s
    }
  } else {
    setSelection([target.path])
  }
  scrollSelIntoView()
  // 方向键到目录时在分栏视图顺带展开
  if (view.value === 'column' && target.isDir) enter(target.path)
}

// ---- 右键菜单（显示权归全局菜单单例 ui/menu.ts：点外部/Esc/滚动统一关闭，
//      多窗口/多菜单互斥；这里只保留"空白处/项目"的内容状态与视口夹取）----
// owner 必须带窗口实例 id：多个文件窗口并存时，owner 不同才能互斥顶替；
// 右键坐标走全局 menuPos，watch 需同时盯它（菜单开着时再次右键=重定位）。
const ctx = ref<'blank' | 'item' | null>(null)
const ctxMenu = useMenu(`files:${props.win.id}`)
const ctxOpen = computed(() => ctxMenu.isOpen && !!ctx.value)
const ctxPos = ref({ x: 0, y: 0 })
const ctxEl = ref<HTMLElement>()
function openCtx(e: MouseEvent, kind: 'blank' | 'item', f?: VFile) {
  // 输入框内右键保留系统菜单（原位重命名/搜索框）
  if ((e.target as HTMLElement).closest('input, textarea, [contenteditable="true"]')) return
  e.preventDefault()
  e.stopPropagation()
  if (kind === 'item' && f && !hasSel(f.path)) setSelection([f.path])
  if (kind === 'blank' && selPaths.value.length > 1) { /* 空白处右键保留多选，供批量粘贴 */ }
  ctx.value = kind
  ctxMenu.open(e.clientX, e.clientY)
}
// 菜单关闭即清空内容；打开/重定位时按全局坐标渲染并夹在视口内（避免右下角弹出越界）
watch([() => ctxMenu.isOpen, menuPos], ([open]) => {
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

// 显示简介
const infoFile = ref<VFile | null>(null)

// 工具栏下拉（排序 / 更多操作）：同样接入全局菜单单例，owner 带窗口 id 保证多窗口互斥
const sortMenu = useMenu(`files:${props.win.id}:sort`)
const moreMenu = useMenu(`files:${props.win.id}:more`)
function setSort(k: string) {
  if (sortBy.value === k) sortDir.value = sortDir.value === 1 ? -1 : 1
  else { sortBy.value = k; sortDir.value = 1 }
}

const usagePct = computed(() => Math.min(100, Math.round((usage.value.used / usage.value.quota) * 100)))
const availText = computed(() => fmtSize(Math.max(0, usage.value.quota - usage.value.used)))

// 打开方式候选（含应用名）
const openWithList = computed(() => {
  const p = primary.value
  if (!p || inTrash.value) return []
  return openWithApps(p).map((id) => ({ id, name: apps.byId(id)?.name || id }))
})

function openWithApp(id: string) {
  ctxMenu.close()
  if (primary.value) openPath(primary.value, id)
}

// —— 画廊视图 ——
watch(view, (v) => {
  if (v === 'gallery' && !selPaths.value.length && shown.value.length) {
    const f = shown.value.find((x) => !x.isDir) || shown.value[0]
    setSelection([f.path])
  }
})
function stepSel(d: number) {
  const list = shown.value
  if (!list.length) return
  let i = list.findIndex((f) => f.path === primary.value)
  i = i === -1 ? 0 : Math.min(list.length - 1, Math.max(0, i + d))
  setSelection([list[i].path])
  nextTick(() => document.getElementById('f-' + encodeURIComponent(list[i].path))?.scrollIntoView({ block: 'nearest', inline: 'center', behavior: 'smooth' }))
}
watch([primary, view], () => {
  if (view.value !== 'gallery' && view.value !== 'column') return
  const list = shown.value
  const i = list.findIndex((f) => f.path === primary.value)
  if (i === -1) {
    const f = selectedFile.value
    if (f && isImage(f)) ensureBlob(f.path)
    return
  }
  for (const f of list.slice(Math.max(0, i - 8), i + 9)) if (isImage(f)) ensureBlob(f.path)
})

// —— 分栏视图：路径链就绪 + 新列滚到最右 ——
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
    <!-- 工具栏（融合窗体标题行）：左侧红绿灯区 → 导航 → 标题 → 视图/排序/搜索/操作 -->
    <div class="os-tb toolbar" data-os-drag>
      <div class="nav-btns">
        <button class="os-tb-btn" :disabled="!canBack" title="后退" @click="goBack">
          <svg viewBox="0 0 16 16"><path d="M9.5 3.5 L5 8 l4.5 4.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
        </button>
        <button class="os-tb-btn" :disabled="!canForward" title="前进" @click="goForward">
          <svg viewBox="0 0 16 16"><path d="M6.5 3.5 L11 8 l-4.5 4.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
        </button>
      </div>
      <div class="tb-title" :title="path">{{ selPaths.length > 1 ? `${curTitle}（已选 ${selPaths.length} 项）` : curTitle }}</div>
      <span class="grow" />

      <div class="os-seg">
        <button class="os-seg-btn" :class="{ on: view === 'icon' }" title="图标视图" @click="view = 'icon'">
          <svg viewBox="0 0 16 16"><rect x="2" y="2" width="5" height="5" rx="1.2" fill="currentColor" /><rect x="9" y="2" width="5" height="5" rx="1.2" fill="currentColor" /><rect x="2" y="9" width="5" height="5" rx="1.2" fill="currentColor" /><rect x="9" y="9" width="5" height="5" rx="1.2" fill="currentColor" /></svg>
        </button>
        <button class="os-seg-btn" :class="{ on: view === 'list' }" title="列表视图" @click="view = 'list'">
          <svg viewBox="0 0 16 16"><rect x="2" y="2.6" width="12" height="1.8" rx="0.9" fill="currentColor" /><rect x="2" y="7.1" width="12" height="1.8" rx="0.9" fill="currentColor" /><rect x="2" y="11.6" width="12" height="1.8" rx="0.9" fill="currentColor" /></svg>
        </button>
        <button class="os-seg-btn" :class="{ on: view === 'column' }" title="分栏视图" @click="view = 'column'">
          <svg viewBox="0 0 16 16"><rect x="1.5" y="2" width="13" height="12" rx="1.6" fill="none" stroke="currentColor" stroke-width="1.4" /><line x1="5.8" y1="2" x2="5.8" y2="14" stroke="currentColor" stroke-width="1.1" /><line x1="10.2" y1="2" x2="10.2" y2="14" stroke="currentColor" stroke-width="1.1" /></svg>
        </button>
        <button class="os-seg-btn" :class="{ on: view === 'gallery' }" title="画廊视图" @click="view = 'gallery'">
          <svg viewBox="0 0 16 16"><rect x="1.5" y="2.5" width="13" height="11" rx="1.6" fill="none" stroke="currentColor" stroke-width="1.4" /><circle cx="5.2" cy="6" r="1.2" fill="currentColor" /><path d="M3.5 12 L7 8.5 L9.5 11 L11.5 9 L14 11.5" fill="none" stroke="currentColor" stroke-width="1.2" /></svg>
        </button>
      </div>

      <!-- 排序方式 -->
      <div class="tb-anchor">
        <button class="os-tb-btn" data-os-menu-anchor title="排序方式" v-html="icons.sort" @click="sortMenu.toggle()" />
        <div v-if="sortMenu.isOpen" class="os-menu os-menu-root sort-menu">
          <div class="grp">排序方式</div>
          <button :class="{ check: sortBy === 'name' }" @click="setSort('name'); sortMenu.close()">名称<span v-if="sortBy === 'name'" class="hint">{{ sortDir === 1 ? '↑' : '↓' }}</span></button>
          <button :class="{ check: sortBy === 'size' }" @click="setSort('size'); sortMenu.close()">大小<span v-if="sortBy === 'size'" class="hint">{{ sortDir === 1 ? '↑' : '↓' }}</span></button>
          <button :class="{ check: sortBy === 'time' }" @click="setSort('time'); sortMenu.close()">修改时间<span v-if="sortBy === 'time'" class="hint">{{ sortDir === 1 ? '↑' : '↓' }}</span></button>
        </div>
      </div>

      <div class="search-wrap">
        <svg viewBox="0 0 16 16"><circle cx="6.8" cy="6.8" r="4.6" fill="none" stroke="currentColor" stroke-width="1.5" /><line x1="10.4" y1="10.4" x2="14.2" y2="14.2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
        <input v-model="query" class="search" placeholder="搜索" />
      </div>

      <!-- 更多操作 -->
      <div class="tb-anchor">
        <button class="os-tb-btn" data-os-menu-anchor title="更多操作" v-html="icons.more" @click="moreMenu.toggle()" />
        <div v-if="moreMenu.isOpen" class="os-menu os-menu-root more-menu">
          <button @click="moreMenu.close(); newFile('md')">新建 Markdown 文稿</button>
          <button @click="moreMenu.close(); newFile('csv')">新建 CSV 表格</button>
          <button @click="moreMenu.close(); newFile('html')">新建 HTML 幻灯片</button>
          <button @click="moreMenu.close(); creating = 'dir'; newName = ''">新建文件夹</button>
          <div class="sep" />
          <label class="up">上传文件…<input type="file" multiple hidden @change="moreMenu.close(); onUpload($event)" /></label>
          <button :disabled="!canPaste()" @click="moreMenu.close(); doPaste()">粘贴项目</button>
          <div class="sep" />
          <button :disabled="!selPaths.length" @click="moreMenu.close(); doDownload()">下载</button>
          <button :disabled="selPaths.length !== 1" @click="moreMenu.close(); startRename(primary)">重新命名</button>
          <button :disabled="!selPaths.length" @click="moreMenu.close(); deleteSelection()">{{ inTrash ? '立即删除…' : '移到废纸篓' }}</button>
          <template v-if="path === trashPath">
            <div class="sep" />
            <button :disabled="!trashFull" class="danger" @click="moreMenu.close(); confirmEmpty = true">清倒废纸篓…</button>
          </template>
        </div>
      </div>
    </div>

    <div class="body">
      <!-- 侧栏：收藏 / 位置 / 废纸篓 -->
      <aside class="sidebar">
        <div class="side-head">收藏</div>
        <button class="side-item" :class="{ active: isActive(home), drop: dragOverDir === home }" @click="enter(home)"
          @dragover="onSideDragOver($event, home)" @dragleave="dragOverDir = ''" @drop="onSideDrop($event, home)">
          <span class="ic">🏠</span><span class="lb">主页</span>
        </button>
        <button v-for="d in favDirs" :key="d.path" class="side-item" :class="{ active: isActive(d.path), drop: dragOverDir === d.path }"
          @click="enter(d.path)" @dragover="onSideDragOver($event, d.path)" @dragleave="dragOverDir = ''" @drop="onSideDrop($event, d.path)">
          <span class="ic">{{ sideIcon(baseName(d.path)) }}</span><span class="lb">{{ baseName(d.path) }}</span>
        </button>
        <template v-if="placeDirs.length">
          <div class="side-head">位置</div>
          <button v-for="d in placeDirs" :key="d.path" class="side-item" :class="{ active: isActive(d.path), drop: dragOverDir === d.path }"
            @click="enter(d.path)" @dragover="onSideDragOver($event, d.path)" @dragleave="dragOverDir = ''" @drop="onSideDrop($event, d.path)">
            <span class="ic">📁</span><span class="lb">{{ baseName(d.path) }}</span>
          </button>
        </template>
        <div class="side-head" />
        <button class="side-item trash" :class="{ active: isActive(trashPath), drop: dragOverDir === trashPath }"
          @click="enter(trashPath)" @dragover="onSideDragOver($event, trashPath)" @dragleave="dragOverDir = ''" @drop="onSideDrop($event, trashPath)">
          <span class="ic">{{ trashFull ? '🗑' : '🗑️' }}</span><span class="lb">废纸篓</span>
        </button>
      </aside>

      <main ref="contentEl" class="content" :class="contentClass" tabindex="0" @keydown="onKeydown"
        @pointerdown.self="setSelection([])" @contextmenu="openCtx($event, 'blank')">
        <div v-if="loading" class="state">加载中…</div>
        <OsEmpty v-else-if="view !== 'column' && !shown.length" :text="query ? '没有匹配的文件' : (path === trashPath ? '废纸篓为空' : '此文件夹为空')" />

        <!-- 图标视图 -->
        <template v-else-if="view === 'icon'">
          <div
            v-for="f in shown" :id="'f-' + encodeURIComponent(f.path)" :key="f.path"
            class="item" :class="{ sel: hasSel(f.path), drop: dragOverDir === f.path }" draggable="true"
            @click="onItemClick($event, f)" @dblclick="openFile(f)"
            @dragstart="onDragStart($event, f)"
            @dragover="onItemDragOver($event, f)" @dragleave="onItemDragLeave(f)" @drop="onItemDrop($event, f)"
            @contextmenu="openCtx($event, 'item', f)"
          >
            <div class="ic">
              <img v-if="!f.isDir && isImage(f) && blobUrl(f.path)" class="thumb" :src="blobUrl(f.path)" alt="">
              <template v-else>{{ f.isDir ? '📁' : fileEmoji(f.path) }}</template>
            </div>
            <input v-if="renaming === f.path" ref="renameEl" v-model="renameTo" class="rn-input os-flat"
              @keydown.enter.prevent="commitRename" @keydown.esc.stop.prevent="renaming = ''" @blur="commitRename" @click.stop />
            <div v-else class="name" :title="baseName(f.path)">{{ baseName(f.path) }}</div>
          </div>
        </template>

        <!-- 列表视图 -->
        <table v-else-if="view === 'list'" class="list-table" @contextmenu="openCtx($event, 'blank')">
          <thead>
            <tr>
              <th class="sortable" @click="setSort('name')">名称<span class="arr" v-if="sortBy === 'name'">{{ sortDir === 1 ? '↑' : '↓' }}</span></th>
              <th class="sortable" @click="setSort('time')">修改时间<span class="arr" v-if="sortBy === 'time'">{{ sortDir === 1 ? '↑' : '↓' }}</span></th>
              <th class="sortable" @click="setSort('size')">大小<span class="arr" v-if="sortBy === 'size'">{{ sortDir === 1 ? '↑' : '↓' }}</span></th>
              <th>种类</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="f in shown" :id="'f-' + encodeURIComponent(f.path)" :key="f.path"
              :class="{ sel: hasSel(f.path), drop: dragOverDir === f.path }" draggable="true"
              @click="onItemClick($event, f)" @dblclick="openFile(f)"
              @dragstart="onDragStart($event, f)"
              @dragover="onItemDragOver($event, f)" @dragleave="onItemDragLeave(f)" @drop="onItemDrop($event, f)"
              @contextmenu="openCtx($event, 'item', f)"
            >
              <td class="tn">
                <img v-if="!f.isDir && isImage(f) && blobUrl(f.path)" class="lt" :src="blobUrl(f.path)" alt="">
                <template v-else>{{ f.isDir ? '📁' : fileEmoji(f.path) }}</template>
                <input v-if="renaming === f.path" ref="renameEl" v-model="renameTo" class="rn-input os-flat"
                  @keydown.enter.prevent="commitRename" @keydown.esc.stop.prevent="renaming = ''" @blur="commitRename" @click.stop />
                <template v-else>{{ baseName(f.path) }}</template>
              </td>
              <td class="dim">{{ f.updatedAt }}</td>
              <td>{{ f.isDir ? '—' : fmtSize(f.size) }}</td>
              <td class="dim">{{ kindOf(f) }}</td>
            </tr>
          </tbody>
        </table>

        <!-- 分栏视图 -->
        <div v-else-if="view === 'column'" ref="colsEl" class="columns" @contextmenu="openCtx($event, 'blank')">
          <section v-for="(colPath, i) in columnPaths" :key="colPath" class="col">
            <template v-if="colCache.has(colPath)">
              <div
                v-for="f in colShown(colPath)" :id="'f-' + encodeURIComponent(f.path)" :key="f.path"
                class="col-item" :class="{ sel: columnPaths[i + 1] === f.path || hasSel(f.path), drop: dragOverDir === f.path }" draggable="true"
                @click="f.isDir ? enter(f.path) : onItemClick($event, f)" @dblclick="openFile(f)"
                @dragstart="onDragStart($event, f)"
                @dragover="onItemDragOver($event, f)" @dragleave="onItemDragLeave(f)" @drop="onItemDrop($event, f)"
                @contextmenu="openCtx($event, 'item', f)"
              >
                <img v-if="!f.isDir && isImage(f) && blobUrl(f.path)" class="lt" :src="blobUrl(f.path)" alt="">
                <span v-else class="ci">{{ f.isDir ? '📁' : fileEmoji(f.path) }}</span>
                <input v-if="renaming === f.path" ref="renameEl" v-model="renameTo" class="rn-input os-flat"
                  @keydown.enter.prevent="commitRename" @keydown.esc.stop.prevent="renaming = ''" @blur="commitRename" @click.stop />
                <span v-else class="cn">{{ baseName(f.path) }}</span>
                <span class="ct">{{ f.isDir ? '' : fmtSize(f.size) }}</span>
                <span v-if="f.isDir" class="chev">›</span>
              </div>
              <div v-if="!colShown(colPath).length" class="col-empty">空文件夹</div>
            </template>
            <div v-else class="col-loading">…</div>
          </section>
          <section v-if="selectedFile && !selectedFile.isDir" class="col col-preview">
            <img v-if="isImage(selectedFile) && blobUrl(selectedFile.path)" class="pv-img" :src="blobUrl(selectedFile.path)" alt="">
            <div v-else class="pv-emoji">{{ fileEmoji(selectedFile.path) }}</div>
            <div class="pv-name">{{ baseName(selectedFile.path) }}</div>
            <div class="pv-meta">{{ kindOf(selectedFile) }}<br>{{ fmtSize(selectedFile.size) }}<br>{{ selectedFile.updatedAt }}</div>
          </section>
        </div>

        <!-- 画廊视图 -->
        <div v-else-if="view === 'gallery'" class="gallery" tabindex="0" @keydown.left.prevent="stepSel(-1)" @keydown.right.prevent="stepSel(1)"
          @contextmenu="openCtx($event, 'blank')">
          <div v-if="selectedFile" class="g-main" @contextmenu="openCtx($event, 'item', selectedFile)">
            <img v-if="isImage(selectedFile) && blobUrl(selectedFile.path)" :src="blobUrl(selectedFile.path)" :alt="baseName(selectedFile.path)" @dblclick="openFile(selectedFile)">
            <div v-else class="g-emoji" @dblclick="openFile(selectedFile)">{{ selectedFile.isDir ? '📁' : fileEmoji(selectedFile.path) }}</div>
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
              v-for="f in shown" :id="'f-' + encodeURIComponent(f.path)" :key="f.path"
              class="thumb" :class="{ sel: hasSel(f.path) }"
              @click="setSelection([f.path])" @dblclick="openFile(f)"
              @contextmenu="openCtx($event, 'item', f)"
            >
              <img v-if="isImage(f) && blobUrl(f.path)" :src="blobUrl(f.path)" alt="">
              <span v-else>{{ f.isDir ? '📁' : fileEmoji(f.path) }}</span>
            </div>
          </div>
        </div>
      </main>
    </div>

    <!-- 底部：选择模式=打开/存储栏；普通模式=路径栏（可点面包屑）+ 项目数与可用空间 -->
    <footer v-if="pickerReq" class="status pick-foot">
      <template v-if="pickerReq.mode === 'save'">
        <label class="pk-lb">存储为：</label>
        <OsInput v-model="pickName" placeholder="文件名" class="pk-name" @keydown.enter.prevent="confirmPick" />
        <span v-if="pickExists" class="pk-warn">{{ pickOverwrite ? '再点一次「替换」覆盖同名文件' : '同名文件已存在' }}</span>
      </template>
      <span v-else class="pk-sel">{{ pickSel ? baseName(pickSel.path) : '' }}</span>
      <span class="grow" />
      <OsButton size="sm" @click="cancelPick">取消</OsButton>
      <OsButton size="sm" variant="primary" :disabled="!canPick" @click="confirmPick">
        {{ pickerReq.mode === 'open' ? '打开' : pickExists && !pickOverwrite ? '替换' : '存储' }}
      </OsButton>
    </footer>
    <footer v-else class="status">
      <nav class="pathbar">
        <template v-for="(c, i) in crumbs" :key="c.path">
          <button :class="{ cur: i === crumbs.length - 1 }" @click="enter(c.path)">{{ c.label }}</button>
          <span v-if="i < crumbs.length - 1" class="psep">›</span>
        </template>
      </nav>
      <span class="grow" />
      <span>{{ shown.length }} 个项目 · 可用 {{ availText }}</span>
    </footer>

    <!-- 右键菜单：显示权归全局菜单单例，根节点标记 os-menu-root -->
    <div v-if="ctxOpen" ref="ctxEl" class="os-menu os-menu-root" :style="{ left: ctxPos.x + 'px', top: ctxPos.y + 'px' }"
      @contextmenu.prevent.stop>
      <!-- 项目菜单 -->
      <template v-if="ctx === 'item'">
        <div class="grp">{{ selPaths.length > 1 ? `${selPaths.length} 个项目` : baseName(primary) }}</div>
        <button @click="ctxMenu.close(); openSelection()">打开</button>
        <div v-if="!inTrash && openWithList.length > 1" class="subwrap">
          <button>打开方式<span class="hint">▸</span></button>
          <div class="os-menu sub">
            <button v-for="a in openWithList" :key="a.id" @click="openWithApp(a.id)">{{ a.name }}</button>
          </div>
        </div>
        <button v-if="selPaths.length === 1" @click="ctxMenu.close(); quickLook(selectedFile!)">快速查看</button>
        <button @click="ctxMenu.close(); infoFile = selectedFile">显示简介</button>
        <div class="sep" />
        <button :disabled="inTrash" @click="ctxMenu.close(); doCopy()">拷贝<span class="hint">⌘C</span></button>
        <button :disabled="inTrash" @click="ctxMenu.close(); doCut(); toast('已剪切，在目标位置粘贴')">剪切<span class="hint">⌘X</span></button>
        <button :disabled="inTrash" @click="ctxMenu.close(); duplicateSelection()">复制副本<span class="hint">⌘D</span></button>
        <div class="sep" />
        <button v-if="selPaths.length === 1" @click="ctxMenu.close(); startRename(primary)">重新命名<span class="hint">⏎</span></button>
        <button @click="ctxMenu.close(); doDownload()">下载</button>
        <button class="danger" @click="ctxMenu.close(); deleteSelection()">{{ inTrash ? '立即删除…' : '移到废纸篓' }}</button>
      </template>

      <!-- 空白处菜单 -->
      <template v-else>
        <button @click="ctxMenu.close(); newFile('md')">新建 Markdown 文稿</button>
        <button @click="ctxMenu.close(); newFile('csv')">新建 CSV 表格</button>
        <button @click="ctxMenu.close(); newFile('html')">新建 HTML 幻灯片</button>
        <button @click="ctxMenu.close(); creating = 'dir'; newName = ''">新建文件夹<span class="hint">⇧⌘N</span></button>
        <div class="sep" />
        <label class="up">上传文件…<input type="file" multiple hidden @change="ctxMenu.close(); onUpload($event)" /></label>
        <button :disabled="!canPaste()" @click="ctxMenu.close(); doPaste()">粘贴项目<span class="hint">⌘V</span></button>
        <div class="sep" />
        <div class="subwrap">
          <button>排序方式按{{ { name: '名称', size: '大小', time: '修改时间' }[sortBy] || '名称' }}<span class="hint">▸</span></button>
          <div class="os-menu sub">
            <button @click="setSort('name'); ctxMenu.close()">名称</button>
            <button @click="setSort('size'); ctxMenu.close()">大小</button>
            <button @click="setSort('time'); ctxMenu.close()">修改时间</button>
          </div>
        </div>
        <button v-if="path === trashPath" class="danger" :disabled="!trashFull" @click="ctxMenu.close(); confirmEmpty = true">清倒废纸篓…</button>
      </template>
    </div>

    <OsModal v-if="creating" title="新建文件夹" okText="创建" @ok="confirmCreate" @cancel="creating = null">
      <OsInput v-model="newName" placeholder="文件夹名称" @keydown.enter="confirmCreate" />
    </OsModal>

    <OsModal v-if="confirmEmpty" title="清倒废纸篓" okText="清倒废纸篓" @ok="emptyTrash" @cancel="confirmEmpty = false">
      <p>将永久删除废纸篓中的全部项目。此操作不能撤销。</p>
    </OsModal>

    <OsModal v-if="confirmDelete" title="立即删除" okText="删除" @ok="deleteSelection" @cancel="confirmDelete = false">
      <p>将永久删除选中的 {{ selPaths.length }} 个项目。此操作不能撤销。</p>
    </OsModal>

    <!-- 显示简介 -->
    <OsModal v-if="infoFile" :title="infoFile.isDir ? '文件夹简介' : '文件简介'" okText="好" :cancelText="''" @ok="infoFile = null" @cancel="infoFile = null">
      <dl class="info">
        <div><dt>名称</dt><dd>{{ baseName(infoFile.path) }}</dd></div>
        <div><dt>种类</dt><dd>{{ kindOf(infoFile) }}</dd></div>
        <div><dt>位置</dt><dd>{{ infoFile.path.replace(/^\/home\/[^/]+\//, '~/') }}</dd></div>
        <div v-if="!infoFile.isDir"><dt>大小</dt><dd>{{ fmtSize(infoFile.size) }}</dd></div>
        <div><dt>修改时间</dt><dd>{{ infoFile.updatedAt }}</dd></div>
      </dl>
    </OsModal>

    <!-- 快速预览（空格） -->
    <div v-if="ql" class="ql" tabindex="0" @keydown="onQlKey" @pointerdown.self="closeQl">
      <header>
        <span class="ql-name">{{ fileEmoji(ql.f.path) }} {{ baseName(ql.f.path) }}</span>
        <button class="ql-close" title="关闭（空格）" @click="closeQl">✕</button>
      </header>
      <div class="ql-body">
        <div v-if="ql.loading" class="ql-state">加载中…</div>
        <img v-else-if="isImage(ql.f) && ql.url" :src="ql.url" :alt="baseName(ql.f.path)">
        <iframe v-else-if="extOf(ql.f.path) === 'pdf' && ql.url" :src="ql.url" class="ql-frame" title="预览" />
        <iframe v-else-if="ql.html && (extOf(ql.f.path) === 'html' || extOf(ql.f.path) === 'htm')" :srcdoc="ql.html" sandbox="allow-scripts" class="ql-frame" title="预览" />
        <div v-else-if="ql.html" class="ql-md" v-html="ql.html" />
        <pre v-else-if="ql.text" class="ql-text">{{ ql.text }}</pre>
      </div>
    </div>
  </div>
</template>

<style scoped>
.files { width: 100%; display: flex; flex-direction: column; background: var(--os-window); }
.toolbar { gap: 8px; }
.nav-btns { display: flex; gap: 4px; }
.grow { flex: 1; }
.tb-title { font-size: 13.5px; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 38%; }
.tb-anchor { position: relative; display: flex; }
.sort-menu, .more-menu { position: absolute; top: 33px; right: 0; z-index: 51; min-width: 168px; }
.more-menu .up, .os-menu .up {
  display: flex; align-items: center; gap: 8px; padding: 5px 10px; border-radius: 6px;
  font-size: 13px; cursor: pointer; white-space: nowrap; color: var(--os-text);
}
.more-menu .up:hover, .os-menu .up:hover { background: var(--os-accent); color: #fff; }
.os-menu .subwrap { position: relative; display: flex; }
.os-menu .subwrap > button { flex: 1; }
.os-menu .subwrap .sub { position: absolute; left: calc(100% - 5px); top: -5px; display: none; min-width: 130px; }
.os-menu .subwrap:hover .sub { display: flex; }
/* 右键菜单是 fixed 定位（teleport 到窗口层），scoped 样式对子选择器有效 */
:deep(.os-menu .check::before) { content: '✓'; width: 14px; display: inline-block; }

.search-wrap { position: relative; display: flex; align-items: center; }
.search-wrap svg { position: absolute; left: 8px; width: 13px; height: 13px; color: var(--os-text-secondary); pointer-events: none; }
.search {
  width: 128px; height: 27px; padding: 0 9px 0 26px; border-radius: 8px; font-size: 12.5px;
  border: 1px solid var(--os-border); background: var(--os-input-bg); color: var(--os-text);
  transition: width .18s var(--os-ease-out), border-color .14s;
}
.search:focus { border-color: var(--os-accent); width: 168px; outline: none; }

/* 主体：侧栏 + 内容 */
.body { flex: 1; display: flex; min-height: 0; }
.sidebar {
  flex: none; width: 176px; overflow-y: auto; padding: 6px 8px 12px;
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
.side-item.drop { outline: 2px solid var(--os-accent); outline-offset: -2px; background: var(--os-selected); }
.side-item.trash { margin-top: 2px; }

.content { flex: 1; min-width: 0; overflow-y: auto; outline: none; }
.content.view-icon {
  padding: 14px; display: grid; gap: 6px; align-content: start;
  grid-template-columns: repeat(auto-fill, minmax(96px, 1fr));
}
.content.view-list { display: block; }
.content.view-column { display: flex; padding: 0; overflow-x: auto; overflow-y: hidden; }
.content.view-gallery { display: flex; flex-direction: column; padding: 0; overflow: hidden; }
/* 空目录/无匹配：flex 居中放空状态文案 */
.content.view-empty { display: flex; align-items: center; justify-content: center; }
.state { padding: 40px; text-align: center; color: var(--os-text-secondary); grid-column: 1 / -1; }

/* 图标视图 */
.item {
  display: flex; flex-direction: column; align-items: center; gap: 4px;
  padding: 10px 4px 8px; border-radius: 10px; cursor: default; text-align: center;
}
.item:hover { background: var(--os-hover); }
.item.sel { background: var(--os-selected); }
.item.drop { outline: 2px solid var(--os-accent); outline-offset: -2px; }
.item .ic { font-size: 40px; width: 48px; height: 48px; display: flex; align-items: center; justify-content: center; }
.item .ic .thumb { max-width: 48px; max-height: 48px; object-fit: cover; border-radius: 6px; box-shadow: var(--os-shadow-sm); }
.item .name {
  font-size: 12px; word-break: break-all; line-height: 1.3; border-radius: 4px; padding: 0 2px;
  max-height: 2.6em; overflow: hidden;
}
.item.sel .name { background: var(--os-accent); color: #fff; }
.rn-input {
  font-size: 12px; width: 92px; border: 1.5px solid var(--os-accent); border-radius: 5px;
  background: var(--os-panel-solid); color: var(--os-text); padding: 1px 4px; text-align: center;
}

/* 列表视图 */
.list-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.list-table th {
  text-align: left; padding: 7px 12px; font-size: 12px; color: var(--os-text-secondary);
  border-bottom: 1px solid var(--os-border); position: sticky; top: 0; background: var(--os-sidebar);
  user-select: none; white-space: nowrap; z-index: 2;
}
.list-table th.sortable { cursor: pointer; }
.list-table th.sortable:hover { color: var(--os-text); }
.list-table th .arr { margin-left: 3px; }
.list-table td { padding: 5px 12px; }
.list-table tbody tr { border-bottom: 1px solid var(--os-border); }
.list-table tr:hover { background: var(--os-hover); }
.list-table tr.sel { background: var(--os-selected); }
.list-table tr.drop { outline: 2px solid var(--os-accent); outline-offset: -2px; }
.list-table .tn { max-width: 360px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; display: flex; align-items: center; gap: 7px; }
.list-table .lt { width: 20px; height: 20px; object-fit: cover; border-radius: 4px; flex: none; }
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
.col-item .lt { width: 18px; height: 18px; object-fit: cover; border-radius: 3px; flex: none; }
.col-item .ci { flex: none; }
.col-item .cn { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.col-item .ct { flex: none; font-size: 11px; color: var(--os-text-secondary); }
.col-item .chev { flex: none; color: var(--os-text-secondary); }
.col-item:hover { background: var(--os-hover); }
.col-item.sel { background: var(--os-selected); }
.col-item.drop { outline: 2px solid var(--os-accent); outline-offset: -2px; }
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
.g-main { flex: 1; min-height: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px; padding: 20px 56px; }
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

/* 底部路径栏（Finder 显示路径栏） */
.status {
  flex: none; padding: 5px 12px; font-size: 11.5px; color: var(--os-text-secondary);
  border-top: 1px solid var(--os-border); background: var(--os-sidebar);
  display: flex; gap: 12px; align-items: center; min-width: 0;
}
.pathbar { display: flex; align-items: center; gap: 1px; overflow: hidden; min-width: 0; }
.pathbar button { border: none; background: none; color: var(--os-text-secondary); font-size: 11.5px; cursor: pointer; padding: 1px 5px; border-radius: 5px; white-space: nowrap; }
.pathbar button:hover { background: var(--os-hover); color: var(--os-text); }
.pathbar button.cur { color: var(--os-text); font-weight: 600; }
.psep { opacity: .5; }

/* 选择模式底部栏（打开/存储对话框形态） */
.pick-foot { gap: 8px; }
.pk-lb { font-size: 12.5px; color: var(--os-text-secondary); white-space: nowrap; }
.pk-name { width: 240px; }
.pk-sel { font-size: 12.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.pk-warn { font-size: 11.5px; color: #e5a00d; white-space: nowrap; }

/* 快速预览 */
.ql {
  position: absolute; inset: 0; z-index: 70; background: rgba(24, 24, 28, .62);
  backdrop-filter: blur(18px); -webkit-backdrop-filter: blur(18px);
  display: flex; flex-direction: column; animation: os-fade-in .14s; outline: none;
}
.ql header {
  display: flex; justify-content: space-between; align-items: center; padding: 12px 18px;
  color: #fff; font-size: 13.5px; font-weight: 600;
}
.ql-close {
  border: none; background: rgba(255, 255, 255, .16); color: #fff; border-radius: 50%;
  width: 26px; height: 26px; cursor: pointer;
}
.ql-close:hover { background: rgba(255, 255, 255, .28); }
.ql-body { flex: 1; min-height: 0; display: flex; align-items: center; justify-content: center; padding: 0 24px 24px; }
.ql-body img { max-width: 100%; max-height: 100%; object-fit: contain; border-radius: 8px; }
.ql-state { color: rgba(255, 255, 255, .7); }
.ql-frame { width: 100%; height: 100%; border: none; border-radius: 10px; background: #fff; }
.ql-md {
  max-width: 760px; max-height: 100%; overflow-y: auto; background: var(--os-panel-solid);
  color: var(--os-text); border-radius: 12px; padding: 40px 48px; font-size: 14px; line-height: 1.8;
}
.ql-md :deep(h1) { font-size: 1.7em; margin: .6em 0 .4em; }
.ql-md :deep(h2) { font-size: 1.4em; margin: .6em 0 .35em; }
.ql-md :deep(h3) { font-size: 1.2em; margin: .6em 0 .3em; }
.ql-md :deep(p) { margin: .45em 0; }
.ql-md :deep(pre) { background: var(--os-active); padding: 12px 14px; border-radius: 8px; font-family: var(--os-mono); font-size: 12.5px; overflow-x: auto; }
.ql-md :deep(code) { font-family: var(--os-mono); background: var(--os-active); border-radius: 4px; padding: 1px 5px; font-size: .92em; }
.ql-md :deep(pre code) { background: none; padding: 0; }
.ql-md :deep(blockquote) { border-left: 3px solid var(--os-accent); padding-left: 14px; color: var(--os-text-secondary); margin: .6em 0; }
.ql-md :deep(table) { border-collapse: collapse; margin: .6em 0; }
.ql-md :deep(th), .ql-md :deep(td) { border: 1px solid var(--os-border-strong); padding: 5px 12px; font-size: .92em; }
.ql-md :deep(a) { color: var(--os-accent); }
.ql-md :deep(img) { max-width: 100%; border-radius: 8px; }
.ql-table { border-collapse: collapse; background: var(--os-panel-solid); color: var(--os-text); border-radius: 12px; overflow: hidden; font-size: 13px; }
.ql-table td { border: 1px solid var(--os-border); padding: 5px 12px; max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ql-text {
  max-width: 760px; width: 100%; max-height: 100%; overflow: auto; background: var(--os-panel-solid);
  color: var(--os-text); border-radius: 12px; padding: 24px 28px; font-family: var(--os-mono);
  font-size: 12.5px; line-height: 1.7; white-space: pre-wrap; user-select: text;
}

/* 简介弹窗 */
.info { text-align: left; }
.info div { display: flex; gap: 12px; padding: 3px 0; }
.info dt { flex: none; width: 56px; color: var(--os-text-secondary); text-align: right; }
.info dd { word-break: break-all; }
</style>
