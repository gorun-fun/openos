// 跨应用拉起服务（deep-link 协议）：泛化 fileDialog 的 reqId+pending 模式为通用能力。
// 调用方 OpenOS.launch(appId, props) 打开目标应用窗口并 await 业务结果；目标应用从
// 窗口 props 的保留键 __launch 识别拉起态，完成时 resolveLaunch(reqId, result) 回执。
// 用户直接关窗（或目标应用不感知本协议）由框架兜底 resolve null——launch 对任何
// 应用都不会悬空：感知应用回传业务结果，非感知应用退化为「等窗口关闭」。
import { watch } from 'vue'
import { useWindows } from '../stores/windows'

/** launch 调用选项（均可选）。 */
export interface LaunchOptions {
  /** 窗口标题（缺省用应用名） */
  title?: string
  width?: number
  height?: number
  /** 调用方标识（写入 __launch.from 供目标应用区分来源；缺省取当前焦点窗口的应用 id） */
  from?: string
}

/** 框架注入窗口 props 保留键 __launch 的信封。 */
export interface LaunchEnvelope {
  /** 本次拉起的应答 id（回执凭据） */
  reqId: string
  /** 调用方应用标识（自动推断时可能为空字符串） */
  from?: string
}

interface Pending {
  resolve: (v: any) => void
  winId: string | null
}

const pending = new Map<string, Pending>()
let seq = 0
let watching = false

/** 关窗兜底（懒装一次）：拉起窗口被关闭而未应答时 resolve null。 */
function installCloseWatch() {
  if (watching) return
  watching = true
  const windows = useWindows()
  watch(
    () => windows.list.map((w) => w.id),
    (ids) => {
      const alive = new Set(ids)
      for (const [reqId, p] of [...pending]) {
        if (p.winId && !alive.has(p.winId)) {
          pending.delete(reqId)
          p.resolve(null)
        }
      }
    },
  )
}

/**
 * 拉起目标应用窗口并等待业务结果（跨应用联动的独立窗口形态；模态浮层形态见
 * openDialog）。目标应用感知协议时经 resolveLaunch(reqId, result) 回传任意 JSON；
 * 取消 / 关窗 / 应用不存在 / 应用不感知协议时 resolve null。props 为应用自定义
 * 业务参数（deep-link 约定键如 path/url/view），框架只追加保留键 __launch——
 * 每次 launch 的 reqId 唯一，同应用同参数的多次拉起各开各的窗口、各自应答
 * （不走 open() 的同参数去重）。
 */
export function launch(appId: string, props?: any, opts?: LaunchOptions): Promise<any> {
  return new Promise((resolve) => {
    const reqId = `ln-${Date.now()}-${++seq}`
    const windows = useWindows()
    const from = opts?.from ?? windows.active?.appId ?? ''
    // 只给一个维度时另一个传 0：open() 对 0/缺省回退应用默认尺寸
    const size = opts?.width || opts?.height ? { w: opts?.width || 0, h: opts?.height || 0 } : undefined
    const winId = windows.open(appId, { ...props, __launch: { reqId, from } }, opts?.title, size)
    if (!winId) {
      resolve(null) // 应用不存在（未注册/未授权/已卸载）
      return
    }
    installCloseWatch()
    pending.set(reqId, { resolve, winId })
  })
}

/** 目标应用回执：完成传业务结果，取消传 null（undefined 归一为 null）。幂等，重复调用只生效一次。 */
export function resolveLaunch(reqId: string, result?: any) {
  const p = pending.get(reqId)
  if (!p) return
  pending.delete(reqId)
  p.resolve(result === undefined ? null : result)
}

/** 目标应用侧：从窗口 props 提取拉起信封（launchOf(props.win.props)）；普通 open() 打开时为 null。 */
export function launchOf(winProps: any): LaunchEnvelope | null {
  const env = winProps?.__launch
  return env && typeof env.reqId === 'string' ? env : null
}
