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
  padding: 6px 14px; font-size: 13px; transition: all .12s; white-space: nowrap;
}
.os-btn.sm { padding: 3px 10px; font-size: 12px; border-radius: 6px; }
.os-btn:hover:not(:disabled) { background: var(--os-hover); }
.os-btn:active:not(:disabled) { transform: scale(.97); }
.os-btn:disabled { opacity: .45; cursor: default; }
.os-btn.primary {
  background: var(--os-accent); border-color: transparent; color: #fff; font-weight: 600;
}
.os-btn.primary:hover:not(:disabled) { filter: brightness(1.1); background: var(--os-accent); }
.os-btn.danger { background: #e5484d; border-color: transparent; color: #fff; }
.os-btn.ghost { background: transparent; border-color: transparent; }
.os-btn.ghost:hover:not(:disabled) { background: var(--os-hover); }
</style>
