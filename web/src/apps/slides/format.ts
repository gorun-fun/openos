// HTML 幻灯片格式（.html）：独立可运行的演示文档。
// 约定：每页幻灯片是 <section class="slide">…</section>，整篇是 <main class="deck"> 内的连续 section；
// 文档自带样式与键盘翻页脚本——下载后双击即可在浏览器放映。
export const SLIDE_THEME_CLASS = {
  light: '',
  dark: 'theme-dark',
}

const STYLE = `
  * { box-sizing: border-box; margin: 0; padding: 0; }
  html, body { height: 100%; }
  body { font-family: -apple-system, "SF Pro Text", "PingFang SC", "Helvetica Neue", sans-serif; }
  .deck { display: none; }
  .stage { position: fixed; inset: 0; display: flex; align-items: center; justify-content: center; background: #f2f2f4; }
  .slide {
    width: min(96vw, calc(96vh * 16 / 9)); aspect-ratio: 16 / 9;
    background: #fff; color: #1d1d1f; border-radius: 14px;
    box-shadow: 0 18px 60px rgba(0, 0, 0, .18);
    padding: 6.5% 7.5%; overflow: hidden; outline: none;
  }
  .slide h1 { font-size: 4.2cqw; margin-bottom: .5em; }
  .slide h2 { font-size: 3.2cqw; margin: .4em 0; }
  .slide p, .slide li { font-size: 2.1cqw; line-height: 1.75; }
  .slide ul { padding-left: 1.5em; }
  .slide blockquote { border-left: .4cqw solid #0a84ff; padding-left: 1em; color: #555; margin: .6em 0; }
  .slide pre { background: #f5f5f7; padding: 1em; border-radius: .8cqw; font-size: 1.7cqw; overflow: auto; }
  .slide img { max-width: 100%; max-height: 60%; border-radius: .8cqw; }
  .slide table { border-collapse: collapse; font-size: 1.9cqw; }
  .slide th, .slide td { border: 1px solid #d2d2d7; padding: .35em .7em; }
  .pager { position: fixed; right: 18px; bottom: 12px; font-size: 13px; color: #86868b; user-select: none; }
  .theme-dark .stage { background: #151517; }
  .theme-dark .slide { background: #1e1e21; color: #f5f5f7; }
  .theme-dark .slide blockquote { color: #aaa; }
  .theme-dark .slide pre { background: #2a2a2e; }
  .theme-dark .slide th, .theme-dark .slide td { border-color: #3a3a3e; }
`

const SCRIPT = `
  (function () {
    var slides = [].slice.call(document.querySelectorAll('.slide'));
    var deck = document.querySelector('.deck');
    var i = 0;
    if (deck) deck.style.display = 'none';
    function show(n) {
      i = Math.max(0, Math.min(slides.length - 1, n));
      slides.forEach(function (s, k) { s.style.display = k === i ? 'flex' : 'none'; });
      var p = document.querySelector('.pager');
      if (p) p.textContent = (i + 1) + ' / ' + slides.length;
    }
    // 首个 section 提到舞台上；其余藏进 deck
    var stage = document.querySelector('.stage');
    slides.forEach(function (s) { (s === slides[0] ? stage : deck).appendChild(s); s.style.flexDirection = 'column'; });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'ArrowRight' || e.key === ' ' || e.key === 'PageDown') show(i + 1);
      if (e.key === 'ArrowLeft' || e.key === 'PageUp') show(i - 1);
    });
    document.addEventListener('click', function (e) {
      if (e.clientX > window.innerWidth / 2) show(i + 1); else show(i - 1);
    });
    show(0);
  })();
`

