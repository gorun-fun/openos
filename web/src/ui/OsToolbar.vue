<script setup lang="ts">
// OsToolbar：应用顶部工具条（文档/表格等编辑类应用使用）。
// merged = 融合窗体标题行（配套 appChrome 'merged'）：工具栏即标题栏——
// 左侧 84px 让位悬浮红绿灯、整条承担窗口拖拽（data-os-drag）、title 渲染窗口标题。
defineProps<{ merged?: boolean; title?: string }>()
</script>

<template>
  <div class="os-toolbar" :class="{ 'os-tb': merged }" :data-os-drag="merged || undefined">
    <div v-if="merged && title" class="tb-title" :title="title">{{ title }}</div>
    <slot />
  </div>
</template>

<style scoped>
.os-toolbar {
  flex: none; display: flex; align-items: center; gap: 4px; padding: 6px 10px; flex-wrap: wrap;
  border-bottom: 1px solid var(--os-border); background: var(--os-window-head);
}
.os-toolbar :deep(button) {
  min-width: 30px; height: 28px; border: none; border-radius: 7px; background: transparent;
  color: var(--os-text); font-size: 13px; cursor: pointer; padding: 0 8px;
}
.os-toolbar :deep(button:hover) { background: var(--os-hover); }
.os-toolbar :deep(button.on) { background: var(--os-selected); }
.os-toolbar :deep(.sep) { width: 1px; height: 18px; background: var(--os-border-strong); margin: 0 4px; }
.os-toolbar :deep(.grow) { flex: 1; }
/* 融合标题行：套全局 .os-tb（46px 高 + 拖拽语义），此处显式覆盖 scoped 默认——
   scoped .os-toolbar 特异性高于全局 .os-tb，不覆盖则 padding/换行仍按普通工具条走 */
.os-toolbar.os-tb {
  padding: 0 12px 0 84px; height: 46px; gap: 8px; flex-wrap: nowrap;
  overflow: hidden; white-space: nowrap; user-select: none;
}
.tb-title {
  font-size: 13.5px; font-weight: 600; color: var(--os-text);
  overflow: hidden; text-overflow: ellipsis; max-width: 38%; flex: none;
}
/* 移动形态：融合工具栏放开 nowrap 换行堆叠（与全局 .os-tb 移动块同款——
   scoped 特异性高于全局，此处不覆盖则本组件渲染的融合工具栏仍按 nowrap 裁剪，
   溢出的工具项在手机端不可达） */
@media (max-width: 767px) {
  .os-toolbar.os-tb {
    height: auto; min-height: 46px; padding: 7px 12px;
    flex-wrap: wrap; white-space: normal; overflow: visible; row-gap: 6px;
  }
  .tb-title { max-width: 100%; }
}
</style>
