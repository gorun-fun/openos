# 宿主集成指南

**简体中文** | [English](host-integration.en.md)

业务系统可以**不 fork 壳代码**，把 OpenOS 当作桌面基础框架消费：宿主前端以 `createOpenOS()` 组装桌面，注入业务应用、品牌与单点登录；服务端之间用共享密钥互通用户体系。这是「方式三」，面向完整业务系统（参考实现：grf-hotel 酒店管理系统、grf-os）。

配套的独立 Go module `hostsdk/` 封装了全部机对机调用（子进程托管 / SSO / BootstrapPage / 用户角色应用 declare / AI 同步），宿主后端直接引用可省去手写 HTTP。

## 前端组壳

```ts
// 宿主应用入口（vite alias: @openos → openos 快照的 web/src）
import { createOpenOS } from '@openos/framework'

createOpenOS({
  // 业务应用（懒加载组件）
  apps: { rooms: () => import('./apps/Rooms.vue') },

  // 可选注入
  icons: { rooms: { svg: '<svg …>' } } as const,     // 业务图标
  brand: { name: '业务系统', logo: '🏨' },            // 品牌（登录页/菜单栏）
  login: { mode: 'sso', url: '/api/auth/sso/login' }, // 登录跳宿主 SSO；401 自动跳回
  theme: { accent: '#e5484d' },                       // 全局强调色（派生色自动计算）
  wallpapers: [...],                                  // 壁纸列表注入
  dockOrder: ['rooms', 'welcome'],                    // Dock 初始排序（用户未自定义时）
  menubarExtras: MyMenuBarExtras,                     // 菜单栏右侧扩展（支持数组多槽）
  appMenus: {...},                                    // 应用菜单注入
  widgets: {...},                                     // 自定义桌面小组件（右键菜单自动出现）
  i18n: { locale: 'zh-CN', messages: {...} },         // 词条注入 / 锁初始语言
  dialogs: {...},                                     // 跨应用弹窗注册
  capabilities: {...},                                // 业务能力暴露给 AI（见 goder 指南）
  remoteApps: { market, hostVersion },                // App 独立发布通道（见下）
})
```

`createOpenOS` 同时是公共 API 面：宿主可直接 import `bus` / `api` / `fsApi` / 全部 Os* 组件 / Pinia stores，无需深入内部路径。

应用可标记 `hidden: true`：可编程打开但不出现在启动台/Dock（适合详情窗口）。

## 服务端互通

### 令牌交换（宿主 SSO → OpenOS 身份）

宿主完成自身认证后，服务端到服务端换取 OpenOS 身份；首次出现的用户自动开通（密码为随机值，登录只能经宿主）：

```bash
# OpenOS 侧启用（仅在宿主同机/可信网络内开放）：
OPENOS_EXCHANGE_SECRET=<共享密钥> ./openos-server

# 宿主侧调用（回跳后把 token 经 URL 片段 #os-token=<jwt> 交给前端）：
curl -X POST http://127.0.0.1:8081/api/auth/exchange \
  -H 'X-Exchange-Secret: <共享密钥>' -H 'Content-Type: application/json' \
  -d '{"username":"zhang","role":"front","displayName":"张前台","perms":["room.view"]}'
# → { "token": "<openos JWT>", "user": {...} }
```

- 密钥恒定时间比较；`OPENOS_EXCHANGE_ROLES` 角色白名单（如 `front,boss,manager`）强烈建议配置——密钥泄露也无法铸造 admin（密钥配置而白名单为空时启动日志告警）。
- `perms[]` 可选：宿主权威库算出的该用户权限并集写入 users.perms（变化即时生效不吊销）；未传时按角色权限矩阵推导。

### 令牌自愈

`GET /api/auth/refresh` 回发新 token 并续种 Cookie。宿主的 401 自愈可直接调它换新令牌（openos 令牌仍有效、仅宿主会话过期等场景），无需每次回 SSO 重新交换。独立部署下前端已内置每 12 小时 + 回前台的滑动续期。

### 反向互通（业务后端验证 OpenOS JWT）

`POST /api/auth/introspect`（RFC 7662 风格，守护同一把密钥，未配置 503）：

