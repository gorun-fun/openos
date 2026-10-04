// host.ts — 宿主业务 API 客户端工厂（@openos/host 消费入口）。
//
// 宿主组壳形态下前端有两套后端：openos 子进程（openos 自己的 api client 已覆盖）
// 与宿主业务后端（/api/v1/*，令牌由宿主 SSO 签发）。本模块给后者一个与 openos
// api 同款使用习惯的 fetch 封装：自动携带 Bearer、统一错误、401 自愈钩子。
//
// 用法（宿主 main.ts）：
//   import { createHostApi } from '@openos/host'
//   export const os = createHostApi({
//     tokenKey: 'token',                       // 宿主令牌 localStorage 键
//     base: '/api/v1',
//     onUnauthorized() { location.href = '/api/v1/auth/sso/login' },
//   })
//   os.get('/me')
//   os.upload('/avatar', file)
export interface HostApiOptions {
  /** 宿主令牌的 localStorage 键名（默认 'token'） */
  tokenKey?: string
  /** API 前缀（默认 '/api/v1'） */
  base?: string
  /** 401 自愈：跳 SSO / 刷新令牌 / 登出等（不设则仅清令牌） */
  onUnauthorized?: () => void
  /** 附加默认请求头（如门店上下文 X-Property-Id） */
  headers?: Record<string, string>
}

export class HostApiError extends Error {
  status: number
  constructor(status: number, msg: string) {
    super(msg)
    this.status = status
  }
}

export interface HostApi {
  get<T>(path: string, headers?: Record<string, string>): Promise<T>
  post<T>(path: string, body?: unknown, headers?: Record<string, string>): Promise<T>
  put<T>(path: string, body?: unknown, headers?: Record<string, string>): Promise<T>
  del<T>(path: string): Promise<T>
  upload<T>(path: string, file: File, field?: string): Promise<T>
  getToken(): string
  setToken(t: string): void
  clearToken(): void
}

export function createHostApi(opts: HostApiOptions = {}): HostApi {
  const tokenKey = opts.tokenKey || 'token'
  const base = (opts.base ?? '/api/v1').replace(/\/$/, '')
  const getToken = () => localStorage.getItem(tokenKey) || ''
  const unauthorized = () => {
    localStorage.removeItem(tokenKey)
    opts.onUnauthorized?.()
  }

  async function handleResp<T>(resp: Response, path: string): Promise<T> {
    if (resp.status === 401) {
      // 登录接口本身的 401（密码错误）不当会话失效
      if (!path.includes('/auth/')) unauthorized()
      throw new HostApiError(401, '登录已过期')
    }
    if (!resp.ok) {
      let msg = `HTTP ${resp.status}`
      try {
        const data = await resp.json()
        if (data && data.error) msg = String(data.error)
      } catch { /* 非 JSON 错误体 */ }
      throw new HostApiError(resp.status, msg)
    }
    return resp.json() as Promise<T>
  }

  async function request<T>(method: string, path: string, body?: unknown, headers?: Record<string, string>): Promise<T> {
    const h: Record<string, string> = { ...opts.headers, ...headers }
    if (body !== undefined) h['Content-Type'] = 'application/json'
    const t = getToken()
    if (t) h['Authorization'] = 'Bearer ' + t
    let resp: Response
    try {
      resp = await fetch(base + path, { method, headers: h, body: body === undefined ? undefined : JSON.stringify(body) })
    } catch {
      throw new HostApiError(0, '网络不可达，请检查服务是否运行')
    }
    return handleResp<T>(resp, path)
  }

  const api: HostApi = {
    get: <T>(p: string, headers?: Record<string, string>) => request<T>('GET', p, undefined, headers),
    post: <T>(p: string, body?: unknown, headers?: Record<string, string>) => request<T>('POST', p, body, headers),
    put: <T>(p: string, body?: unknown, headers?: Record<string, string>) => request<T>('PUT', p, body, headers),
    del: <T>(p: string) => request<T>('DELETE', p),
    /** 文件上传（multipart；不设 Content-Type，边界由浏览器生成） */
    upload: async <T>(p: string, file: File, field = 'file'): Promise<T> => {
      const fd = new FormData()
      fd.append(field, file)
      const h: Record<string, string> = { ...opts.headers }
      const t = getToken()
      if (t) h['Authorization'] = 'Bearer ' + t
      let resp: Response
      try {
        resp = await fetch(base + p, { method: 'POST', headers: h, body: fd })
      } catch {
        throw new HostApiError(0, '网络不可达，请检查服务是否运行')
      }
      return handleResp<T>(resp, p)
    },
    getToken,
    setToken: (t: string) => localStorage.setItem(tokenKey, t),
    clearToken: () => localStorage.removeItem(tokenKey),
  }
  return api
}
