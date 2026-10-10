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
// 通知：OpenOS.toast（轻提示）/ OpenOS.notify（系统通知中心）；
// 文件与窗口：OpenOS.fs / OpenOS.windows；日程：OpenOS.calendar；
// 跨应用弹窗：OpenOS.registerDialog / OpenOS.openDialog（见 ui/dialogService.ts）；
// 跨应用拉起等结果：OpenOS.launch / resolveLaunch / launchOf（见 ui/launch.ts）；
// 功能权限：OpenOS.can(perm)（应用内功能门控，见 ui/perms.ts）。
import { bus } from '../bus/bus'
import * as ui from '../ui'
import * as vueSDK from 'vue'
import { toast } from '../ui/toast'
import { runtimeComponents, appChrome } from '../stores/apps'
import { useWindows } from '../stores/windows'
import { useApps } from '../stores/apps'
import { useSession } from '../stores/session'
import { api, fsApi, getToken, fetchWsTicket, type SessionUser } from '../api/client'
import { notify } from '../stores/notifications'
import { useCalendar, todayStr, REMIND_OPTIONS, type CalMark, type RemindOffset } from '../stores/calendar'
import { registerDialog, unregisterDialog, openDialog, dialogRegistry, type DialogDef } from '../ui/dialogService'
import { registerAgentTool, unregisterAgentTool, agentTools, type AgentToolDef } from '../desktop/agentTools'
import {
  registerCapabilities, unregisterCapabilities, listCapabilities,
  type AppCapabilities, type CapDef, type CapViewDef,
} from '../desktop/capabilities'
import { launch, resolveLaunch, launchOf } from '../ui/launch'
import { can } from '../ui/perms'
import { openPath } from '../apps/openWith'

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
  /**
   * 跨应用弹窗：注册为 `${appId}:${name}`，其他应用经 OpenOS.openDialog 拉起，
   * 无需打开本应用主窗口（弹窗组件认 params/close/cancel 三个 props）。
   */
  dialogs?: Record<string, DialogDef>
  /**
   * 能力清单（能力暴露层 CAP）：声明本应用暴露给 goder AI 的能力——
   * queries（只读数据查询，无窗口执行）/ actions（业务操作）/ views（可视化
   * 入口 + data-os-cap 锚点高亮）。每个能力实现三选一：run 回调 / http 声明 /
   * window 开窗。AI 经 os_caps 发现、os_app_query / os_app_call / os_app_ui 执行。
   */
  capabilities?: Omit<AppCapabilities, 'appId'>
  component: any // Vue 组件（defineComponent 产物或含 render 的对象）
}