```go
// openosToken = 前端业务应用随 API 提交的 OpenOS 令牌
//（壳内应用经 OpenOS.authToken() 取，Authorization: Bearer <openos-jwt>）
func introspect(openosToken string) (id struct {
    Active      bool   `json:"active"`
    Username    string `json:"username"`
    Role        string `json:"role"`
    DisplayName string `json:"displayName"`
    Exp         int64  `json:"exp"`
}, err error) {
    body, _ := json.Marshal(map[string]string{"token": openosToken})
    req, _ := http.NewRequest("POST", "http://127.0.0.1:8081/api/auth/introspect", bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("X-Exchange-Secret", os.Getenv("OPENOS_EXCHANGE_SECRET"))
    resp, err := http.DefaultClient.Do(req)
    if err != nil { return id, err }
    defer resp.Body.Close()
    if err = json.NewDecoder(resp.Body).Decode(&id); err != nil { return id, err }
    if !id.Active { return id, errors.New("openos 会话已失效") }
    return id, nil
}
```

- 响应恒 200：有效 `{"active":true,"sub","username","role","displayName","exp","iat"}`，无效 `{"active":false}`；判定与 REST/WS 同源（吊销即时生效）。
- 逐请求一跳有代价，业务后端应缓存结论（建议 TTL = `min(exp-now, 60s)`）。
- 高信任同机部署可本地 HS256 验签（claims `sub/role/tv/exp/iat`，两方共享 `OPENOS_JWT_SECRET`）——但持有签发密钥等于可伪造任意令牌，跨信任边界一律走内省。

## 机对机 API 组（`/api/host/*`）

与 declare 同一把 `X-Exchange-Secret` 守护的服务端到服务端接口组。宿主**不需要**铸造 admin 服务账号——直接以密钥调用：

| 端点 | 说明 |
|---|---|
| `GET /api/host/users` | 全字段用户列表（含 perms 快照/token 版本/禁用态） |
| `POST /api/host/users` | 显式建用户（密码留空 → 随机值，登录只能经 exchange） |
| `POST /api/host/users/sync` | 批量幂等同步（语义同下「用户同步」） |
| `PUT /api/host/users/{name}` | 字段级更新（role/displayName/disabled/perms/password） |
| `DELETE /api/host/users/{name}` | 删号并级联清理 |
| `GET /api/host/roles` | 角色目录 + 权限点目录（对账/展示） |
| `POST /api/host/apps` / `DELETE /api/host/apps/{id}` | 应用注册（恒信任 locked）/ 强制删除 |
| `GET /api/host/apps` | 全量应用列表（对账/清退旧应用） |
| `GET`/`PUT /api/host/apps/access`、`PUT /api/host/apps/role-labels` | 角色应用授权与显示名 |
| `GET`/`PUT /api/host/ai/user-providers` | AI 每用户供应商 |
| `POST /api/host/goder/tasks*` | goder 无头任务（代指定用户后台执行 AI 会话，见 goder 指南） |

审计 operator 记为 `host`。hostsdk 的 `Manager`（SyncUsers/CreateUser/RoleAppMap/UpsertApp…）已全部封装本组。

**批量用户同步**：`POST /api/users/sync`（users.manage，宿主走 host 版）幂等推送用户清单——不存在则开通（随机密码），存在则按字段更新（角色/显示名/禁用态/权限点快照，变化即时生效不吊销）；最后一名管理员的降级/禁用被跳过并在响应 `skipped` 说明。配合 exchange 的惰性开通，宿主可选「登录时开通」或「启动时全量同步」。

**这是全信任通道**：`OPENOS_EXCHANGE_SECRET` 持有者可管理全部用户体系（含设置 admin 角色）——密钥等同管理员凭证，仅限宿主后端 ↔ OpenOS 同机/可信网络。

## 角色目录与功能级权限

应用级授权（role_apps）之上的功能级权限点模型，双模式并存：

