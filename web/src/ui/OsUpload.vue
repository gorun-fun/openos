<script setup lang="ts">
// OsUpload：文件上传组件——点击选择或拖拽，带单文件进度条。
//
// 两种目标：
//   - 默认上传到 OpenOS VFS：传 dir（目标目录），走 /api/fs/upload；
//   - 任意业务端点：传 url（宿主自己的 API），multipart 字段名默认 file，
//     可用 fields 附加额外表单字段（如业务 ID）。
// 两种方式均自动携带当前登录态（Authorization）。
import { reactive, ref } from 'vue'
import { getToken } from '../api/client'

const props = withDefaults(defineProps<{
  /** VFS 模式：上传目标目录（如 /home/alice/文档） */
  dir?: string
  /** 自定义端点模式：POST 到该 URL（multipart） */
  url?: string
  accept?: string
  multiple?: boolean
  /** 单文件大小上限（MB），默认对齐服务端 VFS 上限 8MB */
  maxMB?: number
  /** 文件字段名（自定义端点模式） */
  fieldName?: string
  /** 附加表单字段（自定义端点模式） */
  fields?: Record<string, string>
}>(), { multiple: true, maxMB: 8, fieldName: 'file' })

const emit = defineEmits<{
  /** 单个文件上传成功（参数为端点 JSON 响应） */
  uploaded: [res: any, file: { name: string; size: number }]
  error: [msg: string]
}>()

interface Item {
  name: string
  size: number
  percent: number
  status: 'uploading' | 'done' | 'error'
  msg?: string
}

const items = ref<Item[]>([])
const dragging = ref(false)
const inputEl = ref<HTMLInputElement>()

function onPick(e: Event) {
  void upload([...(e.target as HTMLInputElement).files || []])
  ;(e.target as HTMLInputElement).value = '' // 允许重复选择同一文件
}
function onDrop(e: DragEvent) {
  e.preventDefault()
  dragging.value = false
  void upload([...(e.dataTransfer?.files || [])])
}

function upload(files: File[]) {
  const list = props.multiple ? files : files.slice(0, 1)
  for (const f of list) {
    if (f.size > props.maxMB! * 1048576) {
      items.value.push({ name: f.name, size: f.size, percent: 0, status: 'error', msg: `超过 ${props.maxMB}MB 上限` })
      continue
    }
    void uploadOne(f)
  }
}

function uploadOne(f: File) {
  const item: Item = reactive({ name: f.name, size: f.size, percent: 0, status: 'uploading' })
  items.value.push(item)

  const fd = new FormData()
  fd.append(props.fieldName, f)
  if (props.dir) fd.append('dir', props.dir)
  for (const [k, v] of Object.entries(props.fields || {})) fd.append(k, v)

  // 用 XHR 而非 fetch：只有它提供上传进度事件
  const xhr = new XMLHttpRequest()
  xhr.open('POST', props.url || '/api/fs/upload')
  xhr.setRequestHeader('Authorization', 'Bearer ' + getToken())
  xhr.upload.onprogress = (e) => {
    if (e.lengthComputable) item.percent = Math.round((e.loaded / e.total) * 100)
  }
  xhr.onload = () => {
    if (xhr.status >= 200 && xhr.status < 300) {
      item.status = 'done'
      item.percent = 100
      let res: any = null
      try { res = JSON.parse(xhr.responseText) } catch { /* 非 JSON 响应 */ }
      emit('uploaded', res, { name: f.name, size: f.size })
    } else {
      let msg = `HTTP ${xhr.status}`
      try { msg = JSON.parse(xhr.responseText).error || msg } catch { /* ignore */ }
      item.status = 'error'
      item.msg = msg
      emit('error', msg)
    }
  }
  xhr.onerror = () => {
    item.status = 'error'
    item.msg = '网络错误'
    emit('error', '网络错误')
  }
  xhr.send(fd)
}

function fmtSize(n: number) {
  return n >= 1048576 ? (n / 1048576).toFixed(1) + ' MB' : Math.max(1, Math.round(n / 1024)) + ' KB'
}
function removeItem(i: number) {
  items.value.splice(i, 1)
}
</script>

<template>
  <div class="os-upload">
    <div
      class="dropzone"
      :class="{ dragging }"
      tabindex="0"
      role="button"
      @click="inputEl?.click()"
      @keydown.enter.prevent="inputEl?.click()"
      @keydown.space.prevent="inputEl?.click()"
      @dragover.prevent="dragging = true"
      @dragleave="dragging = false"
      @drop="onDrop"
    >
      <div class="ic">⤒</div>
      <p>点击选择或拖拽文件到此处</p>
      <p class="sub">{{ dir ? `上传到 ${dir}` : '上传' }} · 单文件 ≤ {{ maxMB }}MB{{ multiple ? ' · 可多选' : '' }}</p>
      <input
        ref="inputEl" type="file" hidden
        :accept="accept" :multiple="multiple"
        @change="onPick"
      />
    </div>
    <ul v-if="items.length" class="files">
      <li v-for="(f, i) in items" :key="f.name + i" :class="f.status">
        <span class="name" :title="f.name">{{ f.name }}</span>
        <span class="size">{{ fmtSize(f.size) }}</span>
        <span class="grow status">
          <template v-if="f.status === 'uploading'">{{ f.percent }}%</template>
          <template v-else-if="f.status === 'done'">✓ 完成</template>
          <template v-else>{{ f.msg }}</template>
        </span>
        <button class="rm" type="button" @click="removeItem(i)">×</button>
        <div class="bar" :style="{ width: f.percent + '%' }" />
      </li>
    </ul>
  </div>
</template>

<style scoped>
.os-upload { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
.dropzone {
  border: 1.5px dashed var(--os-border-strong); border-radius: 10px; padding: 22px 16px;
  text-align: center; cursor: pointer; color: var(--os-text-secondary);
  transition: border-color .14s, background .14s; outline: none;
}
.dropzone:hover, .dropzone:focus-visible, .dropzone.dragging {
  border-color: var(--os-accent); background: rgba(10, 132, 255, .06);
}
.ic { font-size: 22px; margin-bottom: 4px; }
.dropzone p { font-size: 13px; }
.dropzone .sub { font-size: 11.5px; margin-top: 3px; opacity: .8; }
.files { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.files li {
  position: relative; overflow: hidden; display: flex; align-items: center; gap: 10px;
  background: var(--os-panel-solid); border: 1px solid var(--os-border);
  border-radius: 8px; padding: 7px 10px; font-size: 12.5px;
}
.files li.done { border-color: rgba(48, 209, 88, .4); }
.files li.error { border-color: rgba(229, 72, 77, .5); }
.name { max-width: 40%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.size { color: var(--os-text-secondary); flex: none; }
.grow { flex: 1; min-width: 0; }
.status { text-align: right; }
li.done .status { color: #1d8a3e; }
li.error .status { color: #e5484d; }
.rm {
  flex: none; border: none; background: transparent; color: var(--os-text-secondary);
  cursor: pointer; font-size: 15px; line-height: 1; padding: 0 2px; border-radius: 4px;
}
.rm:hover { color: var(--os-text); background: var(--os-hover); }
.bar {
  position: absolute; left: 0; bottom: 0; height: 2px;
  background: var(--os-accent); transition: width .18s ease;
}
li.error .bar { background: #e5484d; }
</style>
