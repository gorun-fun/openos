<script setup lang="ts">
// OsToasts：全局轻提示容器（App 根部挂载一次），由 ui/toast 的 toast() 驱动。
import { toastState } from './toast'

const state = toastState()
</script>

<template>
  <div class="os-toasts">
    <transition-group name="os-toast">
      <div v-for="t in state.list" :key="t.id" class="os-toast os-glass" :class="t.type">
        <span v-if="t.type === 'success'" class="dot ok" />
        <span v-else-if="t.type === 'error'" class="dot err" />
        {{ t.text }}
      </div>
    </transition-group>
  </div>
</template>

<style scoped>
.os-toasts {
  position: fixed; top: 44px; left: 50%; transform: translateX(-50%);
  display: flex; flex-direction: column; gap: 8px; z-index: 9800; align-items: center;
  pointer-events: none;
}
.os-toast {
  padding: 9px 20px; border-radius: 999px; border: 1px solid var(--os-border);
  font-size: 13px; box-shadow: var(--os-shadow-sm), inset 0 1px 0 var(--os-highlight-edge);
  display: flex; align-items: center; gap: 8px; max-width: 70vw;
}
.dot { width: 7px; height: 7px; border-radius: 50%; flex: none; }
.dot.ok { background: #30d158; box-shadow: 0 0 6px rgba(48, 209, 88, .6); }
.dot.err { background: #ff453a; box-shadow: 0 0 6px rgba(255, 69, 58, .6); }
.os-toast.error { border-color: rgba(229, 72, 77, .45); }
.os-toast-enter-active, .os-toast-leave-active { transition: all .25s ease; }
.os-toast-enter-from, .os-toast-leave-to { opacity: 0; transform: translateY(-10px); }
</style>
