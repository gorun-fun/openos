// 统一的文件-应用关联注册表：扩展名 → 应用。
// 供文件应用 / 桌面快捷方式 / 聚焦搜索 / 终端等所有入口共用，避免映射表散落多份。
import type { VFile } from '../api/client'
import { useWindows } from '../stores/windows'

export const IMAGE_EXTS = ['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'ico', 'bmp', 'avif']

export const extOf = (p: string): string => (p.match(/\.([a-z0-9]+)$/i)?.[1] || '').toLowerCase()

export const isImage = (f: { path: string; isDir?: boolean }) => !f.isDir && IMAGE_EXTS.includes(extOf(f.path))

/** 扩展名 → 默认打开应用（未命中兜底 docs 纯文本）。 */
export const APP_FOR_EXT: Record<string, string> = {
  md: 'docs', markdown: 'docs', txt: 'docs', log: 'docs', json: 'docs',
  js: 'docs', ts: 'docs', css: 'docs', sh: 'docs', yml: 'docs', yaml: 'docs', xml: 'docs',
  csv: 'sheets', tsv: 'sheets',
  html: 'slides', htm: 'slides',
  pdf: 'pdf',
}

export function appForPath(path: string): string {
  return APP_FOR_EXT[extOf(path)] || 'docs'
}

/** 各应用能否打开该扩展名（“打开方式”候选过滤）。 */
function canOpen(app: string, ext: string): boolean {
  switch (app) {
    case 'docs': return !IMAGE_EXTS.includes(ext) && ext !== 'pdf'
    case 'sheets': return ['csv', 'tsv', 'txt'].includes(ext)
    case 'slides': return ['html', 'htm'].includes(ext)
    case 'pdf': return ext === 'pdf'
    default: return false
  }
}

/** “打开方式”应用列表：默认应用在前，其后是其余可打开该文件的应用。 */
export function openWithApps(path: string): string[] {
  const ext = extOf(path)
  const primary = appForPath(path)
  const out = [primary]
  for (const id of ['docs', 'sheets', 'slides', 'pdf']) {
    if (id !== primary && canOpen(id, ext)) out.push(id)
  }
  return out
}

export const parentOf = (p: string): string => p.split('/').slice(0, -1).join('/') || '/home'

/**
 * 打开一个 VFS 路径：目录 → 文件应用；图片 → 文件应用并立即快速预览；
 * 其余按关联表选应用。appOverride 用于“打开方式”菜单。
 */
export function openPath(path: string, appOverride?: string): string | null {
  const windows = useWindows()
  if (appOverride) return windows.open(appOverride, { path })
  if (isImage({ path })) return windows.open('files', { path: parentOf(path), preview: path })
  return windows.open(appForPath(path), { path })
}

/** 文件类型显示名（列表“种类”列 / 聚焦搜索预览用）。 */
export function kindOf(f: VFile | { path: string; isDir?: boolean }): string {
  if (f.isDir) return '文件夹'
  const ext = extOf(f.path)
  const names: Record<string, string> = {
    md: 'Markdown 文稿', markdown: 'Markdown 文稿', txt: '纯文本', log: '日志',
    json: 'JSON 文件', csv: 'CSV 表格', tsv: 'TSV 表格', html: 'HTML 幻灯片', htm: 'HTML 幻灯片',
    pdf: 'PDF 文稿', js: 'JavaScript', ts: 'TypeScript', css: '样式表', sh: 'Shell 脚本',
    yml: 'YAML', yaml: 'YAML', xml: 'XML', svg: '图像', png: '图像', jpg: '图像', jpeg: '图像',
    gif: '图像', webp: '图像', ico: '图像', bmp: '图像', avif: '图像',
  }
  return names[ext] || (ext ? ext.toUpperCase() + ' 文件' : '文档')
}

/** 文件的 emoji 图标（无缩略图时的占位；目录恒为文件夹）。 */
export function fileEmoji(p: string, isDir?: boolean): string {
  if (isDir) return '📁'
  const ext = extOf(p)
  if (IMAGE_EXTS.includes(ext)) return '🖼️'
  const map: Record<string, string> = {
    md: '📝', markdown: '📝', txt: '📄', log: '📄', json: '🧾',
    csv: '📊', tsv: '📊', html: '🗳️', htm: '🗳️', pdf: '📕',
    js: '📜', ts: '📜', css: '🎨', sh: '⌨️', yml: '🧩', yaml: '🧩', xml: '🧾',
  }
  return map[ext] || '📄'
}

export function fmtSize(n: number): string {
  if (n < 1024) return n + ' 字节'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / 1024 / 1024).toFixed(1) + ' MB'
}
