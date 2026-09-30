// framework.ts — OpenOS 宿主应用组合入口。
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
//     menubarExtras: PropertySwitcher,                             // 菜单栏右侧扩展
//   })
//
// 独立部署时 main.ts 调用 createOpenOS()（无参），行为与框架化之前完全一致。
import { createApp, type Component } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import './sdk' // 注入 window.OpenOS 第三方应用 SDK（副作用）
import './styles/base.css'
import { builtinComponents } from './stores/apps'
import { registerIcons, type IconDef } from './desktop/icons'
import { shell, type ShellBrand } from './shell'
import { setToken } from './api/client'

export interface CreateOpenOSOptions {
  /** 编译期注册的宿主应用组件（key = appId，值为懒加载 loader），与内置应用合并 */
  apps?: Record<string, () => Promise<any>>
  /** 注册（或覆盖）应用图标，键为 appId；见 desktop/icons.ts 的 IconDef */
  icons?: Record<string, IconDef>
  /** 登录模式：sso = 登录页跳转宿主单点登录入口 */
  login?: { mode: 'sso'; url: string; label?: string }
  /** 品牌覆盖（登录页 / 菜单栏关于） */
  brand?: Partial<ShellBrand>
  /** 菜单栏右侧扩展组件（如业务上下文切换器），渲染在状态 chip 之前 */
  menubarExtras?: Component
  /**
   * SSO 回跳后令牌经 URL 片段交接的片段名（#<name>=<jwt>）。
   * sso 模式默认 'os-token'；显式传 null 关闭。
   */
  tokenFragment?: string | null
  /** 挂载点，默认 #app */
  root?: string | Element
}

export function createOpenOS(opts: CreateOpenOSOptions = {}) {
  // 1. 宿主应用组件与图标注册
  Object.assign(builtinComponents, opts.apps || {})
  if (opts.icons) registerIcons(opts.icons)

  // 2. 壳配置
  if (opts.brand) Object.assign(shell.brand, opts.brand)
  if (opts.login) shell.login = opts.login
  shell.menubarExtras = opts.menubarExtras || null
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
