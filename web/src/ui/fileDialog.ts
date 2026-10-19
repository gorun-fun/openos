// 文件选择对话框服务：复用「文件」应用窗口承担打开/存储选择。
// 与旧的窗口内 Sheet（OsFilePicker）不同，这里开出的是普通文件管理窗口——
// 可自由拖动/缩放，浏览体验（侧栏/多视图/搜索）与访达完全一致；
// 请求方 await fileDialog(...)，用户在窗口内确认后 Promise 以路径 resolve，
// 取消或直接关窗则 resolve null。
// 应答走 ui/launch 的拉起信封（reqId + 关窗兜底 + 幂等回执三件由 launch 统一
// 承载，本文件不再自建 pending 表）：FilesApp 从窗口 props 的 __launch 取
// reqId、经 resolveLaunch 回执路径。
import { launch } from './launch'

export interface FileDialogRequest {
  /** 应答 id：由 launch 信封注入（FilesApp 侧从 __launch.reqId 填充），调用方无须传 */
  reqId?: string
  mode: 'open' | 'save'
  title?: string
  defaultDir?: string
  defaultName?: string
  /** open 模式按扩展名过滤（小写，不含点）；缺省不过滤 */
  exts?: string[]
}

/** 打开文件选择窗口（复用文件应用）；resolve 为选中的目标路径，取消为 null。 */
export function fileDialog(opts: Omit<FileDialogRequest, 'reqId'>): Promise<string | null> {
  // props 带 picker 保证并发的多次选择各自开窗不去重（launch 的 reqId 唯一，
  // 本就不走 open() 的同参数去重）；窗口尺寸沿旧形态。
  return launch('files', { picker: opts }, {
    title: opts.title || (opts.mode === 'open' ? '打开' : '存储为'),
    width: 880, height: 560,
  }) as Promise<string | null>
}
