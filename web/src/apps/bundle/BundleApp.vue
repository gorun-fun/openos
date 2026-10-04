<script setup lang="ts">
// 商店 bundle 应用容器（config.kind=bundle）：动态 import 服务端本地提供的组件
// 模块（GET /api/market/bundle/<id>?v=<version>&ticket=<短票>，ESM 模块请求带不了
// Authorization header 故鉴权走查询参数——用 30s 一次性短票而非长期 JWT，URL 不
// 再泄漏可长期使用的令牌），默认导出须为 Vue 组件定义
// （{ template, setup }，template 由主程序完整版 Vue 运行时编译）。
//
// 模块 URL 带安装版本号：升级换版本即换 URL，浏览器模块缓存天然按版本隔离；
// 同版本内容在服务端不可变（文件名含版本），可安全命中缓存。
// 渲染错误由 WindowFrame 的窗口级错误边界兜底，此处只处理加载阶段的失败。
import { onMounted, ref, shallowRef } from 'vue'
import type { WinState } from '../../stores/windows'
import { useApps } from '../../stores/apps'
import { fetchWsTicket, getToken } from '../../api/client'

const props = defineProps<{ win: WinState }>()

const apps = useApps()
const comp = shallowRef<any>(null)
const failed = ref('')
const loading = ref(true)

onMounted(async () => {
  const appId = props.win.appId
  const def = apps.byId(appId)
  // 短票优先（用后即焚）；取票失败回落长期 token（老服务端兼容）
  let auth = `token=${encodeURIComponent(getToken())}`
  try {
    auth = `ticket=${encodeURIComponent(await fetchWsTicket())}`
  } catch { /* 兼容回落 */ }
  const url = `/api/market/bundle/${encodeURIComponent(appId)}`
    + `?v=${encodeURIComponent(def?.version || '')}&${auth}`
  try {
    const mod: any = await import(/* @vite-ignore */ url)
    const c = mod?.default
    if (!c || typeof c !== 'object' || typeof c === 'function') {
      throw new Error('bundle 默认导出须为 Vue 组件定义（{ template, setup }）')
    }
    comp.value = c
  } catch (e: any) {
    console.error(`[OpenOS] bundle 应用 ${appId} 加载失败:`, e)
    failed.value = e?.message || String(e)
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div class="bundle-app">
    <component :is="comp" v-if="comp" :win="win" />
    <div v-else-if="loading" class="bundle-tip">正在加载应用…</div>
    <div v-else class="bundle-tip bundle-err">
      <p>应用加载失败</p>
      <p class="bundle-detail">{{ failed }}</p>
      <p class="bundle-hint">可尝试卸载后重新安装；若持续失败请联系管理员检查商店目录源。</p>
    </div>
  </div>
</template>

<style scoped>
.bundle-app { width: 100%; height: 100%; display: flex; flex-direction: column; }
.bundle-app > :deep(*) { flex: 1; min-height: 0; }
.bundle-tip {
  flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 6px; padding: 24px; color: var(--os-text-secondary); font-size: 13px;
}
.bundle-err p { margin: 0; }
.bundle-detail { font-size: 12px; opacity: .8; word-break: break-all; text-align: center; }
.bundle-hint { font-size: 12px; opacity: .6; }
</style>
