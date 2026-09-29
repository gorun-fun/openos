<script setup lang="ts">
const model = defineModel<string>()
defineProps<{ tabs: { key: string; label: string }[] }>()
</script>

<template>
  <div class="os-tabs">
    <button
      v-for="t in tabs" :key="t.key"
      :class="{ active: model === t.key }"
      @click="model = t.key"
    >{{ t.label }}</button>
  </div>
</template>

<style scoped>
.os-tabs { display: flex; gap: 2px; background: var(--os-active); border-radius: 9px; padding: 2px; width: fit-content; }
button {
  border: none; background: transparent; padding: 5px 14px; border-radius: 7px;
  font-size: 12.5px; color: var(--os-text-secondary); cursor: pointer; transition: all .15s;
}
button.active { background: var(--os-panel-solid); color: var(--os-text); font-weight: 600; box-shadow: 0 1px 4px rgba(0,0,0,.12); }
</style>
