// ANSI 转义序列 → 安全 HTML（终端彩色输出渲染）。
// 支持 SGR 颜色（30-37 / 90-97 / 38;5;n / 1 加粗 / 0 重置等），其余 CSI/OSC 剥除。

const FG: Record<number, string> = {
  30: '#7c7c82', 31: '#ff6b6b', 32: '#4cd964', 33: '#febc2e',
  34: '#57a9ff', 35: '#d970ff', 36: '#54d1e8', 37: '#e8e8ec',
  90: '#a8a8b0', 91: '#ff8a8a', 92: '#7ce68a', 93: '#ffd466',
  94: '#8ac4ff', 95: '#e8a4ff', 96: '#8ae0f2', 97: '#ffffff',
}

const CSI_RE = /\x1b\[([0-9;?]*)([A-Za-z])/g
const OSC_RE = /\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g
const ESC_OTHER = /\x1b[()][0-9A-B]|\x1b[=>78M]|\r(?!\n)/g

export function ansiToHtml(input: string): string {
  if (!input.includes('\x1b')) return esc(input)
  const text = input.replace(OSC_RE, '').replace(ESC_OTHER, '')

  let html = ''
  let pos = 0
  let bold = false
  let fg = ''
  let open = false

  const close = () => { if (open) { html += '</span>'; open = false } }
  const openSpan = () => {
    close()
    const styles: string[] = []
    if (bold) styles.push('font-weight:700')
    if (fg) styles.push(`color:${fg}`)
    if (styles.length) {
      html += `<span style="${styles.join(';')}">`
      open = true
    }
  }

  CSI_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = CSI_RE.exec(text))) {
    // 重放转义之前的文本
    if (m.index > pos) { openSpan(); html += esc(text.slice(pos, m.index)) }
    pos = CSI_RE.lastIndex
    if (m[2] === 'm') {
      const parts = m[1] === '' ? ['0'] : m[1].split(';')
      for (let i = 0; i < parts.length; i++) {
        const n = Number(parts[i] || '0')
        if (n === 0) { bold = false; fg = ''; close() }
        else if (n === 1) { bold = true }
        else if (n === 22) { bold = false }
        else if (n === 39) { fg = '' }
        else if (FG[n]) { fg = FG[n] }
        else if (n === 38 && parts[i + 1] === '5') {
          const c = Number(parts[i + 2]) // 256 色 → 16 色近似
          if (!isNaN(c)) fg = FG[30 + (c % 16)]
          i += 2
        }
      }
    }
    // 其余 CSI（K/J/光标）剥除：不输出
  }
  if (pos < text.length) { openSpan(); html += esc(text.slice(pos)) }
  close()
  return html
}

function esc(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}
