// 会话仓库：登录 / 登出 / 当前用户。
import { defineStore } from 'pinia'
import { api, getToken, setToken, clearToken, type SessionUser } from '../api/client'

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
        // 续种 Cookie（goder 等嵌入组件的同源请求依赖它）
        await api.get('/api/auth/refresh').catch(() => {})
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
    },
    logout() {
      clearToken()
      this.user = null
      // 同步清除 goder 等嵌入组件使用的 Cookie
      fetch('/api/auth/logout').catch(() => {})
    },
  },
})
