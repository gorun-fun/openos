// 桌面外观仓库：壁纸 / 深浅色，持久化到服务端（按用户）。
import { defineStore } from 'pinia'
import { api } from '../api/client'

export const WALLPAPERS: string[] = [
  'linear-gradient(160deg, #0f2027 0%, #203a43 50%, #2c5364 100%)',
  'linear-gradient(160deg, #1a2980 0%, #26d0ce 100%)',
  'linear-gradient(160deg, #42275a 0%, #734b6d 100%)',
  'linear-gradient(160deg, #ff416c 0%, #ff4b2b 100%)',
  'linear-gradient(160deg, #16222a 0%, #3a6073 100%)',
  'linear-gradient(160deg, #2b5876 0%, #4e4376 100%)',
]

interface State {
  wallpaper: number
  dark: boolean
  dockAutoHide: boolean
  loaded: boolean
}

export const useDesktop = defineStore('desktop', {
  state: (): State => {
    const dark = localStorage.getItem('openos_dark') === '1'
    const dockAutoHide = localStorage.getItem('openos_dockhide') === '1'
    return { wallpaper: 0, dark, dockAutoHide, loaded: false }
  },
  actions: {
    apply() {
      document.documentElement.classList.toggle('dark', this.dark)
    },
    async load() {
      if (this.loaded) return
      try {
        const s = await api.get<Record<string, string>>('/api/settings')
        if (s['wallpaper']) this.wallpaper = Number(s['wallpaper']) || 0
        if (s['dark']) this.dark = s['dark'] === '1'
        if (s['dockAutoHide']) this.dockAutoHide = s['dockAutoHide'] === '1'
      } catch { /* 离线容忍 */ }
      localStorage.setItem('openos_dark', this.dark ? '1' : '0')
      localStorage.setItem('openos_dockhide', this.dockAutoHide ? '1' : '0')
      this.apply()
      this.loaded = true
    },
    setWallpaper(i: number) {
      this.wallpaper = ((i % WALLPAPERS.length) + WALLPAPERS.length) % WALLPAPERS.length
      api.put('/api/settings', { key: 'wallpaper', value: String(this.wallpaper) }).catch(() => {})
    },
    setDark(v: boolean) {
      this.dark = v
      localStorage.setItem('openos_dark', v ? '1' : '0')
      this.apply()
      api.put('/api/settings', { key: 'dark', value: v ? '1' : '0' }).catch(() => {})
    },
    /** 最大化窗口时自动隐藏 Dock（鼠标移到屏幕底部边缘滑出）。 */
    setDockAutoHide(v: boolean) {
      this.dockAutoHide = v
      localStorage.setItem('openos_dockhide', v ? '1' : '0')
      api.put('/api/settings', { key: 'dockAutoHide', value: v ? '1' : '0' }).catch(() => {})
    },
  },
})
