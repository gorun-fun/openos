// 会话仓库：登录 / 登出 / 当前用户 + 令牌滑动续期（半程定时 + 回前台兜底）。
import { defineStore } from 'pinia'
import { api, getToken, setToken, clearToken, type SessionUser } from '../api/client'

// 令牌有效期 24h：每 12h（半程）静默续期；页面回到前台时兜底刷新（间隔 ≥1h），
// 覆盖「桌面挂机过夜 / 系统休眠」场景，避免活跃用户被 24h 硬过期打断。
const REFRESH_INTERVAL = 12 * 60 * 60 * 1000
const REFRESH_MIN_GAP = 60 * 60 * 1000

let refreshTimer: ReturnType<typeof setInterval> | null = null
let lastRefresh = 0

function onVisible() {
  if (document.visibilityState !== 'visible') return
  if (Date.now() - lastRefresh < REFRESH_MIN_GAP) return
  void useSession().refreshToken().catch(() => {})
}

function startAutoRefresh() {
  if (refreshTimer) return
  refreshTimer = setInterval(() => { void useSession().refreshToken().catch(() => {}) }, REFRESH_INTERVAL)
  document.addEventListener('visibilitychange', onVisible)
}

function stopAutoRefresh() {
  if (refreshTimer) { clearInterval(refreshTimer); refreshTimer = null }
  document.removeEventListener('visibilitychange', onVisible)
}

interface State {
  user: SessionUser | null
  ready: boolean
}

export const useSession = defineStore('session', {
  state: (): State => ({ user: null, ready: false }),
  actions: {
    async restore() {
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
      const data = await api.post<{ token: string; user: SessionUser }>('/api/auth/login', { username, password })
      setToken(data.token)
      this.user = data.user
      await api.get('/api/auth/ready')
      startAutoRefresh()
    },
    /** 换取新令牌（24h 有效）；失败抛出，401 由全局处理器接管登出。 */
    async refreshToken() {
      const d = await api.get<{ token: string; user: SessionUser }>('/api/auth/refresh')
      if (d?.token) {
        setToken(d.token)
        lastRefresh = Date.now()
      }
    },
    logout() {
      stopAutoRefresh()
      clearToken()
      this.user = null
      // 同步清除 goder 等嵌入组件使用的 Cookie
      fetch('/api/auth/logout').catch(() => {})
    },
  },
})
