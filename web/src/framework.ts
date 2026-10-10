// framework.ts — OpenOS 宿主应用组合入口与公共 API 面。
//
// OpenOS 定位为通用桌面基础框架：宿主应用（业务系统）不 fork 壳代码，
// 而是在自己的入口调用 createOpenOS() 注入业务应用与登录方式：
//
//   import { createOpenOS } from '@openos/framework'
//   createOpenOS({
//     apps: { dashboard: () => import('./apps/Dashboard.vue') },  // 编译期注册业务 App
//     remoteApps: { market: 'https://…/market', hostVersion: __VER__ }, // App 独立发布货架（可选，见 remote.ts）
//     icons: { dashboard: { svg: '<svg…>' } },                     // 业务图标（可选）
//     login: { mode: 'sso', url: '/api/auth/sso/login' },         // 接宿主单点登录
//     brand: { name: '业务系统', logo: '🏨' },
//     menubarExtras: PropertySwitcher,                             // 菜单栏右侧扩展（可传数组）
//     theme: { accent: '#e91e63' },                                // 主题强调色
//     wallpapers: [ 'linear-gradient(…)' ],                        // 壁纸注入（可选）
//     dockOrder: ['dashboard', 'rooms'],                           // Dock 排序（可选）
//     chrome: { rooms: 'merged' },                                  // 宿主应用窗体形态（可选）
//     appMenus: { rooms: [{ label: '新建房态窗口', run: () => {} }] }, // 应用菜单注入
//     widgets: { weather: { label: '天气', icon: '🌤', component: WeatherWidget } }, // 桌面小组件
//   })
//
// 独立部署时 main.ts 调用 createOpenOS()（无参），行为与框架化之前完全一致。
//
// 本文件同时是公共 API 面：宿主可从 '@openos/framework' 直接导入总线 / API 客户端 /
// 组件库 / 状态仓库，无需深入内部路径。
import { createApp, type App as VueApp, type Component } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import './sdk' // 注入 window.OpenOS 第三方应用 SDK（副作用）
import './styles/base.css'
import { builtinComponents, appChrome } from './stores/apps'
import { loadApp, type RemoteAppsOptions } from './remote'
import { registerIcons, type IconDef } from './desktop/icons'
import { shell, type ShellBrand } from './shell'
import { setToken } from './api/client'
import { registerWidget, type WidgetDef } from './desktop/widgetRegistry'
import { installMenuGuard } from './ui/menu'
import { installKeys } from './ui/keys'
import { installPermDirective } from './ui/perms'
import { installAgentTools, type AgentToolDef } from './desktop/agentTools'
import { installCapabilities, type AppCapabilities } from './desktop/capabilities'
import { installTooltip } from './ui/tooltip'
import { installAppNotifiers } from './desktop/appNotifier'
import { registerDialog, type DialogDef } from './ui/dialogService'
import { initLocale, setLocale, extendMessages, type Locale } from './i18n'

