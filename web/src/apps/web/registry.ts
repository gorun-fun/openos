// 内置网页应用清单：iframe 直嵌外部站点（组件容器见 WebApp.vue）。
// 收录前提：目标站未设置 X-Frame-Options / CSP frame-ancestors 防嵌；
// 跨域 iframe 的登录态由目标站自身 Cookie 策略与浏览器决定（扫码登录类站点天然适配）。
export interface WebAppDef {
  /** 目标站点地址 */
  url: string
  /** 窗口标题 */
  title: string
}

export const WEB_APPS: Record<string, WebAppDef> = {}
