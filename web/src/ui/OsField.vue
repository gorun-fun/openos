<script setup lang="ts">
// OsField：表单字段外壳——标签 + 控件插槽 + 校验错误展示（错误来自最近的 OsForm）。
import { computed, inject, type Ref } from 'vue'

const props = defineProps<{
  label?: string
  /** 对应 OsForm model 的字段名（错误信息按键展示）；不传则纯布局 */
  prop?: string
  /** 标签上加必填星号（仅视觉提示，校验仍以 rules 为准） */
  required?: boolean
}>()

const form = inject<{ errors: Record<string, string>; labelWidth: Ref<string> } | null>('os-form', null)
const error = computed(() => (props.prop && form ? form.errors[props.prop] || '' : ''))
const labelW = computed(() => form?.labelWidth.value || '88px')
</script>

<template>
  <div class="os-field" :class="{ invalid: !!error }">
    <label v-if="label" class="lab" :style="{ width: labelW }">
      <span v-if="required" class="req">*</span>{{ label }}
    </label>
    <div class="body">
      <slot />
      <div v-if="error" class="err">{{ error }}</div>
    </div>
  </div>
</template>

<style scoped>
.os-field { display: flex; align-items: flex-start; gap: 10px; min-width: 0; }
.lab {
  flex: none; font-size: 12.5px; color: var(--os-text-secondary);
  padding-top: 7px; text-align: right; line-height: 1.4;
}
.req { color: #ff453a; margin-right: 2px; }
.body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
.err { font-size: 12px; color: #e5484d; line-height: 1.3; }
</style>
