// shell.ts — 桌面壳配置单例：宿主应用经 createOpenOS() 注入，组件层只读。
// 独立部署（main.ts 直接 createOpenOS()）时全部保持默认值，行为与框架化之前一致。
import { reactive, type Component } from 'vue'

export interface ShellBrand {
  /** 产品名（登录页标题 / 菜单栏「关于」） */
  name: string
  /** 图标 emoji（登录页头像 / 菜单栏 logo） */
  logo: string
  /** 副标题（登录页） */
  sub: string
}

export type ShellLogin =
  | { mode: 'local' } // OpenOS 自带账密登录
  | { mode: 'sso'; url: string; label?: string } // 跳转宿主单点登录（url 为宿主登录入口）

export const shell = reactive<{
  brand: ShellBrand
  login: ShellLogin
  /** 菜单栏右侧扩展组件（门店切换器/业务状态角标等），渲染在状态 chip 之前 */
  menubarExtras: Component | null
  /** SSO 回跳后经 URL 片段交接令牌的片段名；null 关闭片段交接 */
  tokenFragment: string | null
}>({
  brand: { name: 'OpenOS', logo: '🖥️', sub: '仿 macOS 网页桌面 · 多用户 · 事件总线' },
  login: { mode: 'local' },
  menubarExtras: null,
  tokenFragment: null,
})
