# 常见问题

## 登录与账号

**默认账号是什么？**
`admin/admin123`（管理员）、`user/user123`（普通）、`guest/guest123`（访客）。`OPENOS_SEED_DEMO=0` 关闭种子。任何可访问环境部署后请立即改密（强制策略：≥8 位且含字母数字）。

**忘记 admin 密码？**
无邮件服务依赖，即无找回通道。测试环境可直接删除 `data/openos.db` 重来（全部数据丢失）。生产环境恢复：在另一实例上用已知密码建同名用户，把其 `users` 表的密码哈希覆盖到目标库的 admin 行（pbkdf2 哈希可跨实例复用），或提前备好第二个管理员账号。

**登录后一直提示「登录已失效」？**
多实例部署未固定 `OPENOS_JWT_SECRET`——各实例密钥不同导致互相签发的令牌被拒。显式设置同一密钥后重启。

## 部署

**Docker 构建报 `COPY failed: ... .run/goder-src`？**
镜像构建需要先准备 `.run/` 上下文（goder 源码 + gh 二进制），见 [Docker 部署 · 第一步](deploy-docker.md)。

**访问 localhost:8081 白屏 / 404？**
裸机部署时前端不是内嵌的：确认 `OPENOS_DIST` 指向 `web/dist`（run.sh 自动注入；手动运行见[自部署](deploy-bare-metal.md)）。开发模式请访问前端端口 5173 而非 8081。

**WebSocket 连不上（通知/在线状态不工作）？**
反代未配置 Upgrade 头——Nginx 需要 `proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection "upgrade";`，见 [Docker 部署 · HTTPS 反向代理](deploy-docker.md#https-反向代理)。

**开发模式前端连不上后端？**
Vite 代理指向 `127.0.0.1:8081`，确认后端已启动（`make dev` 同时启动两者）。桌面客户端连开发模式用 `-url http://localhost:5173`（vite 只监听 IPv6 localhost）。

## 功能

**终端提示「未启用」？**
真实终端默认关闭（`OPENOS_REAL_SHELL=off`，安全考虑）。受控环境可 `on` 开启；注意 `on` 的「受限」只是 cd 跟踪、不是文件系统沙箱，见[安全说明](security.md)。

**goder 应用显示部署引导？**
goder 组件缺失：Docker 镜像自带；裸机需 `make goder` + PATH 上有 gh，见 [goder 指南](goder.md)。

**AI 不回复 / 回复异常？**
管理员在「系统设置 → AI 服务」配置 OpenAI 兼容接口（可一键「测试连接」）；未配置时是本地演示模式。模型需支持你在配置中声明的模型名。

**添加的网页应用打开是空白？**
目标站设置了 `X-Frame-Options` / `frame-ancestors` 防嵌头——改用「应用代理」形态（勾选应用代理），或用[桌面客户端](desktop-client.md)。

**网易云等网站登录不上 / 请求异常？**
此类站点登录体系复杂（top 窗口执行、风控严判），统一代理已内置适配器与 top 桥；仍异常时：① 用应用工具栏「清除缓存」清登录态重来；② 工具栏「浏览器打开」完成登录后回来刷新；③ 桌面客户端形态最稳（同源策略已关闭）。

**文件分享链接 SSO 模式打不开？**
已知限制：SSO 模式全程跳转可能丢失 `#share=` 片段；本地账号登录不受影响。

**快捷键不生效？**
浏览器拿不到系统级组合键（macOS ⌘Tab / ⌘Space 被系统抢占）：切换器用 Alt+Tab、聚焦搜索用 Alt+Space 最稳。iframe 应用内的全局快捷键经注入脚本转发（跨源直连 iframe 除外）。

## 数据

**我的文件存在哪？**
服务端 `data/homes/<用户名>/` 真实磁盘目录（文件管理 / 终端 / goder 共用）。备份整个 `OPENOS_DATA` 目录即可（见[部署文档](deploy-docker.md#备份)）。

**换浏览器/设备后便签、Dock 布局还在吗？**
在——用户级数据一律服务端 `user_settings` 权威存储 + 本地缓存，多端一致。

**怎么彻底清空重装？**
停服务 → 删除 `data/` 目录 → 重启（重新种子演示数据）。桌面客户端另用 `openos-desktop reset` 清客户端侧数据。
