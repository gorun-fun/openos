// 跨应用弹窗服务：应用 A 一行代码拉起应用 B 注册的业务弹窗（模态浮层），
// 无需打开 B 的主窗口，调用方以 Promise 拿业务结果（模式同 ui/fileDialog 的 reqId 应答）。
//
//   // 房态管理注册（随应用注册，自动带 appId 前缀，或独立 registerDialog）：
//   OpenOS.registerApp('roommgmt', { ..., dialogs: { checkin: { component: CheckInDialog, title: '办理入住', width: 520 } } })
//   // 订单应用调用：
//   const result = await OpenOS.openDialog('roommgmt:checkin', { roomId, orderId })
//
// 弹窗组件声明三个 props：params（调用入参）、close(result)（完成并回传）、cancel()（取消）。
// 渲染宿主见 DialogHost.vue（系统级模态浮层，z 基准 1e6 恒高于窗口层）。
import { defineAsyncComponent, reactive } from 'vue'

export interface DialogDef {
  /** 弹窗组件；也可传 () => import('...') 懒加载 loader */
  component: any
  /** 缺省标题（openDialog 的 opts.title 可覆盖） */
  title?: string
  /** 卡片宽度 px，缺省 480 */
  width?: number
}

export interface DialogOpenOpts {
  title?: string
  width?: number
  /** Esc 关闭（触发 cancel），缺省 true */
  escClose?: boolean
  /** 点遮罩关闭（触发 cancel），缺省 true */
  backdropClose?: boolean
}

export interface DialogState {
  id: string          // 实例 id（dl-1）
  dialogId: string    // 注册 id（如 roommgmt:checkin）
  params: any
  title?: string
  width: number
  escClose: boolean
  backdropClose: boolean
  z: number
  close: (result?: any) => void
  cancel: () => void
}

// 弹窗注册表：id -> 定义（registerApp 的 dialogs 自动以 `${appId}:${name}` 前缀落进来）
export const dialogRegistry = new Map<string, DialogDef>()

// 打开中的弹窗栈（支持叠开，后开的在上）
export const dialogStack = reactive<DialogState[]>([])

let seq = 0
// 窗口 z 从 10 起随焦点递增，弹窗层用 1e6 基准恒居其上（会话内窗口焦点次数不可能追到 1e6）
const Z_BASE = 1_000_000

/** 注册一个可跨应用拉起的弹窗（id 建议 `appId:业务名`）。重复注册覆盖；loader 在注册时即解析。 */
export function registerDialog(id: string, def: DialogDef) {
  dialogRegistry.set(id, {
    ...def,
    component: isLoader(def.component) ? defineAsyncComponent(def.component) : def.component,
  })
}

/** 注销弹窗；仍在打开中的实例按取消关闭。 */
export function unregisterDialog(id: string) {
  dialogRegistry.delete(id)
  for (const d of [...dialogStack]) if (d.dialogId === id) d.cancel()
}

/**
 * 拉起弹窗并等待结果：弹窗内 close(result) → resolve(result)；cancel()/Esc/点遮罩 → resolve(undefined)。
 * 未注册的 id 直接 reject，调用方自行 catch。
 */
export function openDialog(id: string, params?: any, opts: DialogOpenOpts = {}): Promise<any> {
  const def = dialogRegistry.get(id)
  if (!def) return Promise.reject(new Error(`未注册的弹窗: ${id}`))
  return new Promise((resolve) => {
    const inst: DialogState = {
      id: `dl-${++seq}`,
      dialogId: id,
      params,
      title: opts.title ?? def.title,
      width: opts.width ?? def.width ?? 480,
      escClose: opts.escClose !== false,
      backdropClose: opts.backdropClose !== false,
      z: Z_BASE + seq,
      close: (result: any) => settle(inst, () => resolve(result)),
      cancel: () => settle(inst, () => resolve(undefined)),
    }
    dialogStack.push(inst)
  })
}

// 幂等收口：close/cancel/Esc/遮罩多路触发只生效一次
function settle(inst: DialogState, fn: () => void) {
  const i = dialogStack.indexOf(inst)
  if (i < 0) return
  dialogStack.splice(i, 1)
  fn()
}

// 判断组件是现成组件还是懒加载 loader（无 render/setup/template 的函数按 loader 处理）
function isLoader(c: any): boolean {
  return typeof c === 'function' && !c.render && !c.setup && !c.template
}
