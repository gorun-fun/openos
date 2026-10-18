<script setup lang="ts">
// 原位重命名输入块（图标/列表/分栏三视图共用）：挂载即聚焦并全选（Finder 体验）；
// Enter/失焦提交、Esc 取消、点击不冒泡（不触发条目点选）。提交/取消语义由父级
// startRename/commitRename 落地（取消后卸载不再触发 blur 提交，与原实现一致）。
import { onMounted, ref } from 'vue'

defineProps<{ modelValue: string }>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: string): void
  (e: 'commit'): void
  (e: 'cancel'): void
}>()
const el = ref<HTMLInputElement>()
onMounted(() => { el.value?.focus(); el.value?.select() })
</script>

<template>
  <input ref="el" class="rn-input os-flat" :value="modelValue"
    @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)"
    @keydown.enter.prevent="emit('commit')" @keydown.esc.stop.prevent="emit('cancel')"
    @blur="emit('commit')" @click.stop />
</template>

<style scoped>
.rn-input {
  font-size: 12px; width: 92px; border: 1.5px solid var(--os-accent); border-radius: 5px;
  background: var(--os-panel-solid); color: var(--os-text); padding: 1px 4px; text-align: center;
}
</style>
