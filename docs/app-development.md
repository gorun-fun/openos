# 应用开发指南

OpenOS 为第三方提供了从「零代码」到「完整前端工程」的四种接入方式，全部共用同一套桌面能力（窗口、总线、登录态、权限）。本文是前两种（声明式 / 组件 SDK）与跨应用联动的完整参考；宿主框架集成见[宿主集成指南](host-integration.md)，网页应用与统一代理见[统一代理指南](webproxy.md)。

- **方式一：声明式应用**——一份 JSON 注册即得新应用，适合数据看板
- **方式二：组件 SDK**——`OpenOS.registerApp()` 注册 Vue 组件，完整前端能力
- **方式三：宿主框架**——`createOpenOS()` 把 OpenOS 当桌面框架构建业务系统
- **方式四：网页应用**——零代码收录现成网站（iframe 直嵌 / 服务端反代）

## 方式一：声明式应用（零前端代码）

管理员在「系统设置 → 第三方应用」粘贴 JSON Schema 注册（有示例模板一键填入），注册后立即可在 Dock 使用，并可授权给任意角色（授权变更实时广播到所有在线端）。

```jsonc
{
  "title": "团队公告板",
  "dataSource": {
    "kind": "static",                    // 或 "rest"（字段见下表）
    "items": [{ "title": "v1.0 发布", "author": "admin", "date": "周一" }]
  },
  "views": [
    { "type": "stats", "fields": [{ "key": "title", "label": "最新" }] },
    { "type": "cards", "title": "卡片", "fields": [
      { "key": "title", "label": "标题", "primary": true }, { "key": "author", "label": "作者" }] },
    { "type": "table", "title": "列表", "columns": [
      { "key": "title", "label": "标题" }, { "key": "date", "label": "日期" }] }
  ]
}
```

`dataSource.kind = "rest"` 完整字段：

| 字段 | 说明 |
|---|---|
| `url` | 请求地址。**相对路径（`/api/...`）默认携带当前登录态**（走统一 API 客户端，含 401 全局处理）；跨域绝对地址默认经统一代理转发避免令牌外泄，`auth: true` 强制携带登录态，`direct: true` 强制浏览器直连 |
| `method` / `body` | 默认 GET；`POST` + `body`（JSON 对象）发送请求体 |
| `headers` | 自定义请求头（键值对象） |
| `path` | 响应取值路径，支持点路径（如 `"data.items"`） |
| `interval` | 轮询间隔秒（最小 5s），适合无推送的旧接口 |
| `topic` | **总线推送刷新**：收到该主题消息（本地或跨端）立即重取数据，支持通配（如 `"app:rooms"`），与 `interval` 可叠加 |

声明式应用还可以在 config 顶层声明 `agentCaps` 键，把能力暴露给 AI（纯 JSON，仅 http/window 模式，见[goder 指南](goder.md)）。

## 方式二：组件 SDK（完整前端能力）

第三方脚本（控制台、注入或商店 bundle 应用）调用全局 SDK 注册应用：

```js
OpenOS.registerApp('todo', {
  name: '待办', width: 420, height: 400,
  component: {
    data: () => ({ items: ['体验 OpenOS'], text: '' }),
    template: '<div style="padding:16px">…</div>',
  },
})
OpenOS.open('todo')
```

> 应用根元素约定：窗体内容区是 flex 行容器且不强制子项拉伸，根元素需自带 `width: 100%`（高度随 flex 自动铺满）。缺了它内容按 max-content 收缩，窗口拖大时内容不会跟着舒展。

### SDK 面（`window.OpenOS`）

| API | 说明 |
|---|---|
| `OpenOS.bus` | 事件总线客户端（`on` / `emit` / `remote`，支持通配） |
| `OpenOS.api` | 带登录态的 REST 客户端（401 全局登出）；`api.upload()` 走 multipart 文件上传 |
| `OpenOS.ui` | 基础组件库门面（Os* 组件） |
| `OpenOS.open(id, props?, opts?)` / `OpenOS.close(id)` | 打开/关闭应用窗口 |
| `OpenOS.launch(appId, props, opts)` | **可等待**打开：目标应用办完事回执结果（见下） |
| `OpenOS.resolveLaunch(reqId, result)` | 目标端回执（配合 `launchOf`） |
| `OpenOS.openDialog(id, params, opts?)` | 跨应用系统级弹窗（`Promise`） |
| `OpenOS.openFile(path)` | VFS 文件按扩展名分发对应应用打开（md→文档、csv→表格、html→幻灯片、pdf→PDF、图片→快速预览） |
| `OpenOS.toast(msg, opts)` | 轻提示（type: success/error） |
| `OpenOS.notify({title, body, appId, broadcast})` | 系统通知（入通知中心，`broadcast` 跨端） |
| `OpenOS.me()` | 当前用户（用户名/角色/权限） |
| `OpenOS.authToken()` | 当前 JWT（拼 `<img>`/`<a>` 标签 URL 用） |
| `OpenOS.can(perm)` | 功能级权限判定（admin 直通 / `*` 通配 / 精确匹配，与服务端同语义） |
| `OpenOS.proxyUrl(url)` | 生成统一代理 `/px/` 转发地址（编码同 compactTarget 规则） |
| `OpenOS.calendar` / `OpenOS.registerCapabilities` / `OpenOS.registerAgentTool` | 日历读写与 AI 能力声明（见[goder 指南](goder.md)） |
| `OpenOS.vue` | Vue 具名导出（商店 bundle 应用写组件逻辑用） |
| `OpenOS.registerDialog(id, def)` / `OpenOS.registerApp` | 运行期注册弹窗/应用 |

