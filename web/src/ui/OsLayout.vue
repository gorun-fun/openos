<script setup lang="ts">
// OsLayout：应用通用骨架——侧栏 + 内容区 + 可选底部状态栏。
// 移动形态（isMobile）：侧栏退为左上角汉堡唤出的抽屉，竖屏内容占满；
// 选中导航项自动收起。桌面形态结构不变。
import { ref } from 'vue'
import type { WinState } from '../stores/windows'
import { isMobile } from './formfactor'

defineProps<{
  win: WinState
  items: { key: string; label: string; icon?: string }[]
  active: string
  status?: string
}>()
const emit = defineEmits<{ select: [key: string] }>()

const drawer = ref(false)
function onSelect(key: string) {
  emit('select', key)
  drawer.value = false
}
</script>

<template>
  <div class="os-layout" :class="{ mobile: isMobile }">
    <!-- 移动形态：左上角汉堡开关（浮于内容之上）+ 抽屉遮罩 -->
    <button v-if="isMobile" class="nav-fab" :class="{ on: drawer }" title="导航" @click="drawer = !drawer">
      <svg viewBox="0 0 20 20" width="18" height="18"><path d="M3 5h14M3 10h14M3 15h14" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/></svg>
    </button>
    <div v-if="isMobile && drawer" class="nav-mask" @click="drawer = false" />
    <aside class="os-sidebar" :class="{ open: isMobile && drawer }">
      <div class="win-badge">{{ win.title }}</div>
      <button
        v-for="it in items" :key="it.key"
        :data-key="it.key"
        :class="{ active: active === it.key }"
        @click="onSelect(it.key)"
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

/* —— 移动形态：侧栏退为抽屉（左滑入 + 遮罩），汉堡浮钮在内容左上 —— */
.os-layout.mobile { position: relative; }
.os-layout.mobile .os-sidebar {
  position: absolute; z-index: 30; left: 0; top: 0; bottom: 0;
  width: min(76vw, 300px); flex: none;
  transform: translateX(-103%); transition: transform .24s var(--os-ease-out);
}
.os-layout.mobile .os-sidebar.open { transform: none; box-shadow: var(--os-shadow); }
.os-layout.mobile .os-sidebar button { padding: 11px 12px; font-size: 14px; }
.nav-fab {
  position: absolute; z-index: 40; left: 12px; top: 10px;
  width: 40px; height: 40px; border-radius: 50%; border: 1px solid var(--os-border);
  background: var(--os-panel); color: var(--os-text); cursor: pointer;
  backdrop-filter: blur(16px); -webkit-backdrop-filter: blur(16px);
  display: flex; align-items: center; justify-content: center;
  box-shadow: var(--os-shadow-sm);
}
.nav-mask { position: absolute; inset: 0; z-index: 25; background: rgba(0, 0, 0, .32); animation: os-fade-in .14s ease; }
/* 移动内容区给浮钮留出视觉空间（内容从钮下穿过，标题不被压字） */
.os-layout.mobile .os-content { padding: 54px 16px 16px; }
</style>
