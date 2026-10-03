// shadow DOM 应用的左侧导航抽屉化支持（配合 MobileNavToggle 的 .os-side 约定）。
// base.css 里的 .os-side 规则作用在文档级，穿不进 web component 的 shadow root——
// 需要 NavDrawerCss 注入目标 shadow 内，并给侧栏元素加 os-side 类。
// 与文档级规则的差异：position 用 fixed（shadow 内没有可靠的 positioned 祖先，
// 移动形态窗口恒全屏，fixed 即窗口区域；窗口自身的 z-index 构成 stacking context，
// 抽屉不会逃出窗口层压到悬浮胶囊之上）。同样包在媒体查询里，桌面形态零生效。

export const SHADOW_NAV_CSS = `
@media (max-width: 767px) {
  .os-side {
    position: fixed !important; z-index: 30; left: 0; top: 0; bottom: 0;
    width: min(76vw, 300px) !important; max-width: min(76vw, 300px) !important;
    flex: none !important;
    transform: translateX(-103%);
    transition: transform .24s cubic-bezier(0.22, 0.8, 0.32, 1);
  }
  .os-side.os-side-open { transform: none; box-shadow: 0 24px 70px rgba(0, 0, 0, 0.26), 0 4px 14px rgba(0, 0, 0, 0.1); }
}
`

/** 把抽屉化 CSS 注入 shadow root（幂等：已注入则跳过）。返回是否新注入。 */
export function injectShadowNavCss(root: ShadowRoot | null | undefined): boolean {
  if (!root) return false
  if (root.querySelector('style[data-os-nav]')) return false
  const style = document.createElement('style')
  style.setAttribute('data-os-nav', '1')
  style.textContent = SHADOW_NAV_CSS
  root.append(style)
  return true
}
