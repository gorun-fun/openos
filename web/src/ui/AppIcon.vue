<script setup lang="ts">
import { computed } from 'vue'
import { iconOf } from '../desktop/icons'

const props = withDefaults(defineProps<{ app: string; size?: number }>(), { size: 44 })
const def = computed(() => iconOf(props.app))
</script>

<template>
  <!-- 图片图标（如 goder 官方图标）：同样圆角 + 投影，视觉与渐变方块一致 -->
  <img
    v-if="def.src"
    class="app-icon app-icon-img"
    :src="def.src"
    :alt="def.label || ''"
    :style="{ width: size + 'px', height: size + 'px' }"
    draggable="false"
  >
  <span
    v-else
    class="app-icon"
    :style="{
      width: size + 'px', height: size + 'px',
      background: def.grad,
      fontSize: Math.round(size * 0.48) + 'px',
    }"
  >{{ def.symbol }}</span>
</template>

<style scoped>
.app-icon {
  display: inline-flex; align-items: center; justify-content: center;
  border-radius: 23%; color: #fff; flex: none;
  box-shadow: inset 0 1px 0 rgba(255,255,255,.35), 0 2px 6px rgba(0,0,0,.25);
  font-weight: 700; line-height: 1;
  font-family: var(--os-mono);
  text-shadow: 0 1px 2px rgba(0,0,0,.3);
}
.app-icon-img { display: block; object-fit: cover; }
</style>
