// host.ts — 宿主业务 API 客户端工厂（@openos/host 消费入口）。
//
// 宿主组壳形态下前端有两套后端：openos 子进程（openos 自己的 api client 已覆盖）
// 与宿主业务后端（/api/v1/*，令牌由宿主 SSO 签发）。本模块给后者一个与 openos
// api 同款使用习惯的 fetch 封装，并吸收两宿主（grf-hotel/grf-os）各自踩坑迭代出
// 的完整会话自愈语义（原两份 ~170 行同源分叉副本收敛于此）：
//
//   - 令牌内存缓存：权威值在内存，localStorage 仅作 reload 间持久化——webproxy
//     代理应用与宿主同源，其页面脚本（美团 ebooking 风控 SDK 等）会
//     localStorage.clear() 连带清空宿主会话键；重载后由 refresh Cookie
//     （HttpOnly，清不掉）复活。
//   - 401 先静默续期（single-flight POST refresh，Bearer 滑动 / Cookie 复活双
//     通道）并重试原请求一次；无令牌同样先走一次续期（凭 HttpOnly Cookie 复活，
//     2026-10 生产「切店后多应用未登录」事故根因即代理脚本清空宿主存储）。
//   - 续期被拒（401/403 = 无有效凭证）且原有旧令牌 → 整页跳 SSO：跳转期间请求
//     挂起而非抛错（App 不渲染误导性「未登录」），4s 后兜底强刷防导航被拦死挂；
//     无令牌（首访访客）→ 抛「未登录」交登录页/访客浮层渲染，避免与 SSO 回落
//     成环。
//   - 暂时性故障（网络断/服务端 5xx）只报可重试错误、绝不踢人。
//   - 6h 定时保活 + 回前台（≥1h）兜底，覆盖挂机过夜/系统休眠。
//   - 续期响应带 openos_token 时自动同步给 openos 自身 api client（@openos/api
//     同源 importmap 单实例，安全）。
//
// 用法（宿主 os.ts 全文只剩差异配置）：
//   import { createHostApi } from '@openos/host'
//   export const hotel = createHostApi({
//     tokenKey: 'token', base: '/api/v1',
//     headers: () => { const pid = actingPid(); return pid ? { 'X-Property-Id': pid } : {} },
//     session: { loginUrl: '/api/v1/auth/sso/login' },
//   })
//   hotel.get('/me')   // hotel.renew() 可手动触发保活
import { setToken as setOsToken } from './api/client'

export interface HostApiOptions {
  /** 宿主令牌的 localStorage 键名（默认 'token'） */
  tokenKey?: string
  /** API 前缀（默认 '/api/v1'） */
  base?: string
  /** 动态附加请求头（每请求实时取值，如门店上下文 X-Property-Id） */
  headers?: () => Record<string, string>
  /** 401 自愈（静默续期 + SSO 跳转 + 定时保活）；不配置则退化为简单行为 */
  session?: HostSessionOptions
  /** 简单形态（无 session 配置）的 401 处理（不设则仅清令牌） */
  onUnauthorized?: () => void
}

export interface HostSessionOptions {
  /** 续期端点路径（相对 base；默认 '/auth/refresh'）。响应体约定
   *  {code?, msg?, data:{token, openos_token?}}。 */
  refreshPath?: string
  /** 凭证不可续期时的整页登录入口（SSO）。 */
  loginUrl: string
  /** 续期成功回调（新令牌已先行落位后调用，携带续期响应 data）。 */
  onRenewed?: (data: any) => void
  /** 定时保活间隔（默认 6h；TTL 常见 12/24h 下留足余量） */
  keepAliveMs?: number
  /** 回前台兜底的最小间隔（默认 1h，防频繁切窗打爆续期端点） */
  keepAliveMinGapMs?: number
}

export class HostApiError extends Error {
  status: number
  constructor(status: number, msg: string) {
    super(msg)
    this.status = status
  }
}

/** 续期结果：ok=已换新双令牌；rejected=服务端明确拒绝（无有效凭证，须回 SSO）；
 *  error=暂时性失败（网络断/服务端 5xx，稍后可重试，绝不踢人）。 */
export type RenewResult = 'ok' | 'rejected' | 'error'

