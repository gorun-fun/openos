// tabs.ts — 代码管家页签矩阵单源：壳组件取 key/label 组装 tabsDef，capabilities
// 的 views（os_app_ui 可视化入口）从同一份定义派生——增删页签两处同步不脱节。
export interface TabMeta {
  key: string
  label: string
  /** 该页签的用户可见性说明（进 AI 能力描述） */
  note?: string
}

export const STEWARD_TABS: Record<string, string> = {
  overview: '总览', versions: '版本', problems: '问题', runs: '修复',
  repos: '仓库', apps: '应用', settings: '设置',
}

/** 页签元数据平铺（壳组装 tabsDef 时的顺序权威）。 */
export function tabMetas(): TabMeta[] {
  return Object.entries(STEWARD_TABS).map(([key, label]) => ({ key, label }))
}
