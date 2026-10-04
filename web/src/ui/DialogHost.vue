<script setup lang="ts">
// 跨应用弹窗的全局渲染宿主：Desktop 根部挂载一份（独立部署与宿主组壳共用），
// 把 dialogService 栈中的弹窗渲染为系统级模态浮层——遮罩压暗整个桌面、
// 卡片居中（视觉语言与 OsModal 一致），Esc/点遮罩默认取消最上层弹窗。
import { computed, onBeforeUnmount, onMounted } from 'vue'
import { dialogStack, dialogRegistry, type DialogState } from './dialogService'
import { onKey, KEY_PRIORITY, type KeyCtx } from './keys'

// 组件在 registerDialog 时已解析（loader → defineAsyncComponent），此处直接取用
function compOf(d: DialogState) {
  return dialogRegistry.get(d.dialogId)?.component
}

const top = computed(() => dialogStack[dialogStack.length - 1])
// 键盘：统一键盘中枢的 dialog 层（最高优先级，见 ui/keys.ts）——Esc 取消最上层
// 弹窗（escClose 时），Tab 焦点圈锁在最上层卡片内（模态语义，不跑到弹窗后方）
function onKeydown({ info }: KeyCtx) {
  const t = top.value
  if (!t) return false
  if (info.key === 'Escape' && t.escClose) { t.cancel(); return true }
  if (info.key === 'Tab') { trapTab(info.shift); return true }
  return false
}
let offKeys: (() => void) | undefined
onMounted(() => { offKeys = onKey('capture', KEY_PRIORITY.dialog, onKeydown) })
onBeforeUnmount(() => { offKeys?.(); offKeys = undefined })

/** Tab 焦点圈：焦点在卡片内时首尾环绕，在卡片外（或无处）时收进首个可聚焦元素 */
function trapTab(backward: boolean) {
  const cards = document.querySelectorAll<HTMLElement>('.os-dialog')
  const card = cards[cards.length - 1]
  if (!card) return
  const focusables = [...card.querySelectorAll<HTMLElement>(
    'button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [href], [tabindex]:not([tabindex="-1"])',
  )].filter((el) => el.getClientRects().length)
  if (!focusables.length) return
  const idx = focusables.indexOf(document.activeElement as HTMLElement)
  const next = backward
    ? focusables[idx <= 0 ? focusables.length - 1 : idx - 1]
    : focusables[idx === focusables.length - 1 ? 0 : idx + 1]
  next.focus({ preventScroll: true })
}

// 弹窗打开后把焦点移入卡片内首个输入控件（无输入类控件则不动，避免按钮焦点环）
function onCardMount(el: any) {
  if (!(el instanceof HTMLElement)) return
  requestAnimationFrame(() => el.querySelector<HTMLElement>('input,textarea,select')?.focus({ preventScroll: true }))
}
</script>

<template>
  <div v-for="d in dialogStack" :key="d.id" class="os-dialog-mask" :style="{ zIndex: d.z }"
       @pointerdown.self="d.backdropClose && d.cancel()">
    <div class="os-dialog" :style="{ width: d.width + 'px' }" :ref="onCardMount" role="dialog" aria-modal="true" :aria-label="d.title">
      <header v-if="d.title">
        <span class="os-dialog-title">{{ d.title }}</span>
        <button class="os-dialog-x" title="关闭" aria-label="关闭" @click="d.cancel()"></button>
      </header>
      <div class="os-dialog-body">
        <component v-if="compOf(d)" :is="compOf(d)" :params="d.params" :close="d.close" :cancel="d.cancel" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.os-dialog-mask {
  position: fixed; inset: 0; background: rgba(0, 0, 0, .3);
  backdrop-filter: blur(3px); -webkit-backdrop-filter: blur(3px);
  display: flex; align-items: center; justify-content: center;
  animation: os-fade-in .14s;
}
.os-dialog {
  max-width: 90vw; max-height: 82vh; display: flex; flex-direction: column;
  background: var(--os-panel-solid); border: 1px solid var(--os-border);
  border-radius: 16px; box-shadow: var(--os-shadow), inset 0 1px 0 var(--os-highlight-edge);
  overflow: hidden;
  animation: os-dialog-in .22s var(--os-ease-spring);
}
@keyframes os-dialog-in {
  from { opacity: 0; transform: scale(.94) translateY(8px); }
  to { opacity: 1; transform: scale(1) translateY(0); }
}
header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 12px 16px 0 20px; flex-shrink: 0;
}
.os-dialog-title { font-size: 13px; font-weight: 600; }
.os-dialog-x {
  width: 22px; height: 22px; border: none; border-radius: 6px; cursor: pointer;
  background: var(--os-active); position: relative; flex-shrink: 0;
}
.os-dialog-x::before, .os-dialog-x::after {
  content: ''; position: absolute; left: 6px; top: 10px; width: 10px; height: 1.5px;
  background: var(--os-text-secondary); border-radius: 1px;
}
.os-dialog-x::before { transform: rotate(45deg); }
.os-dialog-x::after { transform: rotate(-45deg); }
.os-dialog-x:hover { filter: brightness(1.06); }
.os-dialog-body { padding: 16px 20px 20px; overflow: auto; }
/* 无标题头时贴边留白 */
.os-dialog:not(:has(> header)) .os-dialog-body { padding-top: 20px; }
</style>
