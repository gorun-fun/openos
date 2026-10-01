<script setup lang="ts">
// 空状态：默认不显示图标（文字居中即可），显式传 icon 才渲染。
defineProps<{ text?: string; icon?: string }>()
</script>

<template>
  <div class="os-empty">
    <div v-if="icon" class="ic">{{ icon }}</div>
    <p>{{ text || '空空如也' }}</p>
    <slot />
  </div>
</template>

<style scoped>
.os-empty {
  flex: 1; min-height: 140px; text-align: center;
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 8px; padding: 24px 16px; color: var(--os-text-secondary);
}
.ic { font-size: 36px; }
p { font-size: 13px; }
</style>
