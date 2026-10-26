# 统一代理（网页应用深度指南）

**简体中文** | [English](webproxy.en.md)

「网页应用」把现成网站零代码嵌成桌面应用（方式四，管理员在「系统设置 → 第三方应用 → 添加网页应用」配置）。两条加载路径：

- **iframe 直嵌**（`kind=web`）：目标站未设防嵌头时的默认形态。跨域天然隔离，双方脚本无法互访；跨站 Cookie 受浏览器第三方 Cookie 政策约束（Safari 等严格环境登录异常时用工具栏「浏览器打开」兜底）。
- **统一代理**（`kind=proxy`）：目标站经 OpenOS 服务端反代加载，解决 **iframe 防嵌**（`X-Frame-Options` / CSP `frame-ancestors`）与**跨域**两类问题。

统一代理是一套引擎双入口（`server/internal/webproxy/`）：**改清洗规则只改 engine.go 一处**。

| 入口 | 形态 | 用途 |
|---|---|---|
| `/app/<id>/<scheme>/<host>/…` | 应用反代（HTML/CSS 地址重写 + hook 注入） | 网页应用整页嵌入 |
| `/px/<scheme>/<host>/…` | 通用转发（纯透传，无重写无注入） | 网络请求类应用的统一 CORS 通道 |

## URL 映射（紧凑形态）

目标地址 `https://h:8443/a/b?q` 映射为 `/app/<id>/https/h:8443/a/b?q`——**scheme 段不写 `://`**：Go ServeMux 的 cleanPath 会把路径里的 `//` 折叠成 307 清洗重定向破坏地址（`compactTarget`/`decodeTarget` 是唯一编解码实现，`/px/` 复用）。

## 三层地址覆盖

代理下页面内的所有请求持续命中代理：

1. **服务端重写**（`rewrite.go`）：HTML 的 `href/src/action/srcset/style`、`<style>` 与 CSS 的 `url()`/`@import`、`meta refresh` 全部改写为代理路径——含根绝对路径与跨域 CDN 地址（`<base>` 注入做不到根绝对路径，这是必须重写的原因）；
2. **注入 hook 脚本**（`hook.js`，紧跟 `<head>` 早于页面脚本执行）：包装 `fetch`/`XMLHttpRequest`/`WebSocket`（改走 `/app/<id>/ws/…` 由服务端泵转）/`EventSource`/`Worker`/`window.open`/`sendBeacon`/`history.pushState`/`document.cookie`（改写 `path=/` 防 cookie 种到整个 OpenOS 源）/图片等 URL 反射属性与 `setAttribute`；并禁用目标站在本源注册 Service Worker；
3. **相对地址天然解析**：页面在 `/app/<id>/<目标URL>` 之下，`fetch('api/x')` 无需拦截即落在代理路径内。

## 会话与跳转

- 目标站 `Set-Cookie` 收归代理路径（剥 `Domain`、`Path` 映射到 `/app/<id>/…`；http 部署剥 `Secure` 并把 `SameSite=None` 降级 `Lax`）——登录态在代理路径下闭环；
- 30x 的 `Location` 同样重写；gzip 响应解压后改写；SSE / 大文件 / 视频流式透传；WebSocket 双向泵转。

## 转发语义（头处理）

- **Referer 还原为目标站自身绝对地址**（敏感接口常校验 Referer 白名单；还原值不泄露 OpenOS 宿主信息），不可识别时兜底补目标站根地址；
- 浏览器 `Sec-Fetch-*` 整组剥除（反代后源关系语义失真）；`Origin` 不无条件重建（原生 GET 不带），严判站点由 origin-strict 适配器按主机兜底；
- 代理响应**剥掉 `X-Content-Type-Options: nosniff`**：目标站 JSONP/脚本接口常 MIME 不规范（网易云易盾 application/json、百度 hm.js text/plain），nosniff 硬阻断会让登录/风控初始化整体失败——这是有意为之；
- **改写响应的缓存语义**：重写体与上游校验器不匹配——剥 `ETag`/`Last-Modified` + `Cache-Control: no-cache`，RewriteJS 命中的请求另剥 `If-None-Match`/`If-Modified-Since`（否则 304 协商让浏览器永用改写前缓存体）。

## 自定义请求头（`config.headers`，仅代理模式）

管理员可为该应用配置一组固定请求头，随每个代理请求（含 WS 握手与 favicon 取图）发给目标站——典型：内网系统按 `Authorization`/`X-Api-Key` 鉴权。

- 配置头在**剥除 OpenOS 会话之后**应用，优先级最高（可覆盖默认 `X-Forwarded-*`）；
- `Cookie` 头为合并语义：配置值在前、保留的目标站会话 Cookie 在后（不顶掉登录态）；
- 逐跳/传输控制类头（`Connection`/`Transfer-Encoding`/`Content-Length`/`Host`）黑名单静默丢弃；非法名称或含 CR/LF 的条目丢弃（防头部注入）；
- `env:<NAME>` 形态可展开环境变量。

## 窗口内标签页（仅代理模式）

页面弹出的新窗口被拦截，在原窗体内开标签页：`window.open(...)` 返回真实 window 代理（`closed`/`close()`/`postMessage` 可用，命名窗口复用同一标签）；`target="_blank"` 链接捕获（⌘/Ctrl 修饰键点击放行原生新窗口）；页内 `window.close()` 关标签，关掉最后一个标签即关窗体。标签条多标签时出现，切换不重载。