export interface CreateOpenOSOptions {
  /**
   * 编译期注册的宿主应用组件（key = appId），与内置应用合并。值两种形态：
   * 懒加载 loader（本地源码组件），或 null（remote-only：组件唯一来源=货架，
   * 须配 remoteApps；无本地兜底，货架不可达即加载失败——宿主把 App 源码拆到
   * 独立仓时用，本地 dev 可按需只对部分 App 提供 loader 联调）。
   */
  apps?: Record<string, (() => Promise<any>) | null>
  /**
   * App 独立发布货架（可选，协议见 remote.ts）：声明后 apps 里每个 App 的组件
   * 优先从货架加载最新版本（不经宿主发版即可更新），失败自动回落 apps 本地
   * loader；loader 为 null 的 remote-only 条目无兜底，失败即报错。货架条目仅对
   * apps 已注册的 appId 生效（应用的存在与授权仍由服务端 apps 表决定）。
   * 配套构建工具 web/tooling/（deps 库集 / host external+importmap / app 单文件
   * 产物 / catalog 生成），保证远程 App 与宿主共享同一份 Vue/pinia 单实例。
   */
  remoteApps?: RemoteAppsOptions
  /** 注册（或覆盖）应用图标，键为 appId；见 desktop/icons.ts 的 IconDef */
  icons?: Record<string, IconDef>
  /** 登录模式：sso = 登录页跳转宿主单点登录入口；passwordUrl = 统一身份改密自助页 */
  login?: { mode: 'sso'; url: string; label?: string; passwordUrl?: string; passwordLabel?: string }
  /** 品牌覆盖（登录页 / 菜单栏关于） */
  brand?: Partial<ShellBrand>
  /** 菜单栏右侧扩展组件（如业务上下文切换器），渲染在状态 chip 之前；可传数组多槽 */
  menubarExtras?: Component | Component[]
  /**
   * SSO 回跳后令牌经 URL 片段交接的片段名（#<name>=<jwt>）。
   * sso 模式默认 'os-token'；显式传 null 关闭。
   */
  tokenFragment?: string | null
  /** 挂载点，默认 #app */
  root?: string | Element
  /** 主题定制：accent 覆盖全局强调色（按钮/选中/焦点环等派生色自动计算） */
  /**
   * 主题定制（P5 升级）：accent 强调色；dark 初始深浅色（'auto' 跟随系统
   * prefers-color-scheme，'dark'/'light' 强制初始值——用户仍可在设置里切换）；
   * tokens 为 CSS 变量级覆盖（键 = --os- 令牌名，可省略前缀，如
   * { 'radius': '10px', '--os-shadow': '…' }）——换肤/品牌对齐不必 fork 组件库。
   */
  theme?: { accent?: string; dark?: 'auto' | 'dark' | 'light'; tokens?: Record<string, string> }
  /** 壁纸列表（CSS background 值），覆盖内置 6 张；设置页可选列表同步变化 */
  wallpapers?: string[]
  /** Dock 应用排序（appId 列表，未列出的排在其后按服务器顺序） */
  dockOrder?: string[]
  /** 宿主注入的应用菜单项（菜单栏「当前应用」下拉，按 appId；可覆盖内置应用菜单） */
  appMenus?: Record<string, { label: string; run: () => void }[]>
  /** 注册自定义桌面小组件（右键菜单「添加小组件」自动出现） */
  widgets?: Record<string, Omit<WidgetDef, 'type'> & Partial<Pick<WidgetDef, 'type'>>>
  /**
   * 国际化（可选，见 web/src/i18n）：messages 按 locale 深合并覆盖内置词条
   * （自定义「品牌名/提示文案」等），locale 指定初始语言（用户仍可在
   * 系统设置 → 外观里自行切换，按账号持久化）。
   */
  i18n?: { locale?: Locale; messages?: Partial<Record<Locale, Record<string, string>>> }
  /**
   * 声明宿主应用的窗体外观（键 = appId）：'merged' = 融合工具栏窗体（无独立标题栏，
   * 应用自绘工具栏承担标题行，红绿灯悬浮其上）；缺省 'default' 标准标题栏。
   * 与 OpenOS.registerApp 的 chrome 参数同义，供编译期 apps 注册的应用声明。
   */
  chrome?: Record<string, 'default' | 'merged'>
  /**
   * 注册跨应用业务弹窗（键 = 完整弹窗 id，建议 `appId:业务名`）：
   * 应用 A 经 OpenOS.openDialog('roommgmt:checkin', { roomId }) 直接拉起房态管理的
   * 入住弹窗，无需打开其主窗口；弹窗组件认 params/close/cancel 三个 props，
   * close(result) 的结果成为 openDialog 的 Promise 值。与 OpenOS.registerApp 的
   * dialogs 选项同义，供编译期注册。
   */
  dialogs?: Record<string, DialogDef>
  /**
   * 宿主全屏浮层组件（如访客引导遮罩）：常驻渲染在桌面最顶层，组件内部自行
   * 决定显隐（v-if）。替代「宿主自建第二个 Vue 实例叠在 body 上」的旧做法，
   * 浮层与桌面壳共享同一应用上下文（总线/组件库/主题）。
   */
  overlay?: Component | Component[]
  /**
   * 向 goder AI 声明宿主自定义桌面工具（P6 扩展点）：注册后 AI 经 os_app_tools
   * 可见、os_app_tool 调用（工具名自动加 app_<appId>_ 前缀）。应用运行期也可经
   * OpenOS.registerAgentTool 动态注册。
   */
  agentTools?: AgentToolDef[]
  /**
   * 向 goder AI 声明应用能力清单（能力暴露层 CAP，键 = appId）：queries（只读
   * 数据查询，无需打开应用即可被 AI 执行）/ actions（业务操作）/ views（可视化
   * 操作入口 + data-os-cap 锚点高亮）。每个能力三选一提供实现：
   *   run    fn 回调，桌面壳执行（可调宿主自身 API，如 serviceCall）
   *   http   声明式 REST——相对 /api/… 走 OpenOS 登录态、宿主同源后端配
   *          auth:true（数据层零代码暴露，grf-hotel「查空房」类查询首选）
   *   window 打开应用窗口到对应视图（需要 UI 语境的操作）
   *   createOpenOS({
   *     capabilities: { rooms: { queries: [{ name:'vacant', title:'查询今日空房',
   *       http: { url:'/api/rooms/vacant' } }], views: [{ name:'grid',
   *       title:'房态盘', props:{view:'grid'}, anchor:'room-grid' }] } },
   *   })
   * AI 经 os_caps 发现、os_app_query / os_app_call / os_app_ui 执行；未知字段
   * 忽略（旧版 OpenOS 声明不生效但无副作用，双向兼容）。
   */
  capabilities?: Record<string, Omit<AppCapabilities, 'appId'>>
}

