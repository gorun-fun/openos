<script setup lang="ts">
// StatusChip — GitLab 流水线状态徽章（色语义走 shell-theme.css 的 --grf-* /
// --os-* 变量，明暗两态自适应；无流水线时显示「无 CI」置灰）。
import { computed } from 'vue'
import { statusOf } from './data'

const props = defineProps<{ status?: string | null }>()
const st = computed(() => statusOf(props.status))
</script>

<template>
  <span v-if="st" class="chip" :class="st.cls" :title="status ?? undefined">{{ st.label }}</span>
  <span v-else class="chip none">无 CI</span>
</template>

<style scoped>
.chip { display: inline-block; padding: 1px 8px; border-radius: 8px; font-size: 11px; white-space: nowrap; }
.chip.ok   { color: var(--grf-ok);   background: color-mix(in srgb, var(--grf-ok) 14%, transparent); }
.chip.bad  { color: var(--grf-bad);  background: color-mix(in srgb, var(--grf-bad) 16%, transparent); }
.chip.run  { color: var(--os-accent, #4da3ff); background: color-mix(in srgb, var(--os-accent, #4da3ff) 16%, transparent); }
.chip.wait { color: var(--grf-warn); background: color-mix(in srgb, var(--grf-warn) 15%, transparent); }
.chip.man  { color: var(--grf-violet); background: color-mix(in srgb, var(--grf-violet) 14%, transparent); }
.chip.off  { color: var(--os-text-tertiary); background: var(--os-active); }
.chip.none { color: var(--os-text-tertiary); background: var(--os-active); }
</style>
