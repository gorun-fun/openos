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
      // 协议相对地址按目标站 scheme 解析（代理页是 http 时 location.protocol 会把
      // //cdn 降级成 http，CDN 多为 https-only，降级形态被上游 302/拒连打断加载）
      if (s.indexOf('//') === 0) abs = SCHEME + '://' + s.slice(2);
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
      } else {
        // 无端口的本源形态：页面 JS 常以 location.protocol+'//'+location.hostname
        // 拼绝对地址（hostname 不含端口），如 http://127.0.0.1/discover——不重解释
        // 会代理到本源 80 端口（必拒）。与带端口形态同语义：按目标站路径重新解释
        var noPort = location.protocol + '//' + location.hostname + '/';
        if (abs.indexOf(noPort) === 0) abs = SCHEME + '://' + AUTH + abs.slice(noPort.length - 1);
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

  // ---- 全局快捷键转发：iframe 聚焦后宿主 window 收不到帧内按键 ----
  // 仅转发系统级组合（F4 启动台 / ⌘·Ctrl+Space 聚焦搜索 / Win·⌘ 单击松开切换
  // 启动台），普通按键与打字内容绝不外发。宿主 WebApp 校验消息来源（e.source 须
  // 是其某个标签页的 contentWindow）后交统一键盘中枢（ui/keys.ts）分发。
  try {
    if (window.top !== window.self) {
      var kSolo = false;
      var kSend = function (ev, e, extra) {
        try {
          var msg = {
            type: 'openos:key', ev: ev, key: e.key, code: e.code,
            alt: e.altKey, ctrl: e.ctrlKey, meta: e.metaKey, shift: e.shiftKey,
          };
          if (extra) for (var k in extra) msg[k] = extra[k];
          window.parent.postMessage(msg, '*');
        } catch (e2) { /* 忽略 */ }
      };
      var kIsMeta = function (e) {
        return e.key === 'Meta' || e.key === 'OS' || e.code === 'MetaLeft' || e.code === 'MetaRight';
      };
      document.addEventListener('keydown', function (e) {
        kSolo = kIsMeta(e) && !e.altKey && !e.ctrlKey && !e.shiftKey;
        if (e.key === 'F4') kSend('down', e);
        else if (e.code === 'Space' && (e.metaKey || e.ctrlKey || e.altKey)) kSend('down', e);
      }, true);
      document.addEventListener('keyup', function (e) {
        var solo = kSolo && kIsMeta(e);
        kSolo = false;
        if (solo) kSend('up', e, { soloMeta: true });
      }, true);
      window.addEventListener('blur', function () { kSolo = false; });
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

  // ---- integrity 属性全面失能：SRI 与重写代理不兼容（字节透传也常因跨源语境
  // 校验失败，kdocs 运行时注入的懒加载模块全灭 → 新建菜单不渲染）。三层：
  // property setter 吞掉、setAttribute 剥除（见下）、MutationObserver 清扫
  // innerHTML/解析器路径注入的元素 ----
  try {
    ['HTMLLinkElement', 'HTMLScriptElement'].forEach(function (k) {
      var C = window[k]; if (!C) return;
      var d = Object.getOwnPropertyDescriptor(C.prototype, 'integrity');
      if (d && d.set) {
        Object.defineProperty(C.prototype, 'integrity', {
          configurable: true, enumerable: d.enumerable,
          get: function () { return d.get ? d.get.call(this) : ''; },
          set: function () { /* SRI 失能：吞掉 */ },
        });
      }
    });
    var mo = new MutationObserver(function (muts) {
      for (var i = 0; i < muts.length; i++) {
        var add = muts[i].addedNodes;
        for (var j = 0; j < add.length; j++) {
          var n = add[j];
          if (!n.querySelectorAll) continue;
          if (n.hasAttribute && n.hasAttribute('integrity')) n.removeAttribute('integrity');
          var els = n.querySelectorAll('[integrity]');
          for (var k2 = 0; k2 < els.length; k2++) els[k2].removeAttribute('integrity');
        }
      }
    });
    mo.observe(document.documentElement, { childList: true, subtree: true });
  } catch (e) { /* 个别环境不支持则跳过 */ }

  // ---- setAttribute 通用兜底（srcset 逐项处理）----
  var urlAttrs = {
    href: 1, src: 1, action: 1, formaction: 1, poster: 1,
    background: 1, cite: 1, data: 1, 'xlink:href': 1,
  };
  var sa = Element.prototype.setAttribute;
  Element.prototype.setAttribute = function (name, value) {
    try {
      var n = String(name).toLowerCase();
      if (n === 'integrity') return; // SRI 与重写代理不兼容（运行时注入的 chunk 同样剥除）
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

  // ---- document.domain 赋值静默化 ----
  // 反代页运行在本源（如 127.0.0.1:8081），站点壳页跨子域通信脚本常设置
  // document.domain（网易云音乐壳页）：本源非目标站后缀，非 sandbox 下赋值抛
  // SecurityError、sandbox 帧直接禁——未捕获会中断页面初始化（壳页后续对内容
  // iframe 的导航代码不再执行）。反代下跨子域通信已被同源化（都在 /app/ 前缀），
  // 设置 domain 本就无意义：包装为静默 no-op，页面感知"设置成功"，读取原样。
  try {
    var domDesc = Object.getOwnPropertyDescriptor(Document.prototype, 'domain');
    if (domDesc && domDesc.set && domDesc.configurable) {
      Object.defineProperty(Document.prototype, 'domain', {
        configurable: true,
        get: function () { try { return domDesc.get.call(this); } catch (e) { return location.hostname; } },
        set: function () { /* 反代页中设置无意义，静默吞掉 */ },
      });
    }
  } catch (e) { /* 不可重定义时忽略 */ }

  // ---- contentWindow.location 导航兜底（about:blank 帧）----
  // 部分站点的壳页以 location.protocol+'//'+location.hostname 拼绝对地址后，直接对
  // 子帧 contentWindow.location 赋值导航（网易云音乐壳页的 g_iframe：src 恒
  // about:blank，经 location 赋值跳 /discover——location 是 Unforgeable，属性包装
  // 拦不住；拼出的无端口本源地址直连 80 端口必拒，连服务端回捕都够不着）。
  // 仅对 src 为空/about:blank 的帧生效（此类帧只可能被 JS 经 location 导航）：
  // 包装 contentWindow 返回代理，location 的赋值/assign/replace 经 toProxy 改写
  // 回代理路径；同一真窗口与 location 各缓存一份保持引用相等性。有实际地址的帧
  // 原样返回，零影响（美团等容器的 postMessage 通信不受波及）。
  try {
    var cwDesc = Object.getOwnPropertyDescriptor(window.HTMLIFrameElement.prototype, 'contentWindow');
    if (cwDesc && cwDesc.get && cwDesc.configurable) {
      var cwCache = new WeakMap();  // iframe → {win, proxy}
      var locCache = new WeakMap(); // 真 location → 伪装对象
      // 导航执行器：toProxy 改写后先在当前上下文直接执行（非 sandbox 场景正常
      // 工作）；被 sandbox 拒（外层 iframe 带 sandbox 防 framebusting 时，文档无权
      // 导航子帧，Chrome 硬限制，SecurityError 同步抛出）则转发顶层 OpenOS 页
      //（同源、不在 sandbox 内，有权导航任何同源帧）代为执行；顶层直开代理页
      //（parent===window）或宿主未挂 __osPxNav__ 时 rethrow 原异常
      var pxNavRaw = function (loc, pv) {
        try {
          loc.href = pv;
        } catch (e) {
          if (window.parent !== window && window.parent.__osPxNav__) {
            window.parent.__osPxNav__(loc, pv);
            return;
          }
          throw e;
        }
      };
      var pxNav = function (loc, v) {
        var pv;
        try { pv = toProxy(String(v)); } catch (e) { pv = v; }
        pxNavRaw(loc, pv);
      };
      // 伪装 Location 用普通对象：Proxy 方案在 non-configurable 属性上两头不讨好——
      // href 等 accessor 经 set trap 包装合法，但 replace/assign 这类 non-configurable
      // 数据属性 get 必须返回原值（invariant），返回原函数又会被以代理为 this 调用
      //（Illegal invocation，网易云音乐壳页即踩此坑）。普通对象无 invariant 限制：
      // 读取属性转发真值，三个导航入口（href 赋值/assign/replace）统一走 pxNav。
      var wrapFrameLoc = function (loc) {
        var p = locCache.get(loc);
        if (p) return p;
        var shadow = {};
        ['href', 'hash', 'search', 'host', 'hostname', 'pathname', 'port', 'protocol', 'origin'].forEach(function (k) {
          try {
            Object.defineProperty(shadow, k, {
              configurable: true,
              get: function () { try { return loc[k]; } catch (e) { return ''; } },
              set: function (v) { pxNav(loc, v); },
            });
          } catch (e) { /* 个别属性不可配则跳过 */ }
        });
        try { shadow.toString = function () { return loc.href; }; } catch (e) { }
        try { shadow.valueOf = function () { return loc.href; }; } catch (e) { }
        try {
          shadow.assign = function (u) {
            var pv; try { pv = toProxy(String(u)); } catch (e) { pv = u; }
            try { loc.assign(pv); } catch (e) {
              if (window.parent !== window && window.parent.__osPxNav__) { window.parent.__osPxNav__(loc, pv); return; }
              throw e;
            }
          };
        } catch (e) { }
        try {
          shadow.replace = function (u) {
            var pv; try { pv = toProxy(String(u)); } catch (e) { pv = u; }
            try { loc.replace(pv); } catch (e) {
              if (window.parent !== window && window.parent.__osPxNav__) { window.parent.__osPxNav__(loc, pv); return; }
              throw e;
            }
          };
        } catch (e) { }
        try { shadow.reload = function () { try { loc.reload(); } catch (e) { /* 无权重载则放弃 */ } }; } catch (e) { }
        locCache.set(loc, shadow);
        return shadow;
      };
      Object.defineProperty(window.HTMLIFrameElement.prototype, 'contentWindow', {
        configurable: true,
        get: function () {
          var w = cwDesc.get.call(this);
          if (!w) return w;
          var src = this.getAttribute('src');
          if (src && src !== 'about:blank') return w;
          var c = cwCache.get(this);
          if (c && c.win === w) return c.proxy;
          var bound = Object.create(null);
          var proxy = new Proxy(w, {
            get: function (t, k) {
              if (k === 'location') return wrapFrameLoc(t.location);
              var v = t[k];
              if (typeof v === 'function') {
                // invariant：non-configurable non-writable 数据属性的 get 必须返回原值
                try {
                  var d = Object.getOwnPropertyDescriptor(t, k);
                  if (d && d.configurable === false && d.writable === false) return v;
                } catch (e) { }
                return bound[k] || (bound[k] = v.bind(t));
              }
              return v;
            },
            set: function (t, k, v) {
              if (k === 'location') { pxNav(t.location, v); return true; }
              t[k] = v;
              return true;
            },
          });
          cwCache.set(this, { win: w, proxy: proxy });
          return proxy;
        },
      });
    }
  } catch (e) { /* contentWindow 不可重定义时忽略 */ }

  // ---- 初始导航失败自愈 ----
  // sandbox 帧内的站点初始化脚本若在文档解析期对子帧发起 location 导航，个别时序
  // 下仍会拿到原始 SecurityError（网易云音乐壳页实测：初始 replace 失败、之后的
  // hashchange 同路径成功）。error 观察标记"发生过 sandbox 导航拒绝"，load 后若
  // 页面仍存在 about:blank 帧（初始内容帧从未被导航），补发一次 hashchange 让
  // 站点自身的 hash 路由重放初始化——对 SPA 幂等，仅补一次。
  try {
    var pxNavDenied = false;
    window.addEventListener('error', function (ev) {
      try {
        if (!pxNavDenied && /does not have permission to navigate/.test((ev && ev.message) || '')) {
          pxNavDenied = true;
        }
      } catch (e) { }
    }, true);
    var pxHeal = function () {
      if (!pxNavDenied) return;
      var frs = document.querySelectorAll('iframe');
      for (var i = 0; i < frs.length; i++) {
        var s = frs[i].getAttribute('src');
        if (!s || s === 'about:blank') {
          try {
            var ev2 = document.createEvent('HTMLEvents');
            ev2.initEvent('hashchange', false, false);
            window.dispatchEvent(ev2);
          } catch (e) { }
          return; // 只补一次
        }
      }
    };
    if (document.readyState === 'complete') setTimeout(pxHeal, 0);
    else window.addEventListener('load', function () { setTimeout(pxHeal, 800); });
  } catch (e) { /* 观察器不可用时忽略 */ }

  // ---- 动态插入的 iframe 兜底 ----
  // innerHTML/document.write 插入的 iframe，src 由 HTML 解析器设置，不走上面的属性
  // setter 与 setAttribute（网易云壳页的内容 iframe即此形态：JS 以
  // location.protocol+'//'+location.hostname 拼绝对地址插入，丢代理前缀与端口，
  // 直连本源 80 端口必拒）。MutationObserver 观察新增节点，iframe 的 src 经
  // toProxy 改写后逃逸形态归位到代理路径（幂等：已是代理路径的不动）。
  try {
    var fixFrameSrc = function (f) {
      if (f.__osPxSeen) return;
      f.__osPxSeen = 1;
      var raw = f.getAttribute('src');
      if (!raw) return;
      try {
        var mapped = toProxy(raw);
        if (mapped !== raw) f.src = mapped; // 属性 setter 已包装且 toProxy 对代理路径幂等
      } catch (e) { /* 保持原值 */ }
    };
    var mo = new MutationObserver(function (muts) {
      for (var i = 0; i < muts.length; i++) {
        var nodes = muts[i].addedNodes;
        for (var k = 0; k < nodes.length; k++) {
          var n = nodes[k];
          if (n.nodeType !== 1) continue;
          if (n.tagName === 'IFRAME') { fixFrameSrc(n); continue; }
          try {
            var fs = n.querySelectorAll('iframe');
            for (var q = 0; q < fs.length; q++) fixFrameSrc(fs[q]);
          } catch (e) { /* 非元素子树忽略 */ }
        }
      }
    });
    mo.observe(document.documentElement || document, { childList: true, subtree: true });
  } catch (e) { /* Observer 不可用时维持现状 */ }
})();
