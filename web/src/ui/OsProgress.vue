<script setup lang="ts">
// OsProgress — 进度条（P5 补组件缺口）：value 0-100 数值形态；未传 value 为
// 不确定态（indeterminate 动画）。
const props = defineProps<{
  /** 进度百分比 0-100；缺省 = 不确定态 */
  value?: number
  /** 高度 px（默认 6） */
  height?: number
}>()
const pct = () => Math.max(0, Math.min(100, Number(props.value ?? 0)))
</script>

<template>
  <div
    class="os-progress"
    :class="{ ind: value == null }"
    :style="{ height: (height || 6) + 'px' }"
    role="progressbar"
    :aria-valuenow="value != null ? pct() : undefined"
    aria-valuemin="0"
    aria-valuemax="100"
  >
    <div v-if="value != null" class="bar" :style="{ width: pct() + '%' }" />
    <div v-else class="bar ind-bar" />
  </div>
</template>

<style scoped>
.os-progress {
  width: 100%; border-radius: 999px; overflow: hidden; flex: none;
  background: color-mix(in srgb, var(--os-text) 12%, transparent);
}
.bar { height: 100%; border-radius: 999px; background: var(--os-accent-grad); transition: width .25s var(--os-ease-out); }
.ind-bar {
  width: 34%; animation: os-prog 1.1s var(--os-ease-out) infinite;
}
@keyframes os-prog { 0% { margin-left: -34% } 100% { margin-left: 100% } }
@media (prefers-reduced-motion: reduce) { .ind-bar { animation: none; margin-left: 0; width: 100%; opacity: .5 } }
</style>