- **system 模式（独立部署）**：`roles`/`role_perms`/`perm_defs` 三表为权威，管理员在「系统设置 → 角色与权限」编辑。
- **host 模式（宿主集成）**：角色与权限的**权威存储留在宿主自己的库**，宿主经 `POST /api/host/declare` 声明式全量投影：

  ```json
  {
    "roles": [
      { "key": "boss", "label": "老板/业主", "perms": ["order.view"], "apps": ["welcome", "calendar"] }
    ],
    "perms": [
      { "code": "order.view", "group": "业务", "label": "订单查看" }
    ],
    "roleWriteback": "http://127.0.0.1:8082/api/v1/openos/role-write"
  }
  ```

  幂等收敛：清单外 host 角色移除（**仍被用户绑定的保留**并在响应 `unresolved` 报告——宿主先把这些用户的角色同步走再重 declare 即收敛）；保存即广播 `sys:apps-changed`、权限即时生效。

- **roleWriteback**：声明回写地址后 host 角色在设置页解锁可编辑——`PUT /api/roles/{key}` 整包转发该地址（携带 X-Exchange-Secret 与 X-OS-User 操作者），宿主权威落库后自行重新 declare 收敛。未声明时 host 角色只读、DELETE 恒拒绝（角色生命周期归宿主）。
- **前端门控**：`can('room.view')` / `v-perm` 指令（admin 直通、`*` 通配、精确匹配，与服务端 RequirePerm 同语义）。
- 角色不限于内置三角色：宿主可注入业务角色，角色为安全字符集的自定义值；内置权限点 `users.manage` / `apps.manage` / `market.manage` / `ai.manage` / `system.manage` 已实际接管对应管理端点（admin 恒直通）。

## 裁剪部署（数据边界）

宿主以「SQLite 只存演示数据 + 每用户界面设置」为目标时：

```bash
OPENOS_VFS=off OPENOS_GODER=off OPENOS_SEED_DEMO=0 \
OPENOS_EXCHANGE_SECRET=<密钥> OPENOS_EXCHANGE_ROLES=front,boss,manager \
./openos-server
```

- VFS/终端类与 goder 内置应用整体下架（含授权矩阵回收），`/api/fs/*` 返回 503——业务文件交由宿主管理；
- 无演示账号、无示例文件，用户全部经 exchange / users/sync 进入；
- 用户禁用即时生效（REST 401、在线总线连接 30s 内踢线；不吊销令牌，重新启用后会话恢复）；
- 删号级联清理全部用户数据（设置/文件/聊天/分享/反馈/AI 供应商/附件 + 磁盘 home + goder 会话目录）。

## App 独立发布（remoteApps + 货架）

宿主业务 App 与宿主镜像解耦的发布通道：App 改动只构建单 App 产物传到货架，用户刷新页面即生效——不经宿主发版、不重启服务。与应用商店并存（商店=管理员安装第三方应用；本通道=宿主自有 App 热发布）。

```ts
createOpenOS({ apps, remoteApps: { market: 'https://…/hotel/market', hostVersion: 'v0.0.84' } })
```

每个 App 优先按货架 catalog 加载远程产物；货架不可达 / 无条目 / `minHostVersion` 不满足 / import 失败均自动回落 `apps` 本地兜底（打进宿主 dist 的 chunk）。货架协议与构建三件套（`web/tooling/`：deps 库集构建 / 宿主 external 化 + importmap 注入 / 单 App 单文件产物 + catalog 合并）见 `web/src/remote.ts` 文件头与 `web/tooling/` 源码注释。

硬约束：**deps-manifest 登记的裸名禁止打进任何 bundle**（双 Vue/pinia 实例会致 toast/store 断裂）；宿主工程须有 esbuild devDep 且 vite 配置声明 postcss CSS 管线（Lightning CSS 会折叠 backdrop-filter 前缀对致毛玻璃失效，已踩实）。

## 微前端与第三方站点接管（config.hook）

kind=proxy 网页应用的 config 可声明 `hook` 字段（JS 源码 ≤256KB，随通用 hook.js 之后注入代理页面）：宿主平台监听目标站接口/DOM，`postMessage` 通知宿主页，再经 `OpenOS.launch` 拉起自家业务应用——判据与命中逻辑全部在宿主仓库维护，经 `/api/host/apps` 随时更新 config 即热生效。详见[统一代理指南](webproxy.md)。
