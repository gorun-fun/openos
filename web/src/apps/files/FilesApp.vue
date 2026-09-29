<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { fsApi, downloadFile, fetchRawBlob, type VFile } from '../../api/client'
import { bus } from '../../bus/bus'
import { OsButton, OsInput, OsSelect, OsEmpty, OsModal } from '../../ui'

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
const view = ref<'grid' | 'list'>('grid')
const usage = ref({ used: 0, quota: 100 * 1024 * 1024 })
const preview = ref<{ url: string; name: string } | null>(null)
const uploadInput = ref<HTMLInputElement>()

async function load() {
  loading.value = true
  try {
    const [list, u] = await Promise.all([fsApi.list(path.value), fsApi.usage()])
    entries.value = list
    usage.value = u
  } finally {
    loading.value = false
  }
}
onMounted(load)

// 其他窗口（文档/表格/终端）保存后自动刷新
const off = bus.on('fs:changed', () => load())
onBeforeUnmount(off)

const crumbs = computed(() => {
  const parts = path.value.split('/').filter(Boolean)
  let acc = ''
  return parts.map((p: string) => { acc += '/' + p; return { name: p, path: acc } })
})

const shown = computed(() => {
  let list = entries.value
  const q = query.value.trim().toLowerCase()
  if (q) list = list.filter((f) => f.path.toLowerCase().includes(q))
  const name = (f: VFile) => f.path.split('/').pop() || ''
  const cmp: Record<string, (a: VFile, b: VFile) => number> = {
    name: (a, b) => Number(b.isDir) - Number(a.isDir) || name(a).localeCompare(name(b), 'zh'),
    size: (a, b) => Number(b.isDir) - Number(a.isDir) || b.size - a.size,
    time: (a, b) => Number(b.isDir) - Number(a.isDir) || b.updatedAt.localeCompare(a.updatedAt),
  }
  return [...list].sort(cmp[sortBy.value] || cmp.name)
})

function enter(p: string) { path.value = p; selected.value = ''; query.value = ''; load() }

