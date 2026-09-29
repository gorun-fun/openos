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
  position: absolute; inset: 0; z-index: 100; background: rgba(0,0,0,.28);
  display: flex; align-items: center; justify-content: center; animation: os-fade-in .12s;
}
.os-modal {
  min-width: 300px; max-width: 86%; background: var(--os-panel-solid);
  border-radius: 14px; box-shadow: var(--os-shadow); padding: 18px 20px 14px;
  animation: os-window-open .16s cubic-bezier(.2,.9,.3,1.2);
}
h3 { font-size: 14px; margin-bottom: 10px; }
.content { font-size: 13px; color: var(--os-text-secondary); margin-bottom: 14px; }
footer { display: flex; justify-content: flex-end; gap: 8px; }
.btn { padding: 5px 16px; border-radius: 8px; font-size: 13px; cursor: pointer; border: none; }
.btn.cancel { background: var(--os-active); color: var(--os-text); }
.btn.ok { background: var(--os-accent); color: #fff; font-weight: 600; }
</style>
