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
  background: var(--os-active); transition: background .15s; cursor: pointer; padding: 0;
}
.track.on { background: #30d158; }
.thumb {
  position: absolute; top: 2px; left: 2px; width: 18px; height: 18px; border-radius: 50%;
  background: #fff; box-shadow: 0 1px 3px rgba(0,0,0,.3); transition: left .15s;
}
.track.on .thumb { left: 18px; }
</style>
