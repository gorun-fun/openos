# OpenOS

前后端分离的仿 macOS 网页桌面操作系统。**Go + Vite (Vue 3)**，以**事件总线**为核心通信方式，提供统一基础 UI 与强大的第三方开放能力，内置 **goder** AI 助手可操控所有界面。面向产品级使用：完整的安全加固、审计、配额、优雅停机与容器化部署。

![tech](https://img.shields.io/badge/Go-1.26-00ADD8) ![tech](https://img.shields.io/badge/Vue-3.5-42b883) ![tech](https://img.shields.io/badge/SQLite-WAL-003B57)

## 功能总览

| 模块 | 说明 |
|---|---|
| 🪟 窗体管理 | 拖拽、八向缩放、红绿灯、**边缘吸附（上=最大化/左右=半屏，带预览）**、最小化飞入 Dock 动画、焦点层级、窗口状态跨会话恢复 |
| 🚀 桌面体验 | 启动台（F4 / Alt+Space，支持搜索）、Dock 邻居放大、完整菜单栏（系统/窗口/帮助/关于）、快捷键（Alt+W/M/↩/\`）、窗口级错误边界、401 全局登出 |
| 🚌 事件总线 | WebSocket 主题订阅/发布，`*` 通配；本地即时分发 + 跨端广播；断线自动重连并重放订阅；服务端处理器同总线挂载 |
| 🧩 开放能力 | ① 组件型：`OpenOS.registerApp()`（支持 template 字符串）② 声明式：JSON Schema 注册即得新应用（第三方只管数据） |
| ✦ Goder - AI助手 | **完整 AI 编程 IDE 内置**（goder 嵌入式集成：项目/会话/聊天/审查/终端/事件台，每用户独立 gh server，与 grf-play 同款集成方式） |
| 🗒 便签 | 多张彩色便签随手记（自动保存、换色、删除），一键**钉到桌面**成为小组件；桌面便签可拖动、右下角缩放（**尺寸为网格整数倍，精确对齐**） |
| 📦 内置应用 | **双模式终端（虚拟 ⇄ 真实 shell，ANSI 彩色，Tab 补全）**、文件（上传/下载/搜索/排序/双视图/图片预览/配额）、计算器、系统设置 |
| 🖼️ 桌面 | **小组件（时钟/便签/系统监控，可拖动持久化，便签可缩放并对齐网格）**、**桌面快捷方式（启动台拖出、系统文件拖入自动上传、双击打开、右键移除）**、启动台、Dock 邻居放大与悬停名称提示 |
| 📑 办公应用 | 文档（富文本 + **自动保存** + 字数）、表格（**区域选择/TSV 与 Excel 互通/撤销重做/行列操作** + 公式引擎）、幻灯片（演示模式）、PDF（本地/云盘文件） |
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

其他环境变量：`OPENOS_ADDR`（默认 127.0.0.1:8081，容器内为 0.0.0.0:8081）、`OPENOS_DATA`（数据目录）、`OPENOS_DIST`（前端目录）、`OPENOS_JWT_SECRET`（默认自动生成并持久化）。

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

**内置主题**：`sys:open` · `sys:cmd`（goder 指令）· `sys:apps-changed` · `sys:presence` · `fs:changed` · `term:<user>` · `chat:goder`

### 前端总线 API

```ts
import { bus } from '@/bus/bus'
bus.on('app:calc', (payload, from, topic) => {})   // 订阅（支持通配）
bus.emit('app:calc', { a: 1 })                     // 本地 + 远程发布
bus.emit('app:calc', { a: 1 }, { remote: false })  // 仅本地
bus.remote('term:admin', 'ls')                     // 仅发服务端处理器
```

## goder 工作台（内置 AI 编程 IDE）

OpenOS 按官方嵌入约定（`goder/docs/embedding.md`，与 grf-play 同款方式）把 [goder](https://git.gorun.fun/share/goder.git) 作为内置应用：

- **后端**托管 `goder-serve` 子进程（header 身份模式）：`/goder-api/*` 反代并注入 `X-GRF-UID/X-GRF-Name`（先剥离外部伪造头），SSE 透传；`/vendor/goder.js` 提供自包含嵌入库
- **每个 OpenOS 用户对应一个独立 gh server 进程**（独立端口/数据目录/令牌，令牌不下发浏览器）；新用户自动种子宿主 `~/.grf` 模型配置（`GODER_SETTINGS_SEED=1`）
- **鉴权**：OpenOS 登录后签发 `openos_token` Cookie（httpOnly），goder 嵌入组件的同源请求自动携带，经反代完成身份注入
- **前端**：goder 应用窗口动态 `import('/vendor/goder.js')` → `configure('openos', { baseUrl: '/goder-api', user, sideTop })` → 渲染 `<goder-app-shell panels="sessions,chat,sidebar,console">`（左会话/中聊天/右栏 tab/底事件台）

部署（`make goder` 自动完成，源码拉取自 `https://git.gorun.fun/share/goder.git`，克隆到仓库内 `.goder-src/`，可用 `GODER_REPO`/`GODER_SRC` 覆盖）：

```bash
# make goder 等价于：
cd <goder 源码> && ./build.sh embed        # 构建嵌入库 frontend/dist-embed/goder.js（需 npm）
cd <goder 源码> && go build -o <openos>/server/data/bin/goder-serve ./cmd/goder-serve
cp <goder 源码>/frontend/dist-embed/goder.js <openos>/server/data/goder/
# 前置：gh 可执行文件在 PATH（GODER_BIN 可覆盖）；组件缺失时 goder 应用内显示部署引导
```

相关环境变量：`OPENOS_GODER_BIN` / `OPENOS_GODER_JS` / `OPENOS_GODER_ADDR`（默认 127.0.0.1:7711）/ `OPENOS_GH_BIN`。

### 真实终端

终端应用支持双模式切换（右上角）：

- **虚拟**：OpenOS 虚拟文件系统（与文件应用同源，`open <app>` 可操控桌面）
- **真实**：服务端 `sh -c` 受控执行——个人沙箱目录（`<data>/homes/<user>`）、10s 超时、64KB 输出截断、`cd` 服务端跟踪（受限沙箱内）、ANSI 彩色渲染

`OPENOS_REAL_SHELL=off` 关闭；`=full` 解除沙箱限制（自担风险）。

## 第三方接入指南

### 方式一：声明式应用（零前端代码，推荐）

管理员在 **系统设置 → 第三方应用** 粘贴 JSON Schema 注册（示例模板一键填入）：

```jsonc
{
  "title": "团队公告板",
  "dataSource": {
    "kind": "static",                    // 或 "rest" + url/path/interval 秒
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

注册后立即可在 Dock 使用，并可授权给任意角色（授权变更实时广播到所有在线端）。

### 方式二：组件型应用（完整前端能力）

第三方脚本（控制台或注入）调用全局 SDK：

```js
// OpenOS.registerApp(id, { name, width, height, component })
// 可用：OpenOS.bus / OpenOS.ui（基础组件库）/ OpenOS.api（带登录态）/ OpenOS.open/close
OpenOS.registerApp('todo', {
  name: '待办', width: 420, height: 400,
  component: {
    data: () => ({ items: ['体验 OpenOS'], text: '' }),
    template: '<div style="padding:16px">…</div>',
  },
})
OpenOS.open('todo')
```

### goder 界面操控指令

任何来源（AI 回复、终端、事件总线）发送 `sys:cmd` 即可操控桌面：

| 指令 | 效果 |
|---|---|
| `{ "cmd": "open", "arg": "calculator" }` | 打开应用 |
| `{ "cmd": "close", "arg": "docs" }` | 关闭应用全部窗口 |
| `{ "cmd": "file", "arg": "/home/alice/文档/a.pdf" }` | 用合适应用打开文件 |
| `{ "cmd": "wallpaper", "arg": "3" }` | 切换壁纸（1-6） |
| `{ "cmd": "dark", "arg": "on" }` | 深色模式开关 |

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
│   │   └── goder/            # goder-serve 子进程托管 + /goder-api 身份反代
│   └── *_test.go            # 单元测试（auth/bus/vfs）
├── web/                     # Vite 前端
│   └── src/
│       ├── desktop/         # Desktop / MenuBar / Dock / Launchpad / WindowFrame / LoginScreen
│       ├── ui/              # 统一基础 UI 组件库（开放能力门面）
│       ├── sdk/             # OpenOS 全局 SDK + SchemaApp 声明式渲染器
│       ├── bus/  api/       # 事件总线客户端 / REST 客户端（401 全局处理）
│       ├── stores/          # Pinia：session / desktop / apps / windows
│       └── apps/            # terminal files calculator notes settings docs sheets slides pdf goder welcome
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
- JWT HS256，24 小时有效，携带 token_version：**改角色/改密码/删用户后旧令牌立即失效**
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