## SPA 路由与登录流

- **SPA 路由还原**：react-router 等按 `location.pathname` 路由的 SPA 在代理前缀下会全部失配——hook 在页面加载最早期 `history.replaceState` 把文档地址还原为目标站形态（网络请求仍映射回代理路径），还原地址持续携带 `__os_px=<id>~<scheme>~<host>` 查询标记；
- **逃逸回捕（CatchEscape）**：`location.href='/x'` 这类对 location 的直接赋值无法被 hook 包装，会逃出代理前缀落到 OpenOS 自身路径（典型：SSO 跳转 `location.href='/api/auth/sso/login'` 撞 OpenOS 路由）。回捕中间件挂在**整棵路由树外层**：请求携带代理页面 Referer（代理响应统一下发 `Referrer-Policy: same-origin`）时 307 拼回该应用的代理路径（保留方法与请求体，表单 POST 逃逸同样生效），兼认 `__os_px` 标记；
- **OAuth 回跳参数双向改写**：hook 把 query 中的 `redirect_uri`/`redirectUri` 改写为代理路径（跳转落回本源、回调与会话经代理完成），服务端转发上游时还原为注册的原始绝对地址（IdP 白名单校验不受影响）；
- **top 代理桥**（`web/src/apps/web/pxTop.ts`）：网易云等站点把脚本/请求提升到顶层窗口执行（原生形态 top=站点自身），代理下 top=桌面页会绕开代理直连——桥把顶层动态构造的绝对地址按已注册代理应用目标域后缀重映射回 `/app/<id>/…`；
- 极少数校验自身域名的站点（OAuth 严格回跳、银行类）仍可能不可用，用工具栏「浏览器打开」或[桌面客户端](desktop-client.md)兜底。

## 站点适配层（`adapter.go`）

特定站点的特殊处理从统一链路解耦，按目标主机（裸域后缀匹配）命中后叠加，四个钩子：`OnRequest` / `OnResponse` / `Hook`（追加注入 JS）/ `RewriteJS`（JS 体定点改写）。内置：origin-strict（美团系/金山文档/微信/网易系写接口严判 Origin 的兜底补源）、weixin-double-ct（重复 Content-Type 收敛）、music163-top-bridge（网易云 top 桥与 QA 环境改写）。**新增站点特殊处理改这个文件，勿动统一链路。**

## 工具栏「清除缓存」（代理形态专属）

清本应用落在本源的全部数据后整窗重建：`__px_<appid>::` 前缀的 storage 键、服务端 Cookie 登记簿逐批过期（`POST /app/<id>/__os_px_clear__`——深路径 Cookie 只有响应头能清，站点再种重新入册）、页面 JS 写入的 Cookie、根路径 Cookie、SW 注册防御性清扫、hook 登记的 IndexedDB 逐库删除。

## 应用级注入脚本（`config.hook`）

JS 源码 ≤256KB，随通用 hook.js 与适配器片段**之后**注入（业务脚本可依赖已包装的 fetch/XHR 入口做接口判据）；脚本内 `__PREFIX__` 占位符同样替换为 `/app/<id>/`。用途：目标站页面内发生业务动作时由宿主平台接管——脚本监听接口/DOM，`postMessage` 通知宿主页，宿主再经 `OpenOS.launch` 拉起自家业务应用。判据写什么、命中后做什么全部在宿主仓库维护（`/api/host/apps` 更新 config 即热生效）。

## 安全边界（务必阅读）

- **仅配置目标**：代理目标只能来自管理员注册的 `config.url`，不接受请求方传 URL；配置目标允许内网地址（管理员的显式选择）。
- **防 SSRF**：目标以外的主机（跨域子资源、跳转、OAuth 流程）在**拨号层**校验——域名解析一次、按 IP 直连（堵 DNS rebinding TOCTOU），回环/内网/链路本地/CGNAT/保留网段一律拒绝；`/px/` 通用转发无配置放行，**仅公网**。
- **会话隔离**：转发前剥离 OpenOS 自身会话（`openos_token`、`openos_*` Cookie、`Authorization`）；访问权逐请求按角色授权矩阵校验（与启动台可见性一致），未登录 401、未授权 403。
- **信任模型**：经代理加载的页面与桌面**同源**执行（与 goder 内嵌同一信任级别），目标站 JS 理论上可访问本源存储与以当前用户身份调用 OpenOS 接口——**仅配置可信应用**（内部系统、自有站点）；不信任的公网站点用 iframe 直嵌。同源存储按源共享，两个代理应用若用相同 localStorage 键可能互相覆盖。

## 通用转发（`/px/`）的消费方

- **网络请求应用（http-client）**：默认经代理发送（顶栏「代理」开关可切回直连；内网/回环目标自动直连；探测 `GET /api/proxy/status` 不可用自动回落）；
- **声明式应用（SchemaApp）**：跨域 `dataSource.kind=rest` 默认经 `OpenOS.proxyUrl()` 走 `/px/`，失败回落浏览器直连（`direct: true` 强制直连）。

## 开关与状态

`OPENOS_PROXY=off` 下架 `/app/*` 与 `/px/*` 路由（应用配置保留，iframe 将得到 404）；`GET /api/proxy/status` 返回统一代理可用性（设置「网页应用」分区与网络请求类应用据此降级）。
