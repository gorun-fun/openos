// Package hostsdk 是「宿主应用集成 OpenOS」的官方 Go SDK——把 grf-hotel /
// grf-os 两代宿主各自维护的约 1500 行集成样板收敛为单一实现：
//
//   - Manager：openos-server 子进程生命周期（拉起/优雅停机/环回反代/健康等待）
//   - Client：服务端到服务端 API（令牌交换/用户同步/角色声明/桌面通知/管理调用）
//   - SSO：Casdoor(OIDC) 授权码登录流（state 防重放/userinfo/get-account 兜底/
//     组归一化），身份解析留给宿主的 ResolveIdentity 回调
//   - HandleSSOCallback：SSO 回调编排（身份解析/签发/exchange 留宿主回调，
//     refresh Cookie + BootstrapPage 双令牌中间页由内核串联）+ 请求期回调地址
//     推导（NewSSOAutoRedirect/RequestRedirectFunc）
//   - RefreshSessions：refresh Cookie 会话续期内核（HMAC 签名双通道自认证，
//     Bearer 滑动续期 / Cookie 过期复活，身份实时重算走宿主 Resolve 回调）
//   - DevLogin：开发登录页（玻璃卡片，单段/两段式取 os 令牌参数化）+ 固定
//     账号账密端点（常数时间校验 + 双 Cookie 种植）
//   - Seed：宿主应用/角色/授权的启动期种子与调和编排（首次种子标记/退休应用
//     对账清退/定向调和/演示账号禁用，均参数化）
//   - Boot：启动/停机样板（子进程→种子→AISyncer/后台任务→HTTP→优雅停机
//     先 HTTP 后子进程；宿主 main 收敛为一行调用）
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
