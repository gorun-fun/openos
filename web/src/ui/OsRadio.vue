<script setup lang="ts">
// OsRadioGroup — 单选组（P5 补组件缺口；与 OsSelect 同款 options 形态）。
// v-model = 选中 value；options: {value,label}[]。
defineProps<{
  modelValue: string
  options: { value: string; label: string }[]
  disabled?: boolean
}>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string): void }>()
</script>

<template>
  <div class="os-radio-group" role="radiogroup">
    <label
      v-for="o in options"
      :key="o.value"
      class="os-radio"
      :class="{ on: o.value === modelValue, ro: disabled }"
    >
      <input
        type="radio"
        :value="o.value"
        :checked="o.value === modelValue"
        :disabled="disabled"
        @change="emit('update:modelValue', o.value)"
      />
      <span class="dot" aria-hidden="true" />
      <span class="lb">{{ o.label }}</span>
    </label>
  </div>
</template>

<style scoped>
.os-radio-group { display: inline-flex; flex-wrap: wrap; gap: 6px 16px; }
.os-radio { display: inline-flex; align-items: center; gap: 7px; cursor: pointer; font-size: 13px; }
.os-radio input { position: absolute; opacity: 0; width: 0; height: 0; }
.os-radio .dot {
  width: 16px; height: 16px; border-radius: 50%; flex: none;
  border: 1.5px solid var(--os-border-strong); background: var(--os-panel-solid);
  display: grid; place-items: center; transition: border-color .12s;
}
.os-radio .dot::after {
  content: ''; width: 8px; height: 8px; border-radius: 50%;
  background: var(--os-accent); transform: scale(0); transition: transform .12s var(--os-ease-out);
}
.os-radio.on .dot { border-color: var(--os-accent); }
.os-radio.on .dot::after { transform: scale(1); }
.os-radio.ro { cursor: not-allowed; opacity: .6; }
.os-radio input:focus-visible + .dot { outline: var(--os-focus-ring); outline-offset: 1px; }
</style>
