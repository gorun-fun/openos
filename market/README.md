# market/ — 应用商店远端货架（download.gorun.fun 镜像）

此目录是官方默认目录源 `https://download.gorun.fun/openos/market/catalog.json` 的
仓库内镜像。**运行期货架以远程合并结果为准**：更新货架 = 改这里的文件并同步上传
到远端，无需发版；同时 `server/internal/market/catalog.json`（go:embed 内置兜底）
应保持与镜像一致（新条目同步加，旧条目同步删）。

## 结构

- `catalog.json` — 货架清单（与内置目录同步维护）
- `bundles/` — bundle 形态应用的组件文件，**按版本命名**（`<id>-<version>.js`），
  与 catalog 条目 `config.url` 一一对应；升级 = 上传新版本文件 + 改条目 version 与 url
- `icons/` — 应用图标（catalog 条目的 icon 地址）

## bundle 形态约定（商店组件应用档）

单文件 ES 模块，**默认导出 Vue 组件定义**（`{ template, setup }`），template 由
OpenOS 主程序的完整版 Vue 运行时编译；模块不 import 任何依赖，能力经
`window.OpenOS` 注入（`vue`/`bus`/`api`/`me()`/`ui`/`toast` 等，见
`web/src/sdk/index.ts`）。服务端在管理员安装时下载落盘（≤2MB、强制 https 地址），
运行期经 `/api/market/bundle/<id>` 本地提供并按角色授权校验——代码只在安装那一刻
取一次远端，与内置应用同级信任，**只收录可信来源**。

## 上传（发布货架）

```bash
scp market/catalog.json 10.8.0.5:/data/download/openos/market/catalog.json
ssh 10.8.0.5 'mkdir -p /data/download/openos/market/bundles /data/download/openos/market/icons'
scp market/bundles/chat-1.0.0.js 10.8.0.5:/data/download/openos/market/bundles/
scp market/icons/chat.svg 10.8.0.5:/data/download/openos/market/icons/
```

上传后商店「刷新」按钮立即生效（POST /api/market/refresh 即时拉取远程源）。
旧版本服务端（不认识 bundle 形态）会跳过 bundle 条目并记日志，不影响其余货架。
