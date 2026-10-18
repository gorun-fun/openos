// capkit.ts — 宿主能力清单声明式构建件（CAP L2 宿主声明面的收敛工具）。
//
// openos 规范（desktop/capabilities.ts）：宿主在 createOpenOS({ capabilities })
// 声明各应用的能力清单，goder 经 osmcp 通用工具发现与执行——os_caps 查清单 /
// os_app_query 只读取数 / os_app_call 业务操作 / os_app_ui 可视化引导。
// 大多数宿主能力本质是「一条 REST 端点 + 权限码 + 参数 schema + 可视化入口」，
// 逐条手写 CapDef run 回调（fetch/路径拼装/守卫调用）是纯样板——本 kit 把它
// 收敛为声明式工厂（自 grf-hotel capabilities.ts 提炼，附 project 投影位承接
// grf-os 的脱敏/截断场景）：
//
//   const caps = createCapKit(hotel, { guard: platformAdminOnly })
//   queries: [caps.q('overview', { title: '经营总览', perm: 'room.view',
//                 path: '/dashboard', desc: '…', result: '{rooms,…}' })]
//   actions: [caps.act('kick', { title: '踢出房间', path: '/rooms/{id}/kick',
//                 method: 'POST', perm: 'room.edit', guard: requireAdmin,
//                 project: r => ({ ok: r.ok }) })]
//   views:   [caps.view('rooms', '房间列表', { view: 'rooms' }, '打开房间页')]
//
// 语义：路径模板 {id} 具名参数替换；GET 余参进 query string（fixed 为固定过滤
// 参数）、写方法余参作 JSON body；project 对响应做投影/脱敏/截断后返回（不设
// 原样透传）。run 回调走宿主自己的 API 客户端（登录态/门店头/401 自愈由客户端
// 承担）；perm 是 UX 门控，权威鉴权恒在宿主后端。
import type { CapDef, CapViewDef } from './desktop/capabilities'

export type CapArgs = Record<string, any>

// ---- Schema 助手（JSON Schema 最小子集） ----

export const capS = (description: string) => ({ type: 'string', description })
export const capN = (description: string) => ({ type: 'number', description })
export const capB = (description: string) => ({ type: 'boolean', description })
export const capA = (description: string) => ({ type: 'array', description })
export const capObj = (properties: CapArgs, required: string[] = []) =>
  ({ type: 'object', properties, required })

/** 路径模板 {id} 替换：具名参数进路径，剩余参数待分流。 */
export function capSplit(tpl: string, args: CapArgs): { path: string; rest: CapArgs } {
  const rest: CapArgs = { ...(args || {}) }
  const path = tpl.replace(/\{(\w+)\}/g, (_, k) => {
    const v = rest[k]
    delete rest[k]
    return v == null ? '' : String(v)
  })
  return { path, rest }
}

export function capQs(rest: CapArgs): string {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(rest)) if (v != null && v !== '') p.set(k, String(v))
  const s = p.toString()
  return s ? `?${s}` : ''
}

/** kit 绑定的宿主 API 客户端面（grf/hotel 等客户端天然满足）。 */
export interface CapKitClient {
  get<T = any>(path: string): Promise<T>
  post<T = any>(path: string, body?: unknown): Promise<T>
  put<T = any>(path: string, body?: unknown): Promise<T>
  del<T = any>(path: string): Promise<T>
}

export interface CapKitOptions {
  /** 全局前置守卫：每条 q/act 执行前调用（如平台超管域校验）。 */
  guard?: () => void
}

export interface CapQuerySpec {
  title: string
  desc: string
  path: string
  perm?: string
  params?: any
  result?: string
  /** 本条专属守卫（叠加在全局 guard 之上）。 */
  guard?: () => void
  /** 固定过滤参数（拼 query string，声明处写死）。 */
  fixed?: CapArgs
  /** 响应投影/脱敏/截断；不设原样透传。 */
  project?: (resp: any) => any
}

export interface CapActSpec {
  title: string
  desc: string
  path: string
  perm?: string
  params?: any
  result?: string
  method?: 'POST' | 'PUT' | 'DELETE' | 'GET'
  guard?: () => void
  project?: (resp: any) => any
}

export interface CapKit {
  /** 只读查询：GET（路径参数替换后其余进 query string）。 */
  q(name: string, o: CapQuerySpec): CapDef
  /** 业务操作：method 缺省 POST（路径参数替换后其余作 JSON body）。 */
  act(name: string, o: CapActSpec): CapDef
  /** 可视化入口：props 直传应用（tab 切初始页签 / view 切视图 / 业务 id 开详情窗）。 */
  view(name: string, title: string, props: CapArgs, desc: string, anchor?: string, steps?: string[]): CapViewDef
}

export function createCapKit(client: CapKitClient, opts: CapKitOptions = {}): CapKit {
  const run = (o: { guard?: () => void; project?: (resp: any) => any }, fn: (args: CapArgs) => Promise<any>) => async (args: CapArgs) => {
    opts.guard?.()
    o.guard?.()
    const r = await fn(args)
    return o.project ? o.project(r) : r
  }
  return {
    q: (name, o) => ({
      name, title: o.title, description: o.desc, perm: o.perm, params: o.params, result: o.result,
      run: run(o, (args) => {
        const { path, rest } = capSplit(o.path, args)
        return client.get(path + capQs({ ...(o.fixed || {}), ...rest }))
      }),
    }),
    act: (name, o) => ({
      name, title: o.title, description: o.desc, perm: o.perm, params: o.params, result: o.result,
      run: run(o, (args) => {
        const { path, rest } = capSplit(o.path, args)
        const m = o.method || 'POST'
        if (m === 'DELETE') return client.del(path)
        if (m === 'GET') return client.get(path + capQs(rest))
        const body: CapArgs = {}
        for (const [k, v] of Object.entries(rest)) if (v !== undefined) body[k] = v
        return m === 'PUT' ? client.put(path, body) : client.post(path, body)
      }),
    }),
    view: (name, title, props, desc, anchor, steps) =>
      ({ name, title, description: desc, props, anchor, steps }),
  }
}
