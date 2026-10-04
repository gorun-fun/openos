<script setup lang="ts">
// 应用切换器 overlay（渲染 appSwitcher.ts 的状态）：居中一排应用图标，
// 选中项放大高亮 + 名称提示。键盘交互在 Desktop.vue（Tab 步进 / 松开 Alt 提交 /
// Esc 取消）；点图标 = 直接提交该应用。
import { computed } from 'vue'
import { useApps } from '../stores/apps'
import { switcherState, switcherCommit } from './appSwitcher'
import AppIcon from '../ui/AppIcon.vue'

const apps = useApps()
const items = computed(() =>
  switcherState.appIds.map((id, i) => ({
    id,
    name: apps.byId(id)?.name || id,
    icon: apps.byId(id)?.icon || 'default',
    active: i === switcherState.idx,
  })),
)
</script>

<template>
  <div v-if="switcherState.open" class="app-switcher" @pointerdown.prevent>
    <button
      v-for="it in items" :key="it.id" class="sw-item" :class="{ active: it.active }"
      :title="it.name" @click="switcherCommit()"
    >
      <AppIcon :app="it.id" :icon="it.icon" :size="52" />
    </button>
    <div class="sw-name">{{ items[switcherState.idx]?.name }}</div>
  </div>
</template>

<style scoped>
.app-switcher {
  position: fixed; left: 50%; top: 42%; transform: translate(-50%, -50%);
  display: flex; align-items: center; gap: 10px; flex-wrap: wrap; justify-content: center;
  max-width: min(86vw, 720px);
  padding: 18px 20px 40px; border-radius: 22px; z-index: 9800; /* 启动台(9700)之上、DialogHost(1e6)之下 */
  background: rgba(28, 30, 34, .78); backdrop-filter: blur(22px); -webkit-backdrop-filter: blur(22px);
  box-shadow: 0 18px 50px rgba(0, 0, 0, .4);
}
.sw-item {
  border: none; background: none; padding: 8px; border-radius: 16px; cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  transition: transform .12s ease, background .12s ease;
}
.sw-item:hover { background: rgba(255, 255, 255, .1); }
.sw-item.active {
  background: rgba(255, 255, 255, .18);
  outline: 3px solid rgba(255, 255, 255, .85);
  transform: scale(1.12);
}
.sw-name {
  position: absolute; left: 0; right: 0; bottom: 10px; text-align: center;
  color: #fff; font-size: 13px; text-shadow: 0 1px 3px rgba(0, 0, 0, .5);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis; padding: 0 16px;
}
</style>
