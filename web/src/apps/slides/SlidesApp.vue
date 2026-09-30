<script setup lang="ts">
// 幻灯片应用（HTML 格式）：直接在 16:9 画布上富文本编辑，每页是一个 <section class="slide">。
// 存储为独立 HTML 文档（下载后双击即可在浏览器放映）；兼容打开旧版 .slides.json 并自动升级。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { fsApi } from '../../api/client'
import { bus } from '../../bus/bus'
import { OsFilePicker, toast } from '../../ui'
import { parseDeck, serializeDeck } from './format'

const props = defineProps<{ win: WinState }>()
const windows = useWindows()
const session = useSession()

const initialPath: string = props.win.props?.path || ''
const filePath = ref('')
const slides = ref<string[]>(['<h1>标题页</h1><p>副标题 · 演讲者</p>'])
const cur = ref(0)
const playing = ref(false)
const theme = ref<'light' | 'dark'>('light')
const dirty = ref(false)
const saveHint = ref('')
const picker = ref<null | 'open' | 'save'>(null)
const canvasEl = ref<HTMLDivElement>()

const fileName = computed(() => filePath.value ? filePath.value.split('/').pop()! : '未命名演示.html')

function deckTitle(): string {
  const h = slides.value[0]?.match(/<h1[^>]*>([\s\S]*?)<\/h1>/i)?.[1]?.replace(/<[^>]+>/g, '').trim()
  if (h) return h.slice(0, 60)
  const stem = fileName.value.replace(/\.slides\.json$|\.html?$/i, '')
  return stem || '演示'
}

function syncTitle() {
  windows.setTitle(props.win.id, (dirty.value ? '● ' : '') + fileName.value + ' — 幻灯片')
}
watch([fileName, dirty], syncTitle)

// 画布是非受控 contenteditable：切换页/载入时手动灌 innerHTML，输入时回写数据
watch([cur, playing], async () => {
  if (playing.value) return
  await nextTick()
  if (canvasEl.value) canvasEl.value.innerHTML = slides.value[cur.value] || ''
})

onMounted(async () => {
  if (initialPath) {
    try {
      const d = await fsApi.read(initialPath)
      if (!d.isBinary) {
        const pages = parseDeck(d.content)
        if (pages.length) slides.value = pages
        filePath.value = initialPath
      }
    } catch (e: any) { toast('打开失败：' + e.message, { type: 'error' }) }
  }
  await nextTick()
  if (canvasEl.value) canvasEl.value.innerHTML = slides.value[0] || ''
  syncTitle()
})

// ---- 页操作 ----
function addSlide() {
  slides.value.splice(cur.value + 1, 0, '<h1>标题</h1><p>正文</p>')
  cur.value++
  dirty.value = true
  scheduleAutosave()
}
function removeSlide() {
  if (slides.value.length <= 1) return
  slides.value.splice(cur.value, 1)
  cur.value = Math.min(cur.value, slides.value.length - 1)
  dirty.value = true
  scheduleAutosave()
}
function move(d: number) {
  const i = cur.value + d
  if (i < 0 || i >= slides.value.length) return
  const [s] = slides.value.splice(cur.value, 1)
  slides.value.splice(i, 0, s)
  cur.value = i
  dirty.value = true
  scheduleAutosave()
}

function onEdit() {
  if (!canvasEl.value) return
  slides.value[cur.value] = canvasEl.value.innerHTML
  dirty.value = true
  scheduleAutosave()
}
function cmd(name: string, val?: string) {
  document.execCommand(name, false, val)
  canvasEl.value?.focus()
  onEdit()
}

