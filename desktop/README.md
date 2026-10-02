# openos-desktop —— OpenOS 桌面客户端

用 CDP（Chrome DevTools Protocol）驱动本机 Chrome 以 **app 模式**（独立窗口、无浏览器
外壳）打开 OpenOS，并**关闭同源策略与站点隔离**——专门解决 OpenOS 中网页应用
（`kind=proxy`，服务端反代形态）的跨域问题：反代重写地址、搬运 Cookie，天生难平
目标站的 SSO 跳转 / Referer 风控 / Cookie 轮换；桌面端让网页应用 iframe **直连真实
站点**，使用真实浏览器登录态。

## 构建

```bash
make desktop        # → 仓库根目录 ./openos-desktop（CGO_ENABLED=0，纯 Go）
./run.sh build      # 交叉编译 → release/openos-desktop-{linux,windows}-{amd64,arm64}[.exe]
```

### 应用图标与 Windows 构建

- **图标**（黑底圆角方形 + 白色圆环 O，设计稿 = 仓库根 `icon-o.svg`）：
  `icon.ico` 由纯 Go 生成器产出（8 倍超采样光栅化，不依赖 ImageMagick——其内置
  SVG 渲染器对 stroke 支持不可靠，实测丢白色圆环）：

  ```bash
  cd desktop && go run ./tools/genicon     # 重新生成 icon.ico（改设计稿后执行）
  go run github.com/tc-hib/go-winres@v0.3.3 simply \
    --icon icon.ico --manifest gui --arch amd64,arm64   # 重新生成 rsrc_windows_*.syso
  ```

  生成的 `rsrc_windows_{amd64,arm64}.syso` 随目录提交，`go build`（GOOS=windows）
  自动链入，无需额外构建参数；darwin/linux 构建按文件名后缀自动排除，不受影响。
- **无 cmd 黑框**：Windows 交叉构建注入 `-ldflags "-H windowsgui"`（`run.sh`
  `build_cross` 已内置），PE 子系统为 GUI。代价是 stdio 不再关联控制台，由
  `console_windows.go` 的 `attachParentConsole`（启动时 `AttachConsole` 挂回父
  终端 + 重开 `CONOUT$`/`CONIN$`）恢复——cmd/PowerShell 里启动照常看到横幅、
  `reset` 的输出与 `[y/N]` 确认输入可用；双击启动（无父终端）时 fatal 错误改弹
  系统消息框（否则「未找到 Chrome」静默退出毫无线索）。

## 使用

```bash
./run.sh start            # 推荐：默认 target=desktop，先确保服务端运行再启动本客户端
./run.sh start desktop    # 同上；stop desktop 仅停客户端，restart desktop 整套重启
./run.sh stop             # 整套停止（客户端 + 服务端）
./openos-desktop          # 直接运行：打开已保存配置的地址（无配置 = http://127.0.0.1:8081）
./openos-desktop -url http://localhost:5173   # 开发模式（vite；仅本次生效、不写盘）
./openos-desktop reset    # 子命令：清空全部本地数据（详见下）；-y 跳过确认
./run.sh reset            # 同上（先停客户端再清空，完成后保持停止状态）
```

### reset 子命令（清空所有信息）

`openos-desktop reset [-profile <目录>] [-y]` 把客户端恢复到全新状态——**整目录删除
重建专用 profile（默认 `~/.openos-desktop`）**，清空范围：

- 浏览器登录态与站点数据（Cookie、localStorage、缓存，含美团等第三方会话）；
- 服务器配置与最近使用（`config.json`，重置后需在设置页重新填写兼容端地址）。

安全设计：先停止仍在运行的实例（client.pid + unix 下 pgrep 按命令行匹配 profile，
Windows taskkill /T 树杀）再删目录；**防误删**——目录须带客户端数据特征
（`Default` / `Local State` / `config.json` / `client.pid`）或为空，home、根与无特征
目录一律拒绝；默认交互确认（stdin 非交互时视为取消），`-y` 跳过。

