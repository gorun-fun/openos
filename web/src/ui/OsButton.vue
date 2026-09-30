<script setup lang="ts">
withDefaults(defineProps<{
  variant?: 'primary' | 'plain' | 'danger' | 'ghost'
  size?: 'sm' | 'md'
  disabled?: boolean
}>(), { variant: 'plain', size: 'md' })
</script>

<template>
  <button class="os-btn" :class="[variant, size]" :disabled="disabled">
    <slot />
  </button>
</template>

<style scoped>
.os-btn {
  border: 1px solid var(--os-border-strong); border-radius: 8px; cursor: pointer;
  background: var(--os-panel-solid); color: var(--os-text);
  padding: 6px 14px; font-size: 13px; transition: all .14s var(--os-ease-out); white-space: nowrap;
  box-shadow: 0 1px 2px rgba(0,0,0,.05), inset 0 1px 0 rgba(255,255,255,.1);
}
.os-btn.sm { padding: 3px 10px; font-size: 12px; border-radius: 6px; }
.os-btn:hover:not(:disabled) { background: var(--os-hover); border-color: var(--os-border-strong); }
.os-btn:active:not(:disabled) { transform: scale(.97); }
.os-btn:disabled { opacity: .45; cursor: default; }
.os-btn.primary {
  background: var(--os-accent-grad); border-color: transparent; color: #fff; font-weight: 600;
  box-shadow: 0 2px 8px var(--os-accent-soft), inset 0 1px 0 rgba(255,255,255,.25);
}
.os-btn.primary:hover:not(:disabled) { background: var(--os-accent-grad); filter: brightness(1.08); }
.os-btn.danger {
  background: linear-gradient(180deg, #f0565b, #e5484d); border-color: transparent; color: #fff;
  box-shadow: 0 2px 8px rgba(229,72,77,.3), inset 0 1px 0 rgba(255,255,255,.22);
}
.os-btn.danger:hover:not(:disabled) { filter: brightness(1.06); }
.os-btn.ghost { background: transparent; border-color: transparent; box-shadow: none; }
.os-btn.ghost:hover:not(:disabled) { background: var(--os-hover); }
</style>
