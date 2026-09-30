<script setup lang="ts">
const model = defineModel<string>()
defineProps<{ options: { value: string; label: string }[] }>()
</script>

<template>
  <select v-model="model" class="os-select">
    <option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option>
  </select>
</template>

<style scoped>
.os-select {
  padding: 6px 10px; border-radius: 8px; font-size: 13px;
  border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text);
  cursor: pointer; transition: border-color .14s, box-shadow .14s;
}
.os-select:hover:not(:focus) { border-color: var(--os-text-secondary); }
.os-select:focus { border-color: var(--os-accent); box-shadow: var(--os-focus-ring); outline: none; }
</style>
