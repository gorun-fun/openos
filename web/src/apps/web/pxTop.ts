// 代理应用的 top 桥：把顶层窗口（OpenOS 桌面页）动态构造的目标站地址重映射回
// 该应用的代理路径。
//
// 背景：部分站点（网易云音乐登录体系等）把登录/风控模块以「脚本注入顶层文档 +
// top.fetch/XHR 发请求」的形式提升到 top 窗口执行——原生形态 top 即站点自身壳页，
// 一切自然同源；代理形态下 top 是 OpenOS 桌面页，这些顶层请求会绕开代理直连目标站：
// 跨域失败、直连缓存拿到未改写的旧脚本（站点按 location.host 做环境检测的兜底
// 逻辑全部失真）、QA 兜底域 403 等问题全数中招。
//
// 桥的规则与 hook.js 的 toProxy 同源语义：绝对 http(s) 地址的主机名命中「已注册
// 代理应用目标域的后缀」（目标 host 末两段，如 music.163.com → 163.com）时，改写
// 为 /app/<id>/<scheme>/<host>/<路径>。后缀匹配是必要放宽——顶层脚本资源常分布在
// 目标域的兄弟子域（st.music.163.com / acstatic-dun.126.net）；不匹配的地址（含
// OpenOS 自身的相对路径请求）一律原样放行，桌面自身行为零扰动。
//
// 与站点适配层的分工：这里是通用机制（所有 proxy 应用生效，按注册表动态匹配），
// 特定站点的行为修正仍在服务端 adapter.go（RewriteJS/Hook）。
import { useApps } from '../../stores/apps'

// 桥的安装幂等标记（同一桌面页只装一次；多窗口共享）
let installed = false

/** 目标 host 的注册域近似（末两段，小写）：music.163.com → 163.com。对 co.uk 类
 *  公共后缀会取宽（uk），只用于「是否可能是某代理应用的资源」的宽松判定，
 *  误命中只是多走一次代理，无安全影响（代理目标仍受 SSRF 防线约束）。 */
function registrable(host: string): string {
  const parts = host.toLowerCase().split('.').filter(Boolean)
  return parts.length >= 2 ? parts.slice(-2).join('.') : host.toLowerCase()
}

interface PxRule { id: string; suffix: string }

/** 当前注册表里的代理应用匹配规则（应用列表很小，逐次现算即可，免维护失效缓存）。 */
function pxRules(): PxRule[] {
  const out: PxRule[] = []
  for (const a of useApps().apps) {
    const c: any = a.config
    if (!c) continue
    const cfg = typeof c === 'string' ? safeParse(c) : c
    if (!cfg || cfg.kind !== 'proxy' || typeof cfg.url !== 'string') continue
    try {
      const host = new URL(cfg.url).hostname
      if (host) out.push({ id: a.id, suffix: registrable(host) })
    } catch { /* 配置地址异常：跳过该应用 */ }
  }
  return out
}

function safeParse(s: string): any { try { return JSON.parse(s) } catch { return null } }

/** 绝对 http(s) 地址 → 代理路径；不命中（相对地址/未知域/非 http）返回 null。
 *  编码与后端 compactTarget 同规则（scheme 段不写 ://，防路径 // 折叠）。 */
function remap(u: string): string | null {
  const m = /^https?:\/\/([^/\s?#]+)([\s\S]*)$/i.exec(u)
  if (!m) return null
  const [, host, rest] = m
  const suffix = registrable(host)
  for (const r of pxRules()) {
    if (suffix === r.suffix) return `/app/${r.id}/${u.replace(/^([a-zA-Z][a-zA-Z0-9+.-]*):\/\//, (_s, sc: string) => sc.toLowerCase() + '/')}`
  }
  return null
}

function remapIfHit(u: unknown): unknown {
  if (typeof u !== 'string' || !u) return u
  const r = remap(u)
  return r || u
}

/** 安装 top 桥（幂等）。包装 fetch / XHR.open 与脚本、链接、图片、iframe 的 URL
 *  反射属性——覆盖站点顶层注入的全部动态构造入口；OpenOS 自身请求均为相对路径
 *  或不命中后缀的绝对地址，不受影响。 */
export function installPxTopBridge() {
  if (installed || typeof window !== 'undefined' && window.top !== window.self) return
  installed = true

  const of = window.fetch
  if (of) {
    window.fetch = function (input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
      try {
        if (typeof input === 'string') input = remapIfHit(input) as string
        else if (input instanceof Request && typeof input.url === 'string') {
          const nu = remapIfHit(input.url)
          if (nu !== input.url) input = new Request(nu as string, input)
        }
      } catch { /* 保持原参数 */ }
      return of.call(window, input as RequestInfo, init)
    }
  }

  const oo = XMLHttpRequest.prototype.open
  XMLHttpRequest.prototype.open = function (this: XMLHttpRequest, method: string, url: string | URL, ...rest: any[]) {
    return (oo as any).call(this, method, remapIfHit(String(url)), ...rest)
  } as any

  // URL 反射属性（script/link/img/iframe 等）：与 hook.js 的 urlProps 同套思路
  const urlProps: [any, string][] = [
    [window.HTMLScriptElement, 'src'], [window.HTMLLinkElement, 'href'],
    [window.HTMLImageElement, 'src'], [window.HTMLIFrameElement, 'src'],
    [window.HTMLAudioElement, 'src'], [window.HTMLVideoElement, 'src'],
  ]
  for (const [C, prop] of urlProps) {
    if (!C?.prototype) continue
    let d
    try { d = Object.getOwnPropertyDescriptor(C.prototype, prop) } catch { continue }
    if (!d?.set || !d.get) continue
    try {
      Object.defineProperty(C.prototype, prop, {
        configurable: true,
        get() { return d.get!.call(this) },
        set(v: any) { d.set!.call(this, remapIfHit(String(v)) as any) },
      })
    } catch { /* 属性不可改写时跳过 */ }
  }
}
