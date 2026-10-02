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
  /** 宿主产品版本（可选，构建期注入，如 git tag）：设置「关于」与菜单栏关于弹窗展示；缺省不显示版本 */
  version?: string
}

export type ShellLogin =
  | { mode: 'local' } // OpenOS 自带账密登录
  // 跳转宿主单点登录（url 为宿主登录入口）。passwordUrl = 统一身份系统的账户/
  // 改密自助页：设置-账户「登录与安全」由此改为跳转按钮（本地改密表单仅 local 模式渲染）
  | { mode: 'sso'; url: string; label?: string; passwordUrl?: string; passwordLabel?: string }

/** 应用菜单项（菜单栏「当前应用」下拉里的动作，run 接收窗口参数）。 */
export interface AppMenuItem {
  label: string
  run: () => void
}

export const shell = reactive<{
  brand: ShellBrand
  login: ShellLogin
  /** 菜单栏右侧扩展（单个或多个组件，如门店切换器/业务状态角标），渲染在状态 chip 之前 */
  menubarExtras: Component | Component[] | null
  /** SSO 回跳后经 URL 片段交接令牌的片段名；null 关闭片段交接 */
  tokenFragment: string | null
  /** Dock 应用排序（appId 列表，未列出的排在已列出的之后按服务器顺序）；空 = 服务器顺序 */
  dockOrder: string[]
  /** 壁纸列表（CSS background 值）；null = 内置默认 6 张。注入后设置页同步可选 */
  wallpapers: string[] | null
  /** 宿主注册的应用菜单项（按 appId），与内置应用菜单合并（宿主可覆盖内置应用菜单） */
  appMenus: Record<string, AppMenuItem[]>
  /**
   * 宿主全屏浮层组件（如访客引导/待审批遮罩）：渲染在桌面最顶层（菜单栏/窗口/
   * 通知中心之上），常驻挂载、由组件内部自行决定显隐（v-if）。与 menubarExtras
   * 同理是宿主业务向壳内注入 UI 的扩展点，只是占据整屏层。
   */
  overlay: Component | Component[] | null
}>({
  brand: { name: 'OpenOS', logo: '🖥️', sub: '仿 macOS 网页桌面 · 多用户 · 事件总线' },
  login: { mode: 'local' },
  menubarExtras: null,
  tokenFragment: null,
  dockOrder: [],
  wallpapers: null,
  appMenus: {},
  overlay: null,
})