// ---- 存取 ----
async function writeOut() {
  if (!filePath.value) return
  try {
    await fsApi.write(filePath.value, serializeDeck(slides.value, theme.value, deckTitle()))
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
  picker.value = 'save'
}
async function onPickerConfirm(p: string) {
  const m = picker.value
  picker.value = null
  if (m === 'open') {
    try {
      const d = await fsApi.read(p)
      if (d.isBinary) { toast('二进制文件无法打开', { type: 'error' }); return }
      const pages = parseDeck(d.content)
      if (pages.length) {
        slides.value = pages
        cur.value = 0
        dirty.value = false
        await nextTick()
        if (canvasEl.value) canvasEl.value.innerHTML = slides.value[0] || ''
      }
      filePath.value = p
    } catch (e: any) { toast('打开失败：' + e.message, { type: 'error' }) }
  } else {
    filePath.value = p
    await writeOut()
  }
}

let autoTimer = 0
function scheduleAutosave() {
  if (!filePath.value) return
  clearTimeout(autoTimer)
  autoTimer = window.setTimeout(writeOut, 2000)
}
onBeforeUnmount(() => clearTimeout(autoTimer))

function exportFile() {
  const blob = new Blob([serializeDeck(slides.value, theme.value, deckTitle())], { type: 'text/html' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = fileName.value.endsWith('.html') ? fileName.value : fileName.value.replace(/\.slides\.json$/i, '') + '.html'
  a.click()
  URL.revokeObjectURL(a.href)
}

function onKey(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') { e.preventDefault(); if (e.shiftKey) picker.value = 'save'; else save(); return }
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'o') { e.preventDefault(); picker.value = 'open'; return }
  if (!playing.value) return
  if (e.key === 'ArrowRight' || e.key === ' ') { cur.value = Math.min(cur.value + 1, slides.value.length - 1); e.preventDefault() }
  else if (e.key === 'ArrowLeft') { cur.value = Math.max(cur.value - 1, 0); e.preventDefault() }
  else if (e.key === 'Escape') { playing.value = false; e.preventDefault() }
}

const fmtGroups: { cmd: string; val?: string; label: string; title: string; style?: string }[][] = [
  [
    { cmd: 'bold', label: 'B', title: '加粗', style: 'font-weight:700' },
    { cmd: 'italic', label: 'I', title: '斜体', style: 'font-style:italic' },
    { cmd: 'underline', label: 'U', title: '下划线', style: 'text-decoration:underline' },
  ],
  [
    { cmd: 'formatBlock', val: 'h1', label: '大标题', title: '标题 1' },
    { cmd: 'formatBlock', val: 'h2', label: '中标题', title: '标题 2' },
    { cmd: 'formatBlock', val: 'p', label: '正文', title: '正文' },
  ],
  [
    { cmd: 'insertUnorderedList', label: '• 列表', title: '无序列表' },
    { cmd: 'insertOrderedList', label: '1. 列表', title: '有序列表' },
    { cmd: 'formatBlock', val: 'pre', label: '</>', title: '代码块' },
  ],
]
</script>

<template>
  <div class="slides" tabindex="0" @keydown="onKey">
    <div class="os-tb">
      <button class="os-tb-btn" title="打开…（⌘O）" @click="picker = 'open'">打开…</button>
      <button class="os-tb-btn" title="存储（⌘S）" @click="save">存储</button>
      <button class="os-tb-btn" title="另存为…（⇧⌘S）" @click="picker = 'save'">另存为…</button>
      <span class="os-tb-sep" />
      <button class="os-tb-btn" title="新建一页" @click="addSlide">＋页</button>
      <button class="os-tb-btn" title="删除当前页" @click="removeSlide">－页</button>
      <button class="os-tb-btn" title="上移一页" @click="move(-1)">◀</button>
      <button class="os-tb-btn" title="下移一页" @click="move(1)">▶</button>
      <span class="os-tb-sep" />
      <div class="os-seg">
        <button class="os-seg-btn" :class="{ on: theme === 'light' }" title="浅色主题" @click="theme = 'light'; dirty = true; scheduleAutosave()">浅色</button>
        <button class="os-seg-btn" :class="{ on: theme === 'dark' }" title="深色主题" @click="theme = 'dark'; dirty = true; scheduleAutosave()">深色</button>
      </div>
      <span class="grow" data-os-drag />
      <span class="meta">{{ saveHint || (dirty ? '未存储' : '') }}</span>
      <button class="os-tb-btn" title="导出独立 HTML（双击即可放映）" @click="exportFile">导出</button>
      <button class="os-tb-btn primary" title="播放演示（方向键翻页，Esc 退出）" @click="playing = true">▶ 演示</button>
    </div>

    <div class="main">
      <!-- 页缩略图 -->
      <aside class="thumbs">
        <div
          v-for="(s, i) in slides" :key="i"
          class="t-item" :class="{ cur: i === cur, dark: theme === 'dark' }"
          @click="cur = i"
        >
          <div class="sc t-canvas" v-html="s" />
          <span class="t-num">{{ i + 1 }}</span>
        </div>
      </aside>

      <!-- 画布 + 富文本工具 -->
      <div class="stage" :class="{ dark: theme === 'dark' }">
        <div class="fmt">
          <template v-for="(g, gi) in fmtGroups" :key="gi">
            <button v-for="b in g" :key="b.cmd + b.val" class="fmt-btn" :title="b.title" :style="b.style"
              @mousedown.prevent @click="cmd(b.cmd, b.val)">{{ b.label }}</button>
            <span class="fmt-sep" />
          </template>
        </div>
        <div class="canvas-host">
          <div ref="canvasEl" class="sc canvas" :class="{ dark: theme === 'dark' }" contenteditable="true" spellcheck="false" @input="onEdit" />
        </div>
        <div class="pager">{{ cur + 1 }} / {{ slides.length }}</div>
      </div>
    </div>

    <!-- 演示模式 -->
    <div v-if="playing" class="play" :class="{ dark: theme === 'dark' }">
      <div class="play-host">
        <div class="sc play-slide" v-html="slides[cur]" />
      </div>
      <div class="play-bar">
        <button class="os-tb-btn" @click="cur = Math.max(0, cur - 1)">‹</button>
        <span>{{ cur + 1 }} / {{ slides.length }} · 方向键翻页 · Esc 退出</span>
        <button class="os-tb-btn" @click="cur = Math.min(slides.length - 1, cur + 1)">›</button>
        <button class="os-tb-btn" @click="playing = false">退出</button>
      </div>
    </div>

    <OsFilePicker
      v-if="picker" :mode="picker" :title="picker === 'open' ? '打开演示' : '存储演示为'"
      :default-dir="`/home/${session.user!.username}/文档`" :default-name="fileName"
      :exts="picker === 'open' ? ['html', 'htm'] : undefined"
      @confirm="onPickerConfirm" @cancel="picker = null"
    />
  </div>
</template>

<style scoped>
.slides { width: 100%; display: flex; flex-direction: column; background: var(--os-window); outline: none; }
.grow { flex: 1; }
.meta { font-size: 11.5px; color: var(--os-text-secondary); white-space: nowrap; }
.os-tb-btn.primary { background: var(--os-accent); border-color: var(--os-accent); color: #fff; font-weight: 600; }
.os-tb-btn.primary:hover { background: var(--os-accent-hover); }

.main { flex: 1; display: flex; min-height: 0; }
.thumbs {
  width: 158px; flex: none; overflow-y: auto; border-right: 1px solid var(--os-border);
  background: var(--os-sidebar); padding: 10px 8px; display: flex; flex-direction: column; gap: 10px;
}
.t-item { position: relative; border-radius: 7px; cursor: pointer; border: 2px solid transparent; overflow: hidden; }
.t-item.cur { border-color: var(--os-accent); }
.t-num {
  position: absolute; bottom: 3px; right: 6px; font-size: 9px; color: #86868b;
  background: rgba(255, 255, 255, .7); border-radius: 4px; padding: 0 4px;
}
.stage { flex: 1; display: flex; flex-direction: column; min-width: 0; padding: 10px 16px 12px; gap: 8px; background: var(--os-bg); }
.stage.dark { background: #242427; }
.fmt { display: flex; gap: 4px; align-items: center; flex-wrap: wrap; }
.fmt-btn {
  border: 1px solid var(--os-border-strong); background: var(--os-panel-solid); color: var(--os-text);
  border-radius: 6px; height: 24px; padding: 0 8px; font-size: 11.5px; cursor: pointer;
}
.fmt-btn:hover { background: var(--os-hover); }
.fmt-sep { width: 1px; height: 16px; background: var(--os-border); margin: 0 3px; }
.canvas-host { flex: 1; min-height: 0; display: flex; align-items: center; justify-content: center; }
.pager { text-align: center; font-size: 11.5px; color: var(--os-text-secondary); }

/* —— 幻灯片画布：cqw 字号随容器宽度缩放（缩略图/编辑/演示同一套比例） —— */
.sc {
  width: 100%; aspect-ratio: 16 / 9; container-type: inline-size;
  background: #fff; color: #1d1d1f; border-radius: 10px;
  box-shadow: 0 3px 16px rgba(0, 0, 0, .16);
  padding: 6.5% 7.5%; overflow: hidden;
  display: flex; flex-direction: column; justify-content: center;
  user-select: text; line-height: 1.6;
}
.sc.dark { background: #1e1e21; color: #f5f5f7; }
.sc h1 { font-size: 4.4cqw; margin-bottom: .5em; line-height: 1.25; }
.sc h2 { font-size: 3.2cqw; margin: .4em 0; }
.sc p, .sc li { font-size: 2.1cqw; line-height: 1.75; }
.sc ul, .sc ol { padding-left: 1.5em; margin: .4em 0; }
.sc blockquote { border-left: .4cqw solid #0a84ff; padding-left: 1em; color: #666; margin: .5em 0; }
.sc pre { background: #f5f5f7; padding: .8em 1em; border-radius: .7cqw; font-size: 1.7cqw; overflow: auto; }
.sc.dark pre { background: #2a2a2e; }
.sc img { max-width: 100%; max-height: 60%; border-radius: .7cqw; }
.sc table { border-collapse: collapse; font-size: 1.9cqw; }
.sc th, .sc td { border: 1px solid #d2d2d7; padding: .35em .7em; }
.sc.dark th, .sc.dark td { border-color: #3a3a3e; }

.t-canvas { width: 100%; pointer-events: none; box-shadow: none; border-radius: 5px; }
.canvas { max-width: min(100%, calc((100% ) * 1)); width: min(96%, 980px); outline: none; }
.play { position: absolute; inset: 0; z-index: 60; background: #101014; display: flex; flex-direction: column; }
.play.dark { background: #0a0a0c; }
.play-host { flex: 1; min-height: 0; display: flex; align-items: center; justify-content: center; padding: 20px; }
.play-slide { width: min(96%, calc((100vh - 140px) * 16 / 9)); box-shadow: 0 20px 80px rgba(0, 0, 0, .5); pointer-events: none; }
.play-bar { flex: none; display: flex; align-items: center; gap: 10px; justify-content: center; padding: 8px; color: rgba(255, 255, 255, .7); font-size: 12px; }
</style>
