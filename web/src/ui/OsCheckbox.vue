<script setup lang="ts">
const value = defineModel<boolean>()
defineProps<{ label?: string; disabled?: boolean }>()
</script>

<template>
  <label class="os-checkbox" :class="{ disabled }">
    <input v-model="value" type="checkbox" :disabled="disabled" />
    <span v-if="label" class="lab">{{ label }}</span>
  </label>
</template>

<style scoped>
.os-checkbox { display: inline-flex; align-items: center; gap: 7px; cursor: pointer; font-size: 13px; }
.os-checkbox.disabled { opacity: .45; cursor: default; }
input[type='checkbox'] {
  appearance: none; -webkit-appearance: none; width: 16px; height: 16px; margin: 0; flex: none;
  border: 1.5px solid var(--os-border-strong); border-radius: 4.5px;
  background: var(--os-input-bg); cursor: pointer; position: relative;
  transition: border-color .14s, background .14s, box-shadow .14s;
}
input[type='checkbox']:hover:not(:disabled) { border-color: var(--os-text-secondary); }
input[type='checkbox']:focus-visible { border-color: var(--os-accent); box-shadow: var(--os-focus-ring); outline: none; }
input[type='checkbox']:checked { background: var(--os-accent); border-color: var(--os-accent); }
input[type='checkbox']:checked::after {
  content: ''; position: absolute; left: 4.5px; top: 1.5px; width: 4px; height: 8px;
  border: solid #fff; border-width: 0 2px 2px 0; transform: rotate(45deg);
}
.lab { line-height: 1.3; }
</style>
