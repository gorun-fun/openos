<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { iconOf, APP_ICONS } from '../desktop/icons'

const props = withDefaults(defineProps<{ app: string; icon?: string; size?: number }>(), { size: 44 })
// 图片图标（如网页应用的 favicon 直连）加载失败时回落到 default，避免破图
const srcError = ref(false)
watch(() => [props.app, props.icon], () => { srcError.value = false })
const def = computed(() => {
  const d = iconOf(props.app, props.icon)
  return srcError.value && d.src ? APP_ICONS.default : d
})
</script>

<template>
  <!-- 图片图标（如 goder 官方图标）：同样圆角 + 投影，视觉与 SVG 图标一致 -->
  <img
    v-if="def.src"
    class="app-icon app-icon-img"
    :src="def.src"
    :alt="def.label || ''"
    :style="{ width: size + 'px', height: size + 'px' }"
    draggable="false"
    @error="srcError = true"
  >
  <!-- SVG 图标：容器统一负责圆角裁切与投影，SVG 本体铺满缩放 -->
  <span
    v-else-if="def.svg"
    class="app-icon app-icon-svg"
    :style="{ width: size + 'px', height: size + 'px' }"
    v-html="def.svg"
  />
  <!-- 兜底：图标定义异常时的中性占位 -->
  <span
    v-else
    class="app-icon app-icon-fallback"
    :style="{ width: size + 'px', height: size + 'px' }"
  />
</template>

<style scoped>
.app-icon { display: block; flex: none; }
/* 图片图标（goder 官方黑底图标）：1px 亮边避免在深色壁纸上圆角轮廓消失 */
.app-icon-img {
  object-fit: cover; border-radius: 23%; box-sizing: border-box;
  border: 1px solid rgba(255,255,255,.16);
  box-shadow: 0 1px 2px rgba(0,0,0,.18), 0 4px 10px rgba(0,0,0,.22);
}
.app-icon-svg {
  border-radius: 23%; overflow: hidden;
  box-shadow: 0 1px 2px rgba(0,0,0,.18), 0 4px 10px rgba(0,0,0,.22);
}
.app-icon-svg :deep(svg) { display: block; width: 100%; height: 100%; }
.app-icon-fallback {
  border-radius: 23%;
  background: linear-gradient(145deg, #c3c7cf, #8e939e);
  box-shadow: 0 1px 2px rgba(0,0,0,.18), 0 4px 10px rgba(0,0,0,.22);
}
</style>
