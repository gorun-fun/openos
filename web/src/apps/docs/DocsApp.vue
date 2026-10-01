<script setup lang="ts">
// 文档应用（Markdown 优先）：编辑 / 分栏 / 预览三态，读写 VFS 文件（默认 .md）。
// 打开…/存储/另存为走 fileDialog（复用文件管理窗口选择）；已有文件 1.5s 防抖自动保存。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { fsApi } from '../../api/client'
import { bus } from '../../bus/bus'
import { fileDialog, icons, toast } from '../../ui'
import { renderMarkdown } from '../../lib/md'

const props = defineProps<{ win: WinState }>()
const windows = useWindows()
const session = useSession()
const docsDir = `/home/${session.user!.username}/文档`

const initialPath: string = props.win.props?.path || ''
const filePath = ref('')
const text = ref('')
const dirty = ref(false)
const saveHint = ref('')
const mode = ref<'edit' | 'split' | 'preview'>('split')
const editorEl = ref<HTMLTextAreaElement>()

const ext = computed(() => filePath.value.match(/\.([a-z0-9]+)$/i)?.[1]?.toLowerCase() || '')
const isMd = computed(() => !ext.value || ['md', 'markdown'].includes(ext.value))
const fileName = computed(() => filePath.value ? filePath.value.split('/').pop()! : '未命名文稿.md')

