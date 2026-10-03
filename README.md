# OpenOS

前后端分离的仿 macOS 网页桌面操作系统。**Go + Vite (Vue 3)**，以**事件总线**为核心通信方式，提供统一基础 UI 与强大的第三方开放能力，内置 **goder** AI 助手可操控所有界面。面向产品级使用：完整的安全加固、审计、配额、优雅停机与容器化部署。

![tech](https://img.shields.io/badge/Go-1.26-00ADD8) ![tech](https://img.shields.io/badge/Vue-3.5-42b883) ![tech](https://img.shields.io/badge/SQLite-WAL-003B57)

## 功能总览

| 模块 | 说明 |
|---|---|
| 🪟 窗体管理 | 拖拽、八向缩放、红绿灯、**边缘吸附（上=最大化/左右=半屏，带预览）**、最小化飞入 Dock 动画、焦点层级、窗口状态跨会话恢复、**最大化直角全屏形态** |
| 🔎 聚焦搜索 | **Spotlight 拟合（⌘/Alt+Space 或菜单栏放大镜）**：应用 + 文件递归搜索 + 即时计算器 + 系统操作，文件结果带右侧预览面板（种类/大小/时间/缩略图） |
| 🚀 桌面体验 | 启动台（F4，带搜索）、Dock 邻居放大、**Dock 可定制（拖动排序 / 拖出或右键取消固定 / 从启动台·桌面快捷方式拖入固定，未固定的运行应用临时驻留）**、**Dock 废纸篓（满/空状态、右键清倒）**、完整菜单栏（系统/窗口/帮助/关于、快捷键字形、macOS 时间格式）、快捷键（Alt+W/M/↩/\`）、窗口级错误边界、401 全局登出 |
| 🚌 事件总线 | WebSocket 主题订阅/发布，`*` 通配；本地即时分发 + 跨端广播；断线自动重连并重放订阅；服务端处理器同总线挂载 |
| 🧩 开放能力 | ① 组件型：`OpenOS.registerApp()`（支持 template 字符串）② 声明式：JSON Schema 注册即得新应用（**带鉴权 REST / POST / 点路径 / 总线推送刷新**）③ Os* 业务组件库：**表格分页排序与单元格渲染插槽、表单校验（OsForm/OsField）、上传（OsUpload 带进度）、全局 Toast、打开/存储文件对话框（`fileDialog` 复用文件管理窗口选择；OsFilePicker 为窗口内 Sheet 形态）** |
| ✦ Goder - AI助手 | **完整 AI 编程 IDE 内置**（goder 嵌入式集成：项目/会话/聊天/审查/终端/事件台，每用户独立 gh server，与 grf-play 同款集成方式），**默认置于桌面** |
| 🗒 便签 | 多张彩色便签随手记（自动保存、换色、删除），一键**钉到桌面**成为小组件；桌面便签可拖动、右下角缩放（**尺寸为网格整数倍，精确对齐**） |
| 🔔 通知中心 | **系统级通知（仿 macOS）**：任何应用经 `OpenOS.notify()` 或总线 `sys:notify` 发送 → 右上角横幅（悬停暂停/点击跳转来源应用）+ **菜单栏时钟唤出的通知面板**（历史回看、未读红点、提示声/勿扰开关、一键清除）；历史与偏好按用户持久化；goder 的 `os_notify` 同走此通道 |
| 📅 日历 | **中国节假日同步（内置已发布年份 + holiday-cn/timor 双源在线更新，调休「休/班」标记）**、农历/传统节日副行（干支/生肖）、日程管理（标题/日期/时间/提前提醒：事件时刻~提前1天）、**提醒到期经通知中心横幅弹出（应用关闭也提醒）**；桌面日历小组件（今日大字 + 迷你月历，点日期直达） |
| 📦 内置应用 | **双模式终端（虚拟 ⇄ 真实 shell，ANSI 彩色，Tab 补全，一体式工具栏）**、**访达式文件管理（见下）**、计算器（圆形按键/明暗主题/退格）、系统设置（彩色边栏 + 分组行 + 壁纸缩略图）、**通用网页应用容器（iframe 直嵌 / 服务端代理两形态，管理界面可零代码收录站点）** |
| 🗂 文件（访达拟合） | **图标/列表/分栏/画廊四视图**、**右键菜单（打开/打开方式/快速查看/显示简介/拷贝/剪切/复制副本）**、**多选（⌘点选/Shift 范围/⌘A）**、**键盘导航（方向键/⏎重命名/空格快速预览/⌫ 移到废纸篓）**、**拖拽移动（拖入文件夹/侧栏目录/废纸篓，⌥ 拖拽复制）**、**废纸篓（移入/清倒/Dock 联动）**、图片缩略图、底部路径栏、原位重命名、`/api/fs/search` 递归搜索与 `/api/fs/copy` 复制端点 |
| 📑 办公应用 | **文档 = Markdown 编辑器（编辑/分栏/预览，⌘S/另存为/打开…，自动保存，md 源码保真）**；**表格 = CSV/TSV（动态行列、边缘自动扩张、⌘S/另存为/打开…、Excel 剪贴板互通、公式引擎）**；**幻灯片 = HTML 格式（画布直接富文本编辑、深浅主题、演示模式、导出独立 HTML 双击即可放映、兼容旧 .slides.json 自动升级）**；PDF（文件空间/本机打开、翻页缩放）——三者均直接读写服务端文件并与文件应用双击联动 |
| 👥 多用户多角色 | admin/user/guest，角色×应用授权矩阵实时生效，**角色变更/删除用户令牌即时吊销** |
| 🔐 安全 | pbkdf2-20000（旧 hash 透明升级）、JWT 带版本号、登录防爆破锁定、IP 限流、安全响应头、审计日志、最后管理员保护、路径越权防护 |
| ⚙️ 运维 | 访问日志、panic 恢复、优雅停机、系统统计/审计查看、Docker 一键部署、多平台发布包 |

## 快速开始

```bash
# 一键准备（含 goder 组件：构建 goder-serve 网关 + 拷贝嵌入库）
make goder        # 源码自动克隆自 https://git.gorun.fun/share/goder.git（需 go/npm/git）

# 开发模式（后端 8081 + 前端 5173 热更新）
make dev

# 或分步：
cd server && go run .                    # 后端
cd web && npm install && npm run dev     # 前端

# 生产部署（单二进制 + 静态目录）
make build && cd server && ./openos-server   # → http://127.0.0.1:8081

# Docker
docker compose up -d --build   # → http://localhost:8081（数据默认持久化在宿主机 /app/docker/openos，本地开发可设 OPENOS_DATA_DIR=./data）

# 多平台发布包（darwin/linux × amd64/arm64）
make release

# 单元测试
make test
```

**默认账号**：`admin/admin123`（管理员）· `user/user123`（普通）· `guest/guest123`（访客）
⚠️ 生产部署后请立即在「系统设置 → 账户」修改默认密码（系统已强制密码强度 ≥8 位且含字母数字）。

### 接入真实 AI（goder）

**推荐方式：在界面配置（即时生效，无需重启）**——管理员进入「系统设置 → AI 服务」，填入任意 OpenAI 兼容接口的 Base URL、API Key、模型名，保存即接入真实大模型，可一键「测试连接」。密钥仅存于服务端 SQLite，界面始终脱敏。

也可通过环境变量在首启时回填：

```bash
export OPENOS_AI_BASE_URL=https://open.bigmodel.cn/api/paas/v4
export OPENOS_AI_API_KEY=sk-xxx
export OPENOS_AI_MODEL=glm-4-flash
```

未配置时相关 AI 能力（终端 `ai` 指令、`/api/ai/chat` 开放接口）运行于本地演示模式。

其他环境变量：`OPENOS_ADDR`（默认 127.0.0.1:8081，容器内为 0.0.0.0:8081）、`OPENOS_DATA`（数据目录）、`OPENOS_DIST`（前端目录）、`OPENOS_JWT_SECRET`（默认自动生成并持久化到 system_kv 表）。

**功能裁剪与数据治理**（宿主集成 / 数据边界，详见「方式三」）：

| 环境变量 | 说明 |
|---|---|
| `OPENOS_VFS=off` | 下架 VFS/终端类内置应用（终端/文件/文档/表格/幻灯片/PDF），`/api/fs/*` 统一 503——业务文件交由集成方管理 |
| `OPENOS_GODER=off` | 停用 goder（不启动子进程、下架应用）——纯业务宿主不必携带 AI IDE |
| `OPENOS_SEED_DEMO=0` | 不种默认演示账号与 VFS 示例文件（宿主模式账号体系完全交由集成方） |
| `OPENOS_EXCHANGE_ROLES` | exchange 角色白名单（逗号分隔，如 `front,boss,manager`）——共享密钥泄露也无法铸造 admin |
| `OPENOS_AUDIT_RETENTION_DAYS` | 审计日志保留天数（默认 90，0=永久）；启动与每日自动清理 |
| `OPENOS_CHAT_RETENTION_DAYS` | AI 对话历史保留天数（默认 90，0=永久），含终端伪用户 `__term__` 孤儿数据 |

## 架构

```
┌────────────────────────── 浏览器 ──────────────────────────┐
│  Desktop Shell（菜单栏 / Dock / 启动台 / 窗口管理器）       │
│  ├── 基础 UI 库（OsButton/OsInput/OsTable/OsLayout…）      │
│  ├── App SDK（registerApp 组件型 · SchemaApp 声明式）      │
│  └── 内置 App（终端/文件/计算器/便签/设置/文档/表格/幻灯片/PDF/goder）│
│        │ REST /api/*                │ WS /ws（事件总线）   │
└────────┼────────────────────────────┼─────────────────────┘
         ▼                            ▼
┌─────────────────── Go 服务端 ─────────────────────────────┐
│ 恢复/日志/安全头/限流 → 路由 → auth(JWT+防爆破+热吊销)      │
│ apps(注册表+角色授权)  vfs(虚拟FS+终端+配额+二进制)         │
│ bus(Hub)  ai(在线配置热更/流式/历史)  audit(审计日志)       │
│                     SQLite（WAL，modernc 纯 Go）           │
└───────────────────────────────────────────────────────────┘
```

### 事件总线协议

```jsonc
// 客户端 → 服务端
{ "action": "sub",  "topics": ["sys:*", "fs:changed"] }      // 订阅（* 结尾前缀匹配）
{ "action": "pub", "topic": "app:calc", "payload": {...} }   // 发布（不回声发布者）

// 服务端 → 客户端
{ "topic": "app:calc", "payload": {...}, "from": "alice", "ts": 1760000000 }
```

**内置主题**：`sys:open` · `sys:cmd`（goder 指令）· `sys:apps-changed` · `sys:presence`（连接上下线事件，连接粒度——同用户多标签页多条连接）· `sys:notify`（系统通知：前端 `notify({broadcast:true})` 广播 / 服务端 `PublishTo` 定向）· `sys:agent`（goder 桌面控制插件下行，PublishTo 定向到用户）· `agent:resp`（前端桥应答，服务端处理器接住）· `fs:changed` · `term:<user>` · `chat:goder` · `chat:room`（聊天室应用，全员默认订阅）

### 在线用户与系统附件

- **在线用户**：`GET /api/presence` 返回当前在线用户名快照（登录可用）；增量走 `sys:presence` 主题事件 `{user, online}`。
- **聊天室消息历史**：总线 `chat:room` 的消息由服务端处理器落库（`chat_room_messages` 表，From/TS 由 Hub 注入不可伪造；附件存内嵌快照，附件本体可能先被保留期清理）；`GET /api/chat/history?before=<id>&limit=50`（登录可用，倒序分页、正序返回，limit 上限 100）查询；保留期随 `OPENOS_CHAT_RETENTION_DAYS`（默认 90 天）清理。同一用户多标签页 = 多条连接，应用侧应按连接计数（在线名单以快照纠偏为准）。
- **系统附件**（`server/internal/attach`）：平台级文件分享能力，聊天附件等共用——`POST /api/attachments`（multipart 字段 `file`，单文件 ≤10MB，登录）上传返回 `{id,name,mime,size}`（id 为 128bit 随机不可枚举，mime 以内容嗅探为准）；`GET /api/attachments/{id}?token=<jwt>` 下载（`<img>/<a>` 标签带不了 Authorization header，鉴权走查询参数同 `/ws`；`api.upload()` 客户端方法自动携带登录态）。**安全边界**：Content-Type 只对图片白名单（png/jpeg/gif/webp/bmp/avif）内联，其余（含 SVG/HTML——可携带脚本）一律 `application/octet-stream` 下载 + `nosniff`，附件服务不可变成 HTML 注入源；保留期清理 `OPENOS_ATTACH_RETENTION_DAYS`（默认 30 天，0=永久），文件存 `<data>/attachments/`。

### 前端总线 API

```ts
import { bus } from '@/bus/bus'
bus.on('app:calc', (payload, from, topic) => {})   // 订阅（支持通配）
bus.emit('app:calc', { a: 1 })                     // 本地 + 远程发布
bus.emit('app:calc', { a: 1 }, { remote: false })  // 仅本地
bus.remote('term:admin', 'ls')                     // 仅发服务端处理器
```

订阅任意主题（含业务自定义主题，如 `hotel:rooms`）时会自动向服务端补发远程订阅，
跨端推送即订即达；本地最后一个消费者移除后自动退订。

## goder 工作台（内置 AI 编程 IDE）

OpenOS 按官方嵌入约定（`goder/docs/embedding.md`，与 grf-play 同款方式）把 [goder](https://git.gorun.fun/share/goder.git) 作为内置应用：

- **后端**托管 `goder-serve` 子进程（header 身份模式）：`/goder-api/*` 反代并注入 `X-GRF-UID/X-GRF-Name`（先剥离外部伪造头），SSE 透传；`/vendor/goder.js` 提供自包含嵌入库
- **每个 OpenOS 用户对应一个独立 gh server 进程**（独立端口/数据目录/令牌，令牌不下发浏览器）；新用户自动种子宿主 `~/.grf` 模型配置（`GODER_SETTINGS_SEED=1`）
- **鉴权**：OpenOS 登录后签发 `openos_token` Cookie（httpOnly），goder 嵌入组件的同源请求自动携带，经反代完成身份注入
- **前端**：goder 应用窗口动态 `import('/vendor/goder.js')` → `configure('openos', { baseUrl: '/goder-api', user, sideTop })` → 渲染 `<goder-app-shell panels="sessions,chat,sidebar,console">`（左会话/中聊天/右栏 tab/底事件台）

部署（两种方式）：

**Docker（推荐）**：goder 组件已全部内置在镜像中（构建时自动克隆 `https://git.gorun.fun/share/goder.git` 编译 goder-serve 与 goder.js，并从 `download.gorun.fun/grf-harness` 下载对应架构的最新 gh），部署即用，无需额外操作。

**本地/裸机**：`make goder` 自动完成（源码拉取自 `https://git.gorun.fun/share/goder.git`，克隆到仓库内 `.goder-src/`，可用 `GODER_REPO`/`GODER_SRC` 覆盖）：

```bash
# make goder 等价于：
cd <goder 源码> && ./build.sh embed        # 构建嵌入库 frontend/dist-embed/goder.js（需 npm）
cd <goder 源码> && go build -o <openos>/server/data/bin/goder-serve ./cmd/goder-serve
cp <goder 源码>/frontend/dist-embed/goder.js <openos>/server/data/goder/
# 前置：gh 可执行文件在 PATH（GODER_BIN 可覆盖）；组件缺失时 goder 应用内显示部署引导
```

相关环境变量：`OPENOS_GODER_BIN` / `OPENOS_GODER_JS` / `OPENOS_GODER_ADDR`（默认 127.0.0.1:7711）/ `OPENOS_GH_BIN`；`OPENOS_GODER=off` 可整体停用（不启动子进程、下架应用，纯业务宿主建议开启此开关）。

### 真实终端

终端应用支持双模式切换（右上角）：

- **虚拟**：OpenOS 虚拟文件系统（与文件应用同源，`open <app>` 可操控桌面）
- **真实**：服务端 `sh -c` 受控执行——个人沙箱目录（`<data>/homes/<user>`）、10s 超时、64KB 输出截断、`cd` 服务端跟踪（受限沙箱内）、ANSI 彩色渲染

`OPENOS_REAL_SHELL=off` 关闭；`=full` 解除沙箱限制（自担风险）。

### goder 桌面控制插件（openos MCP）

goder 里的 AI（gh 智能体）可通过内置的 **openos MCP 插件** 控制当前用户桌面上的所有功能应用：开/关/排列窗口、切换深色与壁纸、管理便签、读写 VFS 文件（文档/表格应用的数据）、发通知、发布任意总线主题（驱动声明式/第三方应用）。goder 启用时自动部署，无需配置。

**架构**（不修改 goder 上游，接入方式与 goder 自带 `goder-console` 插件同款）：

```
gh server（每用户一个，goder-serve 子进程）
  └─ 自动发现 grf-home/plugins/openos（wrapper 脚本，OpenOS 落盘）
       └─ exec openos-server mcp-shim <endpoint> <token> <user>   ← stdio MCP 插件
            ↕ HTTP JSON-RPC（127.0.0.1:7712/mcp，Bearer 令牌 + X-OS-User）
openos-server 进程内 osmcp 包（20 个 os_* 工具，gh 侧全名 mcp__openos__os_*）
  ├─ 服务端直答：应用列表 / 用户设置 / VFS 文件（复用 vfs 权限与配额）/ 总线发布
  └─ 前端桥：PublishTo(user,"sys:agent") ↓ 浏览器 agentBridge 执行
              bus.remote("agent:resp") ↑ 回传结果（15s 超时兜底）
```

**工具清单**（gh 把描述拼进系统提示词，AI 自主选用）：

| 类别 | 工具 |
|---|---|
| 系统 | `os_info`（版本/用户/goder 状态/在线用户）、`os_apps_list`（可用应用） |
| 窗口/桌面 | `os_desktop_state`（窗口列表+外观实时状态）、`os_window_open/close/do`（focus/minimize/最大化/半屏吸附/移动尺寸/改标题）、`os_app_open_file`（按扩展名选应用打开）、`os_desktop_do`（壁纸/深色/Dock 自动隐藏）、`os_notify`（系统通知，入通知中心可回看） |
| 数据 | `os_notes_do`（便签增删改查）、`os_settings_get/put`、`os_fs_list/read/write/mkdir/delete/search/usage`（VFS，限定 `/home/<用户>`、≤8MB、配额 100MB，写入广播 `fs:changed` 自动刷新文件应用） |
| 通用通道 | `os_bus_publish`（向自己桌面发布任意总线主题，可驱动声明式应用的 `dataSource.topic`） |

**安全边界**：MCP 端点仅监听回环地址 + Bearer 令牌（`<data>/goder/osmcp.json`，0600 持久化）；wrapper 0700；工具执行前校验用户存在且未禁用；**所有操作只作用于发起用户本人**（不提供跨用户与管理员操作）；写类工具入审计日志（`goder.tool`）。

**生效时机与自愈**：新用户首次访问 `/goder-api` 时落 wrapper（先于该用户 gh 懒启动，gh 启动即发现插件）；服务重启时扫描存量用户目录自动重装（应对二进制路径变化，dev 的 `go run` 亦适用）。**已在运行的存量 gh server 不热加载插件**——部署本功能后重启 openos-server 即可（容器部署天然干净）；宿主 `~/.grf` 种子若预置了 `plugins` 键（显式列表模式），插件以命令项 upsert 进入，同样需 gh 重启。

相关环境变量：`OPENOS_OSMCP=off`（停用插件）、`OPENOS_OSMCP_ADDR`（默认 127.0.0.1:7712，被占自动回退随机端口）。

**与 `sys:cmd` 的关系**：`sys:cmd` 是轻量指令通道（见下节），适合脚本与简单场景；openos MCP 插件是面向 AI 的完整控制面（带状态查询与结构化应答），两者并存互补。

## 通知中心与日历

### 通知中心（系统级通知，仿 macOS）

任何应用（内置 / 宿主 / 第三方脚本 / goder AI）都可以发系统通知：右上角横幅自动消失（悬停暂停、点击跳转来源应用），并记入**通知中心面板**（点菜单栏时钟唤出；未读时时钟显小红点）。面板支持提示声开关、勿扰模式（横幅静默、仅入列表）与一键清除；通知历史与偏好按用户持久化（`user_settings` 的 `notifications` 键，最多保留 100 条，跨设备一致）。

```ts
import { notify } from '@/framework'          // 或 window.OpenOS.notify（第三方脚本）

// 本机通知：横幅 + 入通知中心
notify({ title: '下载完成', body: 'report.pdf 已保存', appId: 'files' })

// 广播通知：同一账号其他在线端（或 sys:* 订阅者）各自弹出（总线 sys:notify）
notify({ title: '系统公告', body: '今晚 22:00 例行维护', broadcast: true })
```

服务端推送（定向到某个用户的桌面）：`hub.PublishTo(user, "sys:notify", map[string]any{"title": "...", "body": "...", "appId": "..."})`；goder 桌面控制插件的 `os_notify` 工具同走通知中心。提示音由 WebAudio 合成（无外部资源）。

### 日历（中国节假日 + 农历 + 日程提醒）

- **节假日**：后端 `GET /api/calendar/holidays?year=YYYY`（需登录）返回 `{year, source, days:[{name,date,isOffDay}]}`；`isOffDay=false` 为调休补班日。数据分层：**内置已发布年份**（2025/2026，与国务院办公厅安排一致）→ **在线同步其余年份**（holiday-cn → timor.tech 双源回退，结果缓存 24h；未发布年份缓存 1h 后重试，`days` 恒为数组）。`OPENOS_HOLIDAY_SYNC=off` 可关闭在线同步（纯内网部署）。
- **月历**：周一开头；网格线形态，**上下滚动/滑动翻月**（带滑入动画）；法定节假日红字 + 「休/班」角标，农历日/传统节日（春节/元宵/端午/七夕/中秋/重阳/腊八/除夕）作副行，选中日显示干支生肖年全称（农历换算为内置 1900-2100 速查表算法，无外部依赖）。
- **自由日标记**：应用可在任意日期上叠加自定义彩色标签（月历格子内 chip、选中日徽标、小组件彩点三处渲染；内存态不持久化，生命周期由来源应用维护；同 `appId` 同日重复登记为替换）：`OpenOS.calendar.mark('2026-10-05', { appId: 'release', text: '发布', color: '#e5484d' })`，批量传日期数组；`unmark(dates, appId?)` 移除，`marksOf(date)` 查询。
- **日程与提醒**：事件存 `user_settings` 的 `calendar.events` 键（`{id,title,date,time?,remind}`，自动保存）；提醒可选事件时刻/提前 5m/15m/1h/1 天（全天事件按 09:00 计），到期由桌面级提醒引擎（30s 轮询）经**通知中心**横幅弹出——日历应用关着也会提醒；已触发记录防重复。
- **桌面小组件**：右键桌面 → 添加小组件 → 日历（今日大字 + 迷你月历含节假日红字/班标/事件圆点/日标记彩点；点任意日期打开日历并定位到该天）。
- **SDK**：`OpenOS.calendar.list()/add()/remove()/holidayOf()/mark()/unmark()/marksOf()/today()`；宿主可经 `@openos/framework` 导入 `useCalendar` 等直接读写。

## 应用商店

内置「应用商店」应用（`web/src/apps/store/` + 服务端 `internal/market`）：浏览目录 → 详情（权限说明/版本日志）→ 管理员安装/更新/卸载，安装时一次性选择授权角色（admin 恒有授权），安装/卸载经 `sys:apps-changed` 总线广播实时出现在 Dock/启动台。

- **目录（多源）**：远程清单**并发拉取合并**（启动即拉、此后每 10 分钟，单源限 2MB/15s 超时）——**自定义源**（管理员在「商店 → 设置」添加，最多 10 个，`GET/PUT /api/market/sources`，持久化 system_kv `market_sources`）排在**官方默认源** `OPENOS_MARKET_URL`（默认 `https://download.gorun.fun/openos/market/catalog.json`）之前，同 id 条目**首见优先**（自定义源可遮蔽官方）；任一源成功即以成功源合并结果为生效目录，全部失败沿用现行，初始兜底 = 随二进制发布的内置清单 `internal/market/catalog.json`（go:embed）。保存自定义源后服务端立即拉取（响应带成功源数与条目数）。`OPENOS_MARKET_URL=off` 只关官方默认源，自定义源仍可用（纯内置则还需清空自定义源）。更新货架只改远端 JSON，无需发版；宿主也可把默认源指向私有清单。内置与远程共用同一套条目校验（`TestCatalogValid`/`parseCatalog`，坏条目跳过记日志）。收录形态三档：**网页应用**（`type:web`，iframe 直嵌允许被嵌的站点，渲染走 WebApp 容器）、**声明式应用**（`type:schema`，config 即 SchemaApp schema，无代码执行）与**组件应用**（`type:bundle`，见下条）；`type:proxy` 预留（webproxy 反代，安装即放行其目标主机）。web/proxy 条目收录前用 `curl -sIL` 核对目标站无 `X-Frame-Options`/`frame-ancestors` 拒嵌头。
- **组件应用（bundle 档）**：单文件 ES 模块（≤2MB，地址强制 https），**默认导出 Vue 组件定义**（`{ template, setup }`，template 由主程序完整版 Vue 运行时编译），不 import 任何依赖，能力经 `window.OpenOS` 注入（`vue`/`bus`/`api`/`me()`/`ui`/`toast` 等，见 `web/src/sdk/index.ts`）。安装时服务端从目录地址**下载落盘** `<data>/market/bundles/<id>-<version>.js`（下载失败即安装失败；升级换代自动清理旧版本文件，卸载即删），运行期经 `GET /api/market/bundle/{id}?v=&token=` 提供（ESM import 无法带 Authorization header，鉴权走查询参数 token 同 `/ws`；逐请求校验登录态 + `source=market` + 角色授权），前端 `BundleApp` 容器动态 import 渲染——**代码只在管理员安装那一刻取一次远端，运行期不依赖外部源**；与内置应用同级信任，宿主只应收录可信来源的目录。货架源文件在仓库 `market/` 目录（远端镜像 + bundles + icons，上传方法见其 README）。首个 bundle 应用：**聊天室**（`chat`，消息走总线 `chat:room` 主题实时推送、在线状态走 `sys:presence` 事件 + `GET /api/presence` 快照；v1.1.0 起支持图片/文件附件——先传系统附件服务再随消息发引用，应用没打开时新消息由桌面壳常驻监听（`desktop/appNotifier.ts` 规则驱动）经通知中心提醒）。
- **安装模型**：`POST /api/market/install {id, roles}`（admin）= `UpsertApp`（`builtin=0/locked=0`）+ `SetAppRoles` 整组覆盖该应用的角色授权；未知角色忽略（防 role_apps 幽灵行），审计 `market.install`。卸载走既有 `DELETE /api/apps/{id}`（受 builtin/locked 保护规则约束）。
- **版本与升级**：apps 表 `version`/`source` 列标记商店安装（`source=market`）；`GET /api/market/apps` 聚合本机状态返回 `installed/upgradable/managed/conflict/protected`。UpsertApp 对 version/source **空值不覆盖**——管理员经设置页编辑商店应用不断开升级链。目录版本较新时详情页出现「更新」。
- **边界**：与内置/宿主锁定（builtin/locked）应用撞 id 的条目拒绝安装（`protected` 状态 + HTTP 403）；同 id 手工注册应用存在时标记 `conflict`，重装覆盖其配置。普通用户可浏览目录与打开已装应用，安装/更新仅管理员。
- **裁剪**：`OPENOS_MARKET=off` 下架商店应用与 `/api/market/*` 路由（同 `OPENOS_VFS`/`OPENOS_GODER` 模式）。

## 第三方接入指南

### 方式一：声明式应用（零前端代码，推荐）

管理员在 **系统设置 → 第三方应用** 粘贴 JSON Schema 注册（示例模板一键填入）：

```jsonc
{
  "title": "团队公告板",
  "dataSource": {
    "kind": "static",                    // 或 "rest"（字段见下）
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
| `url` | 请求地址。**相对路径（`/api/...`）默认携带当前登录态**（走统一 API 客户端，含 401 全局处理）；跨域绝对地址默认裸 fetch 避免令牌外泄，可用 `auth: true` 强制携带 |
| `method` / `body` | 默认 GET；`POST` + `body`（JSON 对象）发送请求体 |
| `headers` | 自定义请求头（键值对象） |
| `path` | 响应取值路径，支持点路径（如 `"data.items"`） |
| `interval` | 轮询间隔秒（最小 5s），适合无推送的旧接口 |
| `topic` | **总线推送刷新**：收到该主题消息（本地或跨端）立即重取数据，支持通配（如 `"app:rooms"`），与 `interval` 可叠加 |

注册后立即可在 Dock 使用，并可授权给任意角色（授权变更实时广播到所有在线端）。

### 方式二：组件型应用（完整前端能力）

第三方脚本（控制台或注入）调用全局 SDK：

```js
// OpenOS.registerApp(id, { name, width, height, component })
// 可用：OpenOS.bus / OpenOS.ui（基础组件库）/ OpenOS.api（带登录态，含 api.upload 文件上传）
//      / OpenOS.open/close / OpenOS.toast / OpenOS.notify / OpenOS.me() / OpenOS.authToken()
//      / OpenOS.vue（Vue 具名导出，商店 bundle 应用写组件逻辑用）
OpenOS.registerApp('todo', {
  name: '待办', width: 420, height: 400,
  component: {
    data: () => ({ items: ['体验 OpenOS'], text: '' }),
    template: '<div style="padding:16px">…</div>',
  },
})
OpenOS.open('todo')
```

> 应用根元素约定：窗体内容区是 flex 行容器且不强制子项拉伸，根元素需自带
> `width: 100%`（高度随 flex 自动铺满）。缺了它内容按 max-content 收缩，
> 窗口拖大时内容不会跟着舒展。

### 方式三：宿主应用集成（编译期组壳，面向完整业务系统）

业务系统可以**不 fork 壳代码**，把 OpenOS 当作桌面基础框架消费：宿主前端以
`createOpenOS()` 组装桌面，注入自己的业务应用、图标、品牌与单点登录：

```ts
// 宿主应用入口（vite alias: @openos → openos 快照的 web/src）
import { createOpenOS } from '@openos/framework'
createOpenOS({
  apps: { rooms: () => import('./apps/Rooms.vue') },   // 编译期注册业务 App（懒加载）
  icons: { rooms: { svg: '<svg …>' } },                // 业务图标（可选）
  login: { mode: 'sso', url: '/api/auth/sso/login' },  // 登录跳宿主 SSO
  brand: { name: '业务系统', logo: '🏨' },
  menubarExtras: MyMenuBarExtras,                      // 菜单栏右侧扩展组件
})
```

服务端配套 **令牌交换**：宿主完成自身认证后，服务端到服务端换取 OpenOS 身份
（首次出现的用户自动开通，登录只能经宿主）：

```bash
# OpenOS 侧启用（仅在宿主同机/可信网络内开放）：
OPENOS_EXCHANGE_SECRET=<共享密钥> ./openos-server

# 宿主侧调用（回跳后把 token 经 URL 片段 #os-token=<jwt> 交给前端）：
curl -X POST http://127.0.0.1:8081/api/auth/exchange \
  -H 'X-Exchange-Secret: <共享密钥>' -H 'Content-Type: application/json' \
  -d '{"username":"zhang","role":"front","displayName":"张前台"}'
# → { "token": "<openos JWT>", "user": {...} }
```

配套能力：应用可标记 `hidden`（可编程打开、不出现在启动台/Dock，适合详情窗口）；
角色不限于内置三角色——宿主可注入业务角色并按 `role_apps` 矩阵授权（授权矩阵接口
会动态返回全部已出现角色）。401 时 SSO 模式自动跳回宿主登录入口。

**令牌自愈**：`GET /api/auth/refresh` 会**回发新 token**（并续种 Cookie）。宿主的 401
自愈可直接调它换新令牌，无需每次回 SSO 重新交换（openos 令牌仍有效、仅酒店会话过期
等场景）；独立部署下前端已内置每 12 小时 + 回前台的滑动续期，长开桌面不掉线。

**批量用户同步**：`POST /api/users/sync`（admin）幂等推送用户清单——不存在则开通
（随机密码），存在则按字段更新（角色/显示名/禁用态，变化即热吊销）；最后一名管理员
的降级/禁用会被跳过并在响应 `skipped` 中说明。配合 exchange 的惰性开通，宿主可选
「登录时开通」或「启动时全量同步」两种策略。

**裁剪部署（数据边界）**：宿主以 SQLite 只保存**演示数据与每用户界面设置**为目标时：

```bash
OPENOS_VFS=off OPENOS_GODER=off OPENOS_SEED_DEMO=0 \
OPENOS_EXCHANGE_SECRET=<密钥> OPENOS_EXCHANGE_ROLES=front,boss,manager \
./openos-server
```

- VFS/终端类与 goder 内置应用整体下架（含授权矩阵回收），`/api/fs/*` 返回 503；
- 无演示账号、无示例文件，用户全部经 exchange / users/sync 进入；
- 用户禁用（`PUT /api/users/{name}` `{"disabled":true}`）即时生效：REST 立即 401、
  在线总线连接 30s 内踢下线；
- 删除用户级联清理磁盘数据（真实终端沙箱目录、goder 会话目录）；
- 审计与对话历史按保留期自动清理，jwt_secret 存于独立的 `system_kv` 表
  （用户设置数据可安全导出/清理，不波及系统密钥）。

**壳定制**（`createOpenOS()` 新增选项）：`theme: { accent }`（全局强调色，选中/焦点/
渐变派生色自动计算）、`wallpapers`（壁纸注入）、`dockOrder`（Dock 初始排序——用户
未自行定制 Dock 时的默认顺序，用户拖动/固定/移除后以个人配置为准）、
`appMenus`（应用菜单注入）、`widgets`（自定义桌面小组件，右键菜单自动出现）、
`menubarExtras` 支持数组多槽。宿主可从 `@openos/framework` 直接导入 `bus` / `api` /
`fsApi` / 全部 Os* 组件 / 状态仓库，无需深入内部路径。

### 多应用联动（跨应用弹窗与数据服务）

业务系统常有「在 A 应用办到一半，需要 B 应用的一项能力/一份数据」的诉求，OpenOS
内置两条联动通道（见 `web/src/ui/dialogService.ts` 与 `web/src/stores/windows.ts`）：

**① 跨应用弹窗——拉起 B 应用的业务弹窗，不打开 B 的主窗口**

弹窗由提供方应用注册（归属清晰、可懒加载），调用方一行代码拉起并 `await` 业务结果：

```ts
// 房态管理注册「入住」弹窗（方式三编译期注册；运行时等价 OpenOS.registerApp 的 dialogs 选项，
// 或独立 OpenOS.registerDialog('roommgmt:checkin', { component: CheckInDialog, title: '办理入住' })）
createOpenOS({
  dialogs: { 'roommgmt:checkin': { component: () => import('./dialogs/CheckInDialog.vue'), title: '办理入住', width: 520 } },
})

// 订单应用：点击「入住」按钮 → 直接拉起房态管理的入住弹窗，办理完拿到结果继续本应用流程
const checkedIn = await OpenOS.openDialog('roommgmt:checkin', { roomId, orderId })
if (checkedIn) toast('入住成功', { type: 'success' })
```

弹窗组件认三个 props：`params`（调用入参）、`close(result)`（完成并回传，成为
`openDialog` 的 Promise 值）、`cancel()`（取消，Promise 以 `undefined` 收场）。
Esc / 点遮罩默认等同取消（`openDialog` 第三参 `{ escClose, backdropClose, title, width }`
可调）。渲染为系统级模态浮层（`DialogHost`，视觉同 OsModal），支持叠开。

**② 跨应用数据服务——B 应用的数据给 A 应用用**

原则是**数据权威在后端**：金额/房态等业务数据只存一份（宿主后端），各应用经
REST 各自拉取，不存在前端互抄。需要跨应用/跨窗口实时同步时走事件总线：

```ts
// 房态管理：金额变化后广播（app:* 前缀默认远程订阅——多标签页/多窗口自动同步）
bus.emit('app:hotel.finance:changed', { date: '2026-10-02', income: 4820 })

// 经营应用：订阅刷新自己的统计视图（bus.on 返回卸载函数，组件卸载时调用）
const off = bus.on('app:hotel.finance:changed', (p) => refresh(p.date))
```

同 bundle 的宿主应用（方式三）也可以直接共享 Pinia store，按耦合偏好二选一；
独立部署/声明式应用之间则一律走「后端 REST + 总线推送」。弹窗回传值
（`openDialog` 的 Promise）是联动的第三种形态——适合「让用户在 B 里选一次，A 拿结果」
的交互（内置范本：`ui/fileDialog` 文件选择即此模式）。

### 方式四：网页应用（iframe 集成，收录现成站点）

不需要对方提供任何代码，把现成网页站点直接嵌成桌面应用（统一融合工具栏：标题 +
域名 + 刷新 / 浏览器打开）。两条路径：

**管理员界面添加（通用）**：**系统设置 → 第三方应用 → 添加网页应用**，填名称与
`https://` 地址即可，应用落库为 `config = {"kind":"web","url":…}`（渲染走通用
`WebApp.vue` 容器），图标自动取站点 favicon（失败回落默认图标），再到「应用授权」
给角色开放，`sys:apps-changed` 广播后各端 Dock 即时出现。

**内置收录（编译期）**：`web/src/apps/web/registry.ts` 注册表加一行、图标放
`web/public/icons/` 并在 `desktop/icons.ts` 注册、服务端 `store.go` 的
`builtinApps` 加条目与角色授权（存量库升级自动补种，内置应用配置每次启动以种子
为准刷新）。收录前提见上文「跨域边界」——目标站未设防嵌头才可直嵌；设防站点
走下文应用代理（kind=proxy）。

**跨域边界**：iframe 直嵌要求目标站未设置 `X-Frame-Options` / CSP `frame-ancestors`
防嵌头（设防的公网站点无法客户端绕过，会显示空白；此类站点改用应用代理）。跨域
iframe 由浏览器同源策略天然隔离，双方脚本无法互访，OpenOS 登录态与桌面状态不受
影响；iframe 带 sandbox 且**不含 `allow-top-navigation`**——部分站点
会执行 framebusting（`top.location` 自拔），该限制确保被嵌站点永远无法把整个桌面
导航走。扫码登录类站点天然适配（登录态存目标站自身 Cookie，受浏览器第三方 Cookie
政策约束，Safari 等严格环境下如遇登录异常可用工具栏「浏览器打开」兜底）。需要与
桌面双向通信的站点，后续可经 `postMessage` 桥扩展。

#### 应用代理（kind=proxy，拒绝 iframe 嵌入的站点）

添加网页应用时勾选**「应用代理（服务端反代嵌入）」**，应用落库为
`config = {"kind":"proxy","url":"https://目标/","headers":{"X-Api-Key":"…"}}`：目标站点不再由浏览器直连，而是经
OpenOS 服务端反代加载——剥掉 `X-Frame-Options` / CSP `frame-ancestors` 等防嵌响应头，
内部系统与设置了防嵌的公网站点（如 GitHub）都能在窗体内打开。前端仍是同一个
`WebApp.vue` 容器，仅 iframe 地址换成 `/app/<id>/…`。

**URL 映射（紧凑形态）**：目标地址 `https://h:8443/a/b?q` 映射为
`/app/<id>/https/h:8443/a/b?q`（scheme 段不写 `://`，避免路径中的 `//` 被浏览器与
Go ServeMux 的 cleanPath 折叠成 307 清洗重定向）。三层地址覆盖让页面内所有请求
持续命中代理：

1. **服务端重写**（`server/internal/webproxy/rewrite.go`）：HTML 的
   `href/src/action/srcset/style`、`<style>` 与 CSS 的 `url()` / `@import`、
   `meta refresh` 全部改写为代理路径——含根绝对路径与跨域 CDN 地址（`<base>` 注入
   做不到根绝对路径，这是必须重写的原因）；
2. **注入 hook 脚本**（`hook.js`，紧跟 `<head>` 早于页面脚本执行）：包装
   `fetch` / `XMLHttpRequest` / `WebSocket`（改走 `/app/<id>/ws/…` 由服务端泵转）/
   `EventSource` / `Worker` / `window.open` / `sendBeacon` / `history.pushState` /
   `document.cookie`（改写 `path=/` 防止 cookie 种到整个 OpenOS 源）/ 图片等
   URL 反射属性与 `setAttribute`，JS 动态拼接的地址同样映射回代理路径；并禁用
   目标站在本源注册 Service Worker；
3. **相对地址天然解析**：页面在 `/app/<id>/<目标URL>` 之下，`fetch('api/x')` 这类
   相对拼接无需任何拦截即落在代理路径内。

**会话与跳转**：目标站 `Set-Cookie` 被收归代理路径（剥 `Domain`、`Path` 映射到
`/app/<id>/…`，http 部署剥 `Secure` 并把 `SameSite=None` 降级 `Lax`），登录态在
代理路径下闭环；30x 的 `Location` 同样重写。gzip 响应解压后改写；SSE / 大文件 /
视频流式透传；WebSocket 双向泵转。

**自定义请求头（仅代理模式，`config.headers`）**：管理界面添加/编辑网页应用时，
勾选「应用代理」后可为该应用配置一组固定的请求头，随每个代理请求（含 WebSocket
握手与 favicon 取图）发给目标站——典型场景是内网系统按 `Authorization` /
`X-Api-Key` 等头鉴权。语义与限制：

- 配置头在**剥除 OpenOS 会话之后**应用，优先级最高（可覆盖代理默认的
  `X-Forwarded-*`）；管理员显式写死的目标站凭据不受「剥 Authorization」约束。
- `Cookie` 头为合并语义：配置值在前、保留的目标站会话 Cookie 在后（不顶掉用户
  在目标站的登录态）。
- 逐跳与传输控制类头（`Connection` / `Transfer-Encoding` / `Content-Length` /
  `Host` 等）为黑名单，配置了会被静默丢弃；名称非法或值含 CR/LF 的条目同样丢弃
  （防头部注入，纵深防御）。
- iframe 直嵌模式（kind=web）下浏览器无法附带自定义头，编辑区仅代理模式显示。

**Referer / Sec-Fetch / Content-Type 的转发语义**：代理页面的 `Referer` 会被
还原为**目标站自身的绝对地址**再转发（敏感接口常校验 Referer 白名单；还原值是
目标站自己的地址，不泄露 OpenOS 宿主信息），无法识别的 Referer 不发送；浏览器
的 `Sec-Fetch-*` 系头在反代后源关系语义失真，转发前整组剥除；上游偶发重复下发的
`Content-Type`（实测微信 jslogin 同时给 text/javascript 与 text/html 两个值）按
单值语义只透传首个——浏览器取末值会让 `<script>` 的严格 MIME 检查（配 nosniff）
拒绝执行 JSONP。

**窗口内标签页（仅代理模式）**：页面弹出的新窗口被拦截，在原窗体内开标签页而非
弹出真浏览器窗口——① `window.open(...)` 返回新标签的真实 window 代理（`closed` /
`close()` / `postMessage` / `document` 等照常可用，命名窗口二次打开复用同一标签）；
② `target="_blank"` 链接捕获拦截（⌘/Ctrl 等修饰键点击放行原生新窗口）；③ 页内
`window.close()` 关闭所在标签，关掉最后一个标签即关闭窗体。标签条在多标签时出现，
切换不重载（iframe 保活）。宿主 API 挂在窗体根元素（`data-os-webapp` 的
`__openosPxHost`），多代理窗口互不干扰；iframe 直嵌模式（kind=web）因跨域无法
拦截，`_blank` 仍走浏览器原生行为。


**安全边界（务必阅读）**：

- **仅配置目标**：代理目标只能来自管理员注册应用时的 `config.url`，不接受请求方
  传 URL；配置目标本身允许内网地址（如内网 Grafana，这是管理员的显式选择）。
- **防 SSRF**：目标以外的主机（跨域子资源、跳转、OAuth 流程）在**拨号层**校验，
  回环 / 内网 / 链路本地地址一律拒绝，公网站点与跨域 CDN 正常放行。
- **会话隔离**：转发前剥离 OpenOS 自身会话（`openos_token` 与 `openos_*` Cookie、
  `Authorization` 头），OpenOS 登录态绝不外发；访问权按角色授权矩阵（`role_apps`）
  逐请求校验，与启动台可见性一致；未登录 401、未授权 403。
- **信任模型**：经代理加载的页面与桌面**同源**执行（这与 goder 内嵌同一信任级别），
  目标站 JS 理论上可访问本源存储与以当前用户身份调用 OpenOS 接口——**仅配置可信
  应用**（内部系统、自有站点）；不信任的公网站点请用默认 iframe 直嵌（跨域隔离）。
  同源存储按源共享，两个代理应用若使用相同 localStorage 键可能互相覆盖。
- **能力限制与逃逸回捕**：`location.href = '/x'` 这类对 location 的直接赋值无法被
  hook 包装，跳转会逃出代理前缀落到 OpenOS 自身路径——目标站用 `/api` 等本源常见
  前缀极普遍（典型：SSO 登录 `location.href='/api/auth/sso/login'` 撞上 OpenOS
  自身 auth 路由 404，路径重合时甚至会误调系统接口）。对此有三层防护：① 服务端
  **逃逸回捕**（`CatchEscape`，挂在**整棵路由树外层**，覆盖 `/api/*` 等真实路由）——
  请求携带代理页面的 Referer 时（代理响应统一下发 `Referrer-Policy: same-origin`，
  同源全量、跨源不发、转发上游前本就剥 Referer），把请求 307 拼回该应用的代理路径
  （保留方法与请求体，表单 POST 逃逸同样生效；不校验 Referer 与请求 Host 一致，
  开发模式 vite 代理与宿主网关都会改写 Host）；② 前端守卫（`framework.ts`）——
  桌面壳检测到自身在 iframe 内启动时拒绝渲染，杜绝「代理 iframe 里套出整个桌面」
  的套娃形态；③ **SPA 路由还原**——react-router 等按 `location.pathname` 做客户端
  路由的 SPA 在代理前缀下路由表会全部失配（渲染 404 组件），hook 在页面加载最早期
  用 `history.replaceState` 把文档地址还原为目标站形态，网络请求仍由 toProxy 以
  还原前算好的闭包变量映射回代理路径；还原地址持续携带 `__os_px=<id>~<scheme>~
  <host>` 查询标记（pushState 会覆盖 query，hook 负责维持），`CatchEscape` 兼认该
  标记做回捕，转发上游与回捕目标都会剥掉它；④ **OAuth 回跳参数双向改写**——纯
  前端授权的 IdP（Casdoor 等）会从文档 URL 解析 `redirect_uri` 绝对地址、拼上
  code 后用 location 赋值跳转，跨源绝对地址导航会把 iframe 带到真实域名（登录态
  建立在代理之外、还会撞上浏览器第三方 cookie 限制），hook 在维护文档地址时把
  query 中的 `redirect_uri`/`redirectUri` 改写为代理路径（跳转落回本源、回调与
  会话都经代理完成），服务端转发上游时再把该参数还原为注册的原始绝对地址（IdP
  白名单校验不受影响）。极少数校验自身域名的站点（OAuth 严格回跳、银行类）仍
  可能不可用，用工具栏「浏览器打开」兜底。

**开关**：`OPENOS_PROXY=off` 下架 `/app/*` 路由（应用配置保留，iframe 将得到 404）。


### goder 界面操控指令

任何来源（AI 回复、终端、事件总线）发送 `sys:cmd` 即可操控桌面：

| 指令 | 效果 |
|---|---|
| `{ "cmd": "open", "arg": "calculator" }` | 打开应用 |
| `{ "cmd": "close", "arg": "docs" }` | 关闭应用全部窗口 |
| `{ "cmd": "file", "arg": "/home/alice/文档/a.pdf" }` | 用合适应用打开文件 |
| `{ "cmd": "wallpaper", "arg": "3" }` | 切换壁纸（1-6） |
| `{ "cmd": "dark", "arg": "on" }` | 深色模式开关 |

## 桌面客户端（openos-desktop）

反代（应用代理）之外的另一条网页应用接入路线：**本机 Chrome 以 app 模式直开 OpenOS，
iframe 直连真实站点**。适合反代难平的场景（目标站 SSO/风控/Cookie 轮换复杂，如美团
ebooking），代价是依赖客户端环境（需本机 Chrome）。

```bash
make desktop                     # 构建 → ./openos-desktop
./openos-desktop                 # 打开 http://127.0.0.1:8081
./openos-desktop -url http://localhost:5173   # 开发模式（vite 只监听 IPv6 localhost）
```

**原理**：CDP 驱动 Chrome 以 `--app=<url>` + 专用 profile（`~/.openos-desktop`，登录态
持久化）启动，`--disable-web-security` + 关闭站点隔离——跨源 iframe 不再被同源策略 /
X-Frame-Options / CSP frame-ancestors 拦截，网页应用（kind=proxy）经注入标记
`window.__OPENOS_DESKTOP__` 检测后直连 `config.url` 真实地址而非 `/app/<id>/` 反代。

**SameSite 三层桥**（跨站登录的命门，Chrome 154 实测）：跨源 iframe 属第三方上下文，
目标站未标注 SameSite 的会话 Cookie 按 Lax 语义被整体拒收（Set-Cookie 与
document.cookie 双落空，`--disable-features` 关不掉）：

1. **注入 shim**——`document.cookie` 写入自动补 `SameSite=None`（https 帧补 Secure）；
   主帧走 `addScriptToEvaluateOnNewDocument`，跨源子帧由 `Runtime.contextCreated`
   即时补丁 + 周期 walker（contentWindow.eval 跨 realm）兜底；
2. **响应桥**——被拒的 Set-Cookie（`responseReceivedExtraInfo.blockedCookies`）经
   `Network.setCookie` 直写 Cookie jar；
3. **jar 归一化**——周期把非 None Cookie（本地域除外）改写 `SameSite=None; Secure`，
   修复「顶层第一方登录后、iframe 第三方请求不带 Cookie」的发送侧。

前端桌面模式另有**「登录」按钮**（网页应用工具栏）：`window.open` 在顶层第一方窗口完成
登录/SSO（原生语义零损耗），检测到离开登录页后自动关窗并刷新页内标签。

**安全边界**：`--disable-web-security` 意味着窗口内跨源脚本可互访，专用 profile 仅用于
打开 OpenOS；CDP 端口只绑 127.0.0.1；窗口内等于本机真实浏览器会话，勿指向不可信的
OpenOS 实例。无站点隔离下桌面页与嵌入应用共享渲染进程，重应用可能拖慢整页。

## 项目结构

```
openos/
├── server/                  # Go 后端
│   ├── main.go              # 中间件链、路由、优雅停机
│   ├── internal/
│   │   ├── store/           # SQLite 迁移/种子、审计、AI 配置、统计
│   │   ├── auth/            # JWT+热吊销、防爆破、密码策略（含旧 hash 升级）
│   │   ├── bus/             # WebSocket 事件总线 Hub
│   │   ├── vfs/             # 虚拟 FS（文本+二进制+配额）+ 虚拟终端
│   │   ├── ai/              # 在线配置热更、SSE 流式、历史、连通测试
│   │   ├── goder/            # goder-serve 子进程托管 + /goder-api 身份反代
│   │   ├── osmcp/            # goder 桌面控制 MCP 插件（openos，20 个 os_* 工具）
│   │   └── webproxy/         # 应用代理（网页应用 kind=proxy 的服务端反代 + 地址重写）
│   └── *_test.go            # 单元测试（auth/bus/vfs/osmcp/webproxy）
├── web/                     # Vite 前端
│   └── src/
│       ├── desktop/         # Desktop / MenuBar / Dock / Launchpad / WindowFrame / LoginScreen
│       ├── ui/              # 统一基础 UI 组件库（开放能力门面）
│       ├── sdk/             # OpenOS 全局 SDK + SchemaApp 声明式渲染器
│       ├── bus/  api/       # 事件总线客户端 / REST 客户端（401 全局处理）
│       ├── stores/          # Pinia：session / desktop / apps / windows
│       └── apps/            # terminal files calculator notes settings docs sheets slides pdf goder welcome
├── desktop/                 # openos-desktop 桌面客户端（独立 Go module：CDP 驱动 Chrome app 模式 + SameSite Cookie 桥）
├── Dockerfile               # 多阶段构建（node→go→alpine），非 root 运行
├── docker-compose.yml       # 数据卷持久化
└── Makefile                 # dev/build/release/docker/test
```

## CI/CD 发布（GitLab CI）

与 grf-gateway 同套设施（`docker` Runner + 私有仓库 + 发布机），见 `.gitlab-ci.yml`。

```bash
git tag v0.0.1 && git push origin v0.0.1   # 推送 tag 即触发发布
```

版本号规则：从 `v0.0.1` 起，每次发布仅递增**末位**（v0.0.1 → v0.0.2 → …），不修改主/次版本号。

流程（仅 tag 触发，三个 stage）：

1. **build**：多阶段 Dockerfile 构建（Vite 前端 → Go 静态编译 → alpine 运行镜像），打 tag 为 `10.8.0.6:5000/openos:<tag>`
2. **push**：推送镜像到内网 Registry（无认证）
3. **deploy**：SSH 到 `DEPLOY_HOSTS`（10.8.0.5），拷贝 docker-compose.yml 后 `pull` + `up -d --force-recreate`，并清理本应用旧版本镜像

部署机上的 compose 通过 `OPENOS_REGISTRY`/`OPENOS_TAG` 变量从仓库拉取对应版本；运行数据持久化在宿主机 `/app/docker/openos`（bind mount → 容器 `/data`），跨升级保留。发布机需要一次性配置 CI/CD 变量：`DEPLOY_SSH_PRIVATE_KEY`（文件类型，SSH 私钥）；`DEPLOY_USER` 默认 root。

## 生产部署清单

- [ ] **修改默认密码**：admin/user/guest 三个默认账号，或删除不需要的账号
- [ ] **配置 AI 服务**：系统设置 → AI 服务（或环境变量回填）
- [ ] **HTTPS**：置于 Nginx/Caddy 反向代理之后（WebSocket 需配置 Upgrade 头）
- [ ] **备份**：定期备份宿主机 `/app/docker/openos`（即 `OPENOS_DATA`，SQLite WAL，停机拷贝或 `sqlite3 .backup`）
- [ ] **可选**：设置 `OPENOS_JWT_SECRET` 固定密钥（多实例共享时必需）
- 数据保留在单文件 SQLite（WAL），单机场景即开即用；事件总线与终端均为虚拟实现，不暴露宿主 shell

## 安全说明

- 密码 pbkdf2-SHA256（20000 轮 + 随机盐；升级前的存量 hash 自动透明升级）
- JWT HS256，24 小时有效，携带 token_version：**改角色/改密码/删用户/禁用后旧令牌立即失效**
  （REST 每请求实时核对；WebSocket 升级时同套校验，已建立的总线连接经 30s 周期复核踢下线，
  客户端以续期后的令牌自动重连，吊销令牌重连被拒；新建用户 token_version 以当前时间初始化，
  杜绝「删号重建后旧令牌复活」）
- 前端令牌滑动续期：每 12 小时及页面回到前台时静默调 `/api/auth/refresh` 换新令牌
- 用户禁用（`disabled`）：登录 403、在线会话踢下线；最后一名启用中的管理员不可禁用/降级/删除
- exchange 令牌交换：密钥恒定时间比较；可选角色白名单（`OPENOS_EXCHANGE_ROLES`）；
  已禁用用户拒绝交换；显示名变更留审计
- 审计日志 / AI 对话历史按保留期自动清理（默认 90 天，可配置），防止无限膨胀
- 应用代理（`webproxy`）：SSRF 防线在拨号层（配置目标主机放行、其余主机拒绝内网地址）、
  转发前剥 OpenOS 会话 Cookie/Authorization、目标站 Cookie 收归 `/app/<id>/` 代理路径、
  访问权逐请求按角色授权矩阵校验——详见「第三方接入指南 · 应用代理」的安全边界一节
- jwt_secret 等系统级键存于独立的 `system_kv` 表，与用户界面设置（user_settings）分离
- 登录防爆破：每 用户名+IP 10 分钟 8 次失败锁定 15 分钟；全局限流 600 次/分钟/IP
- 虚拟文件系统严格限制在 `/home/<用户>` 内，单文件 ≤8MB，单用户配额 100MB，用户间完全隔离
- 终端默认**虚拟 shell**；真实模式为受控执行（沙箱目录 + 超时 + 截断），可用 `OPENOS_REAL_SHELL=off` 整体关闭
- 审计日志记录登录/用户/授权/AI 配置等敏感操作（系统设置 → 系统信息可查）
- AI 密钥仅存服务端，接口返回始终脱敏

## 开发提示

- 在终端里试试：`ls` `echo 你好 > 便签.txt` `open sheets` `goder 打开计算器`（Tab 键补全命令）
- 文件应用支持拖拽上传、双击图片预览、双击 `.pdf` 直接在阅读器打开云盘文件
- 表格：Shift+方向键选区（状态栏实时求和），Ctrl+C/V 与 Excel 互通，Ctrl+Z 撤销
- 桌面快捷键：Alt+W 关闭 · Alt+M 最小化 · Alt+↩ 缩放 · Alt+\` 循环切换 · F4/Alt+Space 启动台 · 拖窗到屏幕边缘吸附
