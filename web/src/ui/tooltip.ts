// tooltip.ts — 全局快速 tooltip 单例（缩短原生 title 的漫长等待）。
//
// 背景：桌面七应用工具栏的提示用的是原生 title 属性，浏览器自带延迟
// （约 1~2 秒）不可调且观感迟钝。本模块接管全局：
//   - 悬停带 title / data-os-tip 的元素约 400ms 后弹出自绘深色气泡；
//   - 800ms 内连续浏览相邻按钮时延迟降到 60ms（跟随手速，工具栏扫一遍不卡顿）；
//   - 显示前摘下 title 属性抑制原生提示，移出/隐藏时还原（Vue 的 :title
//     绑定不受影响——值变化时 Vue 会重设属性，下次悬停再次接管）；
//   - data-os-tip 优先于 title，可供不想出现原生提示语义的场景显式使用。
//
// 关闭时机与 menu.ts 同一套约定：滚动（capture，含内部容器）、Esc、
// 任意 pointerdown、窗口缩放。焦点可达性：focusin 同样弹出。
// goder 等 custom element 的 shadow DOM 内部事件即使冒泡出来，
// closest 也无法跨越边界，天然跳过——goder 内部 UI 不受接管。
//
// 安装：createOpenOS() 里随 installMenuGuard 一起调用 installTooltip()。

const SHOW_DELAY = 400 // 首次悬停的显示延迟（原生约 1~2s）
const QUICK_DELAY = 60 // 连续浏览（800ms 内再次悬停）的快速延迟
const QUICK_WINDOW = 800
const GAP = 6 // 气泡与元素的间距

let installed = false
let el: HTMLElement | null = null // 单例气泡（首次显示时创建）
let cur: HTMLElement | null = null // 当前挂提示的元素
let timer: number | undefined
let lastHiddenAt = 0
const stashedTitles = new WeakMap<HTMLElement, string>() // 摘下的原生 title，隐藏时还原

/** 安装全局 tooltip 接管（幂等）。 */
export function installTooltip() {
  if (installed) return
  installed = true

  document.addEventListener('pointerover', (e) => {
    const t = e.target
    if (!(t instanceof Element)) return
    const target = t.closest<HTMLElement>('[data-os-tip],[title]')
    if (!target || target === cur) return
    schedule(target)
  }, true)

  document.addEventListener('pointerout', (e) => {
    if (!cur || e.target !== cur) return
    // 移到提示元素自身内部（子节点切换）不算离开
    if (e.relatedTarget && cur.contains(e.relatedTarget as Node)) return
    hide()
  }, true)

  document.addEventListener('focusin', (e) => {
    const t = e.target
    if (!(t instanceof HTMLElement) || !t.matches(':focus-visible')) return
    const target = t.closest<HTMLElement>('[data-os-tip],[title]')
    if (target && target !== cur) show(target)
  }, true)
  document.addEventListener('focusout', () => hide(), true)

  document.addEventListener('pointerdown', () => hide(), true)
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') hide() }, true)
  document.addEventListener('wheel', () => hide(), { capture: true, passive: true })
  window.addEventListener('resize', () => hide())
  document.addEventListener('visibilitychange', () => { if (document.hidden) hide() })
}

/** 悬停目标元素的提示文本（data-os-tip 优先，原生 title 兜底）。 */
function tipText(target: HTMLElement): string {
  return (target.dataset.osTip || target.getAttribute('title') || '').trim()
}

function schedule(target: HTMLElement) {
  cancel()
  if (!tipText(target)) return
  const delay = performance.now() - lastHiddenAt < QUICK_WINDOW ? QUICK_DELAY : SHOW_DELAY
  cur = target
  timer = window.setTimeout(() => show(target), delay)
}

function cancel() {
  if (timer) window.clearTimeout(timer)
  timer = undefined
}

function show(target: HTMLElement) {
  const text = tipText(target)
  if (!text) return
  el = el || createBubble()
  el.textContent = text
  // 摘下原生 title 抑制浏览器提示；data-os-tip 显式提供的无需处理
  if (target.hasAttribute('title')) {
    stashedTitles.set(target, target.getAttribute('title')!)
    target.removeAttribute('title')
  }
  el.style.display = 'block'
  position(el, target)
}

function hide() {
  cancel()
  if (cur) {
    const stashed = stashedTitles.get(cur)
    if (stashed !== undefined && !cur.hasAttribute('title')) cur.setAttribute('title', stashed)
    stashedTitles.delete(cur)
  }
  cur = null
  lastHiddenAt = performance.now()
  if (el) el.style.display = 'none'
}

function position(bubble: HTMLElement, target: HTMLElement) {
  const r = target.getBoundingClientRect()
  bubble.style.left = '0px'
  bubble.style.top = '0px'
  const { innerWidth: vw, innerHeight: vh } = window
  const w = bubble.offsetWidth, h = bubble.offsetHeight
  let x = Math.min(Math.max(r.left + r.width / 2 - w / 2, 8), vw - w - 8)
  let y = r.bottom + GAP
  if (y + h > vh - 8) y = Math.max(r.top - h - GAP, 8) // 底部放不下翻到上方
  bubble.style.left = `${Math.round(x)}px`
  bubble.style.top = `${Math.round(y)}px`
}

function createBubble(): HTMLElement {
  const b = document.createElement('div')
  b.className = 'os-tooltip'
  document.body.appendChild(b)
  const style = document.createElement('style')
  style.textContent = `
.os-tooltip {
  position: fixed; left: 0; top: 0; z-index: 21000; display: none;
  max-width: 320px; padding: 4px 9px; border-radius: 7px;
  background: rgba(28, 28, 32, .92); color: rgba(255, 255, 255, .95);
  font-size: 11.5px; line-height: 1.45; white-space: nowrap;
  box-shadow: 0 3px 12px rgba(0, 0, 0, .28); pointer-events: none;
}`
  document.head.appendChild(style)
  return b
}
