// 轻量电子表格公式引擎：四则运算、单元格引用、区域函数（SUM/AVG/MIN/MAX/COUNT）。
export const COLS = 26
export const ROWS = 60

export function colName(c: number): string { return String.fromCharCode(65 + c) }
export function cellName(r: number, c: number): string { return colName(c) + (r + 1) }

/** getCell(r,c) 返回该单元格原始字符串；used 用于检测循环引用。 */
export function evalCell(raw: string, getCell: (r: number, c: number) => string, used: Set<string> = new Set()): string {
  if (raw == null || raw === '') return ''
  const t = raw.trim()
  if (!t.startsWith('=')) return raw
  try {
    const val = evalExpr(t.slice(1), getCell, used)
    if (typeof val === 'number') {
      if (!isFinite(val)) return '#DIV/0!'
      return String(Math.round(val * 1e10) / 1e10)
    }
    return String(val)
  } catch (e: any) {
    if (e.message === 'CYCLE') return '#CYCLE!'
    return '#ERR!'
  }
}

// ---- tokenizer ----
type Tok = { t: 'num' | 'str' | 'ref' | 'op' | 'fn' | 'lp' | 'rp' | 'comma' | 'colon'; v: string; range?: [number, number, number, number] }

function tokenize(s: string): Tok[] {
  const toks: Tok[] = []
  let i = 0
  while (i < s.length) {
    const ch = s[i]
    if (ch === ' ') { i++; continue }
    if (/[0-9.]/.test(ch)) {
      let j = i
      while (j < s.length && /[0-9.]/.test(s[j])) j++
      toks.push({ t: 'num', v: s.slice(i, j) }); i = j; continue
    }
    if (/[A-Za-z]/.test(ch)) {
      let j = i
      while (j < s.length && /[A-Za-z0-9]/.test(s[j])) j++
      const word = s.slice(i, j)
      if (j < s.length && s[j] === '(') {
        toks.push({ t: 'fn', v: word.toUpperCase() })
        i = j + 1
        toks.push({ t: 'lp', v: '(' })
        continue
      }
      // 单元格引用 A1 或区域 A1:B3（colon 由 parser 处理）
      toks.push({ t: 'ref', v: word.toUpperCase() })
      i = j; continue
    }
    if ('+-*/^%'.includes(ch)) { toks.push({ t: 'op', v: ch }); i++; continue }
    if (ch === '(') { toks.push({ t: 'lp', v: ch }); i++; continue }
    if (ch === ')') { toks.push({ t: 'rp', v: ch }); i++; continue }
    if (ch === ',') { toks.push({ t: 'comma', v: ch }); i++; continue }
    if (ch === ':') { toks.push({ t: 'colon', v: ch }); i++; continue }
    throw new Error('BADCHAR ' + ch)
  }
  return toks
}

function parseRef(v: string): [number, number] {
  const m = v.match(/^([A-Z]+)([0-9]+)$/)
  if (!m) throw new Error('BADREF')
  let c = 0
  for (const ch of m[1]) c = c * 26 + (ch.charCodeAt(0) - 64)
  return [Number(m[2]) - 1, c - 1]
}

