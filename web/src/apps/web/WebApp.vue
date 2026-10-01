<script setup lang="ts">
// 网页应用容器：把内置网页应用注册表（registry.ts）中的外部站点以 iframe 嵌入窗体。
// 跨域 iframe 由浏览器同源策略天然隔离（双方脚本无法互访），因此无法探测目标站
// 是否因 X-Frame-Options 拒载（load 事件恒触发），提供「刷新 / 浏览器打开」兜底。
// 安全约束：sandbox 不给 allow-top-navigation——部分站点会执行
// framebusting（top.location 自拔），没有该限制时整个桌面会被第三方页面导航走。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { useApps } from '../../stores/apps'
import { OsEmpty, icons } from '../../ui'
import { WEB_APPS } from './registry'

const props = defineProps<{ win: WinState }>()
const windows = useWindows()
const apps = useApps()

const def = WEB_APPS[props.win.appId]
// 动态注册的网页应用：config = {"kind":"web"|"proxy","url":…}，地址与名称存在 apps 表。
// proxy 形态（应用代理）：iframe 指向服务端反代 /app/<id>/<目标>，由服务端剥掉
// X-Frame-Options/CSP 等拒绝嵌入的响应头并重写资源地址，可嵌入禁止 iframe 的站点
const dyn = computed(() => apps.byId(props.win.appId))
const dynCfg = computed<{ kind?: string; url?: string }>(() => {
  const c = dyn.value?.config as any
  if (!c) return {}
  if (typeof c === 'string') { try { return JSON.parse(c) } catch { return {} } }
  return c // API 可能直接给嵌套对象
})
// 地址优先级：编程打开（OpenOS.open 传 { url }）→ 动态注册 config → 内置注册表
const url = computed(() => props.win.props?.url || dynCfg.value.url || def?.url || '')
const title = computed(() => def?.title || dyn.value?.name || '网页')
const host = computed(() => { try { return new URL(url.value).host } catch { return '' } })
// 代理模式统一走服务端反代。目标地址以紧凑段嵌入路径（<scheme>/<host>/<路径>，
// 不写 :// —— 含 // 的路径会被 Go ServeMux 的 cleanPath 折叠成 307 清洗重定向）；
// 入口也可直接用 /app/<id>/ 由服务端重定向到配置目标
const pxCompact = (u: string) => u.replace(/^([a-zA-Z][a-zA-Z0-9+.-]*):\/\//, (_m, s: string) => `${s}/`)
const src = computed(() => {
  if (!url.value) return ''
  if (dynCfg.value.kind === 'proxy') return `/app/${props.win.appId}/${pxCompact(url.value)}`
  return url.value
})

// ---- 标签页：单 tab 即原形态；代理模式下页面 window.open / target=_blank 由
// hook.js 拦截，经窗体根元素上的 __openosPxHost 收进新 tab（不弹真浏览器窗口）----
type Tab = { id: number; url: string; title: string; loaded: boolean; seq: number; name?: string }
const rootEl = ref<HTMLElement>()
const tabs = ref<Tab[]>([])
const activeId = ref(0)
let tabSeq = 0
const activeTab = computed(() => tabs.value.find(t => t.id === activeId.value) || null)

// 部分站点在 iframe 内的主文档 load 事件永不派发（长轮询类子资源
// 一直挂起），不能只靠 @load 收起加载提示——超时兜底强制收起，避免内容被白层永久遮挡。
const loading = ref(false)
let loadTimer = 0
function armLoadFallback() {
  clearTimeout(loadTimer)
  loadTimer = window.setTimeout(() => { loading.value = false }, 5000)
}
function resetTabs() {
  if (!src.value) { tabs.value = []; return }
  tabs.value = [{ id: ++tabSeq, url: src.value, title: title.value, loaded: false, seq: 0 }]
  activeId.value = tabs.value[0].id
  loading.value = true
  armLoadFallback()
}
watch(src, resetTabs, { immediate: true })
onBeforeUnmount(() => clearTimeout(loadTimer))

onMounted(() => {
  if (url.value) windows.setTitle(props.win.id, title.value)
  // 宿主标签页 API：挂在窗体根元素上（hook.js 经 frameElement.closest 认领，
  // 多个代理窗口互不干扰）。openTab 同步返回一个惰性转发到新 tab 真实
  // contentWindow 的 Proxy，页面对返回值调 close()/postMessage()/document 照常工作
  if (rootEl.value) {
    ;(rootEl.value as any).__openosPxHost = {
      openTab(tabUrl: string, name?: string): any {
        if (!tabUrl) return null
        // 命名窗口复用（window.open(url,'winA') 二次调用回到同一 tab）
        const exist = name && !name.startsWith('_') ? tabs.value.find(t => t.name === name) : null
        if (exist) {
          exist.url = tabUrl
          exist.seq++
          exist.loaded = false
          exist.title = '加载中…'
          activeId.value = exist.id
          loading.value = true
          armLoadFallback()
          return shimFor(exist)
        }
        const t: Tab = { id: ++tabSeq, url: tabUrl, title: '加载中…', loaded: false, seq: 0, name }
        tabs.value.push(t)
        activeId.value = t.id
        loading.value = true
        armLoadFallback()
        return shimFor(t)
      },
      closeSelf(source: Window) {
        const t = tabs.value.find(x => winOf(x) === source)
        if (t) closeTab(t.id)
      },
    }
  }
})

// 按 tab 找 iframe 的 contentWindow（iframe 由 v-for 渲染，惰性查询）
function winOf(t: Tab): Window | null {
  return rootEl.value?.querySelector<HTMLIFrameElement>(`iframe[data-tab="${t.id}"]`)?.contentWindow ?? null
}
// 惰性转发 shim：iframe 尚未挂载时各属性为 undefined，挂载后转发真实 window。
// closed 按标签是否仍存在判断——iframe 挂载是异步的，以节点判断会让页面在
// openTab 返回后立刻读到 closed=true
function shimFor(t: Tab): any {
  return new Proxy({} as any, {
    get(_t, p) {
      if (typeof p !== 'string') return undefined
      if (p === 'closed') return !tabs.value.includes(t)
      if (p === 'close') return () => { const w = winOf(t); if (w) closeByWindow(w) }
      if (p === 'focus') return () => { activeId.value = t.id }
      const w = winOf(t)
      if (!w) return undefined
      const v = (w as any)[p]
      return typeof v === 'function' ? v.bind(w) : v
    },
  })
}
function closeByWindow(w: Window) {
  const t = tabs.value.find(x => winOf(x) === w)
  if (t) closeTab(t.id)
}

function fallbackTitle(t: Tab): string {
  // 代理路径 /app/<id>/<scheme>/<host>/… 取目标 host；普通 URL 直接取 host
  const parts = t.url.split('/')
  if (parts[0] === '' && parts[1] === 'app' && parts.length > 4) return parts[4]
  try { return new URL(t.url, location.origin).host } catch { return t.url.slice(0, 24) }
}
function onTabLoad(t: Tab, ev: Event) {
  t.loaded = true
  try {
    const d = (ev.target as HTMLIFrameElement).contentDocument
    if (d?.title) t.title = d.title
  } catch { /* 跨域读不到标题（直嵌模式），回落地址 */ }
  if (!t.title || t.title === '加载中…') t.title = fallbackTitle(t)
  if (t.id === activeId.value) { clearTimeout(loadTimer); loading.value = false }
}
function switchTab(id: number) {
  if (activeId.value === id) return
  activeId.value = id
  const t = activeTab.value
  if (t && !t.loaded) { loading.value = true; armLoadFallback() } else loading.value = false
}
function closeTab(id: number) {
  const idx = tabs.value.findIndex(t => t.id === id)
  if (idx < 0) return
  if (tabs.value.length === 1) { windows.close(props.win.id); return } // 最后一个 tab = 关窗
  tabs.value.splice(idx, 1)
  if (activeId.value === id) activeId.value = tabs.value[Math.min(idx, tabs.value.length - 1)].id
  loading.value = !(activeTab.value?.loaded)
}
function refresh() {
  const t = activeTab.value
  if (!t) return
  t.seq++
  t.loaded = false
  t.title = '加载中…'
  loading.value = true
  armLoadFallback()
}
// 代理模式经反代打开（新标签同源携带登录态）；普通模式直开目标站
function openExternal() { if (activeTab.value) window.open(activeTab.value.url, '_blank', 'noopener') }
</script>

<template>
  <div ref="rootEl" class="web-app" data-os-webapp>
    <template v-if="src">
      <!-- 融合窗体工具栏：整条可拖动窗口（data-os-drag 事件委托，控件自身不参与） -->
      <div class="os-tb web-bar" data-os-drag>
        <div class="os-tb-title">{{ title }}</div>
        <span v-if="host" class="host">{{ host }}</span>
        <span class="grow" />
        <button class="os-tb-btn" title="刷新" v-html="icons.refresh" @click="refresh" />
        <button class="os-tb-btn" title="在浏览器中打开" v-html="icons.link" @click="openExternal" />
      </div>
      <!-- 标签条：多 tab 时出现；空白区域可拖动窗口，tab 自身拦截双击 -->
      <div v-if="tabs.length > 1" class="tabs" data-os-drag>
        <div v-for="t in tabs" :key="t.id" class="tab" :class="{ active: t.id === activeId }"
             :title="t.title" @click="switchTab(t.id)" @dblclick.stop>
          <span class="tab-title">{{ t.title }}</span>
          <button class="tab-close" title="关闭标签页" @click.stop="closeTab(t.id)">&times;</button>
        </div>
      </div>
      <div class="frame-wrap">
        <div v-if="loading" class="loading">正在加载…</div>
        <iframe
          v-for="t in tabs"
          :key="`${t.id}-${t.seq}`"
          v-show="t.id === activeId"
          :src="t.url"
          :data-tab="t.id"
          class="frame"
          title="网页应用"
          allow="clipboard-read; clipboard-write"
          referrerpolicy="strict-origin-when-cross-origin"
          sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-popups-to-escape-sandbox allow-downloads"
          @load="onTabLoad(t, $event)"
        />
      </div>
    </template>
    <div v-else class="empty-wrap">
      <OsEmpty text="未配置的网页应用" />
    </div>
  </div>
</template>

<style scoped>
.web-app { width: 100%; display: flex; flex-direction: column; }
.web-bar .grow { flex: 1; }
.host { font-size: 12px; color: var(--os-text-secondary); }
.frame-wrap { position: relative; flex: 1; min-height: 0; background: #fff; }
.loading {
  position: absolute; inset: 0; z-index: 1; display: grid; place-items: center;
  color: var(--os-text-secondary); font-size: 13px; background: #fff;
}
.frame { width: 100%; height: 100%; border: none; display: block; }
.empty-wrap { flex: 1; display: grid; place-items: center; }
/* 标签条：浏览器式，仅多 tab 时出现 */
.tabs {
  display: flex; gap: 4px; align-items: flex-end; padding: 4px 8px 0;
  background: rgba(125, 125, 125, .12); overflow-x: auto; flex-shrink: 0;
}
.tab {
  display: flex; align-items: center; gap: 6px; max-width: 180px; min-width: 90px;
  padding: 4px 8px; border-radius: 6px 6px 0 0; cursor: default; user-select: none;
  background: rgba(125, 125, 125, .15); color: var(--os-text-secondary); font-size: 12px;
}
.tab.active { background: #fff; color: var(--os-text, #222); box-shadow: 0 -1px 2px rgba(0, 0, 0, .05); }
.tab-title { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tab-close {
  border: none; background: none; padding: 0 2px; cursor: pointer; line-height: 1;
  font-size: 14px; color: var(--os-text-secondary); border-radius: 3px;
}
.tab-close:hover { background: rgba(125, 125, 125, .25); color: var(--os-text, #222); }
</style>
