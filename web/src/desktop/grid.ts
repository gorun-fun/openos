// 桌面网格：快捷方式与小组件统一占位注册表。
// 释放后吸附到格子；放置前查询占位，冲突时螺旋搜索最近空位，保证互不覆盖。
export const GRID = { cw: 92, ch: 104, left: 12, top: 48, dockH: 96 }

export interface GridRect {
  c: number
  r: number
  sc: number // 横向跨格数
  sr: number // 纵向跨格数
  kind?: 'shortcut' | 'widget'
}

export function gridMetrics() {
  const cols = Math.max(1, Math.floor((window.innerWidth - GRID.left * 2) / GRID.cw))
  const rows = Math.max(1, Math.floor((window.innerHeight - GRID.top - GRID.dockH) / GRID.ch))
  return { cols, rows }
}

const items = new Map<string, GridRect>()

export function registerItem(key: string, rect: GridRect) { items.set(key, rect) }
export function unregisterItem(key: string) { items.delete(key) }
export function itemAt(key: string) { return items.get(key) }

function overlaps(a: GridRect, b: GridRect) {
  return a.c < b.c + b.sc && b.c < a.c + a.sc && a.r < b.r + b.sr && b.r < a.r + a.sr
}

export function isFree(rect: GridRect, exceptKey?: string) {
  for (const [k, v] of items) {
    if (k !== exceptKey && overlaps(rect, v)) return false
  }
  return true
}

/** 从期望格子起向外螺旋搜索最近的空闲位置（放不下时原样返回作兜底）。 */
export function findFree(c: number, r: number, sc: number, sr: number, kind: GridRect['kind'], exceptKey?: string): GridRect {
  const { cols, rows } = gridMetrics()
  c = Math.min(Math.max(0, c), Math.max(0, cols - sc))
  r = Math.min(Math.max(0, r), Math.max(0, rows - sr))
  for (let d = 0; d <= cols + rows; d++) {
    for (let dr = -d; dr <= d; dr++) {
      const edge = Math.abs(dr) === d
      const dcs = edge ? Array.from({ length: d * 2 + 1 }, (_, i) => i - d) : [-d, d]
      for (const dc of dcs) {
        const cc = c + dc, rr = r + dr
        if (cc < 0 || rr < 0 || cc + sc > cols || rr + sr > rows) continue
        if (isFree({ c: cc, r: rr, sc, sr }, exceptKey)) return { c: cc, r: rr, sc, sr, kind }
      }
    }
  }
  return { c, r, sc, sr, kind }
}

/** 格子 → 像素坐标（格子左上角）。 */
export function posOf(rect: Pick<GridRect, 'c' | 'r'>) {
  return { x: GRID.left + rect.c * GRID.cw, y: GRID.top + rect.r * GRID.ch }
}

/** 像素坐标 → 最近格子（释放吸附用）。 */
export function cellAt(x: number, y: number) {
  return {
    c: Math.round((x - GRID.left) / GRID.cw),
    r: Math.round((y - GRID.top) / GRID.ch),
  }
}

/** 像素坐标 → 光标所在格子（拖放落点用，与"放到哪个图标上"的直觉一致）。 */
export function cellUnder(x: number, y: number) {
  return {
    c: Math.floor((x - GRID.left) / GRID.cw),
    r: Math.floor((y - GRID.top) / GRID.ch),
  }
}
