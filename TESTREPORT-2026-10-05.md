# OpenOS 线上全量回归测试报告

- **测试对象**：https://openos.gorun.fun/（部署版本 v0.0.43，启动于 2026-10-04T23:27:08Z）
- **测试时间**：2026-10-05 07:26 ~ 07:52（北京时间）
- **测试方式**：Playwright 驱动系统 Chrome（1440×900 桌面形态），admin 账号全程黑盒 GUI 测试 + DOM 断言 + 网络响应头探测
- **测试人**：ZCode 自动化回归

## 一、结论

线上 18 个应用、外壳全部功能整体健康，**28 个测试域通过，0 个阻断级缺陷**。发现 3 个应修复问题（1 个 P1 部署可靠性 + 2 个 P3 体验项），另有 2 条环境观察项（非代码问题）。全部修复随 v0.0.44 发布。

## 二、问题清单（修复项）

### Bug#1【P1 · 部署可靠性】静态资源无缓存策略，发版后老页面应用全部打不开

- **现象**：测试期间恰逢 v0.0.43 CI 部署（23:26:48），部署前已加载的页面点击打开「文件」应用 → `GET /assets/FilesApp-Bk1eiYfF.js` 404 → 应用渲染失败。同时 `/api/auth/me` 出现一次 502（网关重启窗口）。
- **根因**：`server/main.go` 的 `spaHandler`/`http.FileServer` 对**所有**静态文件均不下发 `Cache-Control` 响应头（实测 `curl -I`：index.html 与 assets/*.js 都只有 `Last-Modified`）。浏览器因此走**启发式缓存**持有旧 index.html（引用旧 hash 的 chunk），新版本部署删除旧 chunk 后，老页面所有懒加载应用逐一 404。
- **影响**：每次发版，所有在线用户的已打开页面在刷新前打开任何未加载过的应用都会失败，观感为“发版把系统搞坏了”。
- **修复**（v0.0.44）：spaHandler 包装缓存策略——
  - `/assets/*`（vite hash 产物）：`Cache-Control: public, max-age=31536000, immutable`（内容寻址，可永久缓存，同时提升回访性能）；
  - `index.html`、`sw.js`、`manifest.webmanifest` 及其余静态文件：`Cache-Control: no-cache`（每次带条件请求再验证，发版后一次 revalidate 即拿到新引用）。

### Bug#2【P3 · 体验】应用窗口错误边界不识别“发版 chunk 失效”，重试无效且无刷新引导

- **现象**：Bug#1 触发时，窗口内出现通用错误面板（“应用遇到错误/可重试或关闭窗口”）。但 chunk 404 属于**永久失效**（旧 hash 文件已被新部署删除），“重试”必然再次失败，用户不知道需要刷新整页。
- **修复**（v0.0.44）：WindowFrame 错误边界识别动态 import 失败（`Failed to fetch dynamically imported module` / `Importing a module script failed` / `error loading dynamically imported module`），错误面板针对该类错误改为提示“系统已更新，请刷新页面加载新版本”，主按钮改为「刷新页面」（`location.reload()`）。

### Bug#3【P3 · i18n 缺口】菜单栏时钟、小组件日期、在线人数硬编码中文

- **现象**：设置 → 外观 → 语言切换 English 后，系统设置等页面文案正确切换，但菜单栏时钟仍显示「周一 10月5日 07:49」、时钟小组件仍「10月5日 周一」、在线 chip 仍「👥 2 在线」。设置页对该功能的声明是“当前覆盖：登录页、菜单栏、系统提示与系统设置”。
- **根因**：`MenuBar.vue` 的 `timeText` 与 `Widgets.vue` 的 `clockDate()` 用模板字符串硬编码中文日期格式；在线 chip「N 在线」未走 `t()`。
- **修复**（v0.0.44）：时钟/日期改按 locale 用 `Intl.DateTimeFormat`（zh-CN 保持原样式，en-US 走 `Mon Oct 5` / `Oct 5 Mon` 形态）；在线 chip 新增 `menu.presence` 词条（中/英）。

## 三、环境观察项（非代码缺陷，不修）

1. **部署窗口期网关 502**：CI 重新部署容器瞬间 `/api/auth/me` 出现一次 502，约 20 秒内自愈。前端总线按“网络不可达视同闪断”退避重连，行为符合设计。
2. **QQ音乐埋点域名连接失败**：`stat.y.qq.com` 在测试网络 `ERR_CONNECTION_CLOSED`，属目标站自身资源/网络策略，不影响应用加载与交互。

## 四、通过项明细（28 项）

### 外壳与系统
| # | 测试项 | 结果 | 关键证据 |
|---|--------|------|----------|
| 1 | 登录页（默认账号填充/语言切换钮/演示账号提示） | ✅ | 无 console 错误；admin/admin123 登录成功 |
| 2 | 桌面外壳渲染（菜单栏/双小组件/Dock/桌面图标） | ✅ | 时钟+监控小组件（在线2/窗口数/存储）、Goder 桌面图标 |
| 3 | 启动台：弹窗化面板/7 类目胶囊/搜索过滤 | ✅ | 搜“计算”→ 仅剩计算器；装维基百科后自动新增“资料”类目；Esc 关闭 |
| 4 | Spotlight 聚焦搜索 | ✅ | 搜“文件”命中应用组；Enter 直接打开文件应用 |
| 5 | 通知中心 | ✅ | 历史（聊天通知）、提示声开关、勿扰、清除全部 |
| 6 | 深浅色切换 | ✅ | `html.dark` 即时生效并 localStorage 持久化 |
| 7 | i18n（zh↔en 切换+账号级持久化） | ✅（除 Bug#3 缺口） | 设置标题 Appearance/Wallpaper/About 正确切换，切回正常 |
| 8 | 窗口管理：拖动/最小化/Dock 恢复/最大化还原 | ✅ | 拖动 180,77→319,151；最小化后 dock 点击恢复聚焦 |
| 9 | 窗口左半屏吸附 | ✅ | 拖到左缘出现 snap-preview，释放后 x=0/w=720 |
| 10 | 应用切换器（Alt+Tab） | ✅ | files/calculator 两窗间切换，松开 Alt 提交 |
| 11 | 桌面右键菜单 | ✅ | 启动台/终端/文件/Goder/便签/时钟/便签/监控/日历/壁纸/深浅色 |
| 12 | 在线状态 presence | ✅ | 菜单栏「admin、guest 2 在线」实时 |
| 13 | PWA 三件资源 | ✅ | manifest.webmanifest/sw.js/icons 200 |
| 14 | `/api/health` | ✅ | `{"app":"OpenOS","ok":true}` |

### 应用（逐个）
| # | 应用 | 结果 | 覆盖点 |
|---|------|------|--------|
| 15 | 文件 | ✅ | 目录导航/面包屑/4 视图切换/列表排序/新建文件夹/右键菜单/分享链接创建（侧栏计数+1）/移到废纸篓/永久删除/md 双击分发到文档编辑器 |
| 16 | 废纸篓 | ✅ | dock 打开/.Trash 内容/右键“立即删除”语义/清空 |
| 17 | 便签 | ✅ | 新建/输入自动保存/5 色/钉到桌面（widget-note 出现且内容同步）/取消钉/删除确认 |
| 18 | 计算器 | ✅ | 12×3+4=40，显示算式与结果 |
| 19 | 系统设置 | ✅ | 外观（壁纸/语言/Dock 自动隐藏）/账户（显示名/改密）/AI 服务（未配置态+表单）/用户管理（3 用户/角色下拉/禁删保护）/角色与权限（权限点勾选）/应用管理（3 自定义应用）/系统信息（版本 v0.0.43/审计日志） |
| 20 | 应用商店 | ✅ | 目录 8 应用+类目/详情页/安装维基百科→启动台出现→卸载（确认弹窗→回收授权） |
| 21 | 聊天室 | ✅ | 在线列表/历史消息/附件气泡（md 56B）/发送消息总线往返即时上屏 |
| 22 | 日历 | ✅ | 农历/2026 国庆假期“休”标注至 10-7/新建日程/删除日程 |
| 23 | 反馈 | ✅ | 提交反馈→侧栏待处理→管理员回复保存→“已回复”徽标 |
| 24 | 终端 | ✅ | 虚拟终端 ls/cat 欢迎.md 输出正确 |
| 25 | goder | ✅ | goder-serve running（/api/goder/status + /api/ai/status available）/goder.js 加载/shadow DOM 渲染会话列表（2 个历史会话）/API（health/conversations/agent/models）带鉴权全 200 |
| 26 | 世界地图（osm-map） | ✅ | OSM embed iframe 加载无错误 |
| 27 | QQ音乐 | ✅ | y.qq.com iframe 加载（埋点域被断属外部因素） |
| 28 | 文档/表格/幻灯片/PDF 阅读器 | ✅ | md 编辑（编辑/分栏/预览/字数/导出）；表格 A1+1561 单元格+行列操作工具栏；幻灯片标题页/页管理/格式工具栏；PDF 空态引导+拖放提示 |

### SDK 面（顺带验证 v0.0.43 新增能力）
- `window.OpenOS` 挂面完整：`can`（v0.0.43 新增）在列；`open('calculator')` 正常开窗（注意 `open('market')` 返回 null 属预期——商店 id 为 `store`）。

## 五、修复与发版记录

- v0.0.44（commit 3a6e4e8）：修复 Bug#1/2/3（见上），`cd web && npm run typecheck` + `go build/test` 通过，本地冒烟 curl 实测五类路径缓存头与 SPA 回退 200 后提交打 tag，CI 自动构建部署。
- **线上复测（v0.0.44，2026-10-05 08:00）全部通过**：
  - [x] `curl -I`：index.html / sw.js / manifest / icons 均带 `Cache-Control: no-cache`；`/assets/index-*.js` 带 `public, max-age=31536000, immutable`；条件请求（If-Modified-Since）返回 304（no-cache 再验证语义生效）
  - [x] i18n：切 English 后菜单栏时钟显示 `Mon, Oct 5 07:56`、在线 chip 显示 `👥 2 online`；切回中文恢复「周一 10月5日」「N 在线」
  - [x] 回归抽检：文件（目录+文件列表）/计算器/应用商店（8 应用目录）均正常打开，goder-serve running，控制台 0 错误；系统信息显示 v0.0.44
- 已知 cosmetic 尾巴（不构成缺陷，留待后续随版收敛）：en-US 形态日期经 Intl 组合带逗号（Mon, Oct 5），与 macOS 原生无逗号形态（Mon Oct 5）差一个逗号。