function evalExpr(src: string, getCell: (r: number, c: number) => string, used: Set<string>): number {
  const toks = tokenize(src)
  let pos = 0
  const peek = () => toks[pos]
  const next = () => toks[pos++]

  function prim(): number {
    const t = next()
    if (!t) throw new Error('EOF')
    if (t.t === 'num') return parseFloat(t.v)
    if (t.t === 'ref') {
      // 区域？
      if (peek()?.t === 'colon') {
        next()
        const t2 = next()
        if (t2?.t !== 'ref') throw new Error('BADRANGE')
        return sumRange(t.v, t2.v) // 裸区域默认求和（等价 SUM）
      }
      return refValue(t.v)
    }
    if (t.t === 'op' && (t.v === '-' || t.v === '+')) {
      const v = prim()
      return t.v === '-' ? -v : v
    }
    if (t.t === 'lp') {
      const v = expr()
      if (next()?.t !== 'rp') throw new Error('NORP')
      return v
    }
    if (t.t === 'fn') {
      // fn lp 已消费，取参数直到 rp
      const args: number[][] = [] // 每个参数是值列表（区域展开）
      const vals: number[] = []
      if (peek()?.t === 'rp') { next() }
      else {
        for (;;) {
          const p = parseArg()
          args.push(p)
          const nx = next()
          if (nx?.t === 'rp') break
          if (nx?.t !== 'comma') throw new Error('FNARG')
        }
      }
      return applyFn(t.v, args.flat())
    }
    throw new Error('BADPRIM ' + t.v)
  }

  function parseArg(): number[] {
    // 参数可能是区域 → 展开为值数组
    const save = pos
    const t = next()
    if (t?.t === 'ref' && peek()?.t === 'colon') {
      next()
      const t2 = next()
      if (t2?.t !== 'ref') throw new Error('BADRANGE')
      return rangeValues(t.v, t2.v)
    }
    pos = save
    return [expr()]
  }

  function term(): number {
    let v = prim()
    while (peek()?.t === 'op' && '*/%'.includes(peek()!.v)) {
      const op = next().v
      const r = prim()
      if (op === '*') v *= r
      else if (op === '/') v /= r
      else v %= r
    }
    return v
  }

  function expr(): number {
    let v = term()
    while (peek()?.t === 'op' && '+-'.includes(peek()!.v)) {
      const op = next().v
      const r = term()
      v = op === '+' ? v + r : v - r
    }
    return v
  }

  function refValue(name: string): number {
    const [r, c] = parseRef(name)
    if (used.has(name)) throw new Error('CYCLE')
    used.add(name)
    const raw = getCell(r, c) || ''
    const v = raw.trim().startsWith('=') ? Number(evalCell(raw, getCell, used)) : Number(raw)
    if (raw !== '' && isNaN(v as number) && !raw.trim().startsWith('=')) return 0 // 文本当 0
    used.delete(name)
    return v || 0
  }

  function rangeValues(a: string, b: string): number[] {
    const [r1, c1] = parseRef(a), [r2, c2] = parseRef(b)
    const out: number[] = []
    for (let r = Math.min(r1, r2); r <= Math.max(r1, r2); r++) {
      for (let c = Math.min(c1, c2); c <= Math.max(c1, c2); c++) {
        const name = cellName(r, c)
        if (used.has(name)) throw new Error('CYCLE')
        used.add(name)
        const raw = getCell(r, c) || ''
        if (raw.trim() !== '' && !isNaN(Number(raw))) out.push(Number(raw))
        used.delete(name)
      }
    }
    return out
  }

  function sumRange(a: string, b: string): number {
    return rangeValues(a, b).reduce((s, v) => s + v, 0)
  }

  const result = expr()
  if (pos !== toks.length) throw new Error('TRAILING')
  return result
}

function applyFn(name: string, vals: number[]): number {
  const nums = vals.filter((v) => typeof v === 'number' && !isNaN(v))
  switch (name) {
    case 'SUM': return nums.reduce((s, v) => s + v, 0)
    case 'AVG': case 'AVERAGE': return nums.length ? nums.reduce((s, v) => s + v, 0) / nums.length : 0
    case 'MIN': return nums.length ? Math.min(...nums) : 0
    case 'MAX': return nums.length ? Math.max(...nums) : 0
    case 'COUNT': return nums.length
    case 'ABS': return Math.abs(nums[0] || 0)
    case 'ROUND': return Math.round(nums[0] || 0)
    default: throw new Error('BADFN ' + name)
  }
}

// ---- CSV ----

export function parseCSV(text: string): string[][] {
  const rows: string[][] = []
  let row: string[] = [], cur = '', inQ = false
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    if (inQ) {
      if (ch === '"') {
        if (text[i + 1] === '"') { cur += '"'; i++ } else inQ = false
      } else cur += ch
    } else if (ch === '"') inQ = true
    else if (ch === ',') { row.push(cur); cur = '' }
    else if (ch === '\n' || ch === '\r') {
      if (ch === '\r' && text[i + 1] === '\n') i++
      row.push(cur); cur = ''
      if (row.length > 1 || row[0] !== '') rows.push(row)
      row = []
    } else cur += ch
  }
  row.push(cur)
  if (row.length > 1 || row[0] !== '') rows.push(row)
  return rows
}

export function toCSV(grid: string[][]): string {
  return grid
    .filter((row) => row.some((c) => c !== ''))
    .map((row) => row.map((c) => (/[",\n]/.test(c) ? `"${c.replace(/"/g, '""')}"` : c)).join(','))
    .join('\n')
}