/** 组装独立 HTML 文档（slides 为每页 inner HTML）。 */
export function serializeDeck(slides: string[], theme: 'light' | 'dark' = 'light', title = '演示'): string {
  const cls = theme === 'dark' ? 'theme-dark' : ''
  const secs = slides.map((s) => `<section class="slide">${s}</section>`).join('\n')
  return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${escapeHtml(title)}</title>
<style>${STYLE}
.slide { container-type: inline-size; flex-direction: column; justify-content: center; }
</style>
</head>
<body class="${cls}">
  <div class="stage"></div>
  <main class="deck">
${secs}
  </main>
  <div class="pager"></div>
  <script>${SCRIPT}<\/script>
</body>
</html>
`
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

/** 解析幻灯片文件：html（section 约定）或旧版 .slides.json（title/body 数组，自动升级）。 */
export function parseDeck(text: string): string[] {
  const t = text.trim()
  // 旧版 JSON 格式 → 升级为 HTML 页
  if (t.startsWith('[')) {
    try {
      const legacy = JSON.parse(t) as { title: string; body: string }[]
      return legacy.map((s) => `<h1>${escapeHtml(s.title || '')}</h1><ul>${(s.body || '').split('\n').filter(Boolean).map((b) => `<li>${escapeHtml(b)}</li>`).join('')}</ul>`)
    } catch { /* 堕落到 HTML 解析 */ }
  }
  const doc = new DOMParser().parseFromString(t, 'text/html')
  const sections = [...doc.querySelectorAll('.slide')]
  if (sections.length) return sections.map((s) => sanitizeSlideHtml(s.innerHTML))
  // 无 section 结构的普通 HTML：整体作为一页
  const body = doc.body?.innerHTML?.trim()
  return body ? [sanitizeSlideHtml(body)] : ['<h1>空演示</h1>']
}

// —— 净化：幻灯片页内容在编辑器/缩略图/演示三处以同源 innerHTML 渲染，——
// 文件可经分享链接来自其他用户，故解析边界必须剥掉全部执行面：
//   * 脚本/嵌入类元素整棵移除（script/style/iframe/svg/object/…，内容不保留）；
//   * 白名单外标签解包（保留子节点文本与结构，如 <font>、自定义标签）；
//   * 属性只留少量排版属性；a[href]/img[src] 做 scheme 白名单（防 javascript:）；
//   * style 属性、on* 事件属性、注释一律剥除。
const SLIDE_DROP_TAGS = new Set(['script', 'style', 'iframe', 'object', 'embed', 'link', 'meta', 'base', 'form', 'input', 'button', 'textarea', 'select', 'option', 'video', 'audio', 'source', 'track', 'svg', 'math', 'canvas', 'template', 'frame', 'frameset', 'applet'])
const SLIDE_KEEP_TAGS = new Set(['h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'p', 'div', 'span', 'br', 'hr', 'ul', 'ol', 'li', 'blockquote', 'pre', 'code', 'b', 'strong', 'i', 'em', 'u', 's', 'del', 'ins', 'sub', 'sup', 'mark', 'small', 'a', 'img', 'table', 'thead', 'tbody', 'tfoot', 'tr', 'th', 'td', 'caption', 'colgroup', 'col', 'dl', 'dt', 'dd'])
const SLIDE_KEEP_ATTRS: Record<string, Set<string>> = {
  a: new Set(['href']),
  img: new Set(['src', 'alt', 'width', 'height']),
  td: new Set(['colspan', 'rowspan']),
  th: new Set(['colspan', 'rowspan']),
  ol: new Set(['start']),
}
const SAFE_URL = /^(https?:|mailto:|data:image\/(png|jpeg|gif|webp|bmp|avif);)/i

function safeUrl(v: string): boolean {
  // 相对地址与锚点天然安全；绝对地址走 scheme 白名单
  if (/^[a-z][a-z0-9+.-]*:/i.test(v)) return SAFE_URL.test(v)
  return !v.trim().startsWith('//')
}

/** 白名单净化一页幻灯片 innerHTML（同源渲染前的唯一入口）。 */
export function sanitizeSlideHtml(html: string): string {
  const doc = new DOMParser().parseFromString(`<body>${html}</body>`, 'text/html')
  const walk = (parent: Node) => {
    for (const child of [...parent.childNodes]) {
      if (child.nodeType === Node.COMMENT_NODE) { child.remove(); continue }
      if (child.nodeType !== Node.ELEMENT_NODE) continue
      const el = child as Element
      const tag = el.tagName.toLowerCase()
      if (SLIDE_DROP_TAGS.has(tag)) { el.remove(); continue }
      if (!SLIDE_KEEP_TAGS.has(tag)) { el.replaceWith(...el.childNodes); walk(parent); continue }
      for (const attr of [...el.attributes]) {
        const name = attr.name.toLowerCase()
        if (attr.name.startsWith('on') || name === 'style') { el.removeAttribute(attr.name); continue }
        if (!SLIDE_KEEP_ATTRS[tag]?.has(name)) { el.removeAttribute(attr.name); continue }
        if ((tag === 'a' && name === 'href') || (tag === 'img' && name === 'src')) {
          if (!safeUrl(attr.value)) el.removeAttribute(attr.name)
        }
      }
      walk(el)
    }
  }
  walk(doc.body)
  return doc.body.innerHTML
}

/** 新建空白演示（一页标题页）。 */
export function blankDeck(title = '未命名演示'): string {
  return serializeDeck([`<h1>${escapeHtml(title)}</h1><p>开始编辑…</p>`], 'light', title)
}
