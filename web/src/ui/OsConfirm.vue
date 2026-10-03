<script setup lang="ts">
// 统一确认弹窗的弹窗体（经 ui/confirm.ts 的 osConfirm 拉起，渲染宿主 DialogHost）：
// 标题 + 正文 + 右下「取消/确认」按钮排。danger（删除类）时确认按钮红色、
// 默认焦点落在取消按钮防回车误触；普通确认聚焦确认按钮（回车即确认，Esc 走 DialogHost 取消）。
import { onMounted, ref } from 'vue'
import OsButton from './OsButton.vue'

const props = defineProps<{
  params: { title?: string; body: string; danger?: boolean; confirmText?: string; cancelText?: string }
  close: (v?: any) => void
  cancel: () => void
}>()

const cancelBtn = ref<InstanceType<typeof OsButton> | null>(null)
const confirmBtn = ref<InstanceType<typeof OsButton> | null>(null)
onMounted(() => {
  ;(props.params.danger ? cancelBtn.value : confirmBtn.value)?.$el?.focus()
})
</script>

<template>
  <div class="os-confirm">
    <div v-if="params.title" class="ttl">{{ params.title }}</div>
    <div class="bd">{{ params.body }}</div>
    <div class="acts">
      <OsButton ref="cancelBtn" size="sm" @click="cancel()">{{ params.cancelText || '取消' }}</OsButton>
      <OsButton ref="confirmBtn" size="sm" :variant="params.danger ? 'danger' : 'primary'"
                @click="close(true)">{{ params.confirmText || '确认' }}</OsButton>
    </div>
  </div>
</template>

<style scoped>
.os-confirm { display: flex; flex-direction: column; gap: 14px; }
.ttl { font-size: 14px; font-weight: 600; line-height: 1.4; }
.bd { font-size: 13px; color: var(--os-text-secondary); line-height: 1.6; white-space: pre-line; }
.acts { display: flex; justify-content: flex-end; gap: 8px; margin-top: 2px; }
</style>