## 跨应用联动

业务系统常有「在 A 应用办到一半，需要 B 应用的一项能力/一份数据」的诉求，OpenOS 内置三条通道：

### ① 跨应用弹窗——拉起 B 应用的业务弹窗，不打开 B 的主窗口

弹窗由提供方应用注册（归属清晰、可懒加载），调用方一行代码拉起并 `await` 业务结果：

```ts
// 房态管理注册「入住」弹窗（createOpenOS dialogs 选项 / 运行时 OpenOS.registerDialog 等价）
createOpenOS({
  dialogs: { 'roommgmt:checkin': { component: () => import('./dialogs/CheckInDialog.vue'), title: '办理入住', width: 520 } },
})

// 订单应用：拉起弹窗，办理完拿结果继续本应用流程
const checkedIn = await OpenOS.openDialog('roommgmt:checkin', { roomId, orderId })
if (checkedIn) toast('入住成功', { type: 'success' })
```

弹窗组件认三个 props：`params`（调用入参）、`close(result)`（回传，成为 Promise 值）、`cancel()`。Esc / 点遮罩默认等同取消（`openDialog` 第三参可调）；渲染为系统级模态浮层，支持叠开。

### ② 拉起窗口等结果——B 应用开一个完整窗口办事，A 拿结果继续

当 B 的能力需要独立窗口（浏览/多步操作），用 `OpenOS.launch`——`open()` 的「可等待」版本，任何应用（含商店 bundle 应用）都可做目标端：

```ts
// 订单应用：拉起房态管理窗口办理入住
const stay = await OpenOS.launch('roommgmt', { view: 'checkin', roomId: 42 },
                                 { title: '办理入住', width: 760, from: 'orders' })
if (stay) toast(`入住成功：房号 ${stay.roomNo}`, { type: 'success' })
```

```vue
<!-- 目标应用（房态管理主窗口）：从窗口 props 的保留键 __launch 识别拉起态 -->
<script setup lang="ts">
import { launchOf, resolveLaunch } from '@/ui'
const props = defineProps<{ win: WinState }>()
const launched = launchOf(props.win.props)   // 普通 open() 打开时为 null，行为不变
// ……办理流程；用户确认后：
resolveLaunch(launched.reqId, { roomNo: '8203', stayId: 's-1024' })
// 用户取消：resolveLaunch(launched.reqId, null)
</script>
```

协议要点：`resolveLaunch` 幂等只生效一次；**关窗即取消**（未应答时框架兜底 resolve null，永不悬空）；目标应用不感知协议时退化为「等窗口关闭」；每次 launch 的 reqId 唯一（同参数多次拉起各开各窗，不走 `open()` 去重）；`hidden: true` 的详情窗口类应用是典型目标端（不出现在启动台/Dock，只被拉起）。

### ③ 跨应用数据同步——数据权威在后端，总线做实时刷新

```ts
// 房态管理：金额变化后广播（app:* 前缀自动远程订阅——多标签页/多窗口同步）
bus.emit('app:hotel.finance:changed', { date: '2026-10-02', income: 4820 })

// 经营应用：订阅刷新自己的统计视图（bus.on 返回卸载函数，组件卸载时调用）
const off = bus.on('app:hotel.finance:changed', (p) => refresh(p.date))
```

同 bundle 的宿主应用可直接共享 Pinia store（按耦合偏好二选一）；独立部署/声明式应用之间一律走「后端 REST + 总线推送」。

## 事件总线速查

```ts
import { bus } from '@/bus/bus'   // 或第三方经 window.OpenOS.bus
bus.on('app:calc', (payload, from, topic) => {})   // 订阅（支持 * 通配）
bus.emit('app:calc', { a: 1 })                     // 本地 + 远程发布
bus.emit('app:calc', { a: 1 }, { remote: false })  // 仅本地
bus.remote('term:admin', 'pwd')                    // 仅发服务端处理器
```

订阅任意业务主题会自动向服务端补发远程订阅（即订即达），本地最后一个消费者移除后自动退订。断线自动重连（指数退避上限 20 次）；重连前先探测会话，令牌已吊销则直接回登录页——总线不会因会话已死而静默装死。

常用内置主题：`sys:notify`（通知）· `sys:presence`（在线状态，配 `GET /api/presence` 快照）· `sys:apps-changed`（应用列表变化）· `fs:changed`（文件变更）· `chat:room`（聊天室）· `sys:key`（未被消费的按键，仅本地）。

## 后台应用通知

商店 bundle 应用卸载后没有代码在场，「应用未打开时来了消息」由桌面壳常驻兜底（规则驱动：topic / appId / active 判定 / title/body 格式化）。**勿在 bundle 应用组件里自建常驻监听**——卸载即失聪；新应用接入在桌面壳加一条规则即可。

## 系统附件服务（平台级文件分享）

聊天附件、bundle 应用共用的上传通道：

- `POST /api/attachments`（multipart 字段 `file`，单文件 ≤10MB，登录）→ `{id, name, mime, size}`；id 为 128bit 随机不可枚举，mime 以内容嗅探为准（声明可伪造）。
- `GET /api/attachments/{id}?token=<jwt>` 下载（标签带不了 Authorization header，鉴权走查询参数同 `/ws`；`api.upload()` 自动携带登录态）。
- 安全边界：Content-Type 只对图片白名单内联，其余一律 `octet-stream` 下载 + `nosniff`——附件服务不可变成 HTML 注入源。保留期 `OPENOS_ATTACH_RETENTION_DAYS`（默认 30 天）。
