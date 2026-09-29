<script setup lang="ts">
defineProps<{ title?: string }>()
</script>

<template>
  <section class="os-panel">
    <header v-if="title">{{ title }}</header>
    <div class="os-panel-body"><slot /></div>
  </section>
</template>

<style scoped>
.os-panel {
  background: var(--os-panel-solid); border: 1px solid var(--os-border);
  border-radius: var(--os-radius); overflow: hidden;
}
header {
  padding: 10px 14px; font-size: 13px; font-weight: 600;
  border-bottom: 1px solid var(--os-border); background: var(--os-sidebar);
}
.os-panel-body { padding: 14px; }
</style>
