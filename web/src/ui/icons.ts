// 工具栏图标库：16×16、stroke=currentColor 的内联 SVG 字符串，
// 供各应用融合工具栏以 v-html 使用（按钮自身提供 title 提示）。
// 约定：纯几何线条图标，配合 base.css 的 .os-tb-btn svg / .os-seg-btn svg 统一尺寸。

const s = (body: string) =>
  `<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${body}</svg>`

export const icons: Record<string, string> = {
  // 文件操作
  open: s('<path d="M2.5 5.2A1.2 1.2 0 0 1 3.7 4h2.7l1.6 1.8h4.3a1.2 1.2 0 0 1 1.2 1.2v5.8a1.2 1.2 0 0 1-1.2 1.2H3.7a1.2 1.2 0 0 1-1.2-1.2z"/>'),
  save: s('<path d="M8 2.6v7"/><path d="M5.3 7l2.7 2.6L10.7 7"/><path d="M3 11.6v1a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1v-1"/>'),
  saveAs: s('<path d="M6.6 2.6v7"/><path d="M3.9 7l2.7 2.6L9.3 7"/><path d="M1.8 11.6v1a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1v-1"/><path d="M12.6 2.4v3.4M10.9 4.1h3.4"/>'),
  export: s('<path d="M8 9.6V2.6"/><path d="M5.3 5.3 8 2.6l2.7 2.7"/><path d="M3 11.6v1a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1v-1"/>'),
  undo: s('<path d="M4.4 6.4H10a3.3 3.3 0 0 1 0 6.6H6.6"/><path d="M7 3.7 4.2 6.4 7 9.2"/>'),
  redo: s('<path d="M11.6 6.4H6a3.3 3.3 0 0 0 0 6.6h3.4"/><path d="M9 3.7l2.8 2.7L9 9.2"/>'),
  // 表格行列
  insRow: s('<path d="M8 1.2v2.6M6.7 2.5h2.6"/><rect x="2.5" y="5.5" width="11" height="8" rx="1.2"/><path d="M2.5 9.5h11"/>'),
  delRow: s('<path d="M6.7 2.5h2.6"/><rect x="2.5" y="5.5" width="11" height="8" rx="1.2"/><path d="M2.5 9.5h11"/>'),
  insCol: s('<path d="M1.2 8h2.6M2.5 6.7v2.6"/><rect x="5.5" y="2.5" width="8" height="11" rx="1.2"/><path d="M9.5 2.5v11"/>'),
  delCol: s('<path d="M1.2 8h2.6"/><rect x="5.5" y="2.5" width="8" height="11" rx="1.2"/><path d="M9.5 2.5v11"/>'),
  // 幻灯片页
  addPage: s('<path d="M1.2 8h2.6M2.5 6.7v2.6"/><path d="M5.6 2.5h3.9l3 3v8a1 1 0 0 1-1 1H5.6a1 1 0 0 1-1-1v-7"/>'),
  delPage: s('<path d="M1.2 8h2.6"/><path d="M5.6 2.5h3.9l3 3v8a1 1 0 0 1-1 1H5.6a1 1 0 0 1-1-1v-7"/>'),
  sun: s('<circle cx="8" cy="8" r="2.9"/><path d="M8 1.6v1.7M8 12.7v1.7M1.6 8h1.7M12.7 8h1.7M3.5 3.5l1.2 1.2M11.3 11.3l1.2 1.2M12.5 3.5l-1.2 1.2M4.7 11.3l-1.2 1.2"/>'),
  moon: s('<path d="M13.2 9.7A5.6 5.6 0 0 1 6.3 2.8a5.6 5.6 0 1 0 6.9 6.9z"/>'),
  play: s('<path d="M5.2 3.4v9.2l7.4-4.6z" fill="currentColor" stroke="none"/>'),
  // 便签 / 文档
  note: s('<rect x="2.7" y="2.7" width="10.6" height="10.6" rx="2.4"/><path d="M8 5.4v5.2M5.4 8h5.2"/>'),
  edit: s('<path d="M3 13l.4-2.1 7.8-7.8 1.7 1.7-7.8 7.8z"/><path d="M10.4 4.9l1.7 1.7"/>'),
  split: s('<rect x="2.5" y="3" width="11" height="10" rx="1.3"/><path d="M8 3v10"/>'),
  eye: s('<path d="M1.8 8s2.4-4.1 6.2-4.1S14.2 8 14.2 8s-2.4 4.1-6.2 4.1S1.8 8 1.8 8z"/><circle cx="8" cy="8" r="1.9"/>'),
  // PDF / 查看
  laptop: s('<rect x="3.2" y="3.8" width="9.6" height="6.8" rx="1.2"/><path d="M1.6 12.6h12.8"/>'),
  zoomIn: s('<circle cx="7" cy="7" r="4.3"/><path d="M10.2 10.2 14 14M7 4.9v4.2M4.9 7h4.2"/>'),
  zoomOut: s('<circle cx="7" cy="7" r="4.3"/><path d="M10.2 10.2 14 14M4.9 7h4.2"/>'),
  chevL: s('<path d="M9.5 3.8 5.2 8l4.3 4.2"/>'),
  chevR: s('<path d="M6.5 3.8 10.8 8l-4.3 4.2"/>'),
  close: s('<path d="M4 4l8 8M12 4l-8 8"/>'),
  // 终端
  eraser: s('<path d="M6.6 12.8l-3.3-3.3a1.3 1.3 0 0 1 0-1.8l4.5-4.5a1.3 1.3 0 0 1 1.8 0l3 3a1.3 1.3 0 0 1 0 1.8l-5.1 5.1"/><path d="M5.6 6l4.4 4.4"/><path d="M2.6 13.4h10.8"/>'),
  virtual: s('<rect x="2.2" y="2.2" width="7.2" height="7.2" rx="1.4"/><path d="M6.6 13.8h6a1.2 1.2 0 0 0 1.2-1.2v-6"/><path d="M2.2 11.2v2.6h2.6"/>'),
  shell: s('<rect x="2" y="3" width="12" height="10" rx="1.6"/><path d="M4.6 6.1l2.4 1.9-2.4 1.9M8.6 10.2h3"/>'),
  // 文件应用工具栏
  sort: s('<path d="M5 2.8v10M2.6 10.4 5 12.8l2.4-2.4"/><path d="M11 13.2v-10M8.6 5.6 11 3.2l2.4 2.4"/>'),
  more: s('<circle cx="3.6" cy="8" r="1.25" fill="currentColor" stroke="none"/><circle cx="8" cy="8" r="1.25" fill="currentColor" stroke="none"/><circle cx="12.4" cy="8" r="1.25" fill="currentColor" stroke="none"/>'),
  // 网页应用容器（WebApp）
  refresh: s('<path d="M13.7 10a6 6 0 1 1-1.4-6.2L15.3 6.7"/><path d="M15.3 2.7v4h-4"/>'),
  link: s('<path d="M12 8.7v4a1.3 1.3 0 0 1-1.3 1.3H3.3A1.3 1.3 0 0 1 2 12.7V5.3A1.3 1.3 0 0 1 3.3 4h4"/><path d="M10 2h4v4"/><path d="M6.7 9.3 14 2"/>'),
  user: s('<circle cx="8" cy="5.4" r="2.6"/><path d="M2.9 13.4a5.1 5.1 0 0 1 10.2 0"/>'),
}
