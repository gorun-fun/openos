<script setup lang="ts">
// OsForm：表单容器——按 rules 集中校验并收集错误（由 OsField 展示）。
//
// 用法：
//   const form = ref()
//   <OsForm ref="form" :model="m" :rules="rules" @submit="save">
//     <OsField label="用户名" prop="username" required>
//       <OsInput v-model="m.username" />
//     </OsField>
//     <OsButton variant="primary" type="submit">保存</OsButton>
//   </OsForm>
//   // 或手动触发：if (form.value!.validate()) save()
import { computed, provide, reactive } from 'vue'

export interface FormRule {
  /** 必填（空串 / null / undefined / 空数组视为未填） */
  required?: boolean
  /** 字符串按长度、数值按大小比较 */
  min?: number
  max?: number
  pattern?: RegExp
  /** 自定义校验：返回 true 表示通过，否则返回错误提示 */
  validator?: (value: any, model: Record<string, any>) => string | true
  /** 该规则失败时的提示（缺省用内置文案） */
  message?: string
}
export type FormRules = Record<string, FormRule[]>

const props = withDefaults(defineProps<{
  model: Record<string, any>
  rules?: FormRules
  /** 标签宽度（OsField 对齐用） */
  labelWidth?: string
}>(), { labelWidth: '88px' })

const emit = defineEmits<{ submit: [] }>()

// 错误状态注入给 OsField；labelWidth 同时下发保持标签对齐
const errors = reactive<Record<string, string>>({})
provide('os-form', { errors, labelWidth: computed(() => props.labelWidth) })

function isEmpty(v: any) {
  return v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0)
}

/** 校验全部字段：全部通过返回 true，错误信息写入各 OsField。 */
function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]
  for (const [key, rules] of Object.entries(props.rules || {})) {
    const v = props.model[key]
    for (const r of rules) {
      let msg = ''
      if (r.required && isEmpty(v)) {
        msg = r.message || '必填项'
      } else if (!isEmpty(v)) {
        if (r.min !== undefined) {
          const bad = typeof v === 'number' ? v < r.min : String(v).length < r.min
          if (bad) msg = r.message || (typeof v === 'number' ? `不能小于 ${r.min}` : `至少 ${r.min} 个字符`)
        }
        if (!msg && r.max !== undefined) {
          const bad = typeof v === 'number' ? v > r.max : String(v).length > r.max
          if (bad) msg = r.message || (typeof v === 'number' ? `不能大于 ${r.max}` : `最多 ${r.max} 个字符`)
        }
        if (!msg && r.pattern && !r.pattern.test(String(v))) msg = r.message || '格式不正确'
        if (!msg && r.validator) {
          const ret = r.validator(v, props.model)
          if (ret !== true) msg = ret || r.message || '校验未通过'
        }
      }
      if (msg) { errors[key] = msg; break } // 每字段只展示第一条错误
    }
  }
  return Object.keys(errors).length === 0
}

/** 清空全部校验错误。 */
function clearValidate() {
  for (const k of Object.keys(errors)) delete errors[k]
}

function onSubmit() {
  if (validate()) emit('submit')
}

defineExpose({ validate, clearValidate })
</script>

<template>
  <form class="os-form" @submit.prevent="onSubmit"><slot /></form>
</template>

<style scoped>
.os-form { display: flex; flex-direction: column; gap: 12px; min-width: 0; }
</style>
