# OpenOS 文档

**简体中文** | [English](README.en.md)

| 文档 | 内容 |
|---|---|
| [Docker 部署](deploy-docker.md) | 镜像构建上下文准备、compose 启动、升级备份、HTTPS 反代 |
| [自部署（裸机/二进制）](deploy-bare-metal.md) | 源码构建、run.sh / systemd、跨平台发布包 |
| [配置参考](configuration.md) | 全部环境变量（基础 / AI / goder / 裁剪 / 保留期 / 客户端） |
| [安全说明](security.md) | 认证会话 / 总线 ACL / 文件隔离 / 代理 SSRF / 机密管理 |
| [应用开发指南](app-development.md) | 声明式应用、组件 SDK、跨应用弹窗与拉起协议、事件总线 |
| [宿主集成指南](host-integration.md) | createOpenOS 组壳、令牌交换/内省、机对机 API、角色权限投影 |
| [goder：AI IDE 与桌面控制](goder.md) | 部署形态、MCP 桌面控制插件、能力暴露层（CAP）、无头任务 |
| [统一代理指南](webproxy.md) | 网页应用反代：地址重写、登录流、适配器、安全边界 |
| [桌面客户端](desktop-client.md) | openos-desktop：CDP 驱动 Chrome、SameSite 桥、自动更新 |
| [常见问题](faq.md) | 登录 / 部署 / 功能 / 数据的常见排障 |

快速入口：[README](../README.md) · 演示：<https://openos.gorun.fun>

> 每份文档均有英文版：同目录同名 `.en.md`（[英文索引](README.en.md)）；修改文档时请同步中英两版。
