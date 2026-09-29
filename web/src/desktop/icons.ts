// 应用图标定义：macOS 风格圆角渐变方块 + 符号；也可用 src 指向图片（如 goder 官方图标）。
export interface IconDef { grad?: string; symbol?: string; label?: string; src?: string }

export const APP_ICONS: Record<string, IconDef> = {
  terminal:   { grad: 'linear-gradient(145deg,#3a3a3c,#1c1c1e)', symbol: '>_' },
  files:      { grad: 'linear-gradient(145deg,#5eb3f6,#2b7fd9)', symbol: '📁' },
  calculator: { grad: 'linear-gradient(145deg,#ffd60a,#ff9f0a)', symbol: '🧮' },
  settings:   { grad: 'linear-gradient(145deg,#a8adb8,#6e7380)', symbol: '⚙️' },
  docs:       { grad: 'linear-gradient(145deg,#6fb7ff,#2b6fd9)', symbol: '📝' },
  sheets:     { grad: 'linear-gradient(145deg,#63e07a,#1faa4f)', symbol: '📊' },
  slides:     { grad: 'linear-gradient(145deg,#ff9f6b,#f76b1c)', symbol: '📽️' },
  pdf:        { grad: 'linear-gradient(145deg,#ff6b6b,#d1303a)', symbol: '📕' },
  goder:      { src: '/icons/goder.svg', label: 'goder' },
  notes:      { grad: 'linear-gradient(145deg,#ffe45c,#f0a500)', symbol: '🗒️' },
  welcome:    { grad: 'linear-gradient(145deg,#8ee6de,#2aa8a0)', symbol: '👋' },
  // 第三方应用未提供 icon 时使用
  default:    { grad: 'linear-gradient(145deg,#c3c7cf,#8e939e)', symbol: '🧩' },
}

export function iconOf(id: string): IconDef {
  return APP_ICONS[id] || APP_ICONS.default
}
