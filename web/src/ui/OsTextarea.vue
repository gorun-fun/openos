<script setup lang="ts">
const model = defineModel<string>()
withDefaults(defineProps<{
  placeholder?: string
  rows?: number
  mono?: boolean
  disabled?: boolean
  maxlength?: number
}>(), { rows: 3 })
</script>

<template>
  <textarea
    v-model="model"
    class="os-textarea"
    :class="{ mono }"
    :placeholder="placeholder"
    :rows="rows"
    :disabled="disabled"
    :maxlength="maxlength"
  />
</template>

<style scoped>
.os-textarea {
  padding: 6px 10px; border-radius: 8px; font-size: 13px; width: 100%; min-width: 0;
  border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text);
  font-family: inherit; line-height: 1.5; resize: vertical;
  transition: border-color .14s, box-shadow .14s;
}
.os-textarea:hover:not(:focus) { border-color: var(--os-text-secondary); }
.os-textarea:focus { border-color: var(--os-accent); box-shadow: var(--os-focus-ring); outline: none; }
.os-textarea:disabled { opacity: .5; }
.mono { font-family: var(--os-mono); }
</style>
