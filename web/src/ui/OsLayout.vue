<script setup lang="ts">
// OsLayout：应用通用骨架——侧栏 + 内容区 + 可选底部状态栏。
import type { WinState } from '../stores/windows'

defineProps<{
  win: WinState
  items: { key: string; label: string; icon?: string }[]
  active: string
  status?: string
}>()
const emit = defineEmits<{ select: [key: string] }>()
</script>

<template>
  <div class="os-layout">
    <aside class="os-sidebar">
      <div class="win-badge">{{ win.title }}</div>
      <button
        v-for="it in items" :key="it.key"
        :data-key="it.key"
        :class="{ active: active === it.key }"
        @click="emit('select', it.key)"
      >
        <span v-if="it.icon" class="ic">{{ it.icon }}</span>{{ it.label }}
      </button>
      <slot name="sidebar-extra" />
    </aside>
    <main class="os-main">
      <div class="os-content"><slot /></div>
      <footer v-if="status" class="os-statusbar">{{ status }}</footer>
    </main>
  </div>
</template>

<style scoped>
.os-layout { display: flex; width: 100%; min-width: 0; }
.os-sidebar {
  width: 180px; flex: none; background: var(--os-sidebar); border-right: 1px solid var(--os-border);
  padding: 10px 8px; display: flex; flex-direction: column; gap: 2px; overflow-y: auto;
}
.win-badge {
  font-size: 13px; font-weight: 700; padding: 4px 10px 10px; color: var(--os-text);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.os-sidebar button {
  display: flex; align-items: center; gap: 8px; text-align: left; width: 100%;
  padding: 7px 10px; border: none; border-radius: 8px; background: transparent;
  color: var(--os-text); font-size: 13px; cursor: pointer;
}
.os-sidebar button:hover { background: var(--os-hover); }
/* 选中项：accent 实心底白字（macOS 系统设置边栏样式） */
.os-sidebar button.active { background: var(--os-accent); color: #fff; font-weight: 600; }
.ic {
  width: 22px; height: 22px; flex: none; border-radius: 6px; display: inline-flex;
  align-items: center; justify-content: center; font-size: 12px; margin-right: 2px;
}
.os-main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.os-content { flex: 1; min-height: 0; overflow: auto; padding: 16px; display: flex; flex-direction: column; }
/* 子项禁止 flex 收缩：一旦收缩，内容被压变形且溢出被吞掉，滚动永远不触发（窗口拖小时复现） */
.os-content > :deep(*) { flex-shrink: 0; }
.os-statusbar {
  flex: none; height: 24px; display: flex; align-items: center; padding: 0 12px;
  border-top: 1px solid var(--os-border); font-size: 11.5px; color: var(--os-text-secondary);
  background: var(--os-sidebar);
}
</style>
