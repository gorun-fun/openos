// Package hostsdk 是「宿主应用集成 OpenOS」的官方 Go SDK——把 grf-hotel /
// grf-os 两代宿主各自维护的约 1500 行集成样板收敛为单一实现：
//
//   - Manager：openos-server 子进程生命周期（拉起/优雅停机/环回反代/健康等待）
//   - Client：服务端到服务端 API（令牌交换/用户同步/角色声明/桌面通知/管理调用）
//   - SSO：Casdoor(OIDC) 授权码登录流（state 防重放/userinfo/get-account 兜底/
//     组归一化），身份解析留给宿主的 ResolveIdentity 回调
//   - web：SSO 回跳双令牌中间页 / openos_token Cookie / 开发登录页模板
//   - PxEscape：webproxy 代理页逃逸的宿主根层回捕中间件
//
// 消费方式（宿主 go.mod，快照路径随 ensure-openos.sh 拉取）：
//
//	require git.gorun.fun/share/openos/hostsdk v0.0.0
//	replace git.gorun.fun/share/openos/hostsdk => ./.run/openos/hostsdk
//
// 约定：openos 是上游通用框架不 fork；升级 = 推 openos main + 宿主重启拉快照。
package hostsdk
