<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { onKey, KEY_PRIORITY, type KeyCtx } from './keys'

defineProps<{ title: string; okText?: string; cancelText?: string }>()
const emit = defineEmits<{ ok: []; cancel: [] }>()

const cardEl = ref<HTMLElement>()

// 键盘：统一键盘中枢 dialog 层（与 DialogHost 同优先级，同优先级 LIFO——后挂载
// 的模态视觉在上层、先获得按键）。Esc 取消；Tab 焦点圈锁在卡片内；Enter 不做
// 默认确认（按钮可 Tab 到达，删除类确认避免回车误触）。窗口内模态仅在焦点位于
// 本窗口时接管（后台窗口的确认弹窗不抢全局按键）。
function onKeydown({ info }: KeyCtx) {
  const card = cardEl.value
  if (!card) return false
  const own = card.closest('.window')
  if (own && !own.contains(document.activeElement)) return false
  if (info.key === 'Escape') { emit('cancel'); return true }
  if (info.key === 'Tab') { trapTab(info.shift); return true }
  return false
}
let offKeys: (() => void) | undefined
onMounted(() => {
  offKeys = onKey('capture', KEY_PRIORITY.dialog, onKeydown)
  // 初始焦点落卡片本身：不抢进按钮/输入框，交由内容语义决定后续（modal 容器
  // 可聚焦，键盘事件不再悬挂在弹窗外的旧焦点元素上）
  cardEl.value?.focus({ preventScroll: true })
})
onBeforeUnmount(() => { offKeys?.(); offKeys = undefined })

/** Tab 焦点圈：可聚焦元素首尾环绕 */
function trapTab(backward: boolean) {
  const card = cardEl.value
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
</script>

<template>
  <div class="os-modal-mask" @pointerdown.self="emit('cancel')">
    <div ref="cardEl" class="os-modal" role="dialog" aria-modal="true" :aria-label="title" tabindex="-1">
      <h3>{{ title }}</h3>
      <div class="content"><slot /></div>
      <footer>
        <slot name="footer">
          <button class="btn cancel" @click="emit('cancel')">{{ cancelText || '取消' }}</button>
          <button class="btn ok" @click="emit('ok')">{{ okText || '好' }}</button>
        </slot>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.os-modal-mask {
  position: absolute; inset: 0; z-index: 100; background: rgba(0,0,0,.3);
  backdrop-filter: blur(3px); -webkit-backdrop-filter: blur(3px);
  display: flex; align-items: center; justify-content: center; animation: os-fade-in .14s;
}
.os-modal {
  min-width: 320px; max-width: 86%; background: var(--os-panel-solid);
  border: 1px solid var(--os-border);
  border-radius: 16px; box-shadow: var(--os-shadow), inset 0 1px 0 var(--os-highlight-edge);
  padding: 20px 22px 16px;
  animation: modal-in .22s var(--os-ease-spring);
}
.os-modal:focus { outline: none; }
/* 移动形态：弹窗占满可用宽度、超高内容可滚动——
   ① min-width 320 在 320~372px 窄屏上贴边无留白，降为 0 由 max-width 控制；
   ② 内容高于窗口时 flex 居中会把上半截裁出滚动区外（经典 flexbox 坑），
      遮罩改可滚动 + 弹窗 margin:auto（空间够时仍居中、不够时归零可滚） */
@media (max-width: 767px) {
  .os-modal-mask { overflow-y: auto; }
  .os-modal {
    min-width: 0; max-width: calc(100% - 24px);
    margin: auto; padding: 16px 14px 12px;
  }
}
@keyframes modal-in {
  from { opacity: 0; transform: scale(.92) translateY(8px); }
  to { opacity: 1; transform: scale(1) translateY(0); }
}
h3 { font-size: 14px; margin-bottom: 8px; text-align: center; }
.content { font-size: 13px; color: var(--os-text-secondary); margin-bottom: 16px; text-align: center; }
footer { display: flex; justify-content: flex-end; gap: 8px; }
.btn {
  padding: 5px 16px; border-radius: 8px; font-size: 13px; cursor: pointer; border: none;
  transition: filter .13s;
}
.btn.cancel { background: var(--os-active); color: var(--os-text); }
.btn.cancel:hover { filter: brightness(1.06); }
.btn.ok {
  background: var(--os-accent-grad); color: #fff; font-weight: 600;
  box-shadow: 0 2px 8px var(--os-accent-soft), inset 0 1px 0 rgba(255,255,255,.25);
}
.btn.ok:hover { filter: brightness(1.08); }
.btn:active { transform: scale(.97); }
</style>
