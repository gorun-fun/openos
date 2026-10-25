// 会话仓库：登录 / 登出 / 当前用户 + 令牌滑动续期（半程定时 + 回前台兜底）。
import { defineStore } from 'pinia'
import { bus } from '../bus/bus'
import { api, getToken, setToken, clearToken, type SessionUser } from '../api/client'

// 默认令牌有效期 24h：每 12h（半程）静默续期；页面回到前台时兜底刷新（间隔 ≥1h），
// 覆盖「桌面挂机过夜 / 系统休眠」场景，避免活跃用户被 24h 硬过期打断。
// 服务端可经 OPENOS_TOKEN_TTL 配置其它有效期（登录/续期响应带 tokenTtl），
// 此时按实际 TTL 的一半自适应（钳在 [5 分钟, 12h]），短 TTL 令牌不会在
// 定时器到点前过期。
const DEFAULT_REFRESH_INTERVAL = 12 * 60 * 60 * 1000
const REFRESH_MIN_GAP = 60 * 60 * 1000
const REFRESH_MIN_INTERVAL = 5 * 60 * 1000

let refreshInterval = DEFAULT_REFRESH_INTERVAL
let refreshTimer: ReturnType<typeof setInterval> | null = null
let lastRefresh = 0

function onVisible() {
  if (document.visibilityState !== 'visible') return
  if (Date.now() - lastRefresh < REFRESH_MIN_GAP) return
  void useSession().refreshToken().catch(() => {})
}

function startAutoRefresh() {
  stopAutoRefresh()
  refreshTimer = setInterval(() => { void useSession().refreshToken().catch(() => {}) }, refreshInterval)
  document.addEventListener('visibilitychange', onVisible)
}

function stopAutoRefresh() {
  if (refreshTimer) { clearInterval(refreshTimer); refreshTimer = null }
  document.removeEventListener('visibilitychange', onVisible)
}

/** 按服务端告知的令牌 TTL 自适应续期间隔（半程，钳在 [5min, 12h]）。 */
function applyTokenTtl(ttlSec?: number) {
  if (!ttlSec || ttlSec <= 0) return
  const half = (ttlSec / 2) * 1000
  const next = Math.max(REFRESH_MIN_INTERVAL, Math.min(half, DEFAULT_REFRESH_INTERVAL))
  if (Math.abs(next - refreshInterval) < 60 * 1000) return
  refreshInterval = next
  if (refreshTimer) startAutoRefresh() // 已在续期循环中：按新间隔重建定时器
}

// 服务端身份变化推送（sys:user，PublishTo 定向直达本用户全部连接）：角色/权限/
// 显示名调整就地回正会话信息——授权变化实时生效、不再 401 掉线重登（角色变化
// 另有 sys:apps-changed 广播刷新应用列表）。禁用态不在此更新（由 401/总线复核
// 踢线接管）。
let identityHooked = false
function hookIdentityPush() {
  if (identityHooked) return
  identityHooked = true
  bus.on('sys:user', (p: any) => {
    const s = useSession()
    if (!p || !s.user || p.username !== s.user.username || p.disabled) return
    s.user = {
      username: p.username,
      role: p.role ?? s.user.role,
      displayName: p.displayName ?? s.user.displayName,
      perms: Array.isArray(p.perms) ? p.perms : [],
    }
  })
}

interface State {
  user: SessionUser | null
  ready: boolean
}

export const useSession = defineStore('session', {
  state: (): State => ({ user: null, ready: false }),
  actions: {
    async restore() {
      hookIdentityPush()
      if (!getToken()) { this.ready = true; return }
      try {
        this.user = await api.get<SessionUser>('/api/auth/me')
        await api.get('/api/auth/ready') // 确保 home 目录
        // 滑动续期：刷新本地令牌并续种 Cookie（goder 等嵌入组件依赖 Cookie）
        await this.refreshToken().catch(() => {})
        startAutoRefresh()
      } catch {
        clearToken()
      }
      this.ready = true
    },
    async login(username: string, password: string) {
      const data = await api.post<{ token: string; tokenTtl?: number; user: SessionUser }>('/api/auth/login', { username, password })
      setToken(data.token)
      this.user = data.user
      applyTokenTtl(data.tokenTtl)
      hookIdentityPush()
      await api.get('/api/auth/ready')
      startAutoRefresh()
    },
    /** 换取新令牌（默认 24h 有效，服务端 OPENOS_TOKEN_TTL 可配）；失败抛出，
     *  401 由全局处理器接管登出。 */
    async refreshToken() {
      const d = await api.get<{ token: string; tokenTtl?: number; user: SessionUser }>('/api/auth/refresh')
      if (d?.token) {
        setToken(d.token)
        applyTokenTtl(d.tokenTtl)
        lastRefresh = Date.now()
      }
    },
    logout() {
      stopAutoRefresh()
      clearToken()
      this.user = null
      // 同步清除 goder 等嵌入组件使用的 Cookie
      fetch('/api/auth/logout', { method: 'POST' }).catch(() => {})
    },
  },
})
