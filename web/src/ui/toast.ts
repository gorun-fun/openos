// 全局轻提示（Toast）：任何内置应用 / 宿主应用 / SDK 第三方脚本都可调用。
//
//   import { toast } from '@/ui'
//   toast('已保存')
//   toast('保存失败', { type: 'error' })
//
// 渲染容器为 OsToasts（App 根部挂载一次），无需在使用方再放置任何标记。
import { reactive } from 'vue'

export type ToastType = 'info' | 'success' | 'error'

export interface ToastItem {
  id: number
  text: string
  type: ToastType
}

// 模块级单例状态：跨组件共享，桌面 / 登录页 / 宿主扩展均可触发
const state = reactive<{ list: ToastItem[] }>({ list: [] })
let seq = 0

/** 弹出一条轻提示，默认 2.6 秒后自动消失。 */
export function toast(text: string, opts: { type?: ToastType; duration?: number } = {}) {
  const item: ToastItem = { id: ++seq, text, type: opts.type || 'info' }
  state.list.push(item)
  // 同屏最多保留 5 条，避免连续触发堆积
  if (state.list.length > 5) state.list.splice(0, state.list.length - 5)
  const duration = opts.duration ?? 2600
  if (duration > 0) {
    setTimeout(() => {
      const i = state.list.findIndex((t) => t.id === item.id)
      if (i >= 0) state.list.splice(i, 1)
    }, duration)
  }
  return item.id
}

/** 供 OsToasts 容器消费的只读状态。 */
export function toastState() {
  return state
}
