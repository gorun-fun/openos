# 配置参考

**简体中文** | [English](configuration.en.md)

OpenOS 全部配置经环境变量注入，均有合理默认，最小部署零配置即可启动。管理员可在线调整的部分（AI 服务、应用、角色权限等）持久化在 SQLite，优先级高于环境变量回填。

## 基础

| 变量 | 默认 | 说明 |
|---|---|---|
| `OPENOS_ADDR` | `127.0.0.1:8081` | HTTP 监听地址（容器内 `0.0.0.0:8081`；对外部署配合反代） |
| `OPENOS_DATA` | `data`（相对工作目录） | 运行时数据目录：SQLite、`homes/<用户>`、附件、goder 会话等 |
| `OPENOS_DIST` | `web/dist` | 前端静态目录；run.sh / 容器以绝对路径注入 |
| `OPENOS_SEED_DEMO` | 开 | `0` = 不种默认演示账号（admin/user/guest）与示例文件 |
| `OPENOS_JWT_SECRET` | 自动生成并持久化 | JWT 签名密钥（存 `system_kv` 表）；**多实例共享会话时必须显式固定** |
| `OPENOS_TOKEN_TTL` | `24h` | 令牌有效期（Go duration，如 `168h`）；前端按响应 `tokenTtl` 自适应续期 |
| `OPENOS_TRUST_PROXY` | 关 | `1` = 防爆破计数按 `X-Forwarded-For` 末位分桶（单层反代语义；反代后建议开启） |

## AI 服务

| 变量 | 说明 |
|---|---|
| `OPENOS_AI_BASE_URL` | OpenAI 兼容接口地址（如 `https://open.bigmodel.cn/api/paas/v4`） |
| `OPENOS_AI_API_KEY` | API Key（仅存服务端，接口返回脱敏） |
| `OPENOS_AI_MODEL` | 默认模型名 |

以上仅作**首次启动回填**；之后以「系统设置 → AI 服务」的在线配置为准（保存即时生效、无需重启）。

## goder（AI 编程 IDE）

| 变量 | 默认 | 说明 |
|---|---|---|
| `OPENOS_GODER` | 开 | `off` = 整体停用（不启动子进程、下架应用） |
| `OPENOS_GODER_BIN` | `<data>/bin/goder-serve` | goder-serve 网关二进制（`make goder` 产物；不存在则功能自动关闭） |
| `OPENOS_GODER_JS` | `<data>/goder/goder.js` | goder 嵌入库路径 |
| `OPENOS_GODER_ADDR` | `127.0.0.1:7711` | goder-serve 监听地址（**强制回环**，非回环拒绝启动） |
| `OPENOS_GH_BIN` | PATH 上的 `gh` | gh 可执行文件路径（goder 运行期依赖） |
| `GODER_REPO` / `GODER_SRC` | `git.gorun.fun/share/goder.git` / `.goder-src` | 仅 `make goder` 用：源码仓库 / 本地克隆目录 |
| `GODER_SETTINGS_SEED` | — | goder 模型配置种子开关（嵌入约定透传） |

**goder 子进程环境透传**（存在即透传，不继承 `OPENOS_*` 秘密）：

- `GODER_GH_ENV_<KEY>=<value>`：剥前缀后作为 `KEY=<value>` 注入每个 gh 子进程。典型：`GODER_GH_ENV_GRF_TURN_WEBHOOK_URL` / `_SECRET`（回合终态事件 `turn.settled` 外发，HMAC 签名头 `X-GRF-Signature`）
- `GODER_IDLE_TTL`：gh 进程空闲回收
- `GODER_WEBHOOK_URL` / `GODER_WEBHOOK_SECRET`：用户进程生命周期事件回调
- `GODER_ADMIN_TOKEN`：goder-serve 运维面令牌（openos 未配置时每次进程启动自持随机值注入）

## 桌面控制 MCP 插件（osmcp）