const html = computed(() => renderMarkdown(text.value))
const wordCount = computed(() => {
  const cjk = (text.value.match(/[\u4e00-\u9fa5]/g) || []).length
  const words = (text.value.replace(/[\u4e00-\u9fa5]/g, ' ').match(/[A-Za-z0-9_'-]+/g) || []).length
  return cjk + words
})

function syncTitle() {
  windows.setTitle(props.win.id, (dirty.value ? '● ' : '') + fileName.value + ' — 文档')
}
watch([fileName, dirty], syncTitle)

onMounted(async () => {
  if (initialPath) {
    try {
      const d = await fsApi.read(initialPath)
      if (d.isBinary) { toast('二进制文件，请在文件应用中预览', { type: 'error' }); return }
      filePath.value = initialPath
      text.value = d.content
      if (!isMd.value) mode.value = 'edit'
    } catch (e: any) { toast('打开失败：' + e.message, { type: 'error' }) }
  } else {
    text.value = '# 未命名文稿\n\n'
  }
  syncTitle()
})

// ---- 保存（⌘S）/ 另存为（⇧⌘S）/ 自动保存 ----
async function writeOut() {
  if (!filePath.value) return
  try {
    await fsApi.write(filePath.value, text.value)
    dirty.value = false
    const t = new Date()
    saveHint.value = `已存储 ${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}`
    bus.emit('fs:changed', { path: filePath.value, kind: 'write' }, { remote: false })
  } catch (e: any) {
    saveHint.value = ''
    toast('保存失败：' + e.message, { type: 'error' })
  }
}

function save() {
  if (filePath.value) { writeOut(); return }
  openSaveDialog()
}
function saveAs() { openSaveDialog() }

// 打开/存储选择：复用文件管理窗口（可拖动、与访达一致的浏览体验）
async function openOpenDialog() {
  const p = await fileDialog({ mode: 'open', title: '打开文稿', defaultDir: docsDir })
  if (!p) return
  try {
    const d = await fsApi.read(p)
    if (d.isBinary) { toast('二进制文件，请在文件应用中预览', { type: 'error' }); return }
    filePath.value = p
    text.value = d.content
    dirty.value = false
    if (!isMd.value) mode.value = 'edit'
  } catch (e: any) { toast('打开失败：' + e.message, { type: 'error' }) }
}
async function openSaveDialog() {
  const p = await fileDialog({ mode: 'save', title: '存储文稿为', defaultDir: docsDir, defaultName: fileName.value })
  if (!p) return
  filePath.value = p
  await writeOut()
}

let autoTimer = 0
function scheduleAutosave() {
  if (!filePath.value) return
  clearTimeout(autoTimer)
  autoTimer = window.setTimeout(writeOut, 1500)
}
onBeforeUnmount(() => clearTimeout(autoTimer))

function onInput() {
  dirty.value = true
  scheduleAutosave()
}

function onKey(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
    e.preventDefault()
    if (e.shiftKey) saveAs()
    else save()
  } else if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'o') {
    e.preventDefault()
    openOpenDialog()
  }
}

// Tab 插入两个空格（编辑器习惯）
function onTab(e: KeyboardEvent) {
  if (e.key !== 'Tab' || !editorEl.value) return
  e.preventDefault()
  const el = editorEl.value
  const s = el.selectionStart, ed = el.selectionEnd
  text.value = text.value.slice(0, s) + '  ' + text.value.slice(ed)
  requestAnimationFrame(() => { el.selectionStart = el.selectionEnd = s + 2 })
}

function exportFile() {
  const blob = new Blob([text.value], { type: 'text/plain' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = fileName.value
  a.click()
  URL.revokeObjectURL(a.href)
}
</script>

<template>
  <div class="docs" tabindex="0" @keydown="onKey">
    <!-- 融合窗体工具栏：整条可拖动窗口（data-os-drag 事件委托，控件自身不参与） -->
    <div class="os-tb" data-os-drag>
      <button class="os-tb-btn" title="打开…（⌘O）" v-html="icons.open" @click="openOpenDialog" />
      <button class="os-tb-btn" title="存储（⌘S）" v-html="icons.save" @click="save" />
      <button class="os-tb-btn" title="另存为…（⇧⌘S）" v-html="icons.saveAs" @click="saveAs" />
      <span class="os-tb-sep" />
      <div class="tb-title" :title="filePath || '尚未存储'">
        <span v-if="dirty" class="dot" />{{ fileName }}
      </div>
      <span class="grow" />
      <div v-if="isMd" class="os-seg">
        <button class="os-seg-btn" :class="{ on: mode === 'edit' }" title="编辑" v-html="icons.edit" @click="mode = 'edit'" />
        <button class="os-seg-btn" :class="{ on: mode === 'split' }" title="分栏" v-html="icons.split" @click="mode = 'split'" />
        <button class="os-seg-btn" :class="{ on: mode === 'preview' }" title="预览" v-html="icons.eye" @click="mode = 'preview'" />
      </div>
      <span class="os-tb-sep" />
      <span class="meta">{{ saveHint || (dirty ? '未存储' : '') }}</span>
      <span class="meta dim">{{ wordCount }} 字</span>
      <button class="os-tb-btn" title="导出到本机" v-html="icons.export" @click="exportFile" />
    </div>

    <div class="main" :class="'mode-' + mode">
      <textarea
        v-show="mode !== 'preview'"
        ref="editorEl" v-model="text" class="editor os-flat" spellcheck="false"
        placeholder="# 开始输入 Markdown…"
        @input="onInput" @keydown="onTab"
      />
      <div v-show="mode !== 'edit'" class="preview md-body" v-html="html" />
    </div>
  </div>
</template>

<style scoped>
.docs { width: 100%; display: flex; flex-direction: column; background: var(--os-window); outline: none; }
.tb-title { max-width: 40%; display: flex; align-items: center; gap: 6px; }
.dot { width: 7px; height: 7px; border-radius: 50%; background: var(--os-text-secondary); flex: none; }
.grow { flex: 1; }
.meta { font-size: 11.5px; color: var(--os-text-secondary); white-space: nowrap; }
.meta.dim { opacity: .8; }

.main { flex: 1; min-height: 0; display: flex; }
.main.mode-preview { background: var(--os-bg); }
.editor {
  flex: 1; min-width: 0; border: none; outline: none; resize: none;
  background: var(--os-window); color: var(--os-text);
  font-family: var(--os-mono); font-size: 13.5px; line-height: 1.8;
  padding: 22px 26px; user-select: text;
}
.main.mode-split .editor { border-right: 1px solid var(--os-border); }
.preview { flex: 1; min-width: 0; overflow-y: auto; padding: 26px 34px; user-select: text; }

/* Markdown 渲染样式（编辑器内预览与快速查看保持一致观感） */
.md-body { font-size: 14.5px; line-height: 1.8; color: var(--os-text); }
.md-body :deep(h1) { font-size: 1.75em; margin: .65em 0 .4em; line-height: 1.3; }
.md-body :deep(h2) { font-size: 1.42em; margin: .65em 0 .35em; }
.md-body :deep(h3) { font-size: 1.2em; margin: .6em 0 .3em; }
.md-body :deep(h4), .md-body :deep(h5), .md-body :deep(h6) { font-size: 1.05em; margin: .6em 0 .3em; }
.md-body :deep(p) { margin: .5em 0; }
.md-body :deep(a) { color: var(--os-accent); }
.md-body :deep(pre) {
  background: var(--os-active); padding: 12px 14px; border-radius: 8px; overflow-x: auto;
  font-family: var(--os-mono); font-size: 12.5px; line-height: 1.6; margin: .6em 0;
}
.md-body :deep(code) {
  font-family: var(--os-mono); background: var(--os-active); border-radius: 4px;
  padding: 1px 5px; font-size: .9em;
}
.md-body :deep(pre code) { background: none; padding: 0; }
.md-body :deep(blockquote) {
  border-left: 3px solid var(--os-accent); padding-left: 14px; color: var(--os-text-secondary); margin: .6em 0;
}
.md-body :deep(ul), .md-body :deep(ol) { padding-left: 1.6em; margin: .5em 0; }
.md-body :deep(li) { margin: .2em 0; }
.md-body :deep(table) { border-collapse: collapse; margin: .6em 0; }
.md-body :deep(th), .md-body :deep(td) { border: 1px solid var(--os-border-strong); padding: 5px 12px; font-size: .92em; }
.md-body :deep(th) { background: var(--os-sidebar); }
.md-body :deep(img) { max-width: 100%; border-radius: 8px; }
.md-body :deep(hr) { border: none; border-top: 1px solid var(--os-border-strong); margin: 1.2em 0; }
</style>
