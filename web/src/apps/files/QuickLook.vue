<script setup lang="ts">
// 快速预览（空格唤起的覆盖层）：接收待预览文件，内部按扩展名加载渲染——
// 图片/PDF 走 blob 直览、Markdown 渲染 HTML、CSV/TSV 出表格（前 60 行 × 12 列）、
// HTML 进沙箱 iframe、其余纯文本；Esc/空格/点遮罩关闭。
// blob 生命周期自管：切换预览目标或组件卸载时释放，父级只管开合（qlFile）。
import { onBeforeUnmount, ref, watch } from 'vue'
import type { VFile } from '../../api/client'
import { fetchRawBlob, fsApi } from '../../api/client'
import { extOf, fileEmoji, IMAGE_EXTS, isImage } from '../openWith'
import { renderMarkdown } from '../../lib/md'
import { parseCSV } from '../sheets/engine'

const props = defineProps<{ file: VFile | null }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const baseName = (p: string) => p.split('/').pop() || ''
const ql = ref<{ f: VFile; url?: string; html?: string; text?: string; loading: boolean } | null>(null)

// 打开即预览（聚焦搜索/打开方式把图片交给文件应用时父级直接置 file）
watch(() => props.file, (f) => { if (f) open(f) }, { immediate: true })

async function open(f: VFile) {
  release()
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
function release() {
  if (ql.value?.url) URL.revokeObjectURL(ql.value.url)
  ql.value = null
}
function close() {
  release()
  emit('close')
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); close() }
}
// 整窗关闭时预览还开着：释放 blob（URL 回收不可见，行为不变）
onBeforeUnmount(release)
</script>

<template>
  <div v-if="ql" class="ql" tabindex="0" @keydown="onKey" @pointerdown.self="close">
    <header>
      <span class="ql-name">{{ fileEmoji(ql.f.path) }} {{ baseName(ql.f.path) }}</span>
      <button class="ql-close" title="关闭（空格）" @click="close">✕</button>
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
</template>

<style scoped>
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
</style>
