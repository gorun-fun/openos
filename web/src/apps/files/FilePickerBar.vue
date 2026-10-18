<script setup lang="ts">
// 选择模式底部栏（打开/存储对话框形态的 footer）：save 模式出文件名输入 + 重名提示，
// open 模式出选中文件名；右侧取消/确认（确认按钮文案随模式与重名态变化）。
// 状态与回执语义在 useFilePicker，这里只有展示与事件转发。
import { OsButton, OsInput } from '../../ui'

defineProps<{
  mode: 'open' | 'save'
  name: string // save 模式输入的文件名（v-model:name）
  selName: string // open 模式选中的文件名
  exists: boolean // save 模式目标已存在
  overwrite: boolean // 重名已点过一次（再点确认即覆盖）
  can: boolean // 可确认
}>()
const emit = defineEmits<{
  (e: 'update:name', v: string): void
  (e: 'confirm'): void
  (e: 'cancel'): void
}>()
// OsInput 的 model 是 string|number 联合（输入事件实际恒为字符串），这里收窄回 string
function onName(v: string | number | undefined) { emit('update:name', String(v ?? '')) }
</script>

<template>
  <footer class="status pick-foot">
    <template v-if="mode === 'save'">
      <label class="pk-lb">存储为：</label>
      <OsInput :model-value="name" placeholder="文件名" class="pk-name"
        @update:model-value="onName" @keydown.enter.prevent="emit('confirm')" />
      <span v-if="exists" class="pk-warn">{{ overwrite ? '再点一次「替换」覆盖同名文件' : '同名文件已存在' }}</span>
    </template>
    <span v-else class="pk-sel">{{ selName }}</span>
    <span class="grow" />
    <OsButton size="sm" @click="emit('cancel')">取消</OsButton>
    <OsButton size="sm" variant="primary" :disabled="!can" @click="emit('confirm')">
      {{ mode === 'open' ? '打开' : exists && !overwrite ? '替换' : '存储' }}
    </OsButton>
  </footer>
</template>

<style scoped>
/* 根 footer 的 .status 基础样式由父级 scoped 提供（单根组件继承父作用域）；
   gap 覆盖写成 .status.pick-foot 提高特异度，不依赖父/子样式打包先后 */
.grow { flex: 1; }
.status.pick-foot { gap: 8px; }
.pk-lb { font-size: 12.5px; color: var(--os-text-secondary); white-space: nowrap; }
.pk-name { width: 240px; }
.pk-sel { font-size: 12.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.pk-warn { font-size: 11.5px; color: #e5a00d; white-space: nowrap; }
</style>
