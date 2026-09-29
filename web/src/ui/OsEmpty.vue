<script setup lang="ts">
defineProps<{ text?: string; icon?: string }>()
</script>

<template>
  <div class="os-empty">
    <div class="ic">{{ icon || '📭' }}</div>
    <p>{{ text || '空空如也' }}</p>
    <slot />
  </div>
</template>

<style scoped>
.os-empty { text-align: center; padding: 36px 16px; color: var(--os-text-secondary); }
.ic { font-size: 36px; margin-bottom: 8px; }
p { font-size: 13px; }
</style>
