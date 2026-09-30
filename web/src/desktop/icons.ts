// 应用图标定义：macOS/iOS 风格 squircle + 手绘精细 SVG（64×64 viewBox，随容器缩放）。
// 也可用 src 指向图片（如 goder 官方图标）；第三方应用未匹配时落到 default。
//
// SVG 约定：
//   - 每个图标自带 <defs>，渐变 id 以图标名缩写作前缀，避免同页多实例串色；
//   - 底板统一 <rect rx="15">（≈23% 圆角），叠加白色顶光渐变营造玻璃质感；
//   - 投影由 AppIcon.vue 的容器负责，SVG 内不画外阴影。
export interface IconDef { svg?: string; src?: string; label?: string }

// 通用顶光：叠加在底板渐变之上、图形之下
const gloss = (p: string, opacity = 0.2) =>
  `<linearGradient id="${p}-gl" x1="0" y1="0" x2="0" y2="1">
     <stop offset="0" stop-color="#fff" stop-opacity="${opacity}"/>
     <stop offset=".55" stop-color="#fff" stop-opacity="0"/>
   </linearGradient>`

const shell = (p: string, from: string, to: string, body: string, diagonal = false) => `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
  <defs>
    <linearGradient id="${p}-bg" x1="0" y1="0" x2="${diagonal ? 1 : 0}" y2="1">
      <stop offset="0" stop-color="${from}"/><stop offset="1" stop-color="${to}"/>
    </linearGradient>
    ${gloss(p)}
  </defs>
  <rect width="64" height="64" rx="15" fill="url(#${p}-bg)"/>
  <rect width="64" height="64" rx="15" fill="url(#${p}-gl)"/>
  ${body}
</svg>`

// 折角文档页：白色竖版页面 + 右上角折角（docs/sheets/pdf 共用轮廓）
const page = (fold: string) => `
  <path d="M19 13 h16 l10 10 v27 q0 3 -3 3 h-20 q-3 0 -3 -3 Z" fill="#fff"/>
  <path d="M35 13 l10 10 h-7 q-3 0 -3 -3 Z" fill="${fold}"/>`

