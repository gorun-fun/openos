// 轻量 Markdown 渲染器（无第三方依赖）：文档应用与快速预览共用。
// 输入先整体 HTML 转义再应用 Markdown 语法，输出可安全 v-html。
const ESC: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }
const esc = (s: string) => s.replace(/[&<>"]/g, (c) => ESC[c])

/** 行内语法：代码 / 加粗 / 斜体 / 删除线 / 链接 / 图片 / 自动链接。 */
function inline(src: string): string {
  const codes: string[] = []
  let s = esc(src)
  // 行内代码先占位，避免内部再被替换
  s = s.replace(/`([^`]+)`/g, (_, c) => {
    codes.push(`<code>${c}</code>`)
    return `\u0000${codes.length - 1}\u0000`
  })
  s = s
    .replace(/!\[([^\]]*)\]\(([^)\s]+)\)/g, '<img alt="$1" src="$2" loading="lazy">')
    .replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>')
    .replace(/(https?:\/\/[^\s<]+)$/gm, '<a href="$1" target="_blank" rel="noopener">$1</a>')
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/~~([^~]+)~~/g, '<del>$1</del>')
    .replace(/(^|[^*])\*([^*\s][^*]*)\*/g, '$1<em>$2</em>')
    .replace(/(^|[^\w])_([^_\s][^_]*)_([^\w]|$)/g, '$1<em>$2</em>$3')
  return s.replace(/\u0000(\d+)\u0000/g, (_, i) => codes[Number(i)])
}

/** 块级渲染：标题/水平线/代码块/引用/列表/表格/段落。 */
export function renderMarkdown(src: string): string {
  const lines = src.replace(/\r\n?/g, '\n').split('\n')
  const out: string[] = []
  let i = 0
  let para: string[] = []

  const flush = () => {
    if (para.length) {
      out.push(`<p>${inline(para.join(' '))}</p>`)
      para = []
    }
  }

  while (i < lines.length) {
    const line = lines[i]

    // 围栏代码块
    const fence = line.match(/^```(\w*)/)
    if (fence) {
      flush()
      const buf: string[] = []
      i++
      while (i < lines.length && !lines[i].startsWith('```')) { buf.push(lines[i]); i++ }
      i++ // 跳过收尾 ```
      out.push(`<pre><code>${esc(buf.join('\n'))}</code></pre>`)
      continue
    }

    // 标题
    const h = line.match(/^(#{1,6})\s+(.*)/)
    if (h) { flush(); out.push(`<h${h[1].length}>${inline(h[2])}</h${h[1].length}>`); i++; continue }

    // 水平线
    if (/^\s*([-*_])\s*(\1\s*){2,}$/.test(line)) { flush(); out.push('<hr>'); i++; continue }

    // 引用块
    if (/^>\s?/.test(line)) {
      flush()
      const buf: string[] = []
      while (i < lines.length && /^>\s?/.test(lines[i])) { buf.push(lines[i].replace(/^>\s?/, '')); i++ }
      out.push(`<blockquote>${renderMarkdown(buf.join('\n'))}</blockquote>`)
      continue
    }

    // 表格：当前行含 | 且下一行是 |---|---|
    if (line.includes('|') && /^\s*\|?[\s:-]*-[\s:|-]*$/.test(lines[i + 1] || '')) {
      flush()
      const cells = (l: string) => l.replace(/^\s*\|/, '').replace(/\|\s*$/, '').split('|').map((c) => c.trim())
      const head = cells(line)
      i += 2
      const rows: string[][] = []
      while (i < lines.length && lines[i].includes('|') && lines[i].trim() !== '') {
        rows.push(cells(lines[i])); i++
      }
      out.push('<table><thead><tr>' + head.map((c) => `<th>${inline(c)}</th>`).join('') + '</tr></thead><tbody>'
        + rows.map((r) => '<tr>' + r.map((c) => `<td>${inline(c)}</td>`).join('') + '</tr>').join('') + '</tbody></table>')
      continue
    }

    // 列表（- / * / + / 1.），两层嵌足常见用法
    const li = line.match(/^(\s*)([-*+]|\d+\.)\s+(.*)/)
    if (li) {
      flush()
      const ordered = /\d+\./.test(li[2])
      const items: string[] = []
      const sub: string[] = []
      while (i < lines.length) {
        const m = lines[i].match(/^(\s*)([-*+]|\d+\.)\s+(.*)/)
        if (!m) break
        if (m[1].length >= 2) sub.push(m[3])
        else items.push(m[3] + (sub.length ? sub.splice(0).map((s2) => `<li>${inline(s2)}</li>`).join('') : ''))
        i++
      }
      const tag = ordered ? 'ol' : 'ul'
      out.push(`<${tag}>${items.map((c) => `<li>${inline(c)}</li>`).join('')}</${tag}>`)
      continue
    }

    // 空行结束段落
    if (line.trim() === '') { flush(); i++; continue }

    para.push(line.trim())
    i++
  }
  flush()
  return out.join('\n')
}
