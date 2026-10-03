// 设备形态判定：窄屏（手机）进入移动形态，宽屏（桌面/平板）保持桌面形态。
// 断点按视口宽度而非 pointer 类型——平板外接触屏仍适合桌面形态，
// 触摸能力补齐与形态判定解耦（iPad 大屏走桌面壳 + 触摸可用）。
import { ref } from 'vue'

const mq = typeof window !== 'undefined' && window.matchMedia
  ? window.matchMedia('(max-width: 767px)')
  : null

/** 手机形态（响应式）：旋转/拉伸窗口跨断点时自动切换外壳。 */
export const isMobile = ref(!!mq?.matches)

if (mq) {
  const onChange = (e: MediaQueryListEvent) => { isMobile.value = e.matches }
  if (mq.addEventListener) mq.addEventListener('change', onChange)
  else mq.addListener(onChange) // 旧版 Safari
}

/** 移动形态可用视口高：优先 visualViewport——iOS 软键盘弹出时 innerHeight 不变，
 *  键盘弹出让窗口收缩到键盘上方（胶囊/悬浮球同步让位）依赖它。 */
export function mobileViewportH(): number {
  const vv = window.visualViewport
  return Math.round(vv ? vv.height : window.innerHeight)
}