export const OpenOS = {
  version: '1.2',
  /** 注册组件型第三方应用（即时生效，刷新后消失；持久化请用声明式注册）。 */
  registerApp(id: string, opts: RegisterOptions) {
    runtimeComponents[id] = opts.component
    if (opts.chrome) appChrome[id] = opts.chrome
    if (opts.dialogs) {
      for (const [name, def] of Object.entries(opts.dialogs)) registerDialog(`${id}:${name}`, def)
    }
    if (opts.capabilities) registerCapabilities({ appId: id, ...opts.capabilities }, 'runtime')
    const apps = useApps()
    if (!apps.byId(id)) {
      apps.apps.push({
        id, name: opts.name, icon: opts.icon || 'default', category: opts.category || '第三方',
        builtin: false, hidden: !!opts.hidden, width: opts.width || 700, height: opts.height || 520,
      })
    }
    return id
  },
  /** 卸载运行时注册的应用（关闭其全部窗口与弹窗；不影响声明式注册的应用）。 */
  unregisterApp(id: string) {
    delete runtimeComponents[id]
    delete appChrome[id]
    unregisterCapabilities(id)
    for (const key of [...dialogRegistry.keys()]) {
      if (key.startsWith(id + ':')) unregisterDialog(key)
    }
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
  /**
   * 拉起目标应用窗口并等待业务结果（open 的可等待版，独立窗口形态的跨应用联动）：
   * 目标应用从窗口 props 的保留键 __launch 识别（OpenOS.launchOf）并以
   * OpenOS.resolveLaunch(reqId, result) 回执；取消/关窗/应用不感知协议时 resolve null。
   * const stay = await OpenOS.launch('roommgmt', { view: 'checkin', roomId }, { title: '办理入住' })
   */
  launch,
  /** 目标应用回执 launch：完成传业务结果，取消传 null；幂等，重复调用只生效一次。 */
  resolveLaunch,
  /** 目标应用侧辅助：从窗口 props 提取拉起信封（launchOf(props.win.props)），普通 open() 打开时为 null。 */
  launchOf,
  /** 打开一个 VFS 文件：按扩展名分发到对应应用（md/txt→文档、csv→表格、
   *  html→幻灯片、pdf→PDF、图片→文件应用快速预览），映射与文件应用一致。 */
  openFile(path: string): string | null {
    return openPath(path)
  },
  /** 注册跨应用弹窗（id 建议 `appId:业务名`）；registerApp 的 dialogs 选项是其应用内等价物。 */
  registerDialog,
  /** 注销跨应用弹窗（打开中的实例按取消关闭）。 */
  unregisterDialog,
  /**
   * 拉起跨应用弹窗并等待结果（无需打开目标应用主窗口）：
   * 弹窗内 close(result) → resolve(result)；cancel()/Esc/点遮罩 → resolve(undefined)。
   * 例如订单应用：await OpenOS.openDialog('roommgmt:checkin', { roomId, orderId })。
   */
  openDialog,
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
  /** Vue 运行时 API（ref/computed/onMounted 等，主程序完整版 Vue 的具名导出原样转发）。
   *  商店 bundle 应用是不带依赖的单文件 ES 模块，组件逻辑从这里取响应式 API：
   *  const { ref } = window.OpenOS.vue。 */
  vue: vueSDK,
  /** 当前登录用户（未登录为 null）。 */
  me(): SessionUser | null {
    return useSession().user
  },
  /** 功能级权限判定：admin 角色直通 / 权限集含 `*` 通配 / 其余精确匹配（与
   *  ui/perms.ts 的 can()、服务端 RequirePerm 同语义）。商店 bundle 应用按它
   *  控制应用内功能入口：OpenOS.can('room.view') 为 false 时隐藏对应按钮/页签。 */
  can,
  /** 当前登录令牌（拼 <img>/<a> 等带不了 Authorization header 的 URL 用，如附件下载）。
   *  ⚠ 令牌属长期凭证——优先用 authTicket()（30s 一次性短票），新代码勿再用本接口拼 URL。 */
  authToken(): string {
    return getToken()
  },
  /** 一次性短票（30s 用后即焚）：query 鉴权 URL 场景（/ws、附件、商店取件）的
   *  推荐凭据，URL 泄漏窗口极短。失败时调用方自行回落 authToken()。 */
  async authTicket(): Promise<string> {
    return fetchWsTicket()
  },
  /** 向 goder AI 注册自定义桌面工具（P6 扩展点）：AI 经 os_app_tools 可见、
   *  os_app_tool({tool, args}) 调用（run 在桌面壳执行）。返回完整工具名。
   *  const key = OpenOS.registerAgentTool({ appId:'rooms', name:'vacant',
   *    description:'查询今日空房', params:{type:'object',properties:{...}},
   *    run: (args) => hotelApi.get('/rooms/vacant', {params:args}) }) */
  registerAgentTool,
  /** 注销自定义 AI 工具（完整名或 {appId,name}）。 */
  unregisterAgentTool,
  /** 当前在线的自定义 AI 工具表（诊断用）。 */
  agentTools,
  /**
   * 注册应用能力清单（能力暴露层 CAP）：goder AI 经 os_caps 发现、os_app_query/
   * os_app_call/os_app_ui 执行。商店 bundle 应用在 setup 里注册：
   *   OpenOS.registerCapabilities({ appId:'chat', queries:[{ name:'unread',
   *     title:'未读消息', run: () => … }], views:[{ name:'room', props:{…} }] })
   * 能力实现三选一：run（fn 回调，桌面壳执行无窗口）/ http（声明式 REST）/
   * window（开窗到视图）；可视化锚点 = 模板里的 data-os-cap="<name>" 标记。
   */
  registerCapabilities,
  /** 注销应用能力清单（应用卸载/反注册）。 */
  unregisterCapabilities,
  /** 当前全量能力清单（诊断用；与 os_caps 同源）。 */
  capabilities: listCapabilities,
  /** 基础 UI 组件库。 */
  ui,
  /** API 客户端（自动携带登录态）。 */
  api,
  /** 虚拟文件系统客户端（上传/读写/配额）。 */
  fs: fsApi,
  /** 全局轻提示：OpenOS.toast('已保存', { type: 'success' })。 */
  toast,
  /** 系统通知中心（右上角横幅 + 通知面板回看）：
   *  OpenOS.notify({ title: '下载完成', body: 'report.pdf', appId: 'files', broadcast: false })。 */
  notify,
  /** 日程（日历应用同源数据）：新增/查询事件，节假日查询。 */
  calendar: {
    list: () => useCalendar().events,
    add: (input: { title: string; date: string; time?: string; remind?: RemindOffset }) => useCalendar().addEvent(input),
    remove: (id: string) => useCalendar().removeEvent(id),
    holidayOf: (date: string) => useCalendar().holidayOf(date),
    today: () => todayStr(),
    remindOptions: REMIND_OPTIONS,
    /** 自由日标记：在月历格子上显示自定义彩色标签（内存态不持久化，生命周期由来源应用维护）。
     *  OpenOS.calendar.mark('2026-10-05', { appId: 'release', text: '发布', color: '#e5484d' }) */
    mark: (dates: string | string[], opts: { appId?: string; text: string; color?: string }) => useCalendar().mark(dates, opts),
    unmark: (dates: string | string[], appId?: string) => useCalendar().unmark(dates, appId),
    marksOf: (date: string): CalMark[] => useCalendar().marksOf(date),
  },
}

// 注入全局，供第三方脚本使用
;(window as any).OpenOS = OpenOS