/** 应用主题：accent 强调色（派生令牌 color-mix 现算）+ CSS 变量级 tokens 覆盖。 */
function applyTheme(accent?: string, tokens?: Record<string, string>) {
  const s = document.documentElement.style
  if (accent) {
    s.setProperty('--os-accent', accent)
    // 派生色用 color-mix 现算（现代浏览器均支持），无需引入颜色库
    s.setProperty('--os-accent-hover', `color-mix(in srgb, ${accent} 85%, #fff)`)
    s.setProperty('--os-accent-grad', `linear-gradient(180deg, color-mix(in srgb, ${accent} 72%, #fff) 0%, ${accent} 100%)`)
    s.setProperty('--os-accent-soft', `color-mix(in srgb, ${accent} 28%, transparent)`)
    s.setProperty('--os-selected', `color-mix(in srgb, ${accent} 14%, transparent)`)
    s.setProperty('--os-focus-ring', `0 0 0 3.5px color-mix(in srgb, ${accent} 25%, transparent)`)
  }
  // 令牌覆盖：键补全 --os- 前缀（'radius' 与 '--os-radius' 等价）
  for (const [k, v] of Object.entries(tokens || {})) {
    if (!k || v == null) continue
    s.setProperty(k.startsWith('--') ? k : `--os-${k}`, String(v))
  }
}

/** 宿主深浅色策略：'auto' 跟随系统、'dark'/'light' 强制初始值（写入前由
 *  desktop store 尚未挂载，直接落 documentElement 类 + localStorage 缓存键，
 *  store load 时会以服务端/本地值校正）。 */
function applyDarkPolicy(mode?: 'auto' | 'dark' | 'light') {
  if (!mode) return
  const dark = mode === 'auto'
    ? window.matchMedia('(prefers-color-scheme: dark)').matches
    : mode === 'dark'
  document.documentElement.classList.toggle('dark', dark)
  try { localStorage.setItem('openos_dark', dark ? '1' : '0') } catch { /* 存储受限 */ }
}

