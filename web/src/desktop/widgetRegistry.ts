// 桌面小组件注册表：内置三件（时钟/便签/系统监控）与宿主/第三方自定义小组件
// 走同一注册通道。自定义小组件收到 { widget, update } 两个 prop：
//   widget —— 持久化的条目（c/r 网格坐标 + 自定义字段，随用户跨会话保存）
//   update —— (patch) 合并字段并立即持久化
import type { Component } from 'vue'
import CalendarWidget from './widgets/CalendarWidget.vue'
import StewardWidget from '../apps/steward/StewardWidget.vue'

export interface WidgetDef {
  /** 类型标识（持久化用，保持稳定） */
  type: string
  /** 右键菜单「添加小组件」显示名 */
  label: string
  /** 菜单图标（emoji） */
  icon?: string
  /** 自定义小组件的渲染组件（内置三件由 Widgets.vue 内部渲染，不传） */
  component?: Component
  /** 同类型仅允许一个实例（时钟/监控为 true；便签可钉多张） */
  singleton?: boolean
  /** 新建时写入 widget 条目的默认字段 */
  defaults?: Record<string, any>
  /** 声明占用格数（横向 sc × 纵向 sr）。声明后卡片恒为该尺寸、组件可 100% 撑满；
   *  未声明则按内容自然尺寸自动换算格数，卡片撑满格子、内容居中。 */
  span?: { sc: number; sr: number }
}

const defs = new Map<string, WidgetDef>()

/** 注册一个小组件类型（内置三件在模块加载时注册；宿主经 createOpenOS({widgets}) 注册）。 */
export function registerWidget(def: WidgetDef) {
  defs.set(def.type, def)
}

/** 全部已注册的小组件定义（右键菜单/渲染用）。 */
export function widgetDefs(): WidgetDef[] {
  return [...defs.values()]
}

/** 按类型取定义。 */
export function widgetDef(type: string): WidgetDef | undefined {
  return defs.get(type)
}

// ---- 内置小组件 ----
// 时钟/监控/便签由 Widgets.vue 内部渲染（不传 component）；日历小组件自带
// 组件，经通用 <component :is> 通道渲染（与宿主自定义小组件同一通道）。
registerWidget({ type: 'clock', label: '时钟', icon: '🕐', singleton: true })
registerWidget({ type: 'note', label: '便签', icon: '📝' })
registerWidget({ type: 'monitor', label: '系统监控', icon: '📊', singleton: true })
registerWidget({ type: 'calendar', label: '日历', icon: '📅', singleton: true, component: CalendarWidget })
// 代码管家小组件（自 grf-os 宿主上游化）：待确认/修复中/已修复/失败四数，
// 点标题直达 steward App（有待确认进问题页，否则看修复记录）。
registerWidget({ type: 'steward', label: '代码管家', icon: '🛠', singleton: true, component: StewardWidget })
