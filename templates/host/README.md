# 宿主集成脚手架（templates/host）

把一个业务系统接成「OpenOS 桌面壳宿主」所需的全部样板。以 grf-hotel / grf-os
两个既有宿主为参照提炼；新宿主从这里拷贝起步，避免再手工拼装约 1500 行集成代码。

## 组成

```
scripts/ensure-openos.sh        # 拉取/更新 openos 源码快照到 .run/openos（reset --hard 跟踪 main）
scripts/link-openos-modules.sh  # .run/openos/web/node_modules → 宿主 node_modules 符号链接
                                #（openos 源码在宿主工程外，TS/Vite 跨根目录解析必需；快照重克隆后重跑）
```

## 服务端接线（Go）

宿主 `go.mod`：

```
require git.gorun.fun/share/openos/hostsdk v0.0.0
replace git.gorun.fun/share/openos/hostsdk => ./.run/openos/hostsdk
```

```go
import hostsdk "git.gorun.fun/share/openos/hostsdk"

// 1) 子进程托管（cmd/server/main.go）：
mgr := hostsdk.New(hostsdk.Config{
    ExchangeSecret: cfg.ExchangeSecret,   // 必填：exchange/declare/publish 共用
    ExchangeRoles:  cfg.ExchangeRoles,    // 强烈建议：密钥泄露防铸 admin
    MarketURL:      cfg.MarketURL,        // 宿主货架 catalog.json（App 热发布）
    GoderAddr:      "127.0.0.1:7713",     // 同机多实例错开 goder-serve 端口
    ExtraEnv:       []string{"OPENOS_VFS=off"}, // 按需裁剪
})
if err := mgr.Start(); err != nil { log.Fatal(err) }
defer mgr.Stop() // 优雅停机：先停 HTTP 再停子进程

// 2) 路由边界（根 mux）：
//    /api/v1/*        宿主业务
//    其余 /api /ws /vendor /app /goder-api  → mgr.Handler() 反代子进程
//    根层最外再包 hostsdk.PxEscape（webproxy 代理页逃逸回捕）

// 3) SSO（身份唯一来源；dev 才有账密）：
sso := hostsdk.NewSSO(hostsdk.SSOConfig{Endpoint: ..., ClientID: ..., ClientSecret: ..., RedirectURL: ...})
mux.HandleFunc("GET /api/v1/auth/sso/login", func(w, r) { sso.LoginRedirect(w, r, devLoginFallback) })
mux.HandleFunc("GET /api/v1/auth/sso/callback", func(w, r) {
    u, err := sso.Callback(w, r)
    if err != nil { sso.FailRedirect(w, r, "登录失败"); return }
    claims := resolveIdentity(u)                      // ← 宿主业务身份解析（成员/访客/白名单）
    hostToken := issueHostJWT(claims)                 // ← 宿主自己签发业务令牌
    osToken, _ := mgr.Exchange(u.UID, claims.OpenosRole, claims.Name, claims.Perms)
    hostsdk.BootstrapPage(w, hostToken, osToken, hostsdk.BootstrapOpts{HostTokenKey: "token"})
})

// 4) 角色/权限声明（幂等，启动时调用；权威在宿主自己的库——openos 只读投影）：
mgr.Declare(roleDecls, permDecls)   // 取代 os_role_seed 标记表类 workaround

// 5) 业务事件 → 桌面通知（旁路，失败不影响业务事务）：
mgr.NotifyUsers(userIDs, "保洁任务", "3 楼 301 已派发", "rooms")

// 6) token-hub 套餐 → goder 每用户模型供应商同步（可选，AISyncer）：
hostsdk.NewAISyncer(hostsdk.AISyncConfig{
    THBase: cfg.THURL, THKey: cfg.THKey, SetDefault: true,
}, mgr).Run(ctx)   // 周期全量声明；整轮 users 失败绝不写空清单
```

## 前端接线（app/web）

```ts
import { createOpenOS, createHostApi } from '@openos/framework'

// 宿主业务 API 客户端：令牌内存缓存 + 401 静默续期（refresh Cookie 复活）重试
// + SSO 跳转挂起 + 6h 保活，续期响应带 openos_token 时自动同步 openos 客户端。
// 不配置 session 则退化为简单形态（401 → onUnauthorized）。
// 注意经 '@openos/framework' 导入（非 '@openos/host'）：宿主 deps 构建的
// importmap 只登记既有裸名即可保 openos 令牌态单实例。
export const os = createHostApi({
  tokenKey: 'token', base: '/api/v1',
  headers: () => ({}),                                    // 动态附加头（如门店上下文）
  session: { loginUrl: '/api/v1/auth/sso/login' },
})

// goder 能力清单（CAP）：createCapKit 把「REST 端点 + 权限码 + 参数 schema」
// 收敛为声明式条目（q 只读 / act 操作 + method / view 可视化；project 投影脱敏），
// 见 web/src/capkit.ts。
import { createCapKit, capS, capObj } from '@openos/framework'
const caps = createCapKit(os)
export const CAPABILITIES = {
  rooms: {
    queries: [caps.q('vacant', { title: '今日空房', perm: 'room.view',
      path: '/rooms/vacant', desc: '…' })],
  },
}

createOpenOS({
  apps: { rooms: () => import('./apps/RoomsApp.vue') },   // 编译期兜底
  remoteApps: { market: __APP_MARKET_URL__, hostVersion: __VERSION__ }, // App 热发布货架
  login: { mode: 'sso', url: '/api/v1/auth/sso/login' },
  brand: { name: '业务系统', sub: '基于 OpenOS' },
  chrome: { rooms: 'merged' },                            // 融合窗体
  capabilities: CAPABILITIES,
})
```

构建链（三段式，见 openos README「App 独立发布」）：`build:deps`（tooling/deps.ts
+ 宿主 extraEntries）→ `build:apps`（tooling/app.ts 单文件产物）→ 宿主 `vite build`
（tooling/host.ts external + importmap 注入）。宿主工程需自备 `esbuild` devDep
（tooling 强制 postcss CSS 管线依赖它）。

## 环境变量（宿主进程）

| 变量 | 说明 |
|---|---|
| `OPENOS_EXCHANGE_SECRET` | 与 openos 子进程共享的交换密钥（必填） |
| `OPENOS_EXCHANGE_ROLES` | 允许 exchange 的角色白名单（强烈建议） |
| `OPENOS_MARKET_URL` | 宿主货架 catalog.json（App 热发布 + 商店共用） |
| `OPENOS` | 调试：指向本地 openos 工作树绕过快照拉取 |

## CI 提速（P7）

openos tag 流水线会发布 **dist 产物**到 `https://download.gorun.fun/openos/dist/<tag>/`
（多平台 openos-server 二进制 + goder-serve/goder.js/gh + manifest.json sha256 清单）。
宿主 CI 可用 `ci/host-template.yml` 的 `.openos-prebuilt` 片段按 tag 下载并校验，
替代源码编译 openos-server（消掉最重的 modernc sqlite 编译；Dockerfile 相应把
openos 编译阶段换成 `COPY .run/prebuilt/openos-server /app/openos-server`）。
宿主也可 `include: project: share/openos, file: ci/host-template.yml` 复用部署/
货架 job 骨架与缓存 id 约定。
