<script setup lang="ts">
// OsChip — 信息/状态胶囊（菜单栏扩展、列表标签、工具栏元信息的统一形态，
// P4 自 grf 宿主 MenubarExtras 的自绘 chip 收敛而来）。
// tone 语义色（accent/ok/warn/err/neutral）；title 走全局 tooltip。
const props = defineProps<{
  tone?: 'accent' | 'ok' | 'warn' | 'err' | 'neutral'
  title?: string
}>()
void props
</script>

<template>
  <span class="os-chip" :class="tone || 'neutral'" :title="title || undefined">
    <slot />
  </span>
</template>

<style scoped>
.os-chip {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 2px 9px; border-radius: 999px;
  font-size: 12px; font-weight: 600; line-height: 18px;
  border: 1px solid var(--os-border);
  background: var(--os-panel-solid);
  color: var(--os-text);
  white-space: nowrap;
}
.os-chip.accent { color: var(--os-accent); border-color: color-mix(in srgb, var(--os-accent) 35%, transparent); background: color-mix(in srgb, var(--os-accent) 10%, transparent); }
.os-chip.ok { color: #30a46c; border-color: rgba(48, 164, 108, .35); background: rgba(48, 164, 108, .1); }
.os-chip.warn { color: #b25b00; border-color: rgba(255, 159, 10, .4); background: rgba(255, 159, 10, .12); }
.os-chip.err { color: #e5484d; border-color: rgba(229, 72, 77, .35); background: rgba(229, 72, 77, .1); }
.os-chip.neutral { color: var(--os-text-secondary); }
</style>