export function createOpenOS(opts: CreateOpenOSOptions = {}): VueApp | null {
  // 0.5 i18n：宿主词条注入 → 恢复本地缓存语言（响应式 t() 见 src/i18n）
  if (opts.i18n?.messages) {
    for (const [l, dict] of Object.entries(opts.i18n.messages)) extendMessages(l as Locale, dict || {})
  }
  initLocale()
  if (opts.i18n?.locale) setLocale(opts.i18n.locale)
  // 0. 全局右键守卫：默认屏蔽浏览器原生菜单（登录页与桌面全阶段生效），
  //    应用自定义菜单在组件内 stopPropagation 接管，详见 ui/menu.ts
  installMenuGuard()
  // 统一键盘中枢：window capture/bubble/keyup 三链 + sys:key 总线分发（见 ui/keys.ts）
  installKeys()
  // 全局快速 tooltip：接管原生 title 的悬停提示（约 400ms 弹出，见 ui/tooltip.ts）
  installTooltip()
  // 后台应用通知：应用未打开时的消息提醒（聊天等，见 desktop/appNotifier.ts）
  installAppNotifiers()

  // 1. 宿主应用组件与图标注册（remoteApps 存在时每个 loader 包一层货架优先逻辑，
  //    失败/无条目/版本不兼容均回落本地 loader，见 remote.ts；无 remoteApps 行为
  //    不变。loader 为 null 的 remote-only 条目：有 remoteApps 时注册纯货架加载器
  //    （无兜底），否则视为注册错误跳过并告警）
  const localApps = opts.apps || {}
  for (const [id, loader] of Object.entries(localApps)) {
    if (!loader && !opts.remoteApps) {
      console.warn(`[OpenOS] apps.${id} 为 null（remote-only）但未声明 remoteApps，已忽略该注册`)
      continue
    }
    builtinComponents[id] = opts.remoteApps
      ? () => loadApp(id, opts.remoteApps!, loader || undefined)
      : loader!
  }
  if (opts.chrome) Object.assign(appChrome, opts.chrome)
  if (opts.dialogs) {
    for (const [id, def] of Object.entries(opts.dialogs)) registerDialog(id, def)
  }
  if (opts.icons) registerIcons(opts.icons)

  // 2. 壳配置
  if (opts.brand) Object.assign(shell.brand, opts.brand)
  if (opts.login) shell.login = opts.login
  shell.menubarExtras = opts.menubarExtras || null
  if (opts.dockOrder) shell.dockOrder = opts.dockOrder
  if (opts.wallpapers) shell.wallpapers = opts.wallpapers
  if (opts.appMenus) shell.appMenus = opts.appMenus
  shell.overlay = opts.overlay || null
  // 宿主静态声明的 AI 桌面工具（goder 扩展点，P6）
  installAgentTools(opts.agentTools)
  // 宿主能力清单声明（能力暴露层 CAP：AI 经 os_caps 发现、os_app_* 执行）
  installCapabilities(opts.capabilities)
  if (opts.widgets) {
    for (const [type, def] of Object.entries(opts.widgets)) {
      registerWidget({ type, ...def })
    }
  }
  applyTheme(opts.theme?.accent, opts.theme?.tokens)
  applyDarkPolicy(opts.theme?.dark)
  if (opts.tokenFragment !== undefined) {
    shell.tokenFragment = opts.tokenFragment
  } else if (opts.login?.mode === 'sso') {
    shell.tokenFragment = 'os-token'
  }

  // 3. URL 片段令牌交接：宿主 SSO 回跳到 /#os-token=<jwt>，
  //    此处摘出存入 localStorage 并清理地址栏，随后 restore() 正常恢复会话。
  const frag = shell.tokenFragment
  if (frag && location.hash.startsWith('#' + frag + '=')) {
    const t = decodeURIComponent(location.hash.slice(frag.length + 2))
    if (t) setToken(t)
    history.replaceState(null, '', location.pathname + location.search)
  }

  // 桌面壳不允许在 iframe 内启动：应用代理的越界跳转（location.href='/x' 这类
  // 无法被 hook 包装的赋值）会让 SPA 回落把整个桌面塞进代理 iframe，形成套娃
  // 桌面（还会连上总线、恢复同一份窗口状态）。服务端按 Referer 已把此类跳转
  // 307 回代理路径，这里是纵深防御（如目标站设 no-referrer 时的兜底）
  if (window.self !== window.top) {
    document.documentElement.innerHTML = '<head><meta charset="utf-8"></head><body style="margin:0;display:grid;place-items:center;height:100vh;font:14px/1.7 system-ui,sans-serif;color:#666;background:#f5f5f7;text-align:center">OpenOS 桌面不能在 iframe 内打开<br/>此页面来自代理应用的越界跳转，请回到窗口内重新操作</body>'
    return null
  }

  // 4. 挂载桌面壳
  document.title = shell.brand.name
  const app = createApp(App)
  app.use(createPinia())
  // 功能级权限门控：can() 函数 + v-perm 指令（见 ui/perms.ts）
  installPermDirective(app)
  app.mount(opts.root || '#app')

  // PWA：生产构建注册 service worker（public/sw.js——API/总线直通网络、仅静态资源
  // 缓存；dev 不注册避免热更新被缓存干扰）。宿主 dist 需带上 public/（sw.js +
  // manifest + 图标）并保持根路径部署，否则注册失败（静默降级为普通网页）。
  if (import.meta.env.PROD && 'serviceWorker' in navigator) {
    navigator.serviceWorker.register('/sw.js').catch(() => { /* 静默降级：普通网页 */ })
  }
  return app
}

