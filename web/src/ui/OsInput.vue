<script setup lang="ts">
const model = defineModel<string | number>()
withDefaults(defineProps<{
  type?: string; placeholder?: string; mono?: boolean
}>(), { type: 'text' })
</script>

<template>
  <input
    v-model="model"
    :type="type"
    :placeholder="placeholder"
    class="os-input"
    :class="{ mono }"
  />
</template>

<style scoped>
.os-input {
  padding: 6px 10px; border-radius: 8px; font-size: 13px; min-width: 0;
  border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text);
  transition: border-color .14s, box-shadow .14s;
}
.os-input:hover:not(:focus) { border-color: var(--os-text-secondary); }
.os-input:focus { border-color: var(--os-accent); box-shadow: var(--os-focus-ring); }
.mono { font-family: var(--os-mono); }
</style>
