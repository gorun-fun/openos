<script setup lang="ts">
// goder —— 完整 AI 编程工作台（grf-harness 图形界面）作为 OpenOS 内置应用。
// 集成方式与 grf-play 一致：动态加载 /vendor/goder.js（自包含 ESM web components），
// configure 连接同源 /goder-api（OpenOS 后端反代 goder-serve 并注入登录身份），
// 渲染 <goder-app-shell>（会话列表 + 聊天 + 右栏 tab + 事件台）。
// 融合窗体（appChrome 'merged'）：无系统标题栏，红绿灯悬浮左上角；
// 容器标记 data-os-drag="top" 把 goder 自绘的第一行（左栏顶行/收起后的
// 工作卡头部）作为标题栏 —— WindowFrame 经 composedPath 穿透 shadow DOM
// 委托拖拽；sideTop 注入 72px 占位把折叠侧栏按钮推到红绿灯右侧
// （对应 goder 桌面态 lights 模式的 80px 留位）。
import { nextTick, onMounted, ref, watch } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { api } from '../../api/client'
import { OsButton, MobileNavToggle, toast } from '../../ui'
import { injectShadowNavCss } from '../../ui/shadowNav'

const props = defineProps<{ win: WinState }>()
const windows = useWindows()
const session = useSession()

const loading = ref(true)
const error = ref('')
const unavailable = ref(false)
const hostEl = ref<HTMLElement>()
// 移动形态：左栏（会话导航）抽屉展开态——goder 是 shadow DOM web component，
// .os-side 约定样式经 shadowNav 注入其 shadowRoot（见 init 第 5 步）
const navOpen = ref(false)
const shellReady = ref(false)

onMounted(() => { void init() })

async function init() {
  windows.setTitle(props.win.id, 'Goder - AI助手')

  // 1. 检查服务端集成状态（二进制/嵌入库缺失时给部署引导）
  try {
    const st = await api.get<{ available: boolean; running: boolean; error?: string }>('/api/goder/status')
    if (!st.available) {
      unavailable.value = true
      loading.value = false
      return
    }
  } catch {
    // 状态接口异常不阻塞，继续尝试加载
  }

  // 2. 动态加载嵌入库（不参与构建打包，由服务端提供）
  let ui: any
  try {
    const vendorUrl = '/vendor/goder.js'
    ui = await import(/* @vite-ignore */ vendorUrl)
  } catch (e: any) {
    error.value = 'goder.js 加载失败：' + (e?.message || e)
    loading.value = false
    return
  }

  // 3. 连接配置：同源网关，身份由 OpenOS 后端注入（浏览器不持有 gh 令牌）。
  //    scope 按用户分键（openos:<username>）：goder 的分组索引等本地键以 scope
  //    命名（goder:<scope>:index），同一浏览器切换 OpenOS 账号互不串台。
  const me = session.user!
  const scope = `openos:${me.username}`
  // 3.5 分组索引持久化注入：默认落浏览器 localStorage，换设备即丢分组与自定义
  //     标题（会话本体在服务端不丢）。注入服务端镜像——load 服务端优先（user_settings
  //     的 goder.index 键），无数据回落本地并经 save 迁移上云；save 双写（本地缓存
  //     + 400ms 防抖回写）。旧版 goder.js 未导出 setPersistence 时保持默认行为。
  if (typeof ui.setPersistence === 'function') {
    let saveTimer = 0
    let lastSynced = ''
    const SERVER_KEY = 'goder.index'
    ui.setPersistence({
      async load(sc: string) {
        let local: string | null = null
        try { local = localStorage.getItem(`goder:${sc}:index`) } catch { /* ignore */ }
        try {
          const s = await api.get<Record<string, string>>('/api/settings')
          if (s[SERVER_KEY] !== undefined) {
            lastSynced = s[SERVER_KEY]
            try { localStorage.setItem(`goder:${sc}:index`, s[SERVER_KEY]) } catch { /* ignore */ }
            return JSON.parse(s[SERVER_KEY])
          }
        } catch { /* 离线回落本地 */ }
        // 迁移源优先级：本 scope 本地键 > 旧共享键（scope 分用户化之前 goder:openos:index）
        const legacy = local || localStorage.getItem('goder:openos:index')
        return legacy ? JSON.parse(legacy) : null
      },
      async save(_sc: string, idx: unknown) {
        const v = JSON.stringify(idx)
        try { localStorage.setItem(`goder:${_sc}:index`, v) } catch { /* ignore */ }
        if (v === lastSynced) return
        clearTimeout(saveTimer)
        saveTimer = window.setTimeout(() => {
          lastSynced = v
          api.put('/api/settings', { key: SERVER_KEY, value: v }).catch(() => { lastSynced = '' })
        }, 400)
      },
    })
  }
  try {
    // 项目上下文统一进 OpenOS home：裸会话 workspace 兜底为用户 home 真实路径
    // （未取到时不下发，保持 goder 默认）。以下两项配合服务端 POST /api/goder/project。
    let homePath = ''
    try {
      const h = await api.get<{ path: string }>('/api/goder/home')
      homePath = h.path || ''
    } catch { /* 不阻塞，仅缺兜底 */ }
    ui.configure(scope, {
      baseUrl: '/goder-api',
      auth: {
        onUnauthorized: () => {
          error.value = '登录状态失效，请重新登录 OpenOS 后再试'
        },
      },
      user: {
        name: me.displayName || me.username,
        detail: `${me.username} · OpenOS`,
        chevron: false,
        hit: 'content',
      },
      // 新建项目钩子（goder 嵌入约定）：项目目录由 OpenOS 在统一 home 的
      // ~/goder/<项目名> 下创建并返回真实绝对路径——该目录即会话 workspace
      //（AI 的项目上下文），与文件管理、真实终端共用同一份文件树。
      // 失败（重名/文件系统关闭）toast 提示并返回 null 交回 goder 默认行为。
      newProject: async (name: string) => {
        try {
          const r = await api.post<{ path: string }>('/api/goder/project', { name })
          return r.path
        } catch (e: any) {
          toast(String(e?.message || e) || '项目目录创建失败', { type: 'error' })
          return null
        }
      },
      ...(homePath ? { sessionDefaults: { workspace: homePath } } : {}),
      // 融合窗体红绿灯留位：左栏顶行行首注入 72px 透明占位（84px 悬浮区 -
      // side-top 自身 12px 左内边距），避免折叠侧栏按钮被红绿灯盖住；
      // 渲染进 shadow DOM，样式只能内联
      sideTop: {
        leading: () => {
          const spacer = document.createElement('div')
          spacer.style.cssText = 'flex:none;width:72px'
          return spacer
        },
      },
    })
  } catch (e: any) {
    error.value = 'goder 初始化失败：' + (e?.message || e)
    loading.value = false
    return
  }

  // 4. 装配应用壳：先让容器可见（避免 display:none 下挂载导致布局尺寸为 0），
  //    再 nextTick 后插入 <goder-app-shell>（左会话 + 中聊天 + 右栏 + 底部事件台）
  loading.value = false
  await nextTick()
  const shell = document.createElement('goder-app-shell')
  shell.setAttribute('scope', scope)
  shell.setAttribute('panels', 'sessions,chat,sidebar,console')
  shell.style.cssText = 'width:100%;height:100%'
  hostEl.value!.append(shell)

  // 5. 移动形态抽屉化：goder 左栏（shadow 内 <div class="left">，inline width 248px）
  //    收为汉堡唤出的抽屉。注入的 CSS 带媒体查询（桌面零生效）；类一次加好，
  //    展开态类经下方 watch(navOpen) 手动同步——Vue 模板绑定够不到 shadow 内部。
  //    Lit 首次渲染是挂载后的微任务，轮询等待 .left 出现。
  void (async () => {
    for (let i = 0; i < 40; i++) {
      const root = shell.shadowRoot
      const left = root?.querySelector<HTMLElement>('.left')
      if (root && left) {
        injectShadowNavCss(root)
        // goder 特定补充：左栏抽屉化后拖宽把手悬空失义，移动形态隐藏
        if (!root.querySelector('style[data-os-nav-goder]')) {
          const s = document.createElement('style')
          s.setAttribute('data-os-nav-goder', '1')
          s.textContent = '@media (max-width: 767px) { .gutter-v { display: none !important; } }'
          root.append(s)
        }
        left.classList.add('os-side')
        left.classList.toggle('os-side-open', navOpen.value)
        shellReady.value = true
        return
      }
      await new Promise((r) => setTimeout(r, 50))
    }
  })()
}