export interface HostApi {
  get<T>(path: string): Promise<T>
  post<T>(path: string, body?: unknown): Promise<T>
  put<T>(path: string, body?: unknown): Promise<T>
  del<T>(path: string): Promise<T>
  upload<T>(path: string, file: File, field?: string): Promise<T>
  getToken(): string
  setToken(t: string): void
  clearToken(): void
  /** 手动触发一轮静默续期（JoinOverlay 类「审批通过后换发双令牌」轮询之外的
   *  保活入口；并发自动 single-flight 合并）。 */
  renew(): Promise<RenewResult>
}

export function createHostApi(opts: HostApiOptions = {}): HostApi {
  const tokenKey = opts.tokenKey || 'token'
  const base = (opts.base ?? '/api/v1').replace(/\/$/, '')
  const sess = opts.session
  const refreshPath = sess?.refreshPath || '/auth/refresh'

  // ---- 令牌存取（内存缓存权威，localStorage 仅作 reload 间持久化） ----
  let cachedToken: string | null = null
  const getToken = (): string => {
    if (cachedToken === null) cachedToken = localStorage.getItem(tokenKey) || ''
    return cachedToken
  }
  const setToken = (t: string) => {
    cachedToken = t
    localStorage.setItem(tokenKey, t)
  }
  const clearToken = () => {
    cachedToken = ''
    localStorage.removeItem(tokenKey)
  }

  // ---- 会话续期（401 自愈 + 定时保活；仅 session 配置形态启用） ----

  let renewing: Promise<RenewResult> | null = null
  let lastRenew = 0

  async function renewOnce(): Promise<RenewResult> {
    try {
      const t = getToken()
      const resp = await fetch(base + refreshPath, {
        method: 'POST',
        headers: t ? { Authorization: `Bearer ${t}` } : {},
      })
      // 401/403 = 凭证确实不可续期（过期且无有效 refresh Cookie / 身份被拒）
      if (resp.status === 401 || resp.status === 403) return 'rejected'
      if (!resp.ok) return 'error'
      const data = await resp.json().catch(() => null)
      const tok = data?.data?.token
      if (!tok) return 'error'
      setToken(tok)
      if (data.data.openos_token) setOsToken(data.data.openos_token)
      lastRenew = Date.now()
      sess?.onRenewed?.(data.data)
      return 'ok'
    } catch {
      return 'error'
    }
  }

  function renew(): Promise<RenewResult> {
    if (!renewing) {
      renewing = renewOnce().finally(() => {
        setTimeout(() => { renewing = null }, 0)
      })
    }
    return renewing
  }

  // 已触发 SSO 跳转后挂起后续请求：跳转窗口（Casdoor 静默换发约 1~2s）内页面仍
  // 存活，若继续发请求/抛错，App 会渲染出误导性的「未登录」错误；整页跳转完成
  // 后模块态自然复位。若导航因故未发生（被拦截/极端竞态），4s 后强制整页刷新
  // 自愈，杜绝请求永久悬挂。
  let redirecting = false
  function gotoLogin() {
    redirecting = true
    // 注意：不清旧令牌——清掉后一旦导航被打断，页面就处于「空令牌」死态（业务
    // 请求全部抛「未登录」）；旧令牌无效但无害，登录引导页会整体覆盖
    console.warn('[session] 会话不可续期，跳转统一身份重新认证')
    location.replace(sess!.loginUrl)
    setTimeout(() => { location.reload() }, 4000)
  }
  const suspended = <T>(): Promise<T> => new Promise(() => {})

  /** 401 统一处置：有旧令牌先静默续期重试一次；被拒回 SSO（挂起不抛错）；无
   * 令牌也先走一次续期（Cookie 复活通道）再落「未登录」——与简单形态共用。 */
  async function handle401<T>(hadToken: boolean, retried: boolean, retry: () => Promise<T>): Promise<T> {
    if (!sess) {
      // 简单形态（未配置 session）：清令牌 + 调用方钩子
      clearToken()
      opts.onUnauthorized?.()
      throw new HostApiError(401, '登录已过期')
    }
    if (!retried) {
      const r = await renew()
      if (r === 'ok') return retry()
      if (r === 'error') {
        // 暂时性故障（网络/服务端 5xx）：原地报可重试错误，不清令牌不跳转
        throw new HostApiError(401, '会话续期中，请稍后重试')
      }
      // rejected：凭证确实不可续期。有旧令牌（过期）→ 回 SSO；无令牌（首访
      // 访客）→ 抛「未登录」，交由登录页/访客浮层渲染，避免与 SSO 回落成环
      if (hadToken) {
        gotoLogin()
        return suspended<T>()
      }
      throw new HostApiError(401, '未登录')
    }
    if (hadToken) {
      gotoLogin()
      return suspended<T>()
    }
    throw new HostApiError(401, '未登录')
  }

  // 定时保活 + 回前台兜底：模块加载即启动（本工厂产物经宿主 core 桶全站单份）
  if (sess) {
    const interval = sess.keepAliveMs ?? 6 * 60 * 60 * 1000
    const minGap = sess.keepAliveMinGapMs ?? 60 * 60 * 1000
    window.setInterval(() => { if (getToken()) void renew() }, interval)
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState !== 'visible') return
      if (!getToken() || Date.now() - lastRenew < minGap) return
      void renew()
    })
  }

  // 登录接口本身的 401（密码错误）不当会话失效——简单形态在 request 内按路径
  // 放行原样抛出；session 形态的续期走原生 fetch 不经 request，无此环。

  async function request<T>(method: string, path: string, body?: unknown, retried = false): Promise<T> {
    // SSO 跳转已在进行：挂起，不再发请求也不抛错（跳转完成即整页复位）
    if (redirecting) return suspended<T>()
    const headers: Record<string, string> = { ...opts.headers?.() }
    if (body !== undefined) headers['Content-Type'] = 'application/json'
    const t = getToken()
    if (t) headers.Authorization = `Bearer ${t}`
    let resp: Response
    try {
      resp = await fetch(base + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
    } catch {
      throw new HostApiError(0, '网络不可达，请检查服务是否运行')
    }
    if (resp.status === 401) {
      if (!sess && path.includes('/auth/')) {
        // 简单形态登录端点的 401：原样抛出，不触发登出钩子
        throw new HostApiError(401, '登录已过期')
      }
      return handle401<T>(!!t, retried, () => request<T>(method, path, body, true))
    }
    const data = await resp.json().catch(() => null)
    if (!resp.ok || (data && data.code !== undefined && data.code !== 0)) {
      throw new HostApiError(resp.status, data?.msg || data?.error || `HTTP ${resp.status}`)
    }
    return (data?.data ?? data) as T
  }

  /** 文件上传（multipart；不设 Content-Type，边界由浏览器生成）。响应体按
   *  代理上游形态原样返回（{error} 错误体兼容），不走 {code,data} 解包。 */
  async function uploadReq<T>(p: string, file: File, field: string, retried: boolean): Promise<T> {
    if (redirecting) return suspended<T>()
    const fd = new FormData()
    fd.append(field, file)
    const headers: Record<string, string> = { ...opts.headers?.() }
    const t = getToken()
    if (t) headers.Authorization = `Bearer ${t}`
    let resp: Response
    try {
      resp = await fetch(base + p, { method: 'POST', headers, body: fd })
    } catch {
      throw new HostApiError(0, '网络不可达，请检查服务是否运行')
    }
    if (resp.status === 401) {
      return handle401<T>(!!t, retried, () => uploadReq<T>(p, file, field, true))
    }
    const data = await resp.json().catch(() => null)
    if (!resp.ok) throw new HostApiError(resp.status, data?.msg || data?.error || `HTTP ${resp.status}`)
    return data as T
  }

  const api: HostApi = {
    get: <T>(p: string) => request<T>('GET', p),
    post: <T>(p: string, body?: unknown) => request<T>('POST', p, body),
    put: <T>(p: string, body?: unknown) => request<T>('PUT', p, body),
    del: <T>(p: string) => request<T>('DELETE', p),
    upload: <T>(p: string, file: File, field = 'file') => uploadReq<T>(p, file, field, false),
    getToken,
    setToken,
    clearToken,
    renew: () => (sess ? renew() : Promise.resolve<'rejected'>('rejected')),
  }
  return api
}
