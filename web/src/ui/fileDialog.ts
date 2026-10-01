// 文件选择对话框服务：复用「文件」应用窗口承担打开/存储选择。
// 与旧的窗口内 Sheet（OsFilePicker）不同，这里开出的是普通文件管理窗口——
// 可自由拖动/缩放，浏览体验（侧栏/多视图/搜索）与访达完全一致；
// 请求方 await fileDialog(...)，用户在窗口内确认后 Promise 以路径 resolve，
// 取消或直接关窗则 resolve null。
import { useWindows } from '../stores/windows'

export interface FileDialogRequest {
  reqId: string
  mode: 'open' | 'save'
  title?: string
  defaultDir?: string
  defaultName?: string
  /** open 模式按扩展名过滤（小写，不含点）；缺省不过滤 */
  exts?: string[]
}

const pending = new Map<string, (path: string | null) => void>()
let seq = 0

/** 打开文件选择窗口（复用文件应用）；resolve 为选中的目标路径，取消为 null。 */
export function fileDialog(opts: Omit<FileDialogRequest, 'reqId'>): Promise<string | null> {
  const req: FileDialogRequest = { reqId: `fd-${Date.now()}-${++seq}`, ...opts }
  return new Promise((resolve) => {
    pending.set(req.reqId, resolve)
    const windows = useWindows()
    // props 带 reqId 保证并发的多次选择各自开窗不去重
    windows.open('files', { picker: req }, req.title || (req.mode === 'open' ? '打开' : '存储为'), { w: 880, h: 560 })
  })
}

/** 文件窗口回执：确认（带目标路径）或取消（null）。幂等，重复调用只生效一次。 */
export function resolveFileDialog(reqId: string, path: string | null) {
  const fn = pending.get(reqId)
  if (!fn) return
  pending.delete(reqId)
  fn(path)
}
