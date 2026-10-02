# openos-desktop —— OpenOS 桌面客户端

用 CDP（Chrome DevTools Protocol）驱动本机 Chrome 以 **app 模式**（独立窗口、无浏览器
外壳）打开 OpenOS，并**关闭同源策略与站点隔离**——专门解决 OpenOS 中网页应用
（`kind=proxy`，服务端反代形态）的跨域问题：反代重写地址、搬运 Cookie，天生难平
目标站的 SSO 跳转 / Referer 风控 / Cookie 轮换；桌面端让网页应用 iframe **直连真实
站点**，使用真实浏览器登录态。

## 构建

```bash
make desktop        # → 仓库根目录 ./openos-desktop（CGO_ENABLED=0，纯 Go）
```

## 使用

```bash
./run.sh start            # 推荐：默认 target=desktop，先确保服务端运行再启动本客户端
./run.sh start desktop    # 同上；stop desktop 仅停客户端，restart desktop 整套重启
./run.sh stop             # 整套停止（客户端 + 服务端）
./openos-desktop          # 直接运行：打开 http://127.0.0.1:8081（生产形态）
./openos-desktop -url http://localhost:5173   # 开发模式（vite）
```

参数：

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `-url` | `http://127.0.0.1:8081` | OpenOS 地址（开发模式注意 vite 可能只监听 IPv6 的 localhost，用 `http://localhost:5173` 而非 `127.0.0.1`） |
| `-chrome` | 自动探测 | Chrome/Chromium/Edge 可执行文件路径（`$OPENOS_CHROME` 同效） |
| `-profile` | `~/.openos-desktop` | 专用用户数据目录，**登录态持久化于此**（复登免输账号） |
| `-port` | 自动选取 | CDP 调试端口（仅绑 127.0.0.1） |

## 工作原理

1. 定位 Chrome，以 `--app=<url>` + 专用 profile + 调试端口拉起进程；
2. 连接浏览器级 CDP WebSocket，`Target.setAutoAttach` 附身每个页面目标，注入
   `window.__OPENOS_DESKTOP__` 只读标记（新文档在脚本执行前注入 + 当前文档立即求值）；
3. 前端 `web/src/apps/web/WebApp.vue` 检测到标记后，代理应用 iframe 直连 `config.url`
   真实地址，而非 `/app/<id>/` 反代路径；
4. **SameSite Cookie 三层桥**（跨站登录的关键，Chrome 154 实测结论）：
   - 跨源 iframe 属第三方上下文，目标站未标注 SameSite 的会话 Cookie 按 Lax 语义被
     整体拒收（Set-Cookie 与 document.cookie 双双落空），且该行为已固化、
     `--disable-features` 关不掉；
   - **注入 shim**：`document.cookie` 写入自动补 `SameSite=None`（https 帧再补
     Secure）。主帧走 `addScriptToEvaluateOnNewDocument`；跨源子帧不进该通道，由
     `Runtime.executionContextCreated` 即时补丁 + 周期 walker（contentWindow.eval 跨
     realm）兜底；
   - **响应桥**：监听 `Network.responseReceivedExtraInfo` 的被拒 Set-Cookie
     （blockedReasons 属 SameSite 语境），经 `Network.setCookie` 直写 Cookie jar；
   - **jar 归一化**：周期把 jar 内非 None Cookie（本地域除外）改写为
     `SameSite=None; Secure`，修复「顶层第一方登录后、iframe 第三方请求不带 Cookie」
     的发送侧；
5. **「登录」按钮**（前端，桌面模式专属）：`window.open` 在顶层第一方窗口完成登录/
   SSO/设 Cookie（原生语义零损耗），检测到离开登录页后自动关窗并刷新页内标签，配合
   jar 归一化即恢复会话；
6. **请求级拦截**（Fetch 域）：剥 `Sec-Fetch-*` 整组头（跨站 iframe 请求的
   cross-site 语义会触发目标站服务端风控，美团 ebIframe 实测 302→404）+ 指向
   OpenOS 源的 Referer 改写为目标站自身（微信类 Referer 白名单风控同源问题）；
   `OSD_NO_FETCH=1` 可关闭拦截用于诊断；
7. **微前端容器适配**（美团 ebooking 类应用，多层 handshak 均在桌面端补齐）：
   桌面模式 iframe 不带 sandbox（sandbox 语境下容器判环境异常整页 404，防走失由
   客户端主帧导航守卫回捕）；前端应答 `hotel-query-container` 父容器探测（经
   `e.source.eval` 在对方 realm 自发 postMessage 伪造目标站 origin，过其 isValidHost
   校验）。**容器深链必须带 `.html` 后缀**（裸路径子页 404 连带容器 404）。
6. 关键启动参数：`--disable-web-security`（跨源 iframe 可嵌入、脚本互访）、
   `--disable-site-isolation-trials` + `--disable-features=site-per-process,IsolateOrigins`
   （iframe 不隔离；代价是桌面页与嵌入应用共享渲染进程，重应用可能拖慢整页）、
   `ThirdPartyStoragePartitioning`（嵌入态第三方存储不分区）；
7. 主窗口关闭 / Chrome 退出 / Ctrl+C 时按进程组收尾退出。

## 安全边界

- `--disable-web-security` 意味着窗口内跨源脚本可互访：该窗口**只用于打开 OpenOS**，
  专用 profile 不要当日常浏览器使用；
- CDP 调试端口只绑 `127.0.0.1`；
- 与 goder 同级的信任模型：窗口内等于本机真实浏览器会话，勿在不可信 OpenOS 实例上
  使用本客户端。
