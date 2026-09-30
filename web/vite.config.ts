import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue({
    template: {
      // goder 嵌入库的 web components（<goder-app-shell> 等）
      compilerOptions: { isCustomElement: (tag) => tag.startsWith('goder-') },
    },
  })],
  // 使用完整版 Vue，使第三方 OpenOS.registerApp 可直接传 template 字符串
  resolve: { alias: { vue: 'vue/dist/vue.esm-bundler.js' } },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8081',
      '/goder-api': { target: 'http://127.0.0.1:8081', ws: true },
      '/vendor': 'http://127.0.0.1:8081',
      '/ws': { target: 'ws://127.0.0.1:8081', ws: true },
    },
  },
  build: { chunkSizeWarningLimit: 1500 },
})
