<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { fsApi } from '../../api/client'
import { bus } from '../../bus/bus'
import { OsToolbar, OsButton } from '../../ui'

const props = defineProps<{ win: WinState }>()
const windows = useWindows()
const session = useSession()
const editorEl = ref<HTMLElement>()
const fileName = ref((props.win.props?.path || '').split('/').pop() || '未命名文档.html')
const filePath = ref<string>(props.win.props?.path || '')
const saved = ref(true)
const saveHint = ref('') // "自动保存于 12:03" / "已保存"

onMounted(async () => {
  if (filePath.value) {
    try {
      const data = await fsApi.read(filePath.value)
      editorEl.value!.innerHTML = data.content || '<p><br></p>'
      saved.value = true
    } catch { editorEl.value!.innerHTML = '<p><br></p>' }
  } else {
    editorEl.value!.innerHTML = '<h1>新文档</h1><p>开始输入…</p>'
  }
  windows.setTitle(props.win.id, fileName.value + ' — 文档')
})

function markDirty() { saved.value = false }

// ---- 自动保存（命名后启用，防抖 1.2s） ----
let autoTimer = 0
function scheduleAutosave() {
  if (!filePath.value) return
  clearTimeout(autoTimer)
  autoTimer = window.setTimeout(doSave, 1200)
}
onBeforeUnmount(() => { clearTimeout(autoTimer) })

async function doSave() {
  if (!editorEl.value) return
  await fsApi.write(filePath.value, editorEl.value.innerHTML)
  saved.value = true
  const t = new Date()
  saveHint.value = `自动保存于 ${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}`
  bus.emit('fs:changed', { path: filePath.value, kind: 'write' }, { remote: false })
}

async function save() {
  if (!filePath.value) {
    const name = prompt('保存为（输入文件名）', fileName.value)
    if (!name) return
    fileName.value = name.endsWith('.html') || name.endsWith('.md') ? name : name + '.html'
    filePath.value = `/home/${session.user!.username}/文档/${fileName.value}`
  }
  await doSave()
  windows.setTitle(props.win.id, fileName.value + ' — 文档')
}

function onInput() {
  markDirty()
  scheduleAutosave()
}

// 字数统计
const wordCount = computed(() => {
  const text = editorEl.value?.innerText || ''
  const cjk = (text.match(/[\u4e00-\u9fa5]/g) || []).length
  const words = (text.replace(/[\u4e00-\u9fa5]/g, ' ').match(/[A-Za-z0-9_'-]+/g) || []).length
  return `${cjk + words} 字`
})

function cmd(name: string, val?: string) {
  document.execCommand(name, false, val)
  editorEl.value?.focus()
  markDirty()
  scheduleAutosave()
}

function exportFile() {
  const blob = new Blob([`<!doctype html><meta charset="utf-8"><title>${fileName.value}</title>` + editorEl.value!.innerHTML], { type: 'text/html' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = fileName.value.endsWith('.html') ? fileName.value : fileName.value + '.html'
  a.click()
  URL.revokeObjectURL(a.href)
}

const groups: { cmd: string; val?: string; label: string; title: string }[][] = [
  [
    { cmd: 'bold', label: 'B', title: '加粗' },
    { cmd: 'italic', label: 'I', title: '斜体' },
    { cmd: 'underline', label: 'U', title: '下划线' },
    { cmd: 'strikeThrough', label: 'S', title: '删除线' },
  ],
  [
    { cmd: 'formatBlock', val: 'h1', label: 'H1', title: '标题 1' },
    { cmd: 'formatBlock', val: 'h2', label: 'H2', title: '标题 2' },
    { cmd: 'formatBlock', val: 'h3', label: 'H3', title: '标题 3' },
    { cmd: 'formatBlock', val: 'p', label: '正文', title: '正文' },
  ],
  [
    { cmd: 'insertUnorderedList', label: '• 列表', title: '无序列表' },
    { cmd: 'insertOrderedList', label: '1. 列表', title: '有序列表' },
    { cmd: 'formatBlock', val: 'blockquote', label: '❝ 引用', title: '引用' },
    { cmd: 'formatBlock', val: 'pre', label: '</> 代码', title: '代码块' },
  ],
  [
    { cmd: 'justifyLeft', label: '⬅', title: '左对齐' },
    { cmd: 'justifyCenter', label: '⬌', title: '居中' },
    { cmd: 'justifyRight', label: '➡', title: '右对齐' },
    { cmd: 'removeFormat', label: '⌫ 格式', title: '清除格式' },
  ],
]
</script>

<template>
  <div class="docs">
    <OsToolbar>
      <template v-for="(g, gi) in groups" :key="gi">
        <button v-for="b in g" :key="b.cmd + b.val" :title="b.title"
          :class="{ on: false }"
          :style="b.cmd === 'bold' ? 'font-weight:700' : b.cmd === 'italic' ? 'font-style:italic' : b.cmd === 'underline' ? 'text-decoration:underline' : b.cmd === 'strikeThrough' ? 'text-decoration:line-through' : ''"
          @mousedown.prevent @click="cmd(b.cmd, b.val)">{{ b.label }}</button>
        <span class="sep" />
      </template>
      <span class="grow" />
      <span class="save-state">{{ saved ? (saveHint || '已保存') : '未保存' }} · {{ wordCount }}</span>
      <OsButton size="sm" @click="exportFile">导出 HTML</OsButton>
      <OsButton size="sm" variant="primary" @click="save">{{ filePath ? '保存' : '保存到文件' }}</OsButton>
    </OsToolbar>

    <div class="paper-wrap">
      <div
        ref="editorEl"
        class="paper"
        contenteditable="true"
        spellcheck="false"
        @input="onInput"
      />
    </div>
  </div>
</template>

<style scoped>
.docs { width: 100%; display: flex; flex-direction: column; background: var(--os-window); }
.save-state { font-size: 11.5px; color: var(--os-text-secondary); }
.paper-wrap { flex: 1; overflow: auto; padding: 24px; background: var(--os-bg); }
.paper {
  max-width: 760px; margin: 0 auto; min-height: 900px; background: #fff; color: #1d1d1f;
  border-radius: 4px; box-shadow: 0 2px 18px rgba(0,0,0,.14); padding: 60px 64px;
  line-height: 1.8; font-size: 15px; outline: none; user-select: text;
}
.paper:empty::before { content: '开始输入…'; color: #bbb; }
.paper h1, .paper h2, .paper h3 { margin: 0.6em 0 0.35em; }
.paper blockquote { border-left: 3px solid #0a84ff; padding-left: 14px; color: #555; margin: 0.6em 0; }
.paper pre { background: #f5f5f7; padding: 12px 14px; border-radius: 8px; font-family: var(--os-mono); font-size: 13px; overflow-x: auto; }
</style>
