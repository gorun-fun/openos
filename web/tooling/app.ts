// tooling/app.ts — 业务 App 独立产物构建配置（App 独立发布通道的构建侧之二）。
//
// 每个宿主业务 App 单独构建为「单文件 ESM 产物」发布到货架：
//   - 入口 = App 的 .vue SFC，产物 default 导出组件定义（远程 remoteApps 加载器
//     经动态 import 消费，与宿主本地 () => import('./apps/X.vue') 形态一致）
//   - 全部共享依赖（vue/pinia/@openos/*/宿主私有模块）external 保留裸名——运行时
//     经宿主页面 importmap 解析到 deps 产物，与宿主壳共享同一实例
//   - 产物不带 hash：版本在货架目录路径上（files/<id>/<version>/<id>.js），
//     immutable 强缓存天然按版本隔离；回滚 = catalog 改指旧目录
//   - SFC 样式内联进 JS（加载即注入 <style>），产物单文件无外部资产
//
// 宿主脚本逐 App 调用（每次一个 vite build 进程），产物目录由调用方指定
// （本地验证 app/web/public/market、CI market-dist）。
import { defineConfig, type Plugin, type UserConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { existsSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { loadDepsManifest } from './host.ts'

export interface AppBuildOptions {
  /** appId（产物文件名，与货架 catalog/宿主 apps 注册键一致） */
  id: string
  /** App 入口 SFC（宿主工程内路径） */
  entry: string
  /** 产物目录（含版本层级，如 'public/market/files/rooms/1.0.3'） */
  outDir: string
  /** deps-manifest.json 路径（与宿主构建同一份；external 清单由此得出） */
  depsManifest: string
  /** 构建期常量注入（透传 vite define，如 __HOTEL_VERSION__） */
  define?: Record<string, any>
  /** SFC 自定义元素判定（与宿主/deps 构建保持一致） */
  isCustomElement?: (tag: string) => boolean
}

/** 把构建产出的 CSS 文件内联进入口 JS：模块求值时注入 <style>，产物单文件。
 *  文件系统后处理（closeBundle，落盘后）：rolldown-vite 的 CSS 资产在插件
 *  generateBundle 阶段尚不可见，bundle 内改写抓不到，直接读写产物目录最可靠。 */
function cssInjectPlugin(outDir: string, id: string): Plugin {
  return {
    name: 'openos-app-css-inject',
    closeBundle() {
      const dir = resolve(process.cwd(), outDir)
      const js = resolve(dir, `${id}.js`)
      if (!existsSync(js)) return
      for (const f of readdirSync(dir)) {
        if (!f.endsWith('.css')) continue
        const css = readFileSync(resolve(dir, f), 'utf8')
        const inject = `;(function(){var d=document,s=d.createElement('style');s.textContent=${JSON.stringify(css)};d.head.appendChild(s)})();\n`
        writeFileSync(js, inject + readFileSync(js, 'utf8'))
        rmSync(resolve(dir, f))
        console.log(`openos-app-css-inject: ${f} 已内联进 ${id}.js`)
      }
    },
  }
}

/** App 单文件构建配置（宿主脚本以独立配置文件消费：export default defineAppBuild({...})）。 */
export function defineAppBuild(o: AppBuildOptions): UserConfig {
  const manifest = loadDepsManifest(o.depsManifest)
  const bareIds = new Set(Object.keys(manifest.imports))
  return defineConfig({
    plugins: [
      vue({
        template: {
          compilerOptions: { isCustomElement: o.isCustomElement || (() => false) },
        },
      }),
      cssInjectPlugin(o.outDir, o.id),
    ],
    define: o.define,
    // 产物不需要 public 资产（图标经宿主页面注册）；outDir 可能位于 public/ 内
    // （本地货架 public/market），保持默认 publicDir 会把 public 递归拷进产物目录
    publicDir: false,
    // CSS 管线强制 postcss + esbuild（同 deps.ts：防 vite 8 默认 Lightning CSS
    // 把 backdrop-filter 成对前缀折叠成仅 -webkit- 形态，Chromium 不认 → 毛玻璃灭）
    css: { transformer: 'postcss' },
    build: {
      outDir: o.outDir,
      emptyOutDir: true,
      lib: {
        entry: o.entry,
        formats: ['es'],
        name: o.id, // 仅占位（es 格式不用 umd 名）
      },
      rollupOptions: {
        external: (id: string) => bareIds.has(id),
        output: {
          entryFileNames: `${o.id}.js`,
          chunkFileNames: 'chunks/[name]-[hash].js', // 不应出现（App 源内动态 import 会被警告）；兜底
          assetFileNames: '[name][extname]',
        },
      },
      cssCodeSplit: false,
      cssMinify: 'esbuild',
      chunkSizeWarningLimit: 2000,
    },
  })
}
