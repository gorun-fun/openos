// 应用注册表：内置组件 + 服务端下发的应用（含第三方声明式应用）。
import { defineStore } from 'pinia'
import { api, type AppDef } from '../api/client'

// 内置应用组件（懒加载）。第三方组件型应用也通过 window.OpenOS 注册到这里。
export const builtinComponents: Record<string, () => Promise<any>> = {
  terminal: () => import('../apps/terminal/TerminalApp.vue'),
  calculator: () => import('../apps/calculator/CalculatorApp.vue'),
  files: () => import('../apps/files/FilesApp.vue'),
  settings: () => import('../apps/settings/SettingsApp.vue'),
  docs: () => import('../apps/docs/DocsApp.vue'),
  sheets: () => import('../apps/sheets/SheetsApp.vue'),
  slides: () => import('../apps/slides/SlidesApp.vue'),
  pdf: () => import('../apps/pdf/PdfApp.vue'),
  goder: () => import('../apps/goder/GoderStudio.vue'),
  notes: () => import('../apps/notes/NotesApp.vue'),
  welcome: () => import('../apps/welcome/WelcomeApp.vue'),
}

// 第三方在页面运行时通过 window.OpenOS.registerApp() 动态注册的组件
export const runtimeComponents: Record<string, any> = {}

// 应用窗体外观：'merged' = 融合工具栏窗体（无独立标题栏，应用自绘工具栏承担标题行，
// 红绿灯按钮悬浮其上，仿 macOS Finder/邮件等一体式窗口）；缺省 'default' 为标准标题栏。
export const appChrome: Record<string, 'default' | 'merged'> = {
  files: 'merged',
  docs: 'merged',
  sheets: 'merged',
  slides: 'merged',
  pdf: 'merged',
  terminal: 'merged',
  notes: 'merged',
}

interface State {
  apps: AppDef[]
}

export const useApps = defineStore('apps', {
  state: (): State => ({ apps: [] }),
  getters: {
    byId: (s) => (id: string) => s.apps.find((a) => a.id === id),
    // 启动台 / Dock 展示用的应用列表（隐藏应用只可编程打开，如详情窗口）
    visible: (s) => s.apps.filter((a) => !a.hidden),
  },
  actions: {
    async refresh() {
      try {
        this.apps = await api.get<AppDef[]>('/api/apps')
      } catch { /* 保持旧值 */ }
    },
  },
})
