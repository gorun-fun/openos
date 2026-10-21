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
import { OsEmpty, icons, toast } from '../../ui'
import { handleExternalKey } from '../../ui/keys'
import { installPxTopBridge } from './pxTop'
import { WEB_APPS } from './registry'

const props = defineProps<{ win: WinState }>()
const windows = useWindows()
const apps = useApps()

const def = WEB_APPS[props.win.appId]
// 动态注册的网页应用：config = {"kind":"web"|"proxy","url":…[,desktopProxy]}，地址与
// 名称存在 apps 表。proxy 形态（应用代理）：iframe 指向服务端反代 /app/<id>/<目标>，由
// 服务端剥掉 X-Frame-Options/CSP 等拒绝嵌入的响应头并重写资源地址，可嵌入禁止 iframe
// 的站点。desktopProxy（可选，proxy 形态专属）：桌面客户端模式也走服务端反代、不直连
// ——用于"壳可嵌但内部内容 iframe 带防嵌头"的站点（网易云音乐：/discover 等内容页
// X-Frame-Options: sameorigin，祖先链含 OpenOS 源必被拒；CDP Fetch 改头晚于判定、
// fulfillResponse 该版本不可用，客户端层无解，只能回反代）；未声明的 proxy 应用在
// 桌面模式直连真实地址（美团等微前端容器依赖直连形态）
const dyn = computed(() => apps.byId(props.win.appId))
const dynCfg = computed<{ kind?: string; url?: string; desktopProxy?: boolean; noSandbox?: boolean }>(() => {
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
// 桌面端形态（openos-desktop 经 CDP 在每个新文档注入只读标记）：未声明 desktopProxy
// 的代理应用不经服务端反代、iframe 直连真实地址——Chrome 以 --disable-web-security +
// 关闭站点隔离启动，跨源 iframe 不被同源策略/X-Frame-Options/CSP frame-ancestors 拦截，
// 且携带真实浏览器登录态，跨域问题随之消解。惰性读取，晚注入也能生效
const desktopMode = () => !!(window as any).__OPENOS_DESKTOP__
// 代理形态（浏览器模式的 proxy 应用，或桌面端声明 desktopProxy 的）：页面经
// /app/<id>/ 反代落在本源，其数据（存储/Cookie）也在本源、有明确应用边界——
// 清除缓存按钮只对该形态呈现（直连 iframe 的数据在目标站源，本源无从清起）
const isProxy = computed(() => dynCfg.value.kind === 'proxy' && (!desktopMode() || !!dynCfg.value.desktopProxy))
const src = computed(() => {
  if (!url.value) return ''
  if (dynCfg.value.kind === 'proxy' && (!desktopMode() || dynCfg.value.desktopProxy)) return `/app/${props.win.appId}/${pxCompact(url.value)}`
  return url.value
})

// 桌面端形态的 iframe 仅「直连的代理应用」（kind=proxy 且未声明 desktopProxy）不加
// sandbox：美团等微前端容器（WebShell/new-workbench）会在 sandbox 语境下判定环境异常，
// 上报「目标路由不合法」整页 404（实测摘掉即恢复），其防走失（framebusting
// top.location 自拔）由桌面客户端监听主帧导航兜底回捕。其余形态（含 desktopProxy 走
// 反代的、普通 web 的）桌面端仍带 sandbox（同浏览器模式）：这类站点 framebusting 时
// 拔走主帧，客户端回捕虽能拉回但整页重载=窗口全丢，观感即「打开闪退」（网易云音乐
// 实测踩中），sandbox 从浏览器层面直接挡住导航。
// 浏览器模式同样支持按应用放开：config.noSandbox=true 时去 sandbox——sandbox 帧内
// JS 导航 parent/top 一律被 Chrome 拒（仅许自身/子孙），美团 settoken 页登录成功后
// window.parent.location 跳回业务页的链路在 sandbox 下 SecurityError 静默卡死
//（实测）；放开后防走失由 hook __osPxNav__ + checkAnchor 锚定回正兜底，仅管理员
// 对可信站点显式配置
const sandboxAttr = computed(() =>
  dynCfg.value.kind === 'proxy' && !dynCfg.value.desktopProxy && (desktopMode() || dynCfg.value.noSandbox) ? undefined : 'allow-scripts allow-same-origin allow-forms allow-popups allow-popups-to-escape-sandbox allow-downloads')

// ---- 桌面端专用：美团系 ME 微前端的父容器握手应答。子页向父容器发
// {eventType:"hotel-query-container", signature:"ME_IFRAME_…"} 探测，3s 无应答则按
// 环境异常处理（菜单不初始化等）。应答须以目标站自身 origin 发送才能过其
// isValidHost 校验——SOP 已关，经 e.source.eval 在对方 realm 里 window.postMessage
// 给自己即可让 event.origin 变成目标站；origin 字段填 OpenOS 源（其后续
// postMessageToParentWindow 的 targetOrigin 用）----
function onDeskHandshake(e: MessageEvent) {
  if (!desktopMode()) return
  const d: any = e.data
  if (!d || d.eventType !== 'hotel-query-container' || !d.signature) return
  try {
    const resp = { eventType: 'hotel-container-response', iframeInSystem: 'me', origin: location.origin, signature: d.signature }
    ;(e.source as any).eval(`window.postMessage(${JSON.stringify(resp)},"*")`)
  } catch { /* 跨 realm eval 失败则放弃（页面可能已跳转） */ }
}

// ---- iframe 内全局快捷键转发（代理 hook.js / 桌面客户端注入脚本 → postMessage）----
// iframe 聚焦后宿主 window 收不到其内部按键，F4 / ⌘·Ctrl+Space / Win·⌘ 单击等
// 系统级组合由注入脚本识别后转发到父页；只接受来自本窗体 iframe 的消息
// （e.source 须是某个 tab 的 contentWindow，防任意第三方页伪造），交统一键盘中枢
// 走系统层分发。
function onIframeKey(e: MessageEvent) {
  const d: any = e.data
  if (!d || d.type !== 'openos:key' || !d.key) return
  if (!tabs.value.some((t) => winOf(t) === e.source)) return
  handleExternalKey({
    type: d.ev === 'up' ? 'up' : 'down',
    key: d.key, code: d.code || '',
    alt: !!d.alt, ctrl: !!d.ctrl, meta: !!d.meta, shift: !!d.shift,
    soloMeta: !!d.soloMeta,
  })
}

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
onBeforeUnmount(() => { clearTimeout(loadTimer); clearInterval(anchorTimer); window.removeEventListener('message', onDeskHandshake); window.removeEventListener('message', onIframeKey) })

onMounted(() => {
  if (url.value) windows.setTitle(props.win.id, title.value)
  // top 代理桥：站点（网易云登录体系等）把脚本与请求提升到顶层窗口执行，桥把
  // 这些顶层动态构造的目标站地址重映射回代理路径（幂等，首个网页应用窗口安装）
  installPxTopBridge()
  window.addEventListener('message', onDeskHandshake)
  window.addEventListener('message', onIframeKey)
  // 代理页 hook 的越权导航代执行：外层 iframe 带 sandbox（防 framebusting），sandbox
  // 内文档无权导航子帧（网易云音乐壳页导航内容 iframe 必抛 SecurityError）——hook
  // 捕获后经同源 parent 转发到顶层（此处，非 sandbox，有权导航任何同源帧）。挂
  // window 上幂等覆盖，多窗口共用；窗口关闭后残留无引用无害
  ;(window as any).__osPxNav__ = (loc: Location, navUrl: string) => {
    try { loc.href = navUrl } catch { /* 帧可能已销毁 */ }
  }
  anchorTimer = window.setInterval(checkAnchorNow, 1500)
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

// ---- 桌面端防「子 iframe 顶掉容器」：美团等微前端的子页会经 parent.location（或
// pushState 改父地址——不触发 load 事件）把带菜单的容器页整个替换成内层页面（菜单
// 消失、只剩内容区）。锚定规则：本窗体成功加载过与配置地址同源的页面（稳态）后，
// 一旦发现帧文档落在其他源，自动导航回配置地址。触发两路：iframe load 事件 +
// 1.5s 周期巡检（捕获无 load 事件的地址篡改）；冷却 5s 防抖。登录/SSO 的跨源跳转
// 发生在达到稳态之前，不受影响 ----
const anchorHost = computed(() => { try { return new URL(url.value).host } catch { return '' } })
let anchorArmed = false
let lastRestore = 0
let anchorTimer = 0
function restoreAnchor() {
  const now = Date.now()
  if (now - lastRestore < 5000) return
  lastRestore = now
  // 注意：接管只改帧的 location，tab.url 仍是容器地址——seq++ 强制重建 iframe
  const tab = tabs.value.find(x => x.id === activeId.value)
  if (!tab) return
  tab.url = src.value
  tab.seq++
  tab.loaded = false
  tab.title = '加载中…'
  loading.value = true
  armLoadFallback()
}
function checkAnchorNow() {
  if (!desktopMode() || !anchorHost.value || !src.value) return
  // 身份全局桥自愈：注入 shim 捕获到该站新的身份全局（partnerId 等）时置脏，
  // 刷新一次本窗让依赖它的初始化（美团菜单 saga 等）拿到数据
  const G = (window as any).__OSD_GLOBALS__
  if (G && G.__dirty === anchorHost.value && Date.now() - lastRestore >= 5000) {
    G.__dirty = null
    lastRestore = Date.now()
    const tab = tabs.value.find(x => x.id === activeId.value)
    if (tab) {
      tab.seq++
      tab.loaded = false
      tab.title = '加载中…'
      loading.value = true
      armLoadFallback()
    }
    return
  }
  if (!anchorArmed) return
  const f = rootEl.value?.querySelector<HTMLIFrameElement>('iframe.frame')
  if (!f) return
  let host = ''
  try { host = new URL(f.contentWindow!.location.href).host } catch { return }
  if (host !== anchorHost.value) restoreAnchor()
}
function checkAnchor(ev: Event) {
  if (!desktopMode() || !anchorHost.value) return
  let host = ''
  try { host = new URL((ev.target as HTMLIFrameElement).contentWindow!.location.href).host } catch { return }
  if (host === anchorHost.value) { anchorArmed = true; return }
  if (anchorArmed) checkAnchorNow()
}

function fallbackTitle(t: Tab): string {
  // 代理路径 /app/<id>/<scheme>/<host>/… 取目标 host；普通 URL 直接取 host
  const parts = t.url.split('/')
  if (parts[0] === '' && parts[1] === 'app' && parts.length > 4) return parts[4]
  try { return new URL(t.url, location.origin).host } catch { return t.url.slice(0, 24) }
}
function onTabLoad(t: Tab, ev: Event) {
  t.loaded = true
  checkAnchor(ev)
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

// ---- 清除缓存：按应用边界清扫本源的代理数据后整窗刷新 ----
// 代理应用落在 OpenOS 源的数据三类，各有明确归属：
// ① hook 存储隔离门面的 __px_<appid>:: 前缀键（localStorage + sessionStorage）；
// ② 代理 Cookie：服务端 rewriteSetCookie 把目标站 Cookie 的 Path 收归
//    /app/<id>/… 作用域（登录态/票据都在这，清掉即回到未登录——配置注入的
//    MEITUAN_COOKIE 等由服务端每请求重新展开、刷新即恢复）；
// ③ Service Worker：hook 已禁注册，防御性扫掉历史遗留的 /app/<id>/ 作用域注册。
// Cookie 的 Path 属性不可枚举（document.cookie 只给名字），从各 tab 的 iframe
// （代理同源可读）收集可见名字，再按其代理路径的逐级目录前缀写过期删除——
// 命中真实 Path 才有效，写偏是无害空操作。前缀之外还清 Path=/：站点脚本经顶层
// 桥（pxTopBridge 语境无 hook 改写）会把 Cookie 直接种到宿主源根（美团 WM_* /
// WEBDFPID 实测如此，设备指纹类不清就不是完整重置）；宿主前端自身不写 JS 可见
// Cookie（服务端 Cookie 均 HttpOnly 或窄路径），根路径上可见的即站点泄漏，可安全
// 清除。IndexedDB / CacheStorage 的库名无归属信息且 SW 已禁，不清。
async function clearCache() {
  const appId = props.win.appId
  // ① 前缀存储键
  const ns = `__px_${appId}::`
  for (const store of [window.localStorage, window.sessionStorage]) {
    const keys: string[] = []
    for (let i = 0; i < store.length; i++) {
      const k = store.key(i)
      if (k && k.startsWith(ns)) keys.push(k)
    }
    keys.forEach((k) => store.removeItem(k))
  }
  // ② 应用路径作用域 Cookie（含根路径泄漏的）
  const names = new Set<string>()
  const paths = new Set<string>([`/app/${appId}/`, '/'])
  for (const t of tabs.value) {
    try {
      const d = winOf(t)?.document
      if (d) d.cookie.split(';').forEach((c: string) => { const n = c.split('=')[0].trim(); if (n) names.add(n) })
    } catch { /* iframe 尚未挂载或已销毁，尽力而为 */ }
    let p = ''
    try { p = new URL(t.url, location.origin).pathname } catch { continue }
    const segs = p.split('/')
    if (segs[1] !== 'app' || segs[2] !== appId) continue
    let acc = ''
    for (let i = 1; i < segs.length; i++) { acc += `${segs[i]}/`; paths.add(acc) }
  }
  names.forEach((n) => paths.forEach((p) => {
    document.cookie = `${n}=; Path=${p}; Expires=Thu, 01 Jan 1970 00:00:00 GMT; Max-Age=0`
  }))
  // ③ 作用域内 SW 注册（防御性，正常应为空）
  try {
    if (navigator.serviceWorker?.getRegistrations) {
      const regs = await navigator.serviceWorker.getRegistrations()
      const base = `${location.origin}/app/${appId}/`
      regs.forEach((r) => { if (r.scope.startsWith(base)) r.unregister() })
    }
  } catch { /* 无 SW 支持或不可用 */ }
  // 整窗重建（全部 tab 重新加载）并反馈
  tabs.value.forEach((t) => { t.seq++; t.loaded = false; t.title = '加载中…' })
  loading.value = true
  armLoadFallback()
  toast('已清除本应用的存储与 Cookie，正在刷新', { type: 'success' })
}

// ---- 桌面端登录辅助：跨站会话 Cookie（未标注 SameSite）在第三方 iframe 上下文里
// 无法落地/携带，站内登录常常卡在 settoken 一类环节。桌面端改在顶层独立窗口（第一方
// 上下文）完成登录/SSO/设 Cookie——桌面客户端会把 jar 统一归一为 SameSite=None，登录
// 完成回到窗体内刷新即恢复会话。直连形态的 proxy 应用：Cookie 归一后 iframe 内直接
// 生效；desktopProxy 形态：客户端镜像循环（desktop/main.go mirrorLoop）会把 jar 里
// 目标域 Cookie 推到代理路径，同样刷新即恢复。----
const loginAvailable = computed(() => desktopMode() && dynCfg.value.kind === 'proxy' && !!url.value)
function desktopLogin() {
  if (!url.value) return
  const w = window.open(url.value, '_blank')
  if (!w) return
  let settle = 0
  const timer = window.setInterval(() => {
    let done = false
    try {
      done = w.closed || (settle = /\/(login|settoken|passport|oauth|signin)/i.test(w.location.href) ? 0 : settle + 1) >= 4
    } catch { /* 跨源跳转完成前可能读不到，继续等 */ }
    if (!done) return
    window.clearInterval(timer)
    // 留出 settoken 收尾时间再关窗并刷新页内标签
    window.setTimeout(() => { try { if (!w.closed) w.close() } catch { /* 忽略 */ } }, 1500)
    refresh()
  }, 800)
  // 5 分钟兜底停检（用户可能未完成登录）
  window.setTimeout(() => window.clearInterval(timer), 5 * 60 * 1000)
}
</script>

<template>
  <div ref="rootEl" class="web-app" data-os-webapp>
    <template v-if="src">
      <!-- 融合窗体工具栏：整条可拖动窗口（data-os-drag 事件委托，控件自身不参与） -->
      <div class="os-tb web-bar" data-os-drag>
        <div class="os-tb-title">{{ title }}</div>
        <span v-if="host" class="host">{{ host }}</span>
        <span class="grow" />
        <button v-if="loginAvailable" class="os-tb-btn" title="登录（在独立窗口完成，成功后自动回到本页）" v-html="icons.user" @click="desktopLogin" />
        <button class="os-tb-btn" title="刷新" v-html="icons.refresh" @click="refresh" />
        <button class="os-tb-btn" title="在浏览器中打开" v-html="icons.link" @click="openExternal" />
        <button v-if="isProxy" class="os-tb-btn" title="清除缓存（本应用的存储与 Cookie）并刷新" v-html="icons.eraser" @click="clearCache" />
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
          :sandbox="sandboxAttr"
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