export const APP_ICONS: Record<string, IconDef> = {
  // 终端：深灰底 + 窗口（红黄绿灯）+ 绿色提示符
  terminal: {
    label: '终端',
    svg: shell('tm', '#4e4e56', '#1e1e24', `
      <defs><linearGradient id="tm-win" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0" stop-color="#0b0b10"/><stop offset="1" stop-color="#191921"/>
      </linearGradient></defs>
      <rect x="10" y="14" width="44" height="37" rx="6" fill="url(#tm-win)" stroke="rgba(255,255,255,.17)" stroke-width="1"/>
      <circle cx="17.5" cy="20.5" r="2.1" fill="#ff5f57"/>
      <circle cx="24" cy="20.5" r="2.1" fill="#febc2e"/>
      <circle cx="30.5" cy="20.5" r="2.1" fill="#28c840"/>
      <path d="M21 31 l6.5 6 -6.5 6" fill="none" stroke="#45e073" stroke-width="3.6" stroke-linecap="round" stroke-linejoin="round"/>
      <path d="M32 43 h9.5" fill="none" stroke="#d6d6dc" stroke-width="3.6" stroke-linecap="round"/>`),
  },
  // 文件：蓝色底 + 白色文件夹（iOS 文件风格）+ 前盖拼缝
  files: {
    label: '文件',
    svg: shell('fi', '#57b8ff', '#1f6fe0', `
      <path d="M13 43 V25 q0 -4 4 -4 h9 l4.5 4.5 H47 q4 0 4 4 V43 q0 4 -4 4 H17 q-4 0 -4 -4 Z" fill="#fff"/>
      <path d="M13 30.5 H51" stroke="#2f7ce8" stroke-opacity=".4" stroke-width="2" stroke-linecap="round"/>`),
  },
  // 计算器：炭黑底 + 显示屏 + 按键矩阵（末列橙色运算键，首排浅灰）
  calculator: {
    label: '计算器',
    svg: shell('ca', '#43434a', '#17171b', `
      <defs><linearGradient id="ca-or" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0" stop-color="#ffb02e"/><stop offset="1" stop-color="#ff8a00"/>
      </linearGradient></defs>
      <rect x="12" y="11" width="40" height="11" rx="3.5" fill="#0e0e12" stroke="rgba(255,255,255,.07)" stroke-width="1"/>
      <g>
        <rect x="12" y="26" width="8.8" height="8.8" rx="2.6" fill="#8e8e96"/>
        <rect x="22.4" y="26" width="8.8" height="8.8" rx="2.6" fill="#8e8e96"/>
        <rect x="32.8" y="26" width="8.8" height="8.8" rx="2.6" fill="#8e8e96"/>
        <rect x="12" y="36.6" width="8.8" height="8.8" rx="2.6" fill="#54545d"/>
        <rect x="22.4" y="36.6" width="8.8" height="8.8" rx="2.6" fill="#54545d"/>
        <rect x="32.8" y="36.6" width="8.8" height="8.8" rx="2.6" fill="#54545d"/>
        <rect x="12" y="47.2" width="8.8" height="8.8" rx="2.6" fill="#54545d"/>
        <rect x="22.4" y="47.2" width="8.8" height="8.8" rx="2.6" fill="#54545d"/>
        <rect x="32.8" y="47.2" width="8.8" height="8.8" rx="2.6" fill="#54545d"/>
        <rect x="43.2" y="26" width="8.8" height="8.8" rx="2.6" fill="url(#ca-or)"/>
        <rect x="43.2" y="36.6" width="8.8" height="19.4" rx="2.6" fill="url(#ca-or)"/>
      </g>`),
  },
  // 设置：银灰底 + 大齿轮（8 齿 + 中孔）
  settings: {
    label: '设置',
    svg: shell('st', '#c3c8d2', '#7b818f', `
      <g fill="#f4f5f8">
        <circle cx="32" cy="32" r="13.4"/>
        <rect x="28.6" y="10.5" width="6.8" height="11" rx="2.4"/>
        <rect x="28.6" y="10.5" width="6.8" height="11" rx="2.4" transform="rotate(45 32 32)"/>
        <rect x="28.6" y="10.5" width="6.8" height="11" rx="2.4" transform="rotate(90 32 32)"/>
        <rect x="28.6" y="10.5" width="6.8" height="11" rx="2.4" transform="rotate(135 32 32)"/>
        <rect x="28.6" y="10.5" width="6.8" height="11" rx="2.4" transform="rotate(180 32 32)"/>
        <rect x="28.6" y="10.5" width="6.8" height="11" rx="2.4" transform="rotate(225 32 32)"/>
        <rect x="28.6" y="10.5" width="6.8" height="11" rx="2.4" transform="rotate(270 32 32)"/>
        <rect x="28.6" y="10.5" width="6.8" height="11" rx="2.4" transform="rotate(315 32 32)"/>
      </g>
      <circle cx="32" cy="32" r="5.8" fill="#8b919c"/>`),
  },
  // 文档：紫罗兰底 + 白色折角页 + 紫色文字行（与「文件」的蓝色拉开色相）
  docs: {
    label: '文档',
    svg: shell('dc', '#8f74ff', '#5a41e6', `
      ${page('#ddd6ff')}
      <g fill="#8a76f8">
        <rect x="24.5" y="30" width="15" height="2.6" rx="1.3"/>
        <rect x="24.5" y="36" width="15" height="2.6" rx="1.3"/>
        <rect x="24.5" y="42" width="15" height="2.6" rx="1.3"/>
        <rect x="24.5" y="48" width="9" height="2.6" rx="1.3"/>
      </g>`),
  },
  // 表格：绿色底 + 白色折角页 + 表头与网格线（表格在折角以下的留白区居中）
  sheets: {
    label: '表格',
    svg: shell('sh', '#5ee08a', '#17a44c', `
      ${page('#d0f5da')}
      <g stroke="#3ec46f" stroke-width="1.8" stroke-linecap="round">
        <rect x="24" y="28" width="16" height="20" rx="2" fill="#fff"/>
        <path d="M24 34.2 H40 M24 41 H40 M29.4 34.2 V48 M34.8 34.2 V48"/>
      </g>`),
  },
  // 幻灯片：橙色底 + 横版白页 + 橙色柱状图
  slides: {
    label: '幻灯片',
    svg: shell('sl', '#ffb547', '#f0662b', `
      <rect x="13" y="17" width="38" height="30" rx="3.5" fill="#fff"/>
      <rect x="21" y="22" width="13" height="2.8" rx="1.4" fill="#ffcf9e"/>
      <rect x="21" y="31.5" width="5.4" height="10.5" rx="1.8" fill="#ff9648"/>
      <rect x="28.4" y="25.5" width="5.4" height="16.5" rx="1.8" fill="#ff8a3d"/>
      <rect x="35.8" y="29.5" width="5.4" height="12.5" rx="1.8" fill="#ff9648"/>
      <path d="M21 43.8 H42" stroke="#ffd9b8" stroke-width="1.8" stroke-linecap="round"/>`),
  },
  // PDF：红色底 + 白色折角页 + 横向红底白字标签带（略宽于页面，经典 PDF 徽标式）
  pdf: {
    label: 'PDF',
    svg: shell('pd', '#ff7a72', '#d92c34', `
      ${page('#ffd9d6')}
      <rect x="16" y="28.5" width="32" height="12.5" rx="2.5" fill="#e0343c"/>
      <text x="32" y="38" text-anchor="middle" font-family="-apple-system,'Segoe UI',Roboto,'Helvetica Neue',Arial,sans-serif"
        font-size="9" font-weight="800" letter-spacing=".8" fill="#fff">PDF</text>`),
  },
  // 便签：黄色顶带 + 白色正文 + 灰色横线（iOS 备忘录风格）
  notes: {
    label: '便签',
    svg: shell('nt', '#ffe14d', '#ffab19', `
      <path d="M0 25 H64 V49 A15 15 0 0 1 49 64 H15 A15 15 0 0 1 0 49 Z" fill="#fff"/>
      <g fill="#d8d8dd">
        <rect x="10" y="33.5" width="44" height="2.6" rx="1.3"/>
        <rect x="10" y="40.5" width="44" height="2.6" rx="1.3"/>
        <rect x="10" y="47.5" width="28" height="2.6" rx="1.3"/>
      </g>`),
  },
  // 欢迎：青色底 + 白色显示器（屏幕带提示符）+ 星形点缀
  welcome: {
    label: '欢迎',
    svg: shell('we', '#4fd6c6', '#1a9d92', `
      <rect x="15" y="17" width="34" height="23" rx="3.5" fill="#fff"/>
      <rect x="18.5" y="20.5" width="27" height="16" rx="2" fill="#2bb3a6"/>
      <path d="M23.5 26.5 l4 3.5 -4 3.5" fill="none" stroke="#eafffd" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/>
      <path d="M30.5 33.5 h6.5" stroke="#eafffd" stroke-width="2.2" stroke-linecap="round"/>
      <rect x="29.5" y="40" width="5" height="4.5" fill="#fff"/>
      <rect x="24" y="44.5" width="16" height="3.6" rx="1.8" fill="#fff"/>
      <path d="M54 7 l1.5 3.5 3.5 1.5 -3.5 1.5 -1.5 3.5 -1.5 -3.5 -3.5 -1.5 3.5 -1.5 Z" fill="#fff" opacity=".95"/>`),
  },
  goder: { src: '/icons/goder.svg', label: 'goder' },
  // 第三方应用默认图标：浅灰底 + 四色应用矩阵
  default: {
    label: '应用',
    svg: shell('df', '#ced3db', '#8f95a1', `
      <rect x="16.5" y="16.5" width="13" height="13" rx="4.5" fill="#4c9ffe"/>
      <rect x="34.5" y="16.5" width="13" height="13" rx="4.5" fill="#37cf68"/>
      <rect x="16.5" y="34.5" width="13" height="13" rx="4.5" fill="#ffb300"/>
      <rect x="34.5" y="34.5" width="13" height="13" rx="4.5" fill="#ff6b6b"/>`),
  },
}

export function iconOf(id: string): IconDef {
  return APP_ICONS[id] || APP_ICONS.default
}

/** 注册（或覆盖）应用图标：宿主应用经 createOpenOS({icons}) 注入业务图标，可反复调用。 */
export function registerIcons(defs: Record<string, IconDef>) {
  Object.assign(APP_ICONS, defs)
}
