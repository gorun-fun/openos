// OpenOS 第三方应用 SDK。
//
// 组件型接入（第三方带前端能力）：
//   在页面控制台或通过 <script> 执行：
//   OpenOS.registerApp('todo', {
//     name: '待办', width: 480, height: 520, icon: 'notes', hidden: false,
//     component: { setup(){...}, template: '<div>…</div>' } // 需运行时编译器；推荐用 render(h)
//   })
//   OpenOS.unregisterApp('todo') // 卸载运行时注册的应用（声明式注册的应用走管理接口）
//
// 声明式接入（第三方只提供数据，详见 SchemaApp.vue）：
//   由管理员在「系统设置 → 应用管理」注册 JSON schema，即可获得一个新桌面应用。
//
// 通信：OpenOS.bus 与桌面事件总线互通；OpenOS.ui 暴露基础组件库；
// 通知：OpenOS.toast；文件与窗口：OpenOS.fs / OpenOS.windows。
import { bus } from '../bus/bus'
import * as ui from '../ui'
import { toast } from '../ui/toast'
import { runtimeComponents, appChrome } from '../stores/apps'
import { useWindows } from '../stores/windows'
import { useApps } from '../stores/apps'
import { api, fsApi } from '../api/client'

export interface RegisterOptions {
  name: string
  width?: number
  height?: number
  /** 图标标识（desktop/icons.ts 已注册的图标名，缺省 default） */
  icon?: string
  /** 应用分类（显示于启动台，缺省「第三方」） */
  category?: string
  /** 隐藏应用：可编程打开、不出现在启动台/Dock（详情窗口类） */
  hidden?: boolean
  /** 窗体外观：'merged' = 融合工具栏窗体（无独立标题栏，应用自绘标题行，仿 Finder） */
  chrome?: 'default' | 'merged'
  component: any // Vue 组件（defineComponent 产物或含 render 的对象）
}

export const OpenOS = {
  version: '1.2',
  /** 注册组件型第三方应用（即时生效，刷新后消失；持久化请用声明式注册）。 */
  registerApp(id: string, opts: RegisterOptions) {
    runtimeComponents[id] = opts.component
    if (opts.chrome) appChrome[id] = opts.chrome
    const apps = useApps()
    if (!apps.byId(id)) {
      apps.apps.push({
        id, name: opts.name, icon: opts.icon || 'default', category: opts.category || '第三方',
        builtin: false, hidden: !!opts.hidden, width: opts.width || 700, height: opts.height || 520,
      })
    }
    return id
  },
  /** 卸载运行时注册的应用（关闭其全部窗口；不影响声明式注册的应用）。 */
  unregisterApp(id: string) {
    delete runtimeComponents[id]
    delete appChrome[id]
    const apps = useApps()
    const i = apps.apps.findIndex((a) => a.id === id)
    if (i >= 0 && !apps.apps[i].builtin) {
      useWindows().closeApp(id)
      apps.apps.splice(i, 1)
    }
  },
  /** 打开一个应用窗口（可携带 props，如文件路径）。 */
  open(appId: string, props?: any) {
    return useWindows().open(appId, props)
  },
  /** 关闭某应用全部窗口。 */
  close(appId: string) {
    useWindows().closeApp(appId)
  },
  /** 窗口级操作（open/closeApp 之外的细粒度控制）。 */
  windows: {
    list: () => useWindows().list,
    focus: (id: string) => useWindows().focus(id),
    minimize: (id: string) => useWindows().minimize(id),
    toggleMaximize: (id: string) => useWindows().toggleMaximize(id),
    close: (id: string) => useWindows().close(id),
    setTitle: (id: string, title: string) => useWindows().setTitle(id, title),
  },
  /** 事件总线（与桌面同源）。 */
  bus: {
    on: (t: string, fn: any) => bus.on(t, fn),
    once: (t: string, fn: any) => bus.once(t, fn),
    emit: (t: string, payload?: any, opts?: { remote?: boolean }) => bus.emit(t, payload, opts),
    remote: (t: string, payload?: any) => bus.remote(t, payload),
  },
  /** 基础 UI 组件库。 */
  ui,
  /** API 客户端（自动携带登录态）。 */
  api,
  /** 虚拟文件系统客户端（上传/读写/配额）。 */
  fs: fsApi,
  /** 全局轻提示：OpenOS.toast('已保存', { type: 'success' })。 */
  toast,
}

// 注入全局，供第三方脚本使用
;(window as any).OpenOS = OpenOS
