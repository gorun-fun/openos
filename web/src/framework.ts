// framework.ts — OpenOS 宿主应用组合入口与公共 API 面。
//
// OpenOS 定位为通用桌面基础框架：宿主应用（业务系统）不 fork 壳代码，
// 而是在自己的入口调用 createOpenOS() 注入业务应用与登录方式：
//
//   import { createOpenOS } from '@openos/framework'
//   createOpenOS({
//     apps: { dashboard: () => import('./apps/Dashboard.vue') },  // 编译期注册业务 App
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
import { createApp, type Component } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import './sdk' // 注入 window.OpenOS 第三方应用 SDK（副作用）
import './styles/base.css'
import { builtinComponents, appChrome } from './stores/apps'
import { registerIcons, type IconDef } from './desktop/icons'
import { shell, type ShellBrand } from './shell'
import { setToken } from './api/client'
import { registerWidget, type WidgetDef } from './desktop/widgetRegistry'
import { installMenuGuard } from './ui/menu'

export interface CreateOpenOSOptions {
  /** 编译期注册的宿主应用组件（key = appId，值为懒加载 loader），与内置应用合并 */
  apps?: Record<string, () => Promise<any>>
  /** 注册（或覆盖）应用图标，键为 appId；见 desktop/icons.ts 的 IconDef */
  icons?: Record<string, IconDef>
  /** 登录模式：sso = 登录页跳转宿主单点登录入口 */
  login?: { mode: 'sso'; url: string; label?: string }
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
  theme?: { accent?: string }
  /** 壁纸列表（CSS background 值），覆盖内置 6 张；设置页可选列表同步变化 */
  wallpapers?: string[]
  /** Dock 应用排序（appId 列表，未列出的排在其后按服务器顺序） */
  dockOrder?: string[]
  /** 宿主注入的应用菜单项（菜单栏「当前应用」下拉，按 appId；可覆盖内置应用菜单） */
  appMenus?: Record<string, { label: string; run: () => void }[]>
  /** 注册自定义桌面小组件（右键菜单「添加小组件」自动出现） */
  widgets?: Record<string, Omit<WidgetDef, 'type'> & Partial<Pick<WidgetDef, 'type'>>>
  /**
   * 声明宿主应用的窗体外观（键 = appId）：'merged' = 融合工具栏窗体（无独立标题栏，
   * 应用自绘工具栏承担标题行，红绿灯悬浮其上）；缺省 'default' 标准标题栏。
   * 与 OpenOS.registerApp 的 chrome 参数同义，供编译期 apps 注册的应用声明。
   */
  chrome?: Record<string, 'default' | 'merged'>
}

/** 应用主题强调色：设置 --os-accent 及其派生令牌（选中/焦点/渐变），立即生效。 */
function applyTheme(accent: string) {
  if (!accent) return
  const s = document.documentElement.style
  s.setProperty('--os-accent', accent)
  // 派生色用 color-mix 现算（现代浏览器均支持），无需引入颜色库
  s.setProperty('--os-accent-hover', `color-mix(in srgb, ${accent} 85%, #fff)`)
  s.setProperty('--os-accent-grad', `linear-gradient(180deg, color-mix(in srgb, ${accent} 72%, #fff) 0%, ${accent} 100%)`)
  s.setProperty('--os-accent-soft', `color-mix(in srgb, ${accent} 28%, transparent)`)
  s.setProperty('--os-selected', `color-mix(in srgb, ${accent} 14%, transparent)`)
  s.setProperty('--os-focus-ring', `0 0 0 3.5px color-mix(in srgb, ${accent} 25%, transparent)`)
}

export function createOpenOS(opts: CreateOpenOSOptions = {}) {
  // 0. 全局右键守卫：默认屏蔽浏览器原生菜单（登录页与桌面全阶段生效），
  //    应用自定义菜单在组件内 stopPropagation 接管，详见 ui/menu.ts
  installMenuGuard()

  // 1. 宿主应用组件与图标注册
  Object.assign(builtinComponents, opts.apps || {})
  if (opts.chrome) Object.assign(appChrome, opts.chrome)
  if (opts.icons) registerIcons(opts.icons)

  // 2. 壳配置
  if (opts.brand) Object.assign(shell.brand, opts.brand)
  if (opts.login) shell.login = opts.login
  shell.menubarExtras = opts.menubarExtras || null
  if (opts.dockOrder) shell.dockOrder = opts.dockOrder
  if (opts.wallpapers) shell.wallpapers = opts.wallpapers
  if (opts.appMenus) shell.appMenus = opts.appMenus
  if (opts.widgets) {
    for (const [type, def] of Object.entries(opts.widgets)) {
      registerWidget({ type, ...def })
    }
  }
  if (opts.theme?.accent) applyTheme(opts.theme.accent)
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

  // 4. 挂载桌面壳
  document.title = shell.brand.name
  const app = createApp(App)
  app.use(createPinia())
  app.mount(opts.root || '#app')
  return app
}

// ---- 公共 API 面：宿主从 '@openos/framework' 直接导入，不深入内部路径 ----
export { bus } from './bus/bus'
export {
  api, fsApi, rawFileUrl, fetchRawBlob, downloadFile,
  getToken, setToken, clearToken,
  setUnauthorizedHandler, ApiError,
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
export type { CalEvent, RemindOffset, HolidayInfo } from './stores/calendar'
export { shell } from './shell'
export type { ShellBrand, ShellLogin, AppMenuItem } from './shell'
export { registerWidget, widgetDefs, widgetDef } from './desktop/widgetRegistry'
export type { WidgetDef } from './desktop/widgetRegistry'
export { registerIcons } from './desktop/icons'
export type { IconDef } from './desktop/icons'
