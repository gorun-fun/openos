<script setup lang="ts">
// goder —— 完整 AI 编程工作台（grf-harness 图形界面）作为 OpenOS 内置应用。
// 集成方式与 grf-play 一致：动态加载 /vendor/goder.js（自包含 ESM web components），
// configure 连接同源 /goder-api（OpenOS 后端反代 goder-serve 并注入登录身份），
// 渲染 <goder-app-shell>（会话列表 + 聊天 + 右栏 tab + 事件台）。
// 窗口控制（关闭/最小化/最大化/拖动）统一由 OpenOS 系统标题栏承担（深色一体化样式，
// 见 WindowFrame 的 [data-app='goder'] 配色），不向 goder 界面注入任何窗口控件。
import { nextTick, onMounted, ref } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { api } from '../../api/client'
import { OsButton } from '../../ui'

const props = defineProps<{ win: WinState }>()
const windows = useWindows()
const session = useSession()

const loading = ref(true)
const error = ref('')
const unavailable = ref(false)
const hostEl = ref<HTMLElement>()

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

  // 3. 连接配置：同源网关，身份由 OpenOS 后端注入（浏览器不持有 gh 令牌）
  const me = session.user!
  try {
    ui.configure('openos', {
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
  shell.setAttribute('scope', 'openos')
  shell.setAttribute('panels', 'sessions,chat,sidebar,console')
  shell.style.cssText = 'width:100%;height:100%'
  hostEl.value!.append(shell)
}

function retry() {
  error.value = ''
  loading.value = true
  void init()
}
</script>

<template>
  <div class="goder-studio">
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
