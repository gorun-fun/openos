<script setup lang="ts">
defineProps<{ title: string; okText?: string; cancelText?: string }>()
const emit = defineEmits<{ ok: []; cancel: [] }>()
</script>

<template>
  <div class="os-modal-mask" @pointerdown.self="emit('cancel')">
    <div class="os-modal">
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
@keyframes modal-in {
  from { opacity: 0; transform: scale(.92) translateY(8px); }
  to { opacity: 1; transform: scale(1) translateY(0); }
}
h3 { font-size: 14.5px; margin-bottom: 10px; }
.content { font-size: 13px; color: var(--os-text-secondary); margin-bottom: 16px; }
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