| 变量 | 默认 | 说明 |
|---|---|---|
| `OPENOS_OSMCP` | 开 | `off` = 停用 goder 桌面控制插件 |
| `OPENOS_OSMCP_ADDR` | `127.0.0.1:7712` | MCP 服务监听地址（强制回环；被占自动回退随机端口） |
| `OPENOS_OSMCP_ALLOW_REMOTE` | 关 | `1` = 允许非回环监听（分容器部署用，自行保证网络边界） |

## 功能裁剪（数据边界）

| 变量 | 默认 | 说明 |
|---|---|---|
| `OPENOS_VFS` | 开 | `off` = 下架文件/终端类内置应用，`/api/fs/*` 一律 503（宿主形态：业务文件交由集成方） |
| `OPENOS_GODER` | 开 | `off` = 停用 goder（见上） |
| `OPENOS_MARKET` | 开 | `off` = 下架应用商店与 `/api/market/*` |
| `OPENOS_MARKET_URL` | 官方目录地址 | 商店官方默认目录；`off` 只关官方源（自定义源仍可用） |
| `OPENOS_PROXY` | 开 | `off` = 下架统一代理 `/app/*` 与 `/px/*` 路由 |
| `OPENOS_STEWARD` | 开 | `off` = 下架代码管家（引擎与 `/api/steward/*`） |

## 真实终端

| 变量 | 默认 | 说明 |
|---|---|---|
| `OPENOS_REAL_SHELL` | `off` | `on` = 受限执行（cd 跟踪）；`full` = 解除目录限制。**`on` 的「受限」不是文件系统沙箱**——命令以服务进程权限执行，公网部署保持 `off`，详见[安全说明](security.md) |

## 宿主集成（机对机通道）

| 变量 | 默认 | 说明 |
|---|---|---|
| `OPENOS_EXCHANGE_SECRET` | 空 = 禁用 | 宿主间全信任密钥，守护：令牌交换 `/api/auth/exchange`、JWT 内省 `/api/auth/introspect`、角色声明 `/api/host/declare`、机对机组 `/api/host/*`。**等同管理员凭证，仅限可信网络** |
| `OPENOS_EXCHANGE_ROLES` | 空 | exchange 可铸造的角色白名单（逗号分隔）——密钥泄露也无法铸造 admin；**强烈建议配置** |

## 数据保留期

| 变量 | 默认 | 说明 |
|---|---|---|
| `OPENOS_AUDIT_RETENTION_DAYS` | `90` | 审计日志保留天数（0 = 永久），启动与每日自动清理 |
| `OPENOS_CHAT_RETENTION_DAYS` | `90` | 聊天记录保留天数（0 = 永久） |
| `OPENOS_ATTACH_RETENTION_DAYS` | `30` | 系统附件保留天数（0 = 永久） |

## 其他

| 变量 | 默认 | 说明 |
|---|---|---|
| `OPENOS_FS_WATCH` | 开（3s 一轮） | `off` = 关闭统一 home 磁盘变更轮询监控（goder/终端绕过 REST 写盘的 `fs:changed` 兜底来源） |
| `OPENOS_HOLIDAY_SYNC` | 开 | `off` = 日历只用内置节假日数据（纯内网部署） |
| `OPENOS_ROOM_URL` | 空 | 游戏房间服务地址（商店游戏应用经 `/api/rooms/config` 发现后持令牌直连） |

## 桌面客户端（openos-desktop）

| 变量 | 默认 | 说明 |
|---|---|---|
| `OPENOS_DESKTOP_CONF_PORT` | `17700` | 客户端「选择服务器」回环设置页端口（被占自动递增） |
| `OPENOS_DESKTOP_UPDATE_URL` | `https://download.gorun.fun/openos/cli` | 自动更新通道地址 |
| `OPENOS_DESKTOP_URL` | — | run.sh 透传给客户端 `-url` 直连指定服务器（跳过选择页，仅本次生效） |
| `OPENOS_DESKTOP_DOC_REFERRER` | 站点内置值 | 桌面模式 document.referrer 伪造的豁免值（站点容器适配） |
