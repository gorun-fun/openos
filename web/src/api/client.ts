// REST API 客户端：自动携带 JWT，统一错误处理，401 全局登出。
const TOKEN_KEY = 'openos_token'

export function getToken(): string { return localStorage.getItem(TOKEN_KEY) || '' }
export function setToken(t: string) { localStorage.setItem(TOKEN_KEY, t) }
export function clearToken() { localStorage.removeItem(TOKEN_KEY) }

/** 401 时的全局回调（由 App 层注册：清理状态并回到登录页）。 */
let onUnauthorized: (() => void) | null = null
export function setUnauthorizedHandler(fn: () => void) { onUnauthorized = fn }

export class ApiError extends Error {
  status: number
  constructor(status: number, msg: string) { super(msg); this.status = status }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const t = getToken()
  if (t) headers['Authorization'] = 'Bearer ' + t
  let resp: Response
  try {
    resp = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
  } catch {
    throw new ApiError(0, '网络不可达，请检查服务是否运行')
  }
  if (resp.status === 401) {
    // 登录过期 / 凭证被吊销：清理并回到登录页（登录接口本身除外）
    if (!path.includes('/api/auth/login')) {
      clearToken()
      onUnauthorized?.()
      throw new ApiError(401, '登录已过期')
    }
  }
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

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
}

// ---- 类型 ----

export interface SessionUser { username: string; role: string; displayName: string }
export interface AppDef {
  id: string
  name: string
  icon: string
  category: string
  builtin: boolean
  hidden?: boolean
  config?: string
  width: number
  height: number
}
export interface VFile { path: string; isDir: boolean; isBinary?: boolean; size: number; updatedAt: string }

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
