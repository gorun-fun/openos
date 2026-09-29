// 窗口吸附预览共享状态（由 WindowFrame 写入、Desktop 渲染，
// 避免位于 transform 祖先内的 fixed 元素被裁剪）。
import { ref } from 'vue'

export const snapPreview = ref<null | { x: number; y: number; w: number; h: number }>(null)
