<script setup lang="ts">
// 文件夹图标：浅色圆角底衬 + 前 4 个成员图标组成 2x2 缩略（macOS 风格）。
// 桌面快捷方式文件夹与启动台文件夹共用；应用成员用 AppIcon，文件成员用 emoji。
import { computed } from 'vue'
import AppIcon from './AppIcon.vue'
import { fileEmoji } from '../apps/openWith'

const props = withDefaults(defineProps<{ entries: { kind: 'app' | 'file'; id: string; dir?: boolean }[]; size?: number }>(), { size: 48 })

const shown = computed(() => props.entries.slice(0, 4))
const inner = computed(() => Math.round(props.size * 0.4))
</script>

<template>
  <span class="folder-icon" :style="{ width: size + 'px', height: size + 'px' }">
    <span class="grid">
      <template v-for="e in shown" :key="e.kind + ':' + e.id">
        <span class="cell"><AppIcon v-if="e.kind === 'app'" :app="e.id" :size="inner" /><span v-else class="fi" :style="{ fontSize: Math.round(size * 0.3) + 'px' }">{{ fileEmoji(e.id, e.dir) }}</span></span>
      </template>
    </span>
  </span>
</template>

<style scoped>
.folder-icon {
  display: flex; box-sizing: border-box; padding: 11%;
  border-radius: 23%;
  background: linear-gradient(160deg, rgba(240, 242, 246, .95), rgba(204, 208, 217, .95));
  box-shadow: 0 1px 2px rgba(0, 0, 0, .18), 0 4px 10px rgba(0, 0, 0, .22);
}
/* 深色主题下底衬压暗，避免刺眼 */
html.dark .folder-icon { background: linear-gradient(160deg, rgba(118, 122, 130, .6), rgba(86, 90, 98, .65)); }
.grid { display: grid; grid-template-columns: 1fr 1fr; width: 100%; height: 100%; gap: 8%; }
.cell { display: flex; align-items: center; justify-content: center; min-width: 0; min-height: 0; }
/* 小尺寸下 AppIcon 的固定投影会糊成一片，缩略图统一压平 */
.cell :deep(.app-icon-img), .cell :deep(.app-icon-svg) { box-shadow: none; }
.fi { line-height: 1; }
</style>
