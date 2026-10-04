// 桌面外观仓库：壁纸 / 深浅色，持久化到服务端（按用户）。
// 壁纸列表可由宿主经 createOpenOS({ wallpapers }) 注入覆盖（shell 单例）。
import { defineStore } from 'pinia'
import { api } from '../api/client'
import { shell } from '../shell'

/** 内置默认壁纸（宿主未注入时使用）。 */
export const DEFAULT_WALLPAPERS: string[] = [
  // 默认：暮蓝（类 Sonoma 夜色，多层渐变叠出光晕纵深）
  'radial-gradient(120% 90% at 18% 8%, rgba(88, 120, 255, 0.5) 0%, rgba(88, 120, 255, 0) 55%), radial-gradient(110% 100% at 85% 90%, rgba(23, 210, 190, 0.35) 0%, rgba(23, 210, 190, 0) 50%), linear-gradient(158deg, #101c33 0%, #173a5e 48%, #0e5e6b 100%)',
  // 极光青
  'radial-gradient(110% 90% at 80% 10%, rgba(64, 224, 208, 0.45) 0%, rgba(64, 224, 208, 0) 55%), linear-gradient(160deg, #141e63 0%, #1a6f9e 55%, #26d0ce 100%)',
  // 葡萄紫
  'radial-gradient(120% 100% at 15% 85%, rgba(255, 122, 200, 0.3) 0%, rgba(255, 122, 200, 0) 55%), linear-gradient(160deg, #2c1a4d 0%, #5b2c6f 55%, #8a4f7d 100%)',
  // 落日橙红
  'radial-gradient(110% 90% at 50% 100%, rgba(255, 196, 90, 0.5) 0%, rgba(255, 196, 90, 0) 55%), linear-gradient(165deg, #7a1f4f 0%, #d43d5e 55%, #ff7a45 100%)',
  // 石墨青
  'radial-gradient(120% 90% at 85% 15%, rgba(70, 150, 180, 0.4) 0%, rgba(70, 150, 180, 0) 55%), linear-gradient(160deg, #10161d 0%, #1f3a4d 55%, #2e6a7a 100%)',
  // 靛蓝夜
  'radial-gradient(110% 100% at 20% 15%, rgba(120, 90, 255, 0.45) 0%, rgba(120, 90, 255, 0) 55%), linear-gradient(160deg, #1b2a5e 0%, #31427e 55%, #4e4376 100%)',
]

/** 使用指南「不再显示」偏好键（服务端 user_settings，随用户跨端生效）：
 *  WelcomeApp 标题栏「不再显示」按钮写入，Desktop 登录后自动打开前读取。 */
export const WELCOME_HIDDEN_KEY = 'welcome.hidden'

interface State {
  wallpaper: number
  dark: boolean
  dockAutoHide: boolean
  loaded: boolean
  wallpapers: string[]
}

export const useDesktop = defineStore('desktop', {
  state: (): State => {
    const dark = localStorage.getItem('openos_dark') === '1'
    const dockAutoHide = localStorage.getItem('openos_dockhide') === '1'
    return { wallpaper: 0, dark, dockAutoHide, loaded: false, wallpapers: shell.wallpapers || DEFAULT_WALLPAPERS }
  },
  actions: {
    apply() {
      document.documentElement.classList.toggle('dark', this.dark)
    },
    async load() {
      if (this.loaded) return
      try {
        const s = await api.get<Record<string, string>>('/api/settings')
        if (s['wallpaper']) this.wallpaper = (Number(s['wallpaper']) || 0) % this.wallpapers.length
        if (s['dark']) this.dark = s['dark'] === '1'
        if (s['dockAutoHide']) this.dockAutoHide = s['dockAutoHide'] === '1'
      } catch { /* 离线容忍 */ }
      localStorage.setItem('openos_dark', this.dark ? '1' : '0')
      localStorage.setItem('openos_dockhide', this.dockAutoHide ? '1' : '0')
      this.apply()
      this.loaded = true
    },
    setWallpaper(i: number) {
      this.wallpaper = ((i % this.wallpapers.length) + this.wallpapers.length) % this.wallpapers.length
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
