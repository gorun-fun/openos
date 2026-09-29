// OpenOS 第三方应用 SDK。
//
// 组件型接入（第三方带前端能力）：
//   在页面控制台或通过 <script> 执行：
//   OpenOS.registerApp('todo', {
//     name: '待办', width: 480, height: 520,
//     component: { setup(){...}, template: '<div>…</div>' } // 需运行时编译器；推荐用 render(h)
//   })
//
// 声明式接入（第三方只提供数据，详见 SchemaApp.vue）：
//   由管理员在「系统设置 → 应用管理」注册 JSON schema，即可获得一个新桌面应用。
//
// 通信：OpenOS.bus 与桌面事件总线互通；OpenOS.ui 暴露基础组件库。
import { bus } from '../bus/bus'
import * as ui from '../ui'
import { runtimeComponents } from '../stores/apps'
import { useWindows } from '../stores/windows'
import { useApps } from '../stores/apps'
import { api } from '../api/client'

export interface RegisterOptions {
  name: string
  width?: number
  height?: number
  component: any // Vue 组件（defineComponent 产物或含 render 的对象）
}

export const OpenOS = {
  version: '1.0',
  /** 注册组件型第三方应用（即时生效，刷新后消失；持久化请用声明式注册）。 */
  registerApp(id: string, opts: RegisterOptions) {
    runtimeComponents[id] = opts.component
    const apps = useApps()
    if (!apps.byId(id)) {
      apps.apps.push({
        id, name: opts.name, icon: 'default', category: '第三方',
        builtin: false, width: opts.width || 700, height: opts.height || 520,
      })
    }
    return id
  },
  /** 打开一个应用窗口。 */
  open(appId: string, props?: any) {
    return useWindows().open(appId, props)
  },
  /** 关闭某应用全部窗口。 */
  close(appId: string) {
    useWindows().closeApp(appId)
  },
  /** 事件总线（与桌面同源）。 */
  bus: {
    on: (t: string, fn: any) => bus.on(t, fn),
    emit: (t: string, payload?: any) => bus.emit(t, payload),
  },
  /** 基础 UI 组件库。 */
  ui,
  /** API 客户端（自动携带登录态）。 */
  api,
}

// 注入全局，供第三方脚本使用
;(window as any).OpenOS = OpenOS