function extOf(p: string) {
  return (p.match(/\.([a-z0-9]+)$/i)?.[1] || '').toLowerCase()
}
const APP_FOR_EXT: Record<string, string> = {
  txt: 'docs', md: 'docs', html: 'docs', json: 'docs',
  csv: 'sheets', tsv: 'sheets', pdf: 'pdf',
}
const IMAGE_EXT = ['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'ico', 'bmp']

async function openFile(f: VFile) {
  if (f.isDir) { enter(f.path); return }
  const ext = extOf(f.path)
  if (IMAGE_EXT.includes(ext)) {
    try {
      const blob = await fetchRawBlob(f.path)
      if (preview.value) URL.revokeObjectURL(preview.value.url)
      preview.value = { url: URL.createObjectURL(blob), name: f.path.split('/').pop() || '' }
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
  if (!confirm(`删除 ${selected.value.split('/').pop()} ？`)) return
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

async function onDrop(e: DragEvent) {
  e.preventDefault()
  const files = [...(e.dataTransfer?.files || [])]
  if (!files.length) return
  try { await fsApi.upload(path.value, files) } catch (err: any) { alert(err.message) }
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
    case 'slides.json': return '📽️'
    default: return '📄'
  }
}

const usagePct = computed(() => Math.min(100, Math.round((usage.value.used / usage.value.quota) * 100)))

function closePreview() {
  if (preview.value) URL.revokeObjectURL(preview.value.url)
  preview.value = null
}
</script>

<template>
  <div class="files" @dragover.prevent @drop="onDrop">
    <div class="toolbar">
      <OsButton size="sm" @click="enter(home)">🏠 主页</OsButton>
      <OsButton size="sm" :disabled="path === home" @click="enter(path.split('/').slice(0, -1).join('/') || home)">⬆ 上级</OsButton>
      <nav class="crumbs">
        <span v-for="c in crumbs" :key="c.path" @click="enter(c.path)">{{ c.name }}</span>
      </nav>
      <OsInput v-model="query" placeholder="🔍 搜索当前目录" style="width: 150px" />
      <OsSelect v-model="sortBy" :options="[
        { value: 'name', label: '按名称' }, { value: 'size', label: '按大小' }, { value: 'time', label: '按时间' },
      ]" />
      <span class="grow" />
      <button class="view-toggle" :title="view === 'grid' ? '列表视图' : '图标视图'" @click="view = view === 'grid' ? 'list' : 'grid'">
        {{ view === 'grid' ? '☰' : '▦' }}
      </button>
      <label class="upload-btn">⬆ 上传<input ref="uploadInput" type="file" multiple hidden @change="onUpload" /></label>
      <OsButton size="sm" @click="creating = 'dir'; newName = ''">＋ 文件夹</OsButton>
      <OsButton size="sm" @click="creating = 'file'; newName = ''">＋ 文件</OsButton>
      <OsButton size="sm" :disabled="!selected" @click="doDownload">下载</OsButton>
      <OsButton size="sm" :disabled="!selected" @click="renaming = selected; renameTo = selected.split('/').pop() || ''">重命名</OsButton>
      <OsButton size="sm" variant="danger" :disabled="!selected" @click="doRemove">删除</OsButton>
    </div>

    <div class="content" :class="{ list: view === 'list' }">
      <div v-if="loading" class="state">加载中…</div>
      <OsEmpty v-else-if="!shown.length" :text="query ? '没有匹配的文件' : '此文件夹为空（可拖入文件上传）'" icon="📂" />
      <template v-else-if="view === 'grid'">
        <div
          v-for="f in shown" :key="f.path"
          class="item" :class="{ sel: selected === f.path }"
          @click="selected = f.path"
          @dblclick="openFile(f)"
        >
          <div class="ic">{{ fileIcon(f) }}</div>
          <div class="name">{{ f.path.split('/').pop() }}</div>
          <div class="meta">{{ f.isDir ? '文件夹' : fmtSize(f.size) }}</div>
        </div>
      </template>
      <table v-else class="list-table">
        <thead><tr><th>名称</th><th>大小</th><th>类型</th><th>修改时间</th></tr></thead>
        <tbody>
          <tr
            v-for="f in shown" :key="f.path" :class="{ sel: selected === f.path }"
            @click="selected = f.path" @dblclick="openFile(f)"
          >
            <td class="tn">{{ fileIcon(f) }} {{ f.path.split('/').pop() }}</td>
            <td>{{ f.isDir ? '—' : fmtSize(f.size) }}</td>
            <td>{{ f.isDir ? '文件夹' : (extOf(f.path) || '文件') }}</td>
            <td class="dim">{{ f.updatedAt }}</td>
          </tr>
        </tbody>
      </table>
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
      <img :src="preview.url" :alt="preview.name" />
    </div>
  </div>
</template>

<style scoped>
.files { width: 100%; display: flex; flex-direction: column; background: var(--os-window); }
.toolbar {
  display: flex; gap: 6px; align-items: center; padding: 8px 12px; flex-wrap: wrap;
  border-bottom: 1px solid var(--os-border); background: var(--os-sidebar);
}
.crumbs { display: flex; gap: 2px; font-size: 12.5px; color: var(--os-text-secondary); max-width: 260px; overflow: hidden; }
.crumbs span { cursor: pointer; padding: 2px 4px; border-radius: 4px; white-space: nowrap; }
.crumbs span:hover { background: var(--os-hover); color: var(--os-text); }
.crumbs span + span::before { content: '›'; margin-right: 4px; opacity: .5; }
.grow { flex: 1; }
.view-toggle {
  border: 1px solid var(--os-border-strong); background: var(--os-panel-solid); color: var(--os-text);
  border-radius: 7px; width: 30px; height: 28px; cursor: pointer; font-size: 14px;
}
.upload-btn {
  padding: 4px 12px; border-radius: 7px; background: var(--os-accent); color: #fff;
  font-size: 12.5px; cursor: pointer;
}
.content {
  flex: 1; overflow-y: auto; padding: 14px; display: grid; gap: 6px;
  grid-template-columns: repeat(auto-fill, minmax(92px, 1fr)); align-content: start;
}
.content.list { display: block; padding: 0; }
.state { padding: 40px; text-align: center; color: var(--os-text-secondary); grid-column: 1 / -1; }
.item {
  display: flex; flex-direction: column; align-items: center; gap: 4px;
  padding: 10px 4px; border-radius: 10px; cursor: default; text-align: center;
}
.item:hover { background: var(--os-hover); }
.item.sel { background: var(--os-selected); }
.ic { font-size: 34px; }
.name { font-size: 12px; word-break: break-all; line-height: 1.3; }
.meta { font-size: 10.5px; color: var(--os-text-secondary); }
.list-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.list-table th {
  text-align: left; padding: 8px 12px; font-size: 12px; color: var(--os-text-secondary);
  border-bottom: 1px solid var(--os-border); position: sticky; top: 0; background: var(--os-sidebar);
}
.list-table td { padding: 7px 12px; border-bottom: 1px solid var(--os-border); }
.list-table tr:hover { background: var(--os-hover); }
.list-table tr.sel { background: var(--os-selected); }
.tn { max-width: 320px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dim { color: var(--os-text-secondary); font-size: 12px; }
.status {
  flex: none; padding: 5px 12px; font-size: 11.5px; color: var(--os-text-secondary);
  border-top: 1px solid var(--os-border); background: var(--os-sidebar);
  display: flex; gap: 12px; align-items: center;
}
.storage-bar { width: 80px; height: 4px; border-radius: 2px; background: var(--os-active); overflow: hidden; }
.storage-bar i { display: block; height: 100%; background: var(--os-accent); }
.storage-bar i.warn { background: #ff9f0a; }
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
