<script setup lang="ts">
// OsTabApp — tab 型应用聚合壳（自 grf-os 三 App 模式上移，P4）。
//
// 把「功能页做在融合标题栏」的形态收敛为通用件：OsToolbar merged（红绿灯让位
// + 可拖拽）+ OsTabs 胶囊切换 + KeepAlive 缓存各 tab 状态/轮询。用法：
//
//   <OsTabApp :win="win" :tabs="[
//     { key: 'overview', label: '概览', comp: Overview, if: () => can('x.view') },
//     { key: 'keys', label: '密钥', comp: Keys },
//   ]" />
//
//   - 初始 tab 读 windows.open(id, { tab }) 注入的 props（未知/不可见时回落首个）；
//   - `if` 为可见性谓词（权限/角色判定，如 () => can('room.view')），不满足的
//     tab 不出现在 OsTabs，会话权限晚就绪时自动回落（watch）；
//   - 标题栏右侧附加控件用 toolbar 插件位（OsTbOverflow 包裹可自动折叠）。
import { computed, ref, watch } from 'vue'
import type { Component } from 'vue'
import OsToolbar from './OsToolbar.vue'
import OsTabs from './OsTabs.vue'

export interface OsTabDef {
  key: string
  label: string
  comp: Component
  /** 可见性谓词（默认可见）；不满足的 tab 不渲染、不可切 */
  if?: () => boolean
}

const props = defineProps<{
  win?: any
  tabs: OsTabDef[]
  /** 融合标题栏左侧标题（默认不占位） */
  title?: string
}>()

const visible = computed(() => props.tabs.filter((t) => !t.if || t.if()))
const visibleTabs = computed(() => visible.value.map(({ key, label }) => ({ key, label })))
const current = computed(() => visible.value.find((t) => t.key === tab.value)?.comp)

// 初始 tab：windows.open 传入的 props.tab（未知/越权时回落首个可见 tab）
const want = props.win?.props?.tab
const tab = ref(visible.value.some((t) => t.key === want) ? want : visible.value[0]?.key || '')
// 会话角色/权限晚就绪（if 谓词由拒到允）时，当前 tab 可能不在可见集内 → 回落
watch(visible, (v) => {
  if (!v.some((t) => t.key === tab.value)) tab.value = v[0]?.key || ''
})
</script>

<template>
  <div class="os-tabapp">
    <OsToolbar merged :title="title">
      <OsTabs v-model="tab" :tabs="visibleTabs" />
      <slot name="toolbar" :tab="tab" />
    </OsToolbar>
    <div class="os-tabapp-body">
      <KeepAlive v-if="current">
        <component :is="current" />
      </KeepAlive>
      <slot v-else />
    </div>
  </div>
</template>

<style scoped>
.os-tabapp { display: flex; flex-direction: column; width: 100%; height: 100%; min-width: 0; }
.os-tabapp-body { flex: 1; overflow-y: auto; padding: 12px 14px; min-height: 0; }
</style>
