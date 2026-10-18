// 文件选择模式状态机（fileDialog 的第二职责：嵌入完整文件管理窗充当「打开/存储」对话框）。
// 由 win props 的 picker 请求驱动；窗口仍是完整可拖动的文件管理器，仅底部状态栏换成
// 选择栏（FilePickerBar）、双击文件=确认。外部行为契约（ui/fileDialog 消费方依赖，勿破坏）：
// - 打开即进选择模式（初始目录=defaultDir、save 模式初始文件名=defaultName）；
// - open 模式双击/回车选中即 resolve，save 模式重名第一次点变「替换」、再点才覆盖；
// - 直接关窗 = resolve null（幂等，resolveFileDialog 只生效一次）。
import { computed, onBeforeUnmount, ref, watch, type Ref } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import type { VFile } from '../../api/client'
import { resolveFileDialog, type FileDialogRequest } from '../../ui'

const baseName = (p: string) => p.split('/').pop() || ''

export function useFilePicker(opts: {
  win: WinState
  /** win props 里的 picker 请求（无则本窗口是普通文件管理窗，全部状态为空操作） */
  req?: FileDialogRequest
  /** 当前目录（save 模式拼目标路径；切目录重置「替换」确认态） */
  path: Ref<string>
  /** 当前目录条目（save 模式重名检测） */
  entries: Ref<VFile[]>
  /** 当前选中项（open 模式目标 = 选中的非目录文件） */
  selectedFile: Ref<VFile | null>
}) {
  const windows = useWindows()
  const req = opts.req
  const pickName = ref(req?.defaultName || '')
  const pickOverwrite = ref(false) // save 模式重名：第一次点变「替换」，再点才覆盖
  watch([pickName, opts.path], () => { pickOverwrite.value = false })
  onBeforeUnmount(() => { if (req) resolveFileDialog(req.reqId, null) }) // 关窗=取消（幂等）

  // 选择模式目标：open=选中的非目录文件；save=当前目录 + 输入的文件名
  const pickSel = computed(() => (opts.selectedFile.value && !opts.selectedFile.value.isDir ? opts.selectedFile.value : null))
  const pickTarget = computed(() => {
    if (!req) return ''
    if (req.mode === 'open') return pickSel.value?.path || ''
    const n = pickName.value.trim()
    return n && n !== '.' && n !== '..' ? opts.path.value.replace(/\/$/, '') + '/' + n : ''
  })
  const pickExists = computed(() => {
    if (req?.mode !== 'save' || !pickTarget.value) return false
    return opts.entries.value.some((f) => !f.isDir && f.path === pickTarget.value)
  })
  const canPick = computed(() => !!pickTarget.value)

  function confirmPick() {
    if (!req || !pickTarget.value) return
    // save 模式重名：第一次点变更为「替换」，再点才真正覆盖（macOS 行为）
    if (req.mode === 'save' && pickExists.value && !pickOverwrite.value) { pickOverwrite.value = true; return }
    resolveFileDialog(req.reqId, pickTarget.value)
    windows.close(opts.win.id)
  }
  function cancelPick() {
    if (req) resolveFileDialog(req.reqId, null)
    windows.close(opts.win.id)
  }
  // 双击/打开拦截：open=确认选择；save=带出文件名。返回是否已拦截（父级据此短路）
  function handleOpen(f: VFile): boolean {
    if (!req || f.isDir) return false
    if (req.mode === 'open') confirmPick()
    else pickName.value = baseName(f.path)
    return true
  }

  return { req, pickName, pickOverwrite, pickSel, pickTarget, pickExists, canPick, confirmPick, cancelPick, handleOpen }
}
