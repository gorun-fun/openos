// REST API 客户端：自动携带 JWT，统一错误处理，401 全局登出。
// （i18n 的 t() 反向引用于此——i18n 模块保持零依赖避免环，见 src/i18n/index.ts）
import { t } from '../i18n'

const TOKEN_KEY = 'openos_token'

export function getToken(): string { return localStorage.getItem(TOKEN_KEY) || '' }
export function setToken(t: string) { localStorage.setItem(TOKEN_KEY, t) }
export function clearToken() { localStorage.removeItem(TOKEN_KEY) }

/** 取一次性短票（30s 用后即焚，POST /api/auth/ws-ticket，依赖会话 Cookie/令牌）：
 *  /ws 与附件/商店取件等无法携带 header 的 query 鉴权场景用它替代长期 JWT 入 URL
 *  （安全加固第一步：URL/日志不再出现可长期窃取的令牌）。失败时调用方回落 token。 */
export async function fetchWsTicket(): Promise<string> {
  let resp = await fetch('/api/auth/ws-ticket', { method: 'POST' })
  // 401（Cookie 过期而令牌尚可恢复）时先走静默恢复再试一次
  if (resp.status === 401 && await tryTokenRecovery()) {
    resp = await fetch('/api/auth/ws-ticket', { method: 'POST' })
  }
  if (!resp.ok) throw new ApiError(resp.status, '获取短票失败')
  const data = await resp.json()
  return String(data.ticket || '')
}

/** 401 时的全局回调（由 App 层注册：清理状态并回到登录页）。 */
let onUnauthorized: (() => void) | null = null
export function setUnauthorizedHandler(fn: () => void) { onUnauthorized = fn }

/** 401 静默恢复钩子（宿主注册，如 grf-hotel 经宿主 /api/v1/auth/os-token 换发
 *  新桌面令牌）：返回非空令牌 → setToken 后由调用方原地重试原请求，全程不清
 *  状态不跳转；返回空 / 抛错 → 调用方落回 triggerUnauthorized 全局登出。
 *  并发 401 合并为一次恢复（single-flight），未注册时行为与旧版完全一致。 */
type TokenRecovery = () => string | undefined | Promise<string | undefined>
let tokenRecovery: TokenRecovery | null = null
export function setTokenRecovery(fn: TokenRecovery | null) { tokenRecovery = fn }

let recovering: Promise<string | null> | null = null
/** 尝试静默恢复：成功返回新令牌（已 setToken）；失败返回 null——不在这里触发
 *  登出，交由调用方决定（REST 重试 / 总线重连 / 探测各自处理）。 */
export async function tryTokenRecovery(): Promise<string | null> {
  if (!tokenRecovery) return null
  if (!recovering) {
    recovering = (async () => {
      try {
        const t = await tokenRecovery()
        if (t) { setToken(t); return t }
        return null
      } catch { return null }
      finally { setTimeout(() => { recovering = null }, 0) }
    })()
  }
  return recovering
}

/** 触发全局登出（清令牌 + 带 sessionStorage 提示 + 回调）；REST 401 与总线断线
 *  探测共用——提示由登录页读后即清（跨 location.reload / SSO 跳转存活）。 */
export function triggerUnauthorized() {
  try {
    sessionStorage.setItem(AUTH_NOTICE_KEY, t('login.expired'))
  } catch { /* 隐私模式等存储受限时静默降级 */ }
  clearToken()
  onUnauthorized?.()
}

/** 会话失效提示的传递键：总线探测到被踢时写入 sessionStorage，登录页读后即清
 *  （跨 location.reload / SSO 跳转存活，见 bus.ts）。 */
export const AUTH_NOTICE_KEY = 'openos_auth_notice'

export class ApiError extends Error {
  status: number
  constructor(status: number, msg: string) { super(msg); this.status = status }
}

async function handleResp<T>(resp: Response, path: string): Promise<T> {
  if (!resp.ok) {
    let msg = `HTTP ${resp.status}`
    try {
      const data = await resp.json()
      if (data.error) msg = data.error
    } catch { /* ignore */ }
    throw new ApiError(resp.status, msg)
  }
  return resp.json() as Promise<T>
}

/** 401 统一处置：先静默恢复（宿主钩子换新令牌）重试一次；恢复不了才全局登出。
 *  登录接口本身的 401 是密码错误，不在此列。 */
async function handle401<T>(resp: Response, path: string, retry: () => Promise<Response>): Promise<T> {
  if (resp.status !== 401 || path.includes('/api/auth/login')) return handleResp<T>(resp, path)
  if (await tryTokenRecovery()) {
    let again: Response
    try {
      again = await retry()
    } catch {
      throw new ApiError(0, '网络不可达，请检查服务是否运行')
    }
    if (again.status !== 401) return handleResp<T>(again, path)
  }
  triggerUnauthorized()
  throw new ApiError(401, '登录已过期')
}

async function request<T>(method: string, path: string, body?: unknown, headers?: Record<string, string>): Promise<T> {
  const doFetch = (): Promise<Response> => {
    const h: Record<string, string> = { ...headers }
    if (body !== undefined) h['Content-Type'] = 'application/json'
    const t = getToken()
    if (t) h['Authorization'] = 'Bearer ' + t
    return fetch(path, { method, headers: h, body: body === undefined ? undefined : JSON.stringify(body) })
  }
  let resp: Response
  try {
    resp = await doFetch()
  } catch {
    throw new ApiError(0, '网络不可达，请检查服务是否运行')
  }
  return handle401<T>(resp, path, doFetch)
}

