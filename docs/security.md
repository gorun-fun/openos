# 安全说明

**简体中文** | [English](security.en.md)

OpenOS 面向公网可部署的产品级标准（2026-10 完成两轮安全审计并全面收紧）。本文是安全模型的完整速览；涉及区域的代码约定另见 [AGENTS.md](../AGENTS.md)。

## 认证与会话

- **密码**：pbkdf2-SHA256，600,000 轮 + 随机盐；历史 20,000/4,096 轮 hash 在用户下次登录时自动透明升级。密码强度策略：≥8 位且含字母数字。
- **JWT**：HS256，默认 24 小时（`OPENOS_TOKEN_TTL`），携带 token_version；密钥自动生成持久化于独立 `system_kv` 表（与用户设置数据分离）。
- **身份与授权分离**：token_version 吊销**仅发生在密码变更/重置**；角色、权限、显示名、禁用、删号变化走**实时判定**——授权变化即刻生效但**不掉线**（REST 每请求核对；已建立的总线连接 30s 周期复核踢下线）。新建用户 token_version 以当前时间初始化，杜绝「删号重建后旧令牌复活」。
- **无法携带 header 的场景用一次性短票**：`POST /api/auth/ws-ticket` 签发 30 秒用后即焚短票，`/ws`、附件下载、商店组件取件等以 `?ticket=` 替代长期 JWT 入 URL（`?token=` 兼容期内保留）。
- **Cookie**：会话统一 HttpOnly + SameSite=Strict；HTTPS 直连或反代声明 https 时自动加 Secure。
- **前端令牌滑动续期**：每 12 小时及页面回到前台时静默调 `/api/auth/refresh` 换新令牌；401 全局登出（REST 与总线共用同一提示通道）。

## 登录防爆破与限流

- 每「用户名+IP」10 分钟内 8 次失败锁定 15 分钟；全局 600 次/分钟/IP（覆盖 `/api`、`/ws`、`/goder-api`、`/app/*`）。
- 用户不存在路径跑等成本哑哈希，抹平用户名枚举时延差。
- 反代/NAT 部署（compose 端口映射等）下全部外部用户共享同一来源 IP，可能被定向锁号——`OPENOS_TRUST_PROXY=1` 按 `X-Forwarded-For` 末位分桶（单层反代语义，默认关）。

## 事件总线 ACL

- **订阅白名单** = 公共主题（`sys:*`、`fs:changed`、`app:*`、`chat:*`、`notes:*`）+ 自有主题（`term:<自己>`）；`agent:*` 与他人终端主题不可旁路收听。
- **发布白名单** = 公开业务前缀（`app:` / `chat:` / `notes:`）+ 少数精确系统主题 + 自有终端主题；`sys:*` 广播收归服务端（防仿冒系统通知/在线状态）。
- 客户端发布的 `sys:open` / `sys:notify` / `fs:changed` 仅投递给**同一用户**（消费语义 = 本人跨端同步）；`sys:key` 恒本地、不进网络。

## 文件系统（VFS）

- 严格限制在 `/home/<用户>`（映射磁盘 `<data>/homes/<用户>`）；REST 读写经符号链接解析校验拒绝越界（终端 `ln -s` 指向外部不构成越权通道）。
- 单文件 ≤8MB（Write/Touch 请求体硬限 9MB、上传请求体硬限配额+8MB——超限体不再先落盘）、单用户配额 100MB、2 万文件上限；文件写删/重命名/上传/分享全部入审计。
- REST 写入受配额约束；终端 shell 写入不强制（沙箱语义）。
- 分享链接：128bit 随机 id 即授权凭据、登录态是身份门槛，二者缺一不可；下载恒 `attachment` + `nosniff`，绝不内联渲染。

## 应用代理（webproxy）