// 抽屉开合同步进 shadow DOM（composedPath 委托的自动收起改 navOpen，由这里落到元素）
watch(navOpen, (v) => {
  const left = hostEl.value?.querySelector('goder-app-shell')?.shadowRoot?.querySelector('.left')
  left?.classList.toggle('os-side-open', v)
})

function retry() {
  error.value = ''
  loading.value = true
  void init()
}
</script>

<template>
  <!-- data-os-drag="top"：仅顶部一行（goder 自绘顶栏）可拖动窗口，见 WindowFrame。
       os-has-side：移动形态定位容器约定（汉堡/遮罩锚定，桌面形态无样式生效） -->
  <div class="goder-studio os-has-side" data-os-drag="top">
    <!-- 移动形态：左栏抽屉开关（shell 挂载成功后才出现；桌面隐藏） -->
    <MobileNavToggle v-if="shellReady" v-model:open="navOpen" />
    <div v-if="loading" class="state">正在连接 goder 工作台…</div>

    <div v-else-if="unavailable" class="state guide">
      <h3>goder 组件未部署</h3>
      <p>OpenOS 通过 <code>goder-serve</code> 提供 goder 集成：</p>
      <ol>
        <li>构建网关：<code>cd goder && go build -o &lt;openos&gt;/server/data/bin/goder-serve ./cmd/goder-serve</code></li>
        <li>拷贝嵌入库：<code>cp goder/frontend/dist-embed/goder.js &lt;openos&gt;/server/data/goder/</code></li>
        <li>确保 <code>gh</code> 可执行文件在 PATH，重启 OpenOS 服务端</li>
      </ol>
      <p class="dim">每个 OpenOS 用户对应一个独立的 gh server 进程（数据隔离），模型配置沿用 ~/.grf。</p>
    </div>

    <div v-else-if="error" class="state err">
      <p>{{ error }}</p>
      <OsButton size="sm" @click="retry">重试</OsButton>
    </div>

    <div v-show="!loading && !unavailable && !error" ref="hostEl" class="host" />
  </div>
</template>

<style scoped>
.goder-studio {
  width: 100%; height: 100%; background: #14161c; position: relative;
  display: flex; flex-direction: column;
}
.host { flex: 1; min-height: 0; }
.state {
  flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 10px; color: #a7adba; font-size: 13.5px; padding: 24px; text-align: left;
}
.state h3 { color: #e6e9ef; font-size: 15px; }
.state ol { margin-left: 18px; line-height: 2; }
.state code {
  background: rgba(127,127,127,.15); padding: 1px 6px; border-radius: 4px;
  font-family: var(--os-mono); font-size: 11.5px; word-break: break-all;
}
.state.dim { font-size: 12px; color: #6f7682; }
.state.err p { color: #ff8a8a; max-width: 420px; text-align: center; line-height: 1.7; }
</style>