export const api = {
  get: <T>(path: string, headers?: Record<string, string>) => request<T>('GET', path, undefined, headers),
  post: <T>(path: string, body?: unknown, headers?: Record<string, string>) => request<T>('POST', path, body, headers),
  put: <T>(path: string, body?: unknown, headers?: Record<string, string>) => request<T>('PUT', path, body, headers),
  del: <T>(path: string) => request<T>('DELETE', path),
  /** 文件上传（multipart，字段名默认 file）：不设 Content-Type，边界由浏览器生成。 */
  upload: async <T>(path: string, file: File, field = 'file'): Promise<T> => {
    const fd = new FormData()
    fd.append(field, file)
    const doFetch = (): Promise<Response> => {
      const h: Record<string, string> = {}
      const t = getToken()
      if (t) h['Authorization'] = 'Bearer ' + t
      return fetch(path, { method: 'POST', headers: h, body: fd })
    }
    let resp: Response
    try {
      resp = await doFetch()
    } catch {
      throw new ApiError(0, '网络不可达，请检查服务是否运行')
    }
    return handle401<T>(resp, path, doFetch)
  },
}

// ---- 类型 ----

export interface SessionUser { username: string; role: string; displayName: string; perms?: string[] }
export interface AppDef {
  id: string
  name: string
  icon: string
  category: string
  builtin: boolean
  locked?: boolean
  hidden?: boolean
  config?: string
  version?: string
  source?: string
  width: number
  height: number
}
export interface VFile { path: string; isDir: boolean; isBinary?: boolean; size: number; updatedAt: string }
/** 我创建的分享（GET /api/fs/shares） */
export interface ShareDef { id: string; path: string; name: string; size: number; createdAt: string; expiresAt: string }

// 带鉴权的原始文件 URL（fetch 用；<img>/<iframe> 需另走 blob）
export function rawFileUrl(path: string): string {
  return `/api/fs/raw?path=${encodeURIComponent(path)}`
}

export async function fetchRawBlob(path: string): Promise<Blob> {
  const resp = await fetch(rawFileUrl(path), { headers: { Authorization: 'Bearer ' + getToken() } })
  if (!resp.ok) throw new ApiError(resp.status, '读取文件失败')
  return resp.blob()
}

export const fsApi = {
  list: (path: string) => api.get<VFile[]>(`/api/fs/list?path=${encodeURIComponent(path)}`),
  read: (path: string) => api.get<{ path: string; content: string; isBinary: boolean }>(`/api/fs/read?path=${encodeURIComponent(path)}`),
  write: (path: string, content: string) => api.put<{ ok: string }>(`/api/fs/write`, { path, content }),
  mkdir: (path: string) => api.post<{ ok: string }>(`/api/fs/mkdir`, { path }),
  touch: (path: string, content = '') => api.post<{ ok: string }>(`/api/fs/touch`, { path, content }),
  remove: (path: string) => api.del<{ ok: string }>(`/api/fs/remove?path=${encodeURIComponent(path)}`),
  rename: (from: string, to: string) => api.post<{ ok: string }>(`/api/fs/rename`, { from, to }),
  usage: () => api.get<{ used: number; quota: number }>(`/api/fs/usage`),
  search: (q: string, limit = 50) => api.get<VFile[]>(`/api/fs/search?q=${encodeURIComponent(q)}&limit=${limit}`),
  copy: (from: string, to: string) => api.post<{ ok: string }>(`/api/fs/copy`, { from, to }),
  // ---- 文件分享链接（跨用户共享；消费端 openShareDialog / #share=<id>）----
  createShare: (path: string, days?: number) =>
    api.post<{ id: string; url: string; expiresAt: string; days: number }>('/api/fs/shares', { path, days }),
  listShares: () => api.get<ShareDef[]>('/api/fs/shares'),
  revokeShare: (id: string) => api.del<{ ok: string }>(`/api/fs/shares/${encodeURIComponent(id)}`),
  shareInfo: (id: string) =>
    api.get<{ path: string; name: string; owner: string; size: number }>(`/api/share/${encodeURIComponent(id)}`),
  saveShare: (id: string) => api.post<{ path: string }>(`/api/share/${encodeURIComponent(id)}/save`),
  upload: async (dir: string, files: File[]): Promise<{ saved: string[] }> => {
    const fd = new FormData()
    fd.append('dir', dir)
    for (const f of files) fd.append('file', f)
    const resp = await fetch('/api/fs/upload', {
      method: 'POST',
      headers: { Authorization: 'Bearer ' + getToken() },
      body: fd,
    })
    if (!resp.ok) {
      let msg = `上传失败 (${resp.status})`
      try { const d = await resp.json(); if (d.error) msg = d.error } catch { /* ignore */ }
      throw new ApiError(resp.status, msg)
    }
    return resp.json()
  },
}

/** 触发浏览器下载（带鉴权拉取后转 blob）。 */
export async function downloadFile(path: string, name?: string) {
  const blob = await fetchRawBlob(path)
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = name || path.split('/').pop() || 'file'
  a.click()
  URL.revokeObjectURL(a.href)
}