- **SSRF 防线在拨号层**：目标主机域名解析一次、校验后按 IP 直连（堵 DNS rebinding TOCTOU）；管理员配置的目标放行（可为内网，这是管理员的显式选择），其余主机拒绝内网/回环/链路本地/CGNAT/保留网段。
- 转发前剥离 OpenOS 自身会话（`openos_token`、`openos_*` Cookie、Authorization）——OpenOS 登录态绝不外发。
- 目标站 `Set-Cookie` 收归 `/app/<id>/` 代理路径（剥 Domain），隔离在代理前缀下。
- 访问权逐请求按角色授权矩阵（`role_apps`）校验；`/px/` 通用转发仅要求登录、仅公网目标。
- **信任模型**：经代理加载的页面与桌面**同源**执行——目标站 JS 理论上可访问本源存储，**仅配置可信应用**；不信任的站点用 iframe 直嵌（跨域隔离）。

## 子进程与机密管理

- 终端与 goder 子进程环境一律走 `safeenv` 白名单，**绝不继承 `OPENOS_*` 秘密**；git 凭据经 `GIT_CONFIG_*` 环境变量一次性注入，token 不进 argv/URL/日志。
- goder-serve 网关与桌面控制 MCP 端点**强制回环绑定**（非回环拒绝启动）；MCP 令牌按用户分发、令牌↔用户成对校验，工具只作用于发起用户本人，写操作入审计。
- AI 密钥仅存服务端、接口返回脱敏；goder 托管供应商走 OpenOS 自身代理端点（`/api/ai/v1/*`，每用户代理密钥），**平台 Key 不落任何用户盘**。

## XSS 防线

- 幻灯片应用的 HTML 解析做白名单净化（脚本/嵌入元素/事件属性/style 一律剥除，防跨用户存储型 XSS）。
- Markdown 渲染链接/图片 scheme 白名单（拒 `javascript:`）。
- 附件服务：Content-Type 内联白名单仅图片（png/jpeg/gif/webp/bmp/avif），其余（含 SVG/HTML）一律 `octet-stream` + `attachment` + `nosniff`——附件与分享链接不可变成 HTML 注入源。

## 机对机通道（宿主集成）

- `OPENOS_EXCHANGE_SECRET` 持有者可管理全部用户体系（含设置 admin 角色）——**密钥等同管理员凭证**，仅限宿主后端与 OpenOS 同机/可信网络之间使用；`OPENOS_EXCHANGE_ROLES` 仅约束 exchange 的用户令牌铸造面。
- 反向互通 `POST /api/auth/introspect`：判定与 REST/WS 同源（吊销即时生效），恒 200 + active 布尔（RFC 7662 风格），调用方应缓存结论。
- 宿主全部管理调用入审计（operator=`host`）。

## 用户与权限

- 角色目录双模式：独立部署 admin 编辑（system）/ 宿主声明投影（host，权威存储在宿主）；`users.manage` 等功能权限点实际接管管理端点（admin 恒直通）。
- 防提权：持 `users.manage` 的非 admin 不可操作 admin 用户、不可授予 admin 角色。
- 用户禁用全链路生效（登录 403 / REST 401 / WS 踢线）；最后一名启用中的管理员不可禁用/降级/删除。
- 删号级联清理：设置/文件/聊天/分享/反馈/AI 供应商/附件（行 + 磁盘文件）+ 磁盘 home + goder 会话目录。

## 终端的如实声明

`OPENOS_REAL_SHELL=on` 的「受限沙箱」**只限制 cd/工作目录跟踪，不是文件系统沙箱**——命令以服务进程权限执行，可读写服务器本地数据（其他用户 home、SQLite 库、附件等）。面向公网的部署应保持 `off`，或由运维自行加固（chroot / 容器卷隔离）。护栏比较带 `/` 边界；命令入审计 + 超时 + 输出截断。

## 数据治理

- 审计 / AI 对话 / 聊天记录 / 系统附件按保留期自动清理（默认 90 / 90 / 30 天，0 = 永久），防止无限膨胀。
- 用户级数据（便签/通知历史/日历/Dock/窗口会话等）持久化在服务端 `user_settings`，与系统密钥（`system_kv`）分表——用户数据可安全导出/清理，不波及系统密钥。
