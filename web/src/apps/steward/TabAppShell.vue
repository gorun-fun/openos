<script setup lang="ts">
// TabAppShell.vue — tab 型业务 App 聚合壳（5 个 App 共用骨架收敛）。
//
// 原先 tokenhub/gateway/log/rooms/steward 五个壳各复制一份同款结构（标题栏
// OsToolbar merged + OsTabs、初始 tab 读 windows.open 传入 props.tab、
// KeepAlive 缓存页签、同一套 style），变体经 props/slot 承接：
//   - tabs[].adminOnly + 会话角色晚就绪回落（tokenhub 的「我的令牌」全员可见）
//   - toolbar 插槽（gateway 的实例切换器）
//   - epoch prop（gateway 切实例整体重挂载——挂 body 容器连 KeepAlive 一起重建，
//     防旧 epoch 缓存条目累积）
//   - switchTab 经 defineExpose 暴露（steward 的 stewardSwitchTab provide 转发）
import { computed, ref, watch, type Component } from 'vue'
import { OsToolbar, OsTabs } from '../../ui'
import { isAdmin } from './core'
import './shell-theme.css'

export interface TabDef {
  key: string
  label: string
  comp: Component
  /** admin 专属页签（user 会话隐藏；会话角色晚就绪时当前 tab 自动回落可见集） */
  adminOnly?: boolean
}

const props = defineProps<{
  win?: any
  tabs: TabDef[]
  /** body 容器 key（变化即连 KeepAlive 整体重挂载，gateway 切实例用） */
  epoch?: number
}>()

const visible = computed(() => props.tabs.filter((t) => !t.adminOnly || isAdmin()))
const visibleTabs = computed(() => visible.value.map(({ key, label }) => ({ key, label })))
const current = computed(() => visible.value.find((t) => t.key === tab.value)?.comp)

// 初始 tab：windows.open 传入的 props.tab（未知/越权时回落首个可见 tab）
const want = props.win?.props?.tab
const tab = ref(visible.value.some((t) => t.key === want) ? want : visible.value[0].key)
// 会话角色晚就绪（isAdmin 由 false 转 true）时，当前 tab 可能不在可见集内 → 回落
watch(visible, (v) => {
  if (!v.some((t) => t.key === tab.value)) tab.value = v[0].key
})

/** 程序化切页签（未知 key 忽略）；steward 经 ref 转发进 stewardSwitchTab provide。 */
function switchTab(key: string) {
  if (visible.value.some((t) => t.key === key)) tab.value = key
}
defineExpose({ switchTab })
</script>

<template>
  <div class="app grf-app">
    <OsToolbar merged>
      <OsTabs v-model="tab" :tabs="visibleTabs" />
      <slot name="toolbar" />
    </OsToolbar>
    <div :key="epoch ?? 0" class="body">
      <KeepAlive>
        <component :is="current" :key="tab" />
      </KeepAlive>
    </div>
  </div>
</template>

<style scoped>
.app { display: flex; flex-direction: column; width: 100%; height: 100%; min-width: 0; }
.body { flex: 1; overflow-y: auto; padding: 12px 14px; }
/* 标题栏 tab 在窄窗口可横向滚动，不挤压变形 */
.app :deep(.os-tabs) { flex: 0 1 auto; overflow-x: auto; }
@media (max-width: 767px) { .body { padding: 8px; } }
</style>
