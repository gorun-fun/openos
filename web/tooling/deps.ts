// tooling/deps.ts — 共享依赖库集构建（App 独立发布通道的构建侧之一）。
//
// 在宿主工程里作为独立 vite 构建配置运行（宿主 package.json script，如
// `vite build --config deps.config.ts`，deps.config.ts 调 defineDepsBuild）：
// 产出页面级共享 ESM 模块（vue 完整版 / pinia / openos-all 聚合 / 宿主私有模块），
// 外加 deps-manifest.json（裸名 → 产物 URL 映射），供：
//   - tooling/host.ts：宿主构建 external 化 + index.html 注入 importmap
//   - tooling/app.ts：App 产物构建 external 化
// 远程 App 产物与宿主壳的裸导入经 importmap 全部解析回这些产物——单实例的根基。
//
// 入口间仅 external vue/pinia（产物内保留裸名，运行时 importmap 互解）；
// openos 内部依赖（pdfjs 等）打进产物。产物文件名带 hash，内容变即换名，
// 可永久强缓存；deps-manifest.json 由本构建自动生成。
import { defineConfig, type Plugin, type UserConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'
import { readdirSync, writeFileSync } from 'node:fs'
import { dirname, isAbsolute, resolve } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
/** openos web src 绝对路径（宿主工程外，经快照目录定位）。 */
export const openosSrc = resolve(here, '../src')

/**
 * openos-all 产物覆盖的裸导入名（importmap 全部映射到同一 openos-all-<hash>.js；
 * 新增宿主可消费的 openos 公共入口时在此登记）。
 */
export const OPENOS_BARE_IDS = [
  '@openos/framework',
  '@openos/ui',
  '@openos/api/client',
  '@openos/host',
  '@openos/stores/windows',
  '@openos/stores/calendar',
  '@openos/stores/session',
  '@openos/stores/apps',
  '@openos/bus/bus',
  '@openos/desktop/icons',
] as const

/** deps-manifest.json 结构（host/app 构建与运行调试共用）。 */
export interface DepsManifest {
  /** 裸名 → 产物 web URL（importmap imports 直接可用） */
  imports: Record<string, string>
  /** 需随页面加载的 CSS（openos-all 产物提取的组件/基础样式） */
  css: string[]
  /** 入口名 → 产物文件名（诊断用） */
  entries: Record<string, string>
}

export interface DepsBuildOptions {
  /** 产物目录（宿主工程内路径，如 'dist/assets/deps'） */
  outDir: string
  /** 产物目录的 web 访问前缀（如 '/assets/deps'）——manifest 内 URL 由此拼出 */
  webBase: string
  /** 宿主私有共享模块/额外依赖：键 = importmap 裸名（如 hotel-core、echarts），
   *  值 = 入口模块路径（宿主工程内说明符） */
  extraEntries?: Record<string, string>
  /** SFC 自定义元素判定（宿主与 App 构建须一致，透传 plugin-vue） */
  isCustomElement?: (tag: string) => boolean
}

/**
 * 生成 deps-manifest.json 的插件：generateBundle 阶段枚举入口 chunk 的产物文件名，
 * 按映射规则（vue/pinia/extras 各自独立产物；OPENOS_BARE_IDS 全部指向 openos-all）
 * 组装 imports；css 文件在 closeBundle（落盘后）扫产物目录收集——rolldown-vite 的
 * CSS 资产在插件 generateBundle 阶段尚不可见，写 manifest 须等它落盘。
 */
function manifestPlugin(o: DepsBuildOptions): Plugin {
  const entries: Record<string, string> = {}
  return {
    name: 'openos-deps-manifest',
    generateBundle(_opts, bundle) {
      for (const [fileName, chunk] of Object.entries(bundle)) {
        if (chunk.type === 'chunk' && chunk.isEntry) entries[chunk.name] = fileName
      }
    },
    closeBundle() {
      const dir = resolve(process.cwd(), o.outDir)
      const cssFiles = readdirSync(dir).filter(f => f.endsWith('.css'))
      const web = (f: string) => o.webBase.replace(/\/$/, '') + '/' + f
      const imports: Record<string, string> = {}
      if (entries.vue) imports.vue = web(entries.vue)
      if (entries.pinia) imports.pinia = web(entries.pinia)
      if (entries['openos-all']) {
        for (const id of OPENOS_BARE_IDS) imports[id] = web(entries['openos-all'])
      }
      for (const id of Object.keys(o.extraEntries || {})) {
        if (entries[id]) imports[id] = web(entries[id])
      }
      if (!imports.vue || !imports['@openos/framework']) {
        throw new Error('[openos-deps] 基础入口产物缺失：vue / openos-all')
      }
      const manifest: DepsManifest = { imports, css: cssFiles.map(web), entries }
      writeFileSync(resolve(dir, 'deps-manifest.json'), JSON.stringify(manifest, null, 2) + '\n')
      console.log(`openos-deps-manifest: ${Object.keys(imports).length} 个裸名映射，${cssFiles.length} 个 CSS`)
    },
  }
}

/** deps 库集构建配置（宿主以独立配置文件消费：export default defineDepsBuild({...})）。 */
export function defineDepsBuild(o: DepsBuildOptions): UserConfig {
  // 裸名 entry（vue/pinia/echarts 等）解析为宿主 node_modules 内的绝对路径——
  // lib entry 需具体文件路径。必须按 ESM 条件解析（default 的 require 条件会拿到
  // CJS 产物，浏览器加载时 require() 调用直接崩溃）
  const req = createRequire(resolve(process.cwd(), 'package.json'))
  // Node 运行时要求 conditions 为 Set（传 string[] 报 conditions.has is not a
  // function），@types/node 的 RequireResolveOptions 尚未收录该字段——类型差异以断言弥合
  const esmConditions = { conditions: new Set(['module', 'import', 'browser', 'default']) } as unknown as Parameters<typeof req.resolve>[1]
  const asPath = (spec: string) => (spec.startsWith('.') || isAbsolute(spec) ? spec : req.resolve(spec, esmConditions))
  const extraEntries: Record<string, string> = {}
  for (const [k, v] of Object.entries(o.extraEntries || {})) extraEntries[k] = asPath(v)
  const entry: Record<string, string> = {
    vue: asPath('vue/dist/vue.esm-bundler.js'), // 完整版：保留运行时模板编译器（第三方 registerApp 传 template）
    pinia: asPath('pinia'),
    'openos-all': resolve(openosSrc, 'openos-all.ts'),
    ...extraEntries,
  }
  return defineConfig({
    plugins: [
      vue({
        template: {
          compilerOptions: { isCustomElement: o.isCustomElement || (() => false) },
        },
      }),
      manifestPlugin(o),
    ],
    // deps 产物为纯 JS/CSS 库集，不携带宿主 public 静态资产
    publicDir: false,
    // vue/pinia 的 esm-bundler 形态引用 process.env.NODE_ENV（设计上由打包器替换）：
    // deps 产物直接由浏览器加载，必须在此替换，否则求值抛 process is not defined
    define: { 'process.env.NODE_ENV': JSON.stringify('production') },
    // CSS 管线强制 postcss + esbuild（与 openos 独立部署同款）：宿主多用 vite 8，
    // 其默认 Lightning CSS 会把 backdrop-filter + -webkit-backdrop-filter 成对声明
    // 按「前缀回退」折叠成仅剩 -webkit- 形态，而 Chromium 不认该别名（Safari 认）
    // → openos 壳样式（openos-all 全量在此产物）毛玻璃整体退化为纯透明。
    // cssMinify 显式 esbuild：rolldown-vite 不自带 esbuild，宿主工程须有该 devDep。
    css: { transformer: 'postcss' },
    build: {
      outDir: o.outDir,
      emptyOutDir: true,
      lib: {
        entry,
        formats: ['es'],
      },
      rollupOptions: {
        // 仅 vue/pinia 裸名外置（入口互引经 importmap 解析）；其余（pdfjs 等内部
        // 依赖）一律打进产物，保证 deps 目录自洽、不产生未映射裸名
        external: ['vue', 'pinia'],
        output: {
          entryFileNames: '[name]-[hash].js',
          chunkFileNames: 'chunks/[name]-[hash].js',
          assetFileNames: '[name]-[hash][extname]',
        },
      },
      cssCodeSplit: false,
      cssMinify: 'esbuild',
      chunkSizeWarningLimit: 2000,
    },
  })
}
