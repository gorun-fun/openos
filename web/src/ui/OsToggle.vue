<script setup lang="ts">
const value = defineModel<boolean>()
defineProps<{ label?: string }>()
</script>

<template>
  <label class="os-toggle">
    <span v-if="label" class="lab">{{ label }}</span>
    <button type="button" class="track" :class="{ on: value }" @click="value = !value" role="switch">
      <span class="thumb" />
    </button>
  </label>
</template>

<style scoped>
.os-toggle { display: inline-flex; align-items: center; gap: 8px; cursor: pointer; }
.lab { font-size: 13px; }
.track {
  width: 38px; height: 22px; border-radius: 11px; border: none; position: relative;
  background: var(--os-active); transition: background .18s ease; cursor: pointer; padding: 0;
  box-shadow: inset 0 1px 2px rgba(0,0,0,.12);
}
.track.on { background: #30d158; box-shadow: 0 1px 4px rgba(48,209,88,.35); }
.thumb {
  position: absolute; top: 2px; left: 2px; width: 18px; height: 18px; border-radius: 50%;
  background: #fff; box-shadow: 0 1px 3px rgba(0,0,0,.3), 0 0 0 0.5px rgba(0,0,0,.06);
  /* 弹性滑动：贴近原生 iOS/macOS 开关手感 */
  transition: left .2s var(--os-ease-spring);
}
.track.on .thumb { left: 18px; }
</style>
