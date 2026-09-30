<script setup lang="ts">
// SchemaApp：声明式第三方应用渲染器。
// 第三方只需提供 JSON schema（数据 + 视图描述），无需写任何前端代码。
//
// schema 示例：
// {
//   "title": "公告板",
//   "dataSource": { "kind": "static", "items": [...] }
//   "views": [
//     { "type": "stats", "fields": [{"key":"total","label":"总数"},{"key":"today","label":"今日"}] },
//     { "type": "table", "title": "全部", "columns": [{"key":"title","label":"标题"},{"key":"date","label":"日期"}] },
//     { "type": "cards", "title": "卡片", "fields": [{"key":"title","label":"标题","primary":true},{"key":"body","label":"内容"}] }
//   ]
// }
//
// dataSource.kind = "rest" 时的完整字段：
//   url      请求地址。相对路径（/api/...）默认携带当前登录态（走统一 API 客户端）；
//            跨域绝对地址默认裸 fetch，避免令牌外泄，可用 auth: true 强制携带
//   method   可选，默认 GET；POST 时可携带 body（JSON）
//   body     POST 请求体（对象）
//   headers  自定义请求头（对象，键值对）
//   path     响应取值路径，支持点路径（如 "data.items"）
//   interval 轮询间隔秒（可选，最小 5s）
//   topic    事件总线主题：收到该主题推送（本地或跨端）立即刷新，支持通配（如 "app:rooms"）
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { WinState } from '../stores/windows'
import { OsTabs, OsTable, OsEmpty } from '../ui'
import { api } from '../api/client'
import { bus } from '../bus/bus'

const props = defineProps<{ schema: any; win: WinState }>()

interface SchemaView {
  type: 'table' | 'cards' | 'stats'
  title?: string
  columns?: { key: string; label: string }[]
  fields?: { key: string; label: string; primary?: boolean }[]
}

const items = ref<Record<string, any>[]>([])
const loading = ref(true)
const error = ref('')
const view = ref('')
let timer = 0
let offTopic: (() => void) | null = null

const views = computed<SchemaView[]>(() => props.schema?.views || [])
const activeView = computed(() => views.value.find((v) => (v.title || v.type) === view.value) || views.value[0])

// 点路径取值："data.items" → data?.items（越界安全）
function getPath(data: any, path?: string): any {
  if (!path) return data
  return String(path).split('.').reduce((acc, k) => (acc == null ? undefined : acc[k.trim()]), data)
}

async function load() {
  const ds = props.schema?.dataSource
  if (!ds) { loading.value = false; return }
  try {
    error.value = ''
    if (ds.kind === 'static') {
      items.value = ds.items || []
    } else if (ds.kind === 'rest') {
      const method = String(ds.method || 'GET').toUpperCase()
      const isPost = method === 'POST' && ds.body !== undefined
      // 相对路径默认带登录态（同源 API）；跨域绝对地址用裸 fetch，避免令牌外泄
      const withAuth = ds.auth ?? !/^(https?:)?\/\//i.test(ds.url)
      let data: any
      if (withAuth) {
        data = isPost
          ? await api.post(ds.url, ds.body, ds.headers)
          : await api.get(ds.url, ds.headers)
      } else {
        const headers: Record<string, string> = { ...(ds.headers || {}) }
        if (isPost) headers['Content-Type'] = 'application/json'
        const resp = await fetch(ds.url, {
          method: isPost ? 'POST' : method,
          headers,
          body: isPost ? JSON.stringify(ds.body) : undefined,
        })
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
        data = await resp.json()
      }
      items.value = getPath(data, ds.path)
      if (!Array.isArray(items.value)) items.value = []
    }
  } catch (e: any) {
    error.value = '数据加载失败：' + e.message
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  if (!views.value.length) return
  view.value = views.value[0].title || views.value[0].type
  load()
  const ds = props.schema?.dataSource
  if (ds?.kind === 'rest' && ds.interval) {
    timer = window.setInterval(load, Math.max(5, ds.interval) * 1000)
  }
  // 总线推送刷新：收到主题消息（本地或跨端）立即重取数据
  if (ds?.topic) offTopic = bus.on(ds.topic, () => load())
})
onBeforeUnmount(() => {
  clearInterval(timer)
  offTopic?.()
})

