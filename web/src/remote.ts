// remote.ts — 远程 App 货架加载器（宿主 App 独立发布通道的运行时）。
//
// 形态：宿主在 createOpenOS({ apps, remoteApps }) 里声明货架地址后，每个 App 的
// 组件优先从货架远程加载（版本化 ESM 产物，随构建工具 web/tooling/app.ts 产出），
// 任何环节失败（货架不可达 / 无条目 / 版本不兼容 / import 抛错）都回落到 apps
// 传入的本地 loader（打进宿主 dist 的兜底 chunk）——货架故障不影响可用性，只是
// 降级为宿主发版时冻结的版本。
//
// 单实例保证：远程 App 产物内的裸模块导入（vue、@openos/ui…）由宿主页面
// index.html 的 importmap 解析回宿主 deps 库集（web/tooling/deps.ts 产出），
// 页面内所有模块共享同一份 Vue/pinia 实例，与本地编译的 App 无差异。
//
// 货架协议（catalog.json，由 web/tooling/catalog.mjs 生成/合并）：
//   {
//     "version": 1,                       // 协议格式版本
//     "updatedAt": "2026-10-04T…",        // 最后发布时间（展示用）
//     "apps": {
//       "rooms": {
//         "version": "1.2.0",             // 该 App 版本（回滚=改 entry 指回旧目录）
//         "entry": "files/rooms/1.2.0/rooms.js",  // 相对货架根，版本化路径可 immutable 缓存
//         "hash": "sha256:…",             // 产物摘要（审计用；运行期不校验）
//         "minHostVersion": "v0.0.21"     // 最低宿主版本：低于则不用该条目（回落本地）
//       }
//     }
//   }
//
// 部署前提（货架静态服务）：catalog.json 允许跨源（CORS）且短缓存；files/ 长缓存
// immutable（版本化 URL 天然可缓存）。fetch catalog 每页面会话仅一次（F5 即刷新）。

/** 货架声明（createOpenOS 的 remoteApps 选项）。 */
export interface RemoteAppsOptions {
  /** 货架根 URL。跨源 https 地址（如 https://…/hotel/market）或本地同源路径（/market） */
  market: string
  /**
   * 宿主版本（用于条目 minHostVersion 兼容比较）。缺省不比较——货架条目全部放行，
   * 兼容性由发布纪律保证（生产宿主应注入构建版本）。
   */
  hostVersion?: string
}

/** 货架 catalog 的单个 App 条目（协议见文件头）。 */
export interface MarketEntry {
  version: string
  entry: string
  hash?: string
  minHostVersion?: string
}

/** 货架 catalog 文档。 */
export interface MarketCatalog {
  version: number
  updatedAt?: string
  apps: Record<string, MarketEntry>
}

// catalog 会话级缓存：按货架地址键控，失败也缓存（null）——同页面会话不打重试风暴，
// 刷新页面自然重取。ESM 产物经浏览器 HTTP 缓存（版本化 URL immutable），不走此缓存。
const catalogCache = new Map<string, Promise<MarketCatalog | null>>()

function catalogOf(market: string): Promise<MarketCatalog | null> {
  let p = catalogCache.get(market)
  if (!p) {
    p = fetch(market.replace(/\/$/, '') + '/catalog.json', { cache: 'no-store' })
      .then(r => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((doc: MarketCatalog) => (doc && doc.apps ? doc : null))
      .catch(err => {
        console.warn(`[OpenOS] App 货架不可达，全部应用回落本地构建: ${err}`)
        return null
      })
    catalogCache.set(market, p)
  }
  return p
}

/**
 * 比较两个版本号（返回 -1/0/1）：容忍可选的 v 前缀与 git describe 后缀
 * （-dirty / -N-g<hash> 一律剥掉，只比较点分数值段；缺省段按 0 计）。
 * 非常规字符串按 NaN 段归 0 处理，保证任意输入不抛错。
 */
export function compareVersions(a: string, b: string): number {
  const seg = (v: string): number[] =>
    v.trim().replace(/^v/i, '').split('-')[0].split('.').map(s => parseInt(s, 10) || 0)
  const x = seg(a), y = seg(b)
  const n = Math.max(x.length, y.length)
  for (let i = 0; i < n; i++) {
    const d = (x[i] || 0) - (y[i] || 0)
    if (d) return d > 0 ? 1 : -1
  }
  return 0
}

/** 拼货架 URL（market 去尾斜杠 + 相对 entry）。 */
function joinURL(market: string, entry: string): string {
  return market.replace(/\/$/, '') + '/' + entry.replace(/^\//, '')
}

/**
 * 加载单个 App：货架条目兼容则动态 import 远程产物，否则回落本地 loader。
 * 返回 Promise<ESM 模块>（default 导出为组件定义），与 vite 本地 import() 形态
 * 一致，可直接作为 defineAsyncComponent 的 loader 使用。
 */
export async function loadApp(
  id: string,
  opts: RemoteAppsOptions,
  fallback: () => Promise<any>,
): Promise<any> {
  // 开发态（vite serve，宿主源码直引）没有 importmap：远程产物的裸导入
  // （vue/@openos/*）无法解析，直接走本地 loader
  if (import.meta.env?.DEV) return fallback()
  const cat = await catalogOf(opts.market)
  const e = cat?.apps?.[id]
  if (!e || !e.entry) return fallback()
  if (e.minHostVersion && opts.hostVersion && compareVersions(opts.hostVersion, e.minHostVersion) < 0) {
    console.info(`[OpenOS] 货架应用 ${id}@${e.version} 要求宿主 ≥ ${e.minHostVersion}（当前 ${opts.hostVersion}），使用本地构建`)
    return fallback()
  }
  try {
    return await import(/* @vite-ignore */ joinURL(opts.market, e.entry))
  } catch (err) {
    console.warn(`[OpenOS] 货架应用 ${id}@${e.version} 加载失败，回落本地构建:`, err)
    return fallback()
  }
}