参数：

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `-url` | 见优先级 | OpenOS 地址。优先级 = `-url` 显式（仅本次生效、不落盘）> `<profile>/config.json` 已保存配置 > `http://127.0.0.1:8081`。开发模式注意 vite 可能只监听 IPv6 的 localhost，用 `http://localhost:5173` 而非 `127.0.0.1` |
| `-chrome` | 自动探测 | Chrome/Chromium/Edge 可执行文件路径（`$OPENOS_CHROME` 同效） |
| `-profile` | `~/.openos-desktop` | 专用用户数据目录，**登录态持久化于此**（复登免输账号） |
| `-port` | 自动选取 | CDP 调试端口（仅绑 127.0.0.1） |
| `-conf-port` | `17700` | 服务器设置页的回环端口（被占自动递增；`$OPENOS_DESKTOP_CONF_PORT` 同效） |

## 服务器设置（连接 OpenOS 兼容端）

客户端可连接**任何 OpenOS 部署**（本地或在线的兼容端），运行中随时切换：

- **入口（系统设置内）**：OpenOS 前端「系统设置 → 服务器」分区（桌面模式专属，
  内嵌客户端本地设置页 iframe，自动跟随系统深浅主题）；登录前可在登录页底部点
  「⚙ 切换服务器」整窗进入设置页；
- **设置页**：客户端回环地址 `http://127.0.0.1:<conf-port>`，输入兼容端地址（无
  scheme 时本地主机自动补 `http://`、其余补 `https://`）或从「最近使用」一键连接；
  连接后主窗口整页切换到新服务器；
- **持久化**：地址保存在 `<profile>/config.json`（url + 最近 6 条），下次启动自动
  连接；`run.sh` 检测到配置指向远程地址时，`start`/`restart` 跳过本地服务端的
  构建与启动（`OPENOS_DESKTOP_URL` 显式指定时仍走 `-url` 一次性覆盖）；
- **打不开兜底**：服务器地址连不上时自动转入设置页并提示 unreachable；初始导航
  失败（CDP 附身晚于 Chrome 报错的竞态）由 initialWatch 轮询兜底；
- **动态跟随**：切换服务器后，主帧防走失守卫、Fetch Referer 改写的「OpenOS 源」、
  Cookie jar 归一化的豁免域全部即时跟随新地址（http 兼容端的 Cookie 不会被强加
  Secure 而失效）。

注意：连接的兼容端前端需带桌面模式支持（检测 `window.__OPENOS_DESKTOP__`，标记
对象带 `settingsUrl` 即显示「服务器」分区与登录页切换入口）才能使用设置入口并让
网页应用 iframe 直连真实站点；窗口内等于本机真实浏览器会话，**勿连接不可信实例**。

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
   校验）。**容器深链必须带 `.html` 后缀**（裸路径子页 404 连带容器 404）；
8. **服务器设置**（settings.go，见上「服务器设置」章节）：前端「系统设置 → 服务器」
   分区（标记对象 `settingsUrl` 驱动，桌面模式专属）内嵌回环设置页 +
   `<profile>/config.json`；登录页另有「切换服务器」入口；初始导航失败（CDP 附身
   晚于 chrome-error 的竞态）由 initialWatch 轮询 `location.href` 兜底转设置页；
9. 关键启动参数：`--disable-web-security`（跨源 iframe 可嵌入、脚本互访）、
   `--disable-site-isolation-trials` + `--disable-features=site-per-process,IsolateOrigins`
   （iframe 不隔离；代价是桌面页与嵌入应用共享渲染进程，重应用可能拖慢整页）、
   `ThirdPartyStoragePartitioning`（嵌入态第三方存储不分区）；
10. 主窗口关闭 / Chrome 退出 / Ctrl+C 时按进程组收尾退出。

## 安全边界

- `--disable-web-security` 意味着窗口内跨源脚本可互访：该窗口**只用于打开 OpenOS**，
  专用 profile 不要当日常浏览器使用；
- CDP 调试端口只绑 `127.0.0.1`；
- 与 goder 同级的信任模型：窗口内等于本机真实浏览器会话，勿在不可信 OpenOS 实例上
  使用本客户端。
