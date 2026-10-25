# goder：AI 编程 IDE 与桌面控制

OpenOS 按 [goder](https://git.gorun.fun/share/goder.git) 官方嵌入约定（`goder/docs/embedding.md`）把 goder 作为内置应用：完整 AI 编程工作台（项目 / 会话 / 聊天 / 代码审查 / 事件台），叠加两条 OpenOS 特有能力——**桌面控制 MCP 插件**与**能力暴露层（CAP）**。

## 架构与隔离

- **每用户一个独立 gh server 进程**（独立端口/数据目录/令牌，令牌不下发浏览器）；
- OpenOS 服务端托管 `goder-serve` 网关子进程（header 身份模式）：`/goder-api/*` 反代时先剥离外部伪造的 `X-GRF-*` 头再注入 `X-GRF-UID/X-GRF-Name`，SSE 透传；`/vendor/goder.js` 提供自包含嵌入库；
- 鉴权：OpenOS 登录后签发 `openos_token` Cookie（httpOnly），goder 嵌入组件的同源请求自动携带；
- 前端：goder 应用窗口动态 import 嵌入库 → 渲染 `<goder-app-shell panels="sessions,chat,sidebar,console">`；
- **goder-serve 强制回环绑定**（`OPENOS_GODER_ADDR` 非回环拒绝启动）。

## 部署

**Docker（推荐）**：goder 组件全部内置在镜像中（goder-serve + goder.js + gh），部署即用，见 [Docker 部署](deploy-docker.md)。

**本地/裸机**：`make goder` 自动完成（见[自部署](deploy-bare-metal.md)）：

```bash
# make goder 等价于：
cd <goder 源码> && ./build.sh embed         # 构建嵌入库（需 npm）
cd <goder 源码> && go build -o <openos>/server/data/bin/goder-serve ./cmd/goder-serve
cp <goder 源码>/frontend/dist-embed/goder.js <openos>/server/data/goder/
# 前置：gh 可执行文件在 PATH（OPENOS_GH_BIN 可覆盖）
```

组件缺失时自动降级——goder 应用内显示部署引导，**不会变成启动错误**。`OPENOS_GODER=off` 整体停用（纯业务宿主建议开启此开关）。

**项目目录落统一 home**：goder「新建项目」时在用户 home 的 `~/goder/<项目名>` 下创建真实目录作为该项目会话的 workspace——AI 的项目上下文与文件管理 / 真实终端**同盘可见**（走 goder 嵌入约定的 `newProject` 钩子）；存量项目保持原位不迁移。

## 接入模型

管理员在「系统设置 → AI 服务」配置任意 OpenAI 兼容接口（Base URL / Key / 模型），保存即以托管供应商 `OpenOS` 声明进 goder-serve 宿主管理面（`PUT /goder-api/admin/users/{uid}/providers`）并设为默认——**平台 Key 不落任何用户的 goder 配置文件**，goder 内看到的 `OpenOS` 供应商经 OpenOS 自身代理端点（`/api/ai/v1/*`，每用户代理密钥）转发。配置清空则移除托管条目。goder 未部署时设置页提示联系管理员。

## 桌面控制（openos MCP 插件）

goder 里的 AI（gh 智能体）可经内置的 `openos` MCP 插件操控当前用户桌面。goder 启用时自动部署（经 goder-serve 宿主管理面声明式安装），无需配置；服务重启时自动自愈存量用户。

```
gh server（每用户一个）
  └─ 自动发现 openos 插件（wrapper 脚本）
       └─ exec openos-server mcp-shim <endpoint> <token> <user>   ← stdio MCP 插件
            ↕ HTTP JSON-RPC（127.0.0.1:7712/mcp，Bearer 令牌 + X-OS-User）
openos-server 进程内 osmcp 包（os_* 工具，gh 侧全名 mcp__openos__os_*）
  ├─ 服务端直答：应用/用户设置/VFS 文件/总线发布/分享/商店（权限点门控）
  └─ 前端桥：PublishTo(user,"sys:agent") ↓ 浏览器执行
              bus.remote("agent:resp") ↑ 回传结果（15s 超时兜底）
```

### 工具清单

| 类别 | 工具 |
|---|---|
| 系统 | `os_info`（版本/用户/goder 状态/在线用户）、`os_apps_list`、`os_users_list`（需 `users.manage`） |
| 窗口/桌面 | `os_desktop_state`、`os_window_open/close/do`（focus/最小化/最大化/半屏/移动尺寸/改标题）、`os_app_open_file`、`os_desktop_do`（壁纸/深色/Dock）、`os_notify`（入通知中心） |
| 数据 | `os_dock_do`、`os_settings_get/put`、`os_fs_list/read/write/mkdir/delete/search/usage`（限定本人 home、≤8MB、配额 100MB，写入广播 `fs:changed`）；便签/日历数据经能力暴露层 |
| 分享 | `os_share_list/create/revoke`（与 REST 完全一致的 owner 校验/审计） |
| 商店 | `os_market_list/install/uninstall`（需 `market.manage`——按功能权限点门控） |
| 通用通道 | `os_bus_publish`（向自己桌面发布任意主题，可驱动声明式应用刷新） |
| 能力暴露层 | `os_caps`（全量能力清单）、`os_app_query`（只读取数）、`os_app_call`（执行操作）、`os_app_ui`（开窗 + 锚点高亮 + 分步引导） |

**安全边界**：MCP 端点仅监听回环 + Bearer 令牌（`<data>/goder/osmcp.json`，0600，按用户分发、令牌↔X-OS-User 成对校验）；工具执行前校验用户存在且未禁用；**工具只作用于发起用户本人**；写操作入审计 `goder.tool`。

**生效时机**：新用户首次访问 `/goder-api` 时安装（先于该用户 gh 懒启动）；服务重启时自愈。已在运行的存量 gh server 不热加载插件——部署后重启 openos-server 即可。

## 能力暴露层（CAP）

应用暴露给 AI 的能力收敛为**一份标准清单协议**：任何应用按同一协议声明，AI 经 4 个通用工具发现与执行，**服务端不再为新应用加工具**。

### 能力三分类

- `queries`：只读数据查询，**无需打开应用即可执行**（fn 直读 store 或 http 声明式取数）；
- `actions`：业务操作；
- `views`：可视化操作入口（打开到指定视图 + `data-os-cap` 锚点高亮 + `steps` 分步引导文案，AI 可转述给用户）。

### 执行三模式（每个能力三选一，优先级 run > http > window）

- `run`：fn 回调在桌面壳执行（应用窗口无需在场）；
- `http`：声明式 REST——GET/DELETE 时 args 映射 query 参数、POST/PUT 作 JSON body；相对 `/api/…` 走 OpenOS 登录态、绝对地址裸 fetch（`auth:true` 强制携带）；
- `window`：打开/聚焦窗口并携带 props（应用按 props 路由视图）。

### 四层来源（同一协议）

1. **内置应用代码注册**（calendar/notes 首批示范）；
2. **宿主声明** `createOpenOS({ capabilities: { rooms: {...} } })`——业务系统零侵入接入；
3. **运行期注册** `OpenOS.registerCapabilities` / `registerApp({ capabilities })`（商店 bundle 应用经 `window.OpenOS`）；
4. **声明式应用** config 顶层 `agentCaps` 键（纯 JSON，仅 http/window 模式）。

```ts
// 宿主声明示例（grf-hotel「查空房 + 打开房态盘」）
createOpenOS({
  capabilities: {
    rooms: {
      label: '客房',
      queries: [{ name: 'vacant', title: '查询今日空房', http: { url: '/api/rooms/vacant' } }],
      actions: [{ name: 'block', title: '锁定房型', run: (args) => hotelApi.post('/rooms/block', args) }],
      views: [{ name: 'grid', title: '房态盘', props: { view: 'grid' }, anchor: 'room-grid',
        steps: ['左侧选择楼层', '点击房型卡片进入详情'] }],
    },
  },
})
```

**AI 使用范式**：`os_caps` 查清单 → `os_app_query({appId:'rooms', query:'vacant'})` 查数 → `os_app_call({appId:'rooms', capability:'block', args:{…}})` 操控 → `os_app_ui({appId:'rooms', view:'grid'})` 引导用户。

**约束**：能力可声明 `perm` 功能权限点做 UX 门控，**鉴权权威恒在数据所有方**（openos REST / 宿主后端），桥只是调用通道；清单未知字段一律忽略（双向前向兼容）；前端往返受 15s 桥超时约束，长任务应在 run 内异步化（先回受理、结果走 `os_notify` / 总线）；iframe 网页应用无法高亮内部元素（聚焦窗口并提示）。

## goder 无头任务（宿主机对机）

`/api/host/goder/tasks*`（X-Exchange-Secret 守护，见[宿主集成指南](host-integration.md)）：宿主业务系统代指定用户创建 gh server 会话并后台执行（固定 write-workspace + auto_approve），创建后把会话追加进 goder-serve 服务端会话索引（分组/项目 id/显示名为请求参数）；模型经管理面解析全限定名（默认供应商优先，多家歧义与套餐未含 fail-fast 报错、绝不静默换路由）。GoderStudio 前端可 `windows.open('goder', {session: <sid>})` 深链选中会话继续跟进。

## 轻量指令通道（sys:cmd）

任何来源（AI 回复、终端、事件总线）发送 `sys:cmd` 即可操控桌面，适合脚本与简单场景：

| 指令 | 效果 |
|---|---|
| `{ "cmd": "open", "arg": "calculator" }` | 打开应用 |
| `{ "cmd": "close", "arg": "docs" }` | 关闭应用全部窗口 |
| `{ "cmd": "file", "arg": "/home/alice/文档/a.pdf" }` | 用合适应用打开文件 |
| `{ "cmd": "wallpaper", "arg": "3" }` | 切换壁纸（1-6） |
| `{ "cmd": "dark", "arg": "on" }` | 深色模式开关 |

与 openos MCP 插件并存互补：`sys:cmd` 是单向指令（无应答），MCP 是面向 AI 的完整控制面（带状态查询与结构化应答）。

## 宿主简报与环境透传

- **宿主简报（host_context）**：桌面态由 goder 应用组装运行环境简报（OpenOS 通用段 + 宿主经 `createOpenOS({ goderContext })` 声明的业务段）注入每个 gh 会话；无头任务注入固定无人值守简报（不调 ask_user、审批自动放行）。旧版 goder 忽略未知字段，混部不受影响。
- **回合事件外发**：`GODER_GH_ENV_GRF_TURN_WEBHOOK_URL/_SECRET` 开 gh 回合终态事件外发（`turn.settled`，HMAC 签名头 `X-GRF-Signature`）——宿主无头任务由轮询转事件驱动，接收端由部署方提供。
- 其余透传变量（`GODER_IDLE_TTL` / `GODER_WEBHOOK_URL/_SECRET` / `GODER_ADMIN_TOKEN`）见[配置参考](configuration.md)。
