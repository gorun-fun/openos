<script setup lang="ts">
// OsStatusDot — 状态圆点（健康/在线/离线指示，P4 自 grf-os MenubarExtras 的
// 自绘健康点收敛而来）。tone：ok=绿 / warn=橙 / err=红 / neutral=灰 / accent=蓝。
defineProps<{
  tone?: 'ok' | 'warn' | 'err' | 'neutral' | 'accent'
  title?: string
}>()
</script>

<template>
  <span class="os-dot" :class="tone || 'neutral'" :title="title || undefined" role="img" :aria-label="title || tone || 'status'" />
</template>

<style scoped>
.os-dot {
  display: inline-block; width: 8px; height: 8px; border-radius: 50%;
  flex: none; background: var(--os-text-secondary); opacity: .55;
}
.os-dot.ok { background: #30a46c; opacity: 1; }
.os-dot.warn { background: #ff9f0a; opacity: 1; }
.os-dot.err { background: #e5484d; opacity: 1; }
.os-dot.accent { background: var(--os-accent); opacity: 1; }
</style>
