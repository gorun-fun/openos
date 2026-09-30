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
  if (sections.length) return sections.map((s) => s.innerHTML)
  // 无 section 结构的普通 HTML：整体作为一页
  const body = doc.body?.innerHTML?.trim()
  return body ? [body] : ['<h1>空演示</h1>']
}

/** 新建空白演示（一页标题页）。 */
export function blankDeck(title = '未命名演示'): string {
  return serializeDeck([`<h1>${escapeHtml(title)}</h1><p>开始编辑…</p>`], 'light', title)
}
