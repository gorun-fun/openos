# 桌面客户端（openos-desktop）

**简体中文** | [English](desktop-client.en.md)

反代（统一代理）之外的另一条网页应用接入路线：**本机 Chrome 以 app 模式直开 OpenOS，网页应用 iframe 直连真实站点**。适合服务端反代难以覆盖的场景（目标站 SSO / 风控 / Cookie 轮换复杂，如美团 ebooking），代价是依赖客户端环境（需本机 Chrome）。

## 构建与运行

```bash
make desktop                     # 构建 → 仓库根 ./openos-desktop（需本机 Go）
./openos-desktop                 # 先弹「选择服务器」页，选定后进入
./openos-desktop -auto           # 跳过选择页直连已保存配置（无配置 = http://127.0.0.1:8081）
./openos-desktop -url http://localhost:5173   # 开发模式直连 vite（vite 只监听 IPv6 localhost）
./openos-desktop reset           # 清空客户端全部本地数据（交互确认；-y 跳过、-profile 指定目录）
```

配套仓库根进程管理脚本：`./run.sh start|stop|restart|reset`（默认 target=desktop：start 先确保服务端再拉客户端；stop 先客户端后服务端）。

## 原理

CDP（Chrome DevTools Protocol）驱动本机 Chrome 以 `--app=<地址>` + 专用 profile（`~/.openos-desktop`，登录态持久化）启动，`--disable-web-security` + 关闭站点隔离——跨源 iframe 不再被同源策略 / X-Frame-Options / CSP frame-ancestors 拦截。网页应用（kind=proxy）经注入标记 `window.__OPENOS_DESKTOP__` 检测客户端形态后，iframe 直连 `config.url` 真实地址而非 `/app/<id>/` 反代路径。

## 服务器选择与配置

- 默认每次启动先开「选择服务器」页（回环设置页 `?pick=1` 形态，选定后落盘 `<profile>/config.json` 并导航主窗口）；直连优先级 `-url`（仅本次生效）> 已保存配置 > 本地默认；
- 入口在前端「系统设置 → 服务器」分区（客户端模式专属，iframe 内嵌回环设置页）与登录页底部「切换服务器」；设置服务端口默认 17700 递增（`OPENOS_DESKTOP_CONF_PORT` 可改），端口全被占时自动降级为直连；
- 服务器地址打不开（chrome-error:// 页不跑注入脚本）自动转设置页并带 unreachable 提示；切换后守卫 / Referer 改写 / Cookie 归一化的豁免域动态跟随（http 兼容端 Cookie 不强加 Secure）。

## SameSite 三层桥（跨站登录的命门）

Chrome 154 实测：未标注 SameSite 的会话 Cookie 在第三方 iframe 上下文按 Lax 整体拒收（`--disable-features=SameSiteByDefaultCookies` 已失效关不掉）。客户端三层修复：

1. **注入 shim**：`document.cookie` 写入自动补 `SameSite=None`（主帧走 addScriptToEvaluateOnNewDocument；跨源子帧不进该通道，靠 Runtime.contextCreated 即时补丁 + 周期 walker 跨 realm 兜底）；
2. **响应桥**：被拒的 Set-Cookie（responseReceivedExtraInfo.blockedCookies）经 `Network.setCookie` 直写 Cookie jar；
3. **jar 归一化**：周期把非 None Cookie 改写 `None+Secure`（本地域除外），修复「顶层第一方登录后、iframe 第三方请求不带 Cookie」的发送侧。

前端桌面模式网页应用工具栏另有**「登录」按钮**：`window.open` 顶层第一方窗口完成登录/SSO（原生语义零损耗），检测到离开登录页自动关窗并刷新页内标签。

## 版本与自动更新

- 发布通道 `download.gorun.fun/openos/cli`，六平台（macos / macos-arm64 / linux / linux-arm64 / windows / windows-arm64）× 版本目录 × 运行文件 + `.sha256` + notes；
- 流程：启动 2s 静默检查 → 设置页「客户端版本」区块（`GET /api/version` 状态 / `POST /api/update` 触发）→ 下载 `.new` → sha256 校验 → 两步改名原子换文件（失败回滚）→ 同 PID 原位重启（Windows 拉新进程）；
- 版本号构建期注入，`OPENOS_DESKTOP_UPDATE_URL` 可覆盖通道；`go run` 临时构建拒绝自更新；本地 e2e 用 `desktop/e2e-update.sh` 指假服务器。

## 已知边界

- **微前端容器适配**（美团 ebooking 类）需要多项配合：深链带 `.html` 后缀、去 sandbox、容器探测应答、Sec-Fetch 剥除、SameSite 桥、锚定回正（目标站 `parent.location` 把容器页顶走时导航回配置地址）、`document.referrer` 伪造——已在客户端内置，新增站点如遇异常先排查这几项；
- 无站点隔离下桌面页与嵌入应用**共享渲染进程**，重应用会拖慢整页（页面卡时先看是否渲染进程挂起，截图/时钟可判活）；
- 浏览器内全局快捷键经注入脚本 postMessage 转发回宿主；跨源直连 iframe 无法注入的场景不覆盖（已知边界）。

## 安全边界

`--disable-web-security` 意味着窗口内跨源脚本可互访——专用 profile 仅用于打开 OpenOS；CDP 端口只绑 127.0.0.1；窗口内等于本机真实浏览器会话，**勿指向不可信的 OpenOS 实例**。