const stats = computed(() => {
  const f = (activeView.value?.fields) || []
  return f.map((x) => ({
    label: x.label,
    value: typeof items.value[0]?.[x.key] === 'object' ? JSON.stringify(items.value[0]?.[x.key]) : (items.value[0]?.[x.key] ?? '—'),
  }))
})
</script>

<template>
  <div class="schema-app">
    <header class="head">
      <h2>{{ schema?.title || '应用' }}</h2>
      <OsTabs
        v-if="views.length > 1"
        :tabs="views.map((v, i) => ({ key: v.title || v.type || String(i), label: v.title || v.type }))"
        v-model="view"
      />
      <span class="grow" />
      <button class="refresh" title="刷新" @click="load">⟳</button>
    </header>

    <div v-if="loading" class="state">加载中…</div>
    <div v-else-if="error" class="state err">{{ error }}</div>
    <div v-else-if="!views.length" class="state"><OsEmpty text="Schema 为空" /></div>

    <!-- 统计卡 -->
    <div v-else-if="activeView?.type === 'stats'" class="stats">
      <div v-for="s in stats" :key="s.label" class="stat">
        <div class="v">{{ s.value }}</div>
        <div class="l">{{ s.label }}</div>
      </div>
    </div>

    <!-- 表格 -->
    <div v-else-if="activeView?.type === 'table'" class="pad">
      <OsTable :columns="activeView.columns || []" :rows="items" />
    </div>

    <!-- 卡片 -->
    <div v-else-if="activeView?.type === 'cards'" class="cards">
      <article v-for="(it, i) in items" :key="i" class="card">
        <template v-for="f in activeView.fields || []" :key="f.key">
          <div v-if="f.primary" class="primary">{{ it[f.key] }}</div>
          <div v-else class="line"><span class="ll">{{ f.label }}</span> {{ it[f.key] }}</div>
        </template>
      </article>
      <div v-if="!items.length" class="state">暂无数据</div>
    </div>
  </div>
</template>

<style scoped>
.schema-app { display: flex; flex-direction: column; width: 100%; min-height: 0; background: var(--os-window); }
.head { display: flex; align-items: center; gap: 12px; padding: 12px 16px; border-bottom: 1px solid var(--os-border); }
h2 { font-size: 15px; }
.grow { flex: 1; }
.refresh { border: none; background: transparent; font-size: 16px; color: var(--os-text-secondary); cursor: pointer; border-radius: 6px; padding: 4px 8px; }
.refresh:hover { background: var(--os-hover); }
.pad { padding: 12px; overflow: auto; }
.state { padding: 40px; text-align: center; color: var(--os-text-secondary); font-size: 13px; }
.state.err { color: #e5484d; }
.stats { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); gap: 12px; padding: 16px; }
.stat { background: var(--os-panel-solid); border: 1px solid var(--os-border); border-radius: 12px; padding: 16px; }
.stat .v { font-size: 26px; font-weight: 700; }
.stat .l { font-size: 12px; color: var(--os-text-secondary); margin-top: 4px; }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 12px; padding: 16px; overflow: auto; align-content: start; }
.card { background: var(--os-panel-solid); border: 1px solid var(--os-border); border-radius: 12px; padding: 14px; }
.primary { font-size: 15px; font-weight: 700; margin-bottom: 8px; }
.line { font-size: 12.5px; color: var(--os-text-secondary); margin-top: 3px; word-break: break-all; }
.ll { color: var(--os-text); margin-right: 4px; }
</style>
