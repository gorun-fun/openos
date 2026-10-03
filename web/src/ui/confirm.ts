// 统一确认弹窗：浏览器原生 confirm() 的系统级替代（模态浮层视觉与 DialogHost 一致，
// z 基准 1e6 恒在窗口层之上，Esc/点遮罩 = 取消）。内建注册保留 id 'os:confirm'，
// 模块导入即注册（registerDialog 幂等覆盖，多应用重复 import 无副作用）。
//
//   if (!(await osConfirm({ title: '卸载应用', body: '确定卸载「xx」？', danger: true, confirmText: '卸载' }))) return
import OsConfirm from './OsConfirm.vue'
import { registerDialog, openDialog } from './dialogService'

export interface ConfirmOpts {
  /** 标题（加粗行，可省） */
  title?: string
  /** 正文（必填，支持 \n 换行） */
  body: string
  /** 危险操作（删除类）：确认按钮红色，默认焦点落在取消按钮防回车误触 */
  danger?: boolean
  /** 确认按钮文案，缺省「确认」 */
  confirmText?: string
  /** 取消按钮文案，缺省「取消」 */
  cancelText?: string
}

registerDialog('os:confirm', { component: OsConfirm, width: 360 })

/** 拉起确认弹窗：点确认 → true；点取消/Esc/点遮罩 → false。 */
export function osConfirm(opts: ConfirmOpts): Promise<boolean> {
  return openDialog('os:confirm', opts).then((r) => r === true)
}
