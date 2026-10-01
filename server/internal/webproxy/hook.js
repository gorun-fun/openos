// OpenOS 应用代理客户端钩子：由服务端注入到每个代理页面（紧跟 <head> 之后、
// 早于页面脚本执行）。包装 JS 动态构造 URL 的入口，把根绝对/绝对/跨域地址映射回
// /app/<id>/<目标地址>，使未经服务端重写的动态请求同样命中代理路由；相对地址
// 天然按代理路径解析，无需处理。
// 另在加载时把文档地址「还原」为目标站形态（去代理前缀，history.replaceState）：
// SPA（react-router 等）按 location.pathname 做客户端路由，代理前缀会让路由表
// 全部失配（渲染 404）。网络请求仍由 toProxy 以还原前算好的闭包变量映射回代理
// 路径；地址栏/Referer 携带 __os_px 标记，服务端 CatchEscape 据此把还原模式下
// location 赋值类逃逸 307 回代理。
// 注入时 __PREFIX__ 被替换为 /app/<id>/。
(function () {
  if (window.__openosPx) return;
  window.__openosPx = 1;

  var PREFIX = '__PREFIX__';
  var path = location.pathname + location.search;
  var rest = path.indexOf(PREFIX) === 0 ? path.slice(PREFIX.length) : '';
  // rest 为紧凑目标地址：https/host/a/b?q（scheme 段不写 :// —— 避免路径中的 //
  // 被浏览器/服务端归一化折叠）
  var sm = /^([a-z][a-z0-9+.-]*)\/([^\/]+)(.*)$/i.exec(rest);
  var SCHEME = sm ? sm[1].toLowerCase() : 'https';
  var AUTH = sm ? sm[2] : '';                        // host[:port]
  var ORIGIN = sm ? SCHEME + '/' + AUTH : '';        // 紧凑目标源（拼根绝对地址）
  var REALBASE = sm ? SCHEME + '://' + AUTH + (sm[3] || '/') : location.href; // 相对解析基准
  var APPID = PREFIX.split('/')[2] || '';            // /app/<id>/ → <id>
  var PXMARK = sm ? APPID + '~' + SCHEME + '~' + AUTH : ''; // 还原标记（逃逸回捕依据）

  // pxQuery 把 query 中 OAuth 回跳参数（redirect_uri/redirectUri）的绝对地址改写
  // 为代理路径：IdP 前端（Casdoor 等纯前端授权页）会从文档 URL 解析该参数、拼上
  // code 后用 location 赋值跳转——绝对地址会跨源逃逸出代理（iframe 直接导航到
  // 真实域名，登录态建立在代理之外）；改写为代理路径后跳转落回本源，回调经代理
  // 完成、会话落在代理路径下。已是代理前缀的值跳过（幂等）。服务端转发上游时
  // 会把该参数还原为注册的原始值（IdP 白名单校验不受影响）。
  function pxQuery(u) {
    if (!u || !sm || u.charAt(0) === '#') return u;
    try {
      var i = u.indexOf('#'), h = i >= 0 ? u.slice(i) : '';
      if (i >= 0) u = u.slice(0, i);
      var qi = u.indexOf('?');
      if (qi < 0) return u + h;
      var sp = new URLSearchParams(u.slice(qi + 1));
      var hit = false;
      ['redirect_uri', 'redirectUri'].forEach(function (k) {
        var v = sp.get(k);
        if (v && /^https?:\/\//i.test(v)) {
          sp.set(k, PREFIX + compact(v));
          hit = true;
        }
      });
      if (!hit) return u + h;
      return u.slice(0, qi) + '?' + sp.toString() + h;
    } catch (e) { return u; }
  }

  // withMark 给还原空间的地址补 __os_px 标记（pushState 会整体覆盖 query，须持续
  // 维持标记；绝对 URL 原样——跨源 replaceState 本就会被浏览器拒绝）
  function withMark(u) {
    if (!u || !PXMARK || u.charAt(0) === '#' || /^[a-z][a-z0-9+.-]*:/i.test(u)) return u;
    try {
      u = pxQuery(u);
      var i = u.indexOf('#'), h = i >= 0 ? u.slice(i) : '';
      if (i >= 0) u = u.slice(0, i);
      if (u.indexOf('__os_px=') >= 0) return u + h;
      return u + (u.indexOf('?') >= 0 ? '&' : '?') + '__os_px=' + PXMARK + h;
    } catch (e) { return u; }
  }

  // 还原文档地址为目标站形态。REALBASE/PREFIX/AUTH 等已在此前算好，不受影响。
  if (sm && sm[3]) {
    try { history.replaceState(history.state, '', withMark(sm[3])); } catch (e) { /* 保持原地址 */ }
  }

  function compact(abs) {
    return abs.replace(/^([a-z][a-z0-9+.-]*):\/\//i, function (_, s) { return s + '/'; });
  }

  function toProxy(u) {
    if (u == null) return u;
    if (u instanceof URL) u = u.href;
    if (typeof u !== 'string') return u;
    var s = u.trim();
    if (!s || s.charAt(0) === '#') return u;
    if (/^(data|blob|javascript|vbscript|mailto|tel|about|file|chrome|ws|wss):/i.test(s)) return u;
    if (s.indexOf(PREFIX) === 0) return s; // 已是代理路径（服务端重写过的属性值），勿再包一层
    try {
      var abs;
      if (s.indexOf('//') === 0) abs = (location.protocol === 'https:' ? 'https:' : 'http:') + s;
      else if (/^[a-z][a-z0-9+.-]*:\/\//i.test(s)) abs = s;
      else if (s.charAt(0) === '/') abs = SCHEME + '://' + AUTH + s;
      else abs = new URL(s, REALBASE).href;
      // 已落在代理前缀内的地址（如 Request 对象经浏览器解析后带全地址）保持原样；
      // 误解析到本源其他路径的，按目标根路径重新解释
      var oi = abs.indexOf(location.origin);
      if (oi === 0) {
        var sub = abs.slice(location.origin.length);
        if (sub.indexOf(PREFIX) === 0) return abs;
        abs = SCHEME + '://' + AUTH + sub;
      }
      var hi = abs.indexOf('#');
      if (hi >= 0) abs = abs.slice(0, hi);
      return PREFIX + compact(abs);
    } catch (e) {
      return u;
    }
  }

  function toWs(u) {
    try {
      var s = String(u);
      if (!/^wss?:\/\//i.test(s)) return u;
      return PREFIX + compact(s);
    } catch (e) {
      return u;
    }
  }

  // ---- fetch / XMLHttpRequest ----
  var of = window.fetch;
  if (of) {
    window.fetch = function (input, init) {
      try {
        if (typeof input === 'string') input = toProxy(input);
        else if (input instanceof URL) input = toProxy(input.href);
        else if (input && typeof input.url === 'string') {
          var nu = toProxy(input.url);
          if (nu !== input.url) input = new Request(nu, input);
        }
      } catch (e) { /* 保持原参数 */ }
      return of.call(window, input, init);
    };
  }
  var oo = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function () {
    try { arguments[1] = toProxy(arguments[1]); } catch (e) { /* 保持原参数 */ }
    return oo.apply(this, arguments);
  };

  // ---- WebSocket / EventSource / Worker ----
  function wrapCtor(Orig, map) {
    if (!Orig) return;
    var W = function (a, b) { return new Orig(map(a), b); };
    try { W.prototype = Orig.prototype; } catch (e) { /* 原型不可写时 instanceof 略有偏差，可接受 */ }
    try {
      Object.defineProperty(window, Orig.name, { value: W, writable: true, configurable: true });
    } catch (e) { /* 命名属性不可配置时跳过 */ }
  }
  wrapCtor(window.WebSocket, toWs);
  wrapCtor(window.EventSource, toProxy);
  wrapCtor(window.Worker, function (u) { return toProxy(String(u)); });

  // ---- 宿主标签页：window.open / target=_blank 在原窗体内开新 tab，不弹真浏览器窗口 ----
  // 宿主挂在窗体根元素上（frameElement 同源可达），避免多窗口共用水面全局对象互相干扰
  var host = null;
  try {
    var fe = window.frameElement;
    var root = fe && fe.closest ? fe.closest('[data-os-webapp]') : null;
    host = root ? root.__openosPxHost : null;
  } catch (e) { host = null; }

  function isProxied(s) {
    return s.indexOf(PREFIX) === 0 || s.indexOf(location.origin + PREFIX) === 0;
  }

  // ---- sendBeacon / window.open ----
  if (navigator.sendBeacon) {
    var ob = navigator.sendBeacon.bind(navigator);
    navigator.sendBeacon = function (url, data) { return ob(toProxy(url), data); };
  }
  var ow = window.open;
  window.open = function (url, name) {
    try {
      if (host && url != null && url !== '') {
        var nm = name == null ? '' : String(name);
        if (nm !== '_self' && nm !== '_top' && nm !== '_parent') {
          var mapped = toProxy(url);
          if (isProxied(mapped)) { // 新映射成功或本就是代理路径（服务端重写过的值）
            var w = host.openTab(mapped, nm);
            if (w) return w;
          }
        }
      }
    } catch (e) { /* 回落原生行为 */ }
    return ow.apply(window, arguments);
  };

  // target="_blank" 链接：捕获阶段拦截为标签页（修饰键点击放行原生新窗口）
  document.addEventListener('click', function (ev) {
    try {
      if (ev.defaultPrevented || ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return;
      var el = ev.target;
      var a = el && el.closest ? el.closest('a[href]') : null;
      if (!a || !/^_blank$/i.test(a.target || '')) return;
      var href = a.getAttribute('href') || '';
      if (!href || href.charAt(0) === '#' || /^(javascript|mailto|tel|data|blob):/i.test(href)) return;
      var mapped = toProxy(href);
      if (!isProxied(mapped)) return; // 非 http 目标走原生
      ev.preventDefault();
      ev.stopPropagation();
      if (host && host.openTab(mapped, '_blank')) return;
      location.href = mapped; // 无宿主（真浏览器标签内打开时）：就地导航，避免弹真窗
    } catch (e) { /* 放行原生行为 */ }
  }, true);

  // window.close() → 关闭所在标签页（由宿主窗口按 contentWindow 认领）
  try {
    if (host && host.closeSelf) {
      window.close = function () { host.closeSelf(window); };
    }
  } catch (e) { /* 忽略 */ }

  // ---- history（SPA 路由的地址空间）----
  // 还原模式下 SPA 路由工作在目标站地址空间：pushState/replaceState 的地址原样
  // 传递（仅维持 __os_px 标记），不加代理前缀——加前缀会让 SPA 随后读到的
  // location.pathname 带前缀、路由再度失配。网络请求才需要前缀，由 toProxy 在
  // fetch/XHR/URL 属性等请求入口统一映射。未还原（罕见：hook 跑在非代理地址）
  // 时保持旧映射行为。
  ['pushState', 'replaceState'].forEach(function (fn) {
    var orig = history[fn];
    history[fn] = function (state, title, url) {
      try {
        if (url != null && url !== '') {
          arguments[2] = PXMARK ? withMark(String(url)) : toProxy(String(url));
        }
      } catch (e) { /* 保持原参数 */ }
      return orig.apply(history, arguments);
    };
  });

  // ---- Service Worker：目标站绝不能把 SW 注册到 OpenOS 源上 ----
  try {
    if (navigator.serviceWorker && navigator.serviceWorker.register) {
      navigator.serviceWorker.register = function () {
        return Promise.reject(new Error('Service Worker 已被 OpenOS 应用代理禁用'));
      };
    }
  } catch (e) { /* 无 SW API 时忽略 */ }

  // ---- document.cookie：显式 path=/ 会把 cookie 种到整个 OpenOS 源，改写回代理路径 ----
  try {
    var desc = Object.getOwnPropertyDescriptor(Document.prototype, 'cookie');
    if (desc && desc.set && desc.get) {
      Object.defineProperty(document, 'cookie', {
        configurable: true,
        get: function () { return desc.get.call(document); },
        set: function (v) {
          try {
            var s = String(v);
            var re = /(^|;\s*)path\s*=\s*(\/[^;]*)/i;
            if (re.test(s)) {
              v = s.replace(re, function (all, sp, p) { return sp + 'path=' + PREFIX + ORIGIN + p; });
            }
          } catch (e) { /* 保持原值 */ }
          desc.set.call(document, v);
        },
      });
    }
  } catch (e) { /* cookie 描述符不可改写时忽略 */ }

  // ---- URL 反射属性：img.src = '/x' 之类的动态赋值 ----
  var urlProps = [
    [window.HTMLImageElement, 'src'], [window.HTMLScriptElement, 'src'],
    [window.HTMLLinkElement, 'href'], [window.HTMLIFrameElement, 'src'],
    [window.HTMLSourceElement, 'src'], [window.HTMLVideoElement, 'src'],
    [window.HTMLAudioElement, 'src'], [window.HTMLEmbedElement, 'src'],
    [window.HTMLTrackElement, 'src'], [window.HTMLObjectElement, 'data'],
    [window.HTMLFormElement, 'action'], [window.HTMLInputElement, 'formAction'],
    [window.HTMLButtonElement, 'formAction'], [window.HTMLAnchorElement, 'href'],
    [window.HTMLAreaElement, 'href'],
  ];
  urlProps.forEach(function (pair) {
    var C = pair[0], prop = pair[1];
    if (!C || !C.prototype) return;
    var d;
    try { d = Object.getOwnPropertyDescriptor(C.prototype, prop); } catch (e) { return; }
    if (!d || !d.set || !d.get) return;
    try {
      Object.defineProperty(C.prototype, prop, {
        configurable: true,
        get: function () { return d.get.call(this); },
        set: function (v) {
          try { v = toProxy(String(v)); } catch (e) { /* 保持原值 */ }
          d.set.call(this, v);
        },
      });
    } catch (e) { /* 属性不可改写时跳过 */ }
  });

  // ---- setAttribute 通用兜底（srcset 逐项处理）----
  var urlAttrs = {
    href: 1, src: 1, action: 1, formaction: 1, poster: 1,
    background: 1, cite: 1, data: 1, 'xlink:href': 1,
  };
  var sa = Element.prototype.setAttribute;
  Element.prototype.setAttribute = function (name, value) {
    try {
      var n = String(name).toLowerCase();
      if (urlAttrs[n]) value = toProxy(String(value));
      else if (n === 'srcset' && value) {
        value = String(value).split(',').map(function (part) {
          var t = part.trim(), k = t.indexOf(' ');
          return k < 0 ? toProxy(t) : toProxy(t.slice(0, k)) + t.slice(k);
        }).join(', ');
      }
    } catch (e) { /* 保持原参数 */ }
    return sa.call(this, name, value);
  };
})();