// ---- 公共 API 面：宿主从 '@openos/framework' 直接导入，不深入内部路径 ----
export { bus } from './bus/bus'
export {
  api, fsApi, rawFileUrl, fetchRawBlob, downloadFile,
  getToken, setToken, clearToken,
  setUnauthorizedHandler, setTokenRecovery, ApiError,
} from './api/client'
export type { SessionUser, AppDef, VFile } from './api/client'
export * from './ui'
export { useSession } from './stores/session'
export { useApps } from './stores/apps'
export { useWindows } from './stores/windows'
export { useDesktop, DEFAULT_WALLPAPERS } from './stores/desktop'
export { useNotes, NOTE_COLORS } from './stores/notes'
export { useNotifications, notify } from './stores/notifications'
export type { OsNotification, NotifyInput } from './stores/notifications'
export { useCalendar, todayStr, REMIND_OPTIONS } from './stores/calendar'
export type { CalEvent, RemindOffset, HolidayInfo, CalMark } from './stores/calendar'
export { shell } from './shell'
export type { ShellBrand, ShellLogin, AppMenuItem } from './shell'
export { registerWidget, widgetDefs, widgetDef } from './desktop/widgetRegistry'
export type { WidgetDef } from './desktop/widgetRegistry'
// 能力暴露层（CAP）：宿主/应用向 goder AI 声明能力清单（os_caps / os_app_*）
export { registerCapabilities, unregisterCapabilities, listCapabilities } from './desktop/capabilities'
export type { AppCapabilities, CapDef, CapViewDef, CapHTTPSpec, CapWindowSpec, CapOrigin, CapListing } from './desktop/capabilities'
export { registerIcons } from './desktop/icons'
export type { IconDef } from './desktop/icons'
// i18n：宿主从 '@openos/framework' 直接导入（词条注入/切换/取词）
export { t, setLocale, locale, initLocale, extendMessages, LOCALES, LOCALE_SETTINGS_KEY } from './i18n'
export type { Locale } from './i18n'
