<script setup lang="ts">
// 文件条目图标/缩略图统一渲染（图标/列表/分栏/画廊胶片共用）：
// 图片文件出缩略 <img>（url 未就绪回落 emoji），目录/其余文件出 emoji。
// imgClass/emojiTag/emojiClass 按视图适配既有类名（thumb/lt/ci 等，
// 尺寸样式在父级按视图上下文经 :deep 命中）；缺省 emojiTag 渲染裸文本（图标/列表视图）。
import { computed } from 'vue'
import type { VFile } from '../../api/client'
import { fileEmoji, isImage } from '../openWith'

const props = defineProps<{
  file: VFile
  url?: string // 缩略 blob 地址（加载中为空串 → 回落 emoji）
  imgClass?: string
  emojiTag?: 'span' | 'div'
  emojiClass?: string
}>()
const emoji = computed(() => (props.file.isDir ? '📁' : fileEmoji(props.file.path)))
</script>

<template>
  <img v-if="isImage(file) && url" :class="imgClass" :src="url" alt="">
  <component :is="emojiTag || 'span'" v-else-if="emojiTag" :class="emojiClass">{{ emoji }}</component>
  <template v-else>{{ emoji }}</template>
</template>
