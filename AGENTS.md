# AGENTS.md — OpenOS 工作区说明

OpenOS：前后端分离的仿 macOS 网页桌面操作系统。Go 后端（单二进制 + SQLite）+ Vite/Vue 3 前端，以 WebSocket 事件总线为核心通信，内置 goder AI 编程 IDE。所有代码注释与文档使用中文，新增注释保持中文。

## 常用命令

```bash
make dev          # 开发：后端 8081 + 前端 5173 热更新（同时启动）
make server       # 仅后端（等价 cd server && go run .）
make web          # 仅前端
make test         # Go 单元测试（auth/bus/vfs 各有 *_test.go）
make build        # 生产构建：npm run build + go build -o openos-server
make release      # 多平台发布包（darwin/linux × amd64/arm64，CGO_ENABLED=0）
make goder        # 构建 goder 组件（源码自动克隆自 git.gorun.fun/share/goder.git，需 go/npm/git；运行期另需 PATH 上有 gh）
docker compose up -d --build   # 容器部署 → http://localhost:8081

cd web && npm run typecheck    # vue-tsc --noEmit（仓库无 ESLint/Prettier，类型检查是唯一的静态校验）
```

## 目录结构

- `server/main.go` — 唯一入口：中间件链、全部路由注册（Go 1.26 `net/http` 方法路由）、优雅停机。新接口直接在这里挂 `mux.HandleFunc`。
- `server/internal/` — `store`（SQLite 迁移/种子/审计/AI 配置）、`auth`（JWT + token_version 热吊销 + 防爆破）、`bus`（WebSocket Hub）、`vfs`（虚拟 FS + 虚拟/真实终端 + 配额）、`ai`（在线配置热更 + SSE 流式）、`goder`（goder-serve 子进程托管 + `/goder-api` 身份反代）。
- `web/src/` — `desktop/`（Desktop/MenuBar/Dock/Launchpad/WindowFrame/LoginScreen 外壳）、`ui/`（Os* 前缀基础组件库，开放能力门面）、`sdk/`（OpenOS 全局 SDK + SchemaApp 声明式渲染器）、`bus/`+`api/`（总线与 REST 客户端，401 全局登出）、`stores/`（Pinia）、`apps/`（每个内置应用一个目录）。
- `server/data/` — **运行时状态（已 gitignore，勿提交）**：openos.db、homes/<user> 沙箱、bin/goder-serve、goder/goder.js。删除会导致数据丢失。

## 架构要点与边界

- **通信两条通道**：REST `/api/*` + WebSocket `/ws` 事件总线。总线协议：`{action:sub,topics:[...]}` / `{action:pub,topic,payload}`，topic 以 `*` 结尾为前缀通配；发布不回声发布者。内置主题：`sys:*`、`fs:changed`、`term:<user>`、`chat:goder` 等。
- **前端总线 API**：`import { bus } from '@/bus/bus'`；`bus.emit` 默认本地+远程，`{remote:false}` 仅本地，`bus.remote` 仅发服务端处理器。
- **前端使用完整版 Vue**（vite alias 指向 `vue/dist/vue.esm-bundler.js`），因为第三方 `OpenOS.registerApp` 要传 template 字符串；`goder-*` 标签在 vite.config.ts 中注册为 customElement。改 vite 配置时别破坏这两点。
- **Vite 代理**：`/api`、`/goder-api`、`/vendor`、`/ws` → `http://127.0.0.1:8081`。
- **SQLite 用 modernc 纯 Go 驱动**（WAL 模式），因此 CGO_ENABLED=0 可交叉编译——不要换成 mattn/go-sqlite3。
- **goder 集成**遵循官方嵌入约定（`goder/docs/embedding.md`）：每用户独立 gh server 子进程，`/goder-api/*` 反代时先剥离外部伪造的 `X-GRF-*` 头再注入身份；二进制缺失时自动降级（应用内显示部署引导），不要让缺失变成启动错误。
- **宿主应用框架化**（方式三集成，见 README「宿主应用集成」）：`web/src/framework.ts` 的 `createOpenOS(opts)` 是宿主组壳入口（注入业务 App/图标/品牌/SSO 登录/菜单栏扩展），`web/src/main.ts` 是独立部署薄壳；壳配置单例在 `web/src/shell.ts`，改动 LoginScreen/MenuBar/App 的登录与品牌逻辑时读它。令牌交换端点 `POST /api/auth/exchange` 由 `OPENOS_EXCHANGE_SECRET` 守护（空=禁用），自动开通的用户密码为随机值、只能经宿主登录；角色放开为安全字符集的自定义角色（admin 语义仅 `RequireAdmin` 认定），授权矩阵接口动态返回全部已出现角色。
- **应用列表 JSON 恒为数组**：`ListApps`/`ListAppsForRole` 空结果返回 `[]` 而非 `null`（前端按数组消费）。`apps.hidden` 标记的应用可编程打开但不出现在启动台/Dock（apps store 的 `visible` getter）。
- **安全不变量**：VFS 严格限制在 `/home/<用户>`（单文件 ≤8MB、配额 100MB）；JWT 带 token_version，改角色/密码/删用户必须令旧令牌失效；真实终端默认沙箱（10s 超时、64KB 截断）；AI 密钥仅存服务端、接口返回脱敏。改动这些区域前先读 README 的「安全说明」。

## 已知事项

- 默认账号：admin/admin123、user/user123、guest/guest123（auth 包 `SeedDefaults` 种子）。
- 关键环境变量：`OPENOS_ADDR`（默认 127.0.0.1:8081）、`OPENOS_DATA`、`OPENOS_DIST`、`OPENOS_JWT_SECRET`、`OPENOS_EXCHANGE_SECRET`（宿主令牌交换密钥，空=禁用）、`OPENOS_AI_BASE_URL/API_KEY/MODEL`、`OPENOS_REAL_SHELL`（off/full）、`OPENOS_GODER_*`。
- WebSocket 鉴权走查询参数 `?token=<jwt>`（浏览器 WS 无法带 header）。
- `make goder` 从 `https://git.gorun.fun/share/goder.git` 克隆源码到仓库内 `.goder-src/`（已 gitignore；`GODER_REPO`/`GODER_SRC` 可覆盖，`GODER_SRC` 指向已有克隆即免拉取），需 go/npm/git；gh 是运行期依赖（PATH 上）。
- **Docker 镜像自带 goder 全套组件**：Dockerfile 内克隆 goder 仓库编译 goder-serve/goder.js，并从 `download.gorun.fun/grf-harness/{平台}/{最新版}/gh` 下载 gh，经 `OPENOS_GODER_BIN/JS` + `OPENOS_GH_BIN` 指到 `/app/goder/`——容器部署无需跑 `make goder`。
- 权威文档是 `README.md`（含完整架构图、总线协议、goder 部署步骤、第三方接入指南）；修改敏感区域（auth/bus/vfs/goder）前先读对应章节。
- **版本发布规则**：从 `v0.0.1` 起，每次发布**只递增版本号最后一位**（v0.0.1 → v0.0.2 → v0.0.3 …），不得修改主/次版本号。发布方式 = 提交后打 tag 并推送（触发 GitLab CI 自动构建镜像并部署）。
