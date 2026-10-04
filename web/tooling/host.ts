// tooling/host.ts — 宿主构建插件：external 化共享依赖 + index.html 注入 importmap。
//
// 宿主 vite.config.ts 的 plugins 里追加 openosHost({ depsManifest })（在 vue 插件
// 旁），效果：
//   1. deps-manifest.json 里登记的全部裸名（vue/pinia/@openos/*/宿主私有模块）
//      从宿主产物中 external——宿主 dist 不再内嵌这些模块，运行时经 importmap
//      解析到 deps 产物（全站单实例的根基：壳与远程 App 同一份 vue/openos）。
//   2. index.html 头部注入 <script type="importmap">（必须先于任何 module script）
//      与 deps 产物的 CSS <link>。
//
// 前置：先跑 deps 构建（tooling/deps.ts）产出 dist/assets/deps/deps-manifest.json，
// 再跑宿主构建。deps 构建后宿主重新构建即拿到新映射（产物名带 hash）。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import type { HtmlTagDescriptor, Plugin } from 'vite'
import type { DepsManifest } from './deps.ts'

export interface HostBuildOptions {
  /** deps-manifest.json 路径（宿主工程内，如 'dist/assets/deps/deps-manifest.json'） */
  depsManifest: string
}

/** 读 deps-manifest.json（宿主构建配置期同步读取）。 */
export function loadDepsManifest(path: string): DepsManifest {
  const m = JSON.parse(readFileSync(resolve(path), 'utf8')) as DepsManifest
  if (!m?.imports?.vue || !m?.imports['@openos/framework']) {
    throw new Error(`[openos-host] deps-manifest 缺基础映射（vue/@openos/framework）: ${path}`)
  }
  return m
}

export function openosHost(o: HostBuildOptions): Plugin {
  const manifest = loadDepsManifest(o.depsManifest)
  const bareIds = new Set(Object.keys(manifest.imports))
  return {
    name: 'openos-host',
    // 仅生产构建生效：dev（vite serve）源码经 alias 直引、无 importmap，
    // 且 dev 不要求先跑 deps 构建（manifest 读取放插件构造期即本插件的加载期，
    // 若无 apply 限制 dev 配置加载就会因 manifest 缺失崩溃）
    apply: 'build',
    config() {
      return {
        // CSS 管线强制 postcss + esbuild（同 deps.ts 的背书理由：vite 8 默认
        // Lightning CSS 会把 backdrop-filter 成对前缀折叠成仅 -webkit- 形态，
        // Chromium 不认该别名 → 毛玻璃退化为纯透明）。本插件 apply:'build'，
        // dev 侧仍需宿主 vite.config.ts 自行声明同款 css 配置（现有宿主均已带）
        css: { transformer: 'postcss' },
        build: {
          cssMinify: 'esbuild',
          // 不清空 dist：deps 产物（dist/assets/deps）由先行构建产出，宿主构建
          // 只覆盖自己的文件（旧 hash 产物累积无害，deps 构建自身仍清 deps 目录）
          emptyOutDir: false,
          rollupOptions: {
            // 裸名 ∈ deps 清单 → external（保留裸名交给 importmap）；相对导入与
            // 未登记的包照旧打进宿主产物
            external: (id: string) => bareIds.has(id),
          },
        },
      }
    },
    transformIndexHtml(html) {
      const map = { imports: manifest.imports }
      const tags: HtmlTagDescriptor[] = [
        {
          tag: 'script',
          attrs: { type: 'importmap' },
          children: JSON.stringify(map),
          injectTo: 'head-prepend',
        },
        ...manifest.css.map((href): HtmlTagDescriptor => ({
          tag: 'link',
          attrs: { rel: 'stylesheet', href },
          injectTo: 'head-prepend',
        })),
      ]
      return { html, tags }
    },
  }
}
