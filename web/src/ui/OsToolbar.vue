<script setup lang="ts">
// OsToolbar：应用顶部工具条（文档/表格等编辑类应用使用）。
</script>

<template>
  <div class="os-toolbar">
    <slot />
  </div>
</template>

<style scoped>
.os-toolbar {
  flex: none; display: flex; align-items: center; gap: 4px; padding: 6px 10px; flex-wrap: wrap;
  border-bottom: 1px solid var(--os-border); background: var(--os-window-head);
}
.os-toolbar :deep(button) {
  min-width: 30px; height: 28px; border: none; border-radius: 7px; background: transparent;
  color: var(--os-text); font-size: 13px; cursor: pointer; padding: 0 8px;
}
.os-toolbar :deep(button:hover) { background: var(--os-hover); }
.os-toolbar :deep(button.on) { background: var(--os-selected); }
.os-toolbar :deep(.sep) { width: 1px; height: 18px; background: var(--os-border-strong); margin: 0 4px; }
.os-toolbar :deep(.grow) { flex: 1; }
</style>
