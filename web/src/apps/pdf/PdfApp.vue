<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { fetchRawBlob } from '../../api/client'
import { OsToolbar, OsButton } from '../../ui'
import * as pdfjs from 'pdfjs-dist'
import workerUrl from 'pdfjs-dist/build/pdf.worker.min.mjs?url'

pdfjs.GlobalWorkerOptions.workerSrc = workerUrl

const props = defineProps<{ win: WinState }>()
const windows = useWindows()

const pageNum = ref(1)
const pageCount = ref(0)
const scale = ref(1.2)
const loading = ref(false)
const error = ref('')
const fileName = ref('未打开文件')
const canvasEl = ref<HTMLCanvasElement>()
let doc: pdfjs.PDFDocumentProxy | null = null

async function render() {
  if (!doc || !canvasEl.value) return
  const page = await doc.getPage(pageNum.value)
  const viewport = page.getViewport({ scale: scale.value })
  const canvas = canvasEl.value
  canvas.width = viewport.width
  canvas.height = viewport.height
  await page.render({ canvas, canvasContext: canvas.getContext('2d')!, viewport } as any).promise
}

async function openBytes(data: ArrayBuffer, name: string) {
  loading.value = true
  error.value = ''
  try {
    doc = await pdfjs.getDocument({ data }).promise
    pageCount.value = doc.numPages
    pageNum.value = 1
    fileName.value = name
    await render()
    windows.setTitle(props.win.id, name + ' — PDF')
  } catch (e: any) {
    error.value = '无法打开：' + (e?.message || '文件损坏或格式不支持')
  } finally {
    loading.value = false
  }
}

async function openFile(f: File) {
  await openBytes(await f.arrayBuffer(), f.name)
}

function onPicker(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  if (f) openFile(f)
}
function onDrop(e: DragEvent) {
  e.preventDefault()
  const f = e.dataTransfer?.files?.[0]
  if (f && f.name.toLowerCase().endsWith('.pdf')) openFile(f)
}

async function nav(d: number) {
  const next = pageNum.value + d
  if (next < 1 || next > pageCount.value) return
  pageNum.value = next
  await render()
}
async function zoom(d: number) {
  scale.value = Math.min(3, Math.max(0.5, scale.value + d))
  await render()
}

// 从文件系统打开（文件应用双击 .pdf / goder file 指令）
onMounted(async () => {
  if (props.win.props?.path) {
    loading.value = true
    try {
      const blob = await fetchRawBlob(props.win.props.path)
      const name = props.win.props.path.split('/').pop() || fileName.value
      await openBytes(await blob.arrayBuffer(), name)
    } catch (e: any) {
      error.value = '读取文件失败：' + e.message
      loading.value = false
    }
  }
})
onBeforeUnmount(() => doc?.destroy())
</script>

<template>
  <div class="pdf" @dragover.prevent @drop="onDrop">
    <OsToolbar>
      <label class="open-btn">
        打开 PDF
        <input type="file" accept="application/pdf" hidden @change="onPicker" />
      </label>
      <span class="sep" />
      <button :disabled="pageNum <= 1" @click="nav(-1)">‹ 上一页</button>
      <span class="pg">{{ pageCount ? `${pageNum} / ${pageCount}` : '—' }}</span>
      <button :disabled="pageNum >= pageCount" @click="nav(1)">下一页 ›</button>
      <span class="sep" />
      <button @click="zoom(-0.2)">－</button>
      <span class="pg">{{ Math.round(scale * 100) }}%</span>
      <button @click="zoom(0.2)">＋</button>
      <span class="grow" />
      <span class="fname">{{ fileName }}</span>
    </OsToolbar>

    <div class="viewer">
      <label v-if="!pageCount && !loading" class="drop-hint">
        <div class="ic">📕</div>
        <p>拖入 PDF 文件到这里，或点击选择</p>
        <p class="sub">也可以用左上角「打开 PDF」按钮</p>
        <input type="file" accept="application/pdf" hidden @change="onPicker" />
      </label>
      <div v-else-if="loading" class="drop-hint">加载中…</div>
      <div v-else-if="error" class="drop-hint err">{{ error }}</div>
      <div v-else class="canvas-wrap"><canvas ref="canvasEl" /></div>
    </div>
  </div>
</template>

<style scoped>
.pdf { width: 100%; display: flex; flex-direction: column; background: var(--os-bg); }
.open-btn {
  padding: 4px 12px; border-radius: 7px; background: var(--os-accent); color: #fff;
  font-size: 12.5px; cursor: pointer; font-weight: 600;
}
.pg { font-size: 12px; color: var(--os-text-secondary); padding: 0 4px; font-variant-numeric: tabular-nums; }
.fname { font-size: 12px; color: var(--os-text-secondary); max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.viewer { flex: 1; overflow: auto; display: flex; }
.drop-hint { margin: auto; text-align: center; color: var(--os-text-secondary); }
.drop-hint .ic { font-size: 52px; }
.drop-hint p { font-size: 14px; margin-top: 8px; }
.drop-hint .sub { font-size: 12px; }
.drop-hint.err { color: #e5484d; }
.canvas-wrap { margin: auto; padding: 16px; }
canvas { box-shadow: 0 4px 24px rgba(0,0,0,.3); border-radius: 3px; background: #fff; }
</style>
