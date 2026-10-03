<script setup lang="ts">
// MobileNavToggle：手写左侧导航应用（如文件）的移动形态通用开关——
// 左上角汉堡浮钮 + 抽屉遮罩 + 点击侧栏导航项后自动收起。
// 配合全局 .os-side 约定（base.css 移动媒体查询）：
//   侧栏元素加 `os-side` 类 + 展开态 `os-side-open`，其定位容器加 `os-has-side`。
// 桌面形态整个组件由 CSS 隐藏（display:none），不参与布局与渲染语义。
import { onBeforeUnmount, onMounted } from 'vue'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ 'update:open': [v: boolean] }>()

function toggle() { emit('update:open', !props.open) }
function close() { if (props.open) emit('update:open', false) }

// 抽屉开着时点其内部内容即收起（移动优先取舍：goder 等应用的导航条目是
// div+click 而非 button，无法枚举可交互元素；滚动不产生 click，误收面小）。
// 例外：聚焦输入控件不收（左栏搜索框等，输完点条目/遮罩再收）。
// 用 composedPath 而非 closest——shadow DOM 应用（goder 等 web component）的点击
// 冒泡到 document 时 target 已重定向为宿主元素，closest 穿不进 shadow；composedPath
// 返回完整路径（含 shadow 内部节点），光 DOM 与 shadow DOM 同一实现覆盖
function onDocClick(e: Event) {
  if (!props.open) return
  const els = (e.composedPath() as EventTarget[]).filter(
    (el): el is HTMLElement => el instanceof HTMLElement,
  )
  if (!els.some((el) => el.classList.contains('os-side'))) return
  const t = els[0] // 真实点击目标（shadow 内部节点）
  if (t.matches('input, textarea, select, [contenteditable="true"]')) return
  if (t.classList.contains('os-side')) return // 点抽屉背景板不收
  close()
}
onMounted(() => document.addEventListener('click', onDocClick))
onBeforeUnmount(() => document.removeEventListener('click', onDocClick))
</script>

<template>
  <button class="os-nav-fab" :class="{ on: open }" title="导航" @click.stop="toggle">
    <svg viewBox="0 0 20 20" width="18" height="18"><path d="M3 5h14M3 10h14M3 15h14" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/></svg>
  </button>
  <div v-if="open" class="os-side-mask" @click="close" />
</template>

<style scoped>
/* 结构性样式在 base.css 的移动媒体查询里（.os-nav-fab/.os-side-mask）；
   这里只保证桌面形态隐藏——组件约定仅移动形态可见 */
@media (min-width: 768px) {
  .os-nav-fab, .os-side-mask { display: none !important; }
}
</style>
