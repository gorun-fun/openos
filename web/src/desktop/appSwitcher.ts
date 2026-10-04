// 应用切换器（⌘/Alt+Tab 的 macOS 风格 overlay）：状态单例 + 键盘驱动逻辑。
// Desktop.vue 的 onShortcut 调 open/step，AppSwitcher.vue 渲染 overlay；
// 松开修饰键（Alt/Meta keyup）提交选择。浏览器能拿到 Alt+Tab（macOS 的 ⌘Tab
// 与 Windows 的 Win+Tab 被系统吃掉，Alt 最稳；全屏 kiosk 下 Windows 亦可达）。
import { reactive } from 'vue'
import { useWindows } from '../stores/windows'

export const switcherState = reactive({
  open: false,
  /** 参与切换的应用（按最近使用在前：窗口 z 序去重） */
  appIds: [] as string[],
  idx: 0,
})

/** 按窗口 z 序取应用去重列表（最近使用在前）。 */
function recentAppIds(): string[] {
  const wins = useWindows()
  const ids: string[] = []
  for (const w of [...wins.list].sort((a, b) => b.z - a.z)) {
    if (!ids.includes(w.appId)) ids.push(w.appId)
  }
  return ids
}

/** 打开切换器（首次按下 Tab）；单应用时无切换语义，直接聚焦其最新窗口。 */
export function switcherOpen() {
  const ids = recentAppIds()
  if (ids.length < 2) {
    focusApp(ids[0])
    return
  }
  switcherState.appIds = ids
  switcherState.idx = 1 // macOS 语义：第一次按下即指向「上一个」应用
  switcherState.open = true
}

/** Tab/方向键步进（dir=±1，循环）。 */
export function switcherStep(dir: number) {
  if (!switcherState.open) return
  const n = switcherState.appIds.length
  switcherState.idx = (switcherState.idx + dir + n) % n
}

export function switcherCancel() {
  switcherState.open = false
}

/** 提交：聚焦所选应用的最新窗口（focus 自带取消最小化）。 */
export function switcherCommit() {
  if (!switcherState.open) return
  const id = switcherState.appIds[switcherState.idx]
  switcherState.open = false
  focusApp(id)
}

function focusApp(appId: string | undefined) {
  if (!appId) return
  const wins = useWindows()
  const top = [...wins.list].filter((w) => w.appId === appId).sort((a, b) => b.z - a.z)[0]
  if (top) wins.focus(top.id)
}
