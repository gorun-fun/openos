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

  // ---- 「清除缓存」登记面（页面侧写入的记账，WebApp 清除按钮消费）----
  // 服务端登记簿只覆盖响应 Set-Cookie 与请求携带；页面 JS 写的 Cookie 与打开过
  // 的 IndexedDB 库不经服务端——这里把名字/路径/库名记进本应用命名空间（原生
  // 存储引用，不经下方隔离门面；键随 __px_<id>:: 前缀，清除时被前缀清扫收走）。
  var RAWLS = null; try { RAWLS = window.localStorage; } catch (e) { }
  function pxLog(key, val) {
    if (!APPID || !RAWLS) return;
    try {
      var K = '__px_' + APPID + '::' + key, cur;
      try { cur = JSON.parse(RAWLS.getItem(K) || '[]'); } catch (e) { cur = []; }
      if (cur.indexOf(val) < 0) {
        if (cur.length > 500) cur.shift();
        cur.push(val);
        RAWLS.setItem(K, JSON.stringify(cur));
      }
    } catch (e) { }
  }

  // ---- 文档起点错误捕获器（排障基础设施）----
  // 页面早期异常（微前端框架把 mount 错误吞成 minified 告警、详情无从获取）是
  // 代理排障的最大盲区。hook 在文档起点捕获 window error / unhandledrejection /
  // console.error 存入 window.__osPageErrors（同源桌面侧可读，上限 60 条），不
  // 改变任何页面行为。
  try {
    var ERRS = [];
    Object.defineProperty(window, '__osPageErrors', { configurable: true, get: function () { return ERRS.slice(); } });
    function osTrap(kind, detail) {
      try {
        if (ERRS.length < 60) ERRS.push(Date.now() + ' ' + kind + ' ' + String(detail).slice(0, 500));
      } catch (e) { }
    }
    window.addEventListener('error', function (e) {
      osTrap('error', (e.message || '') + ' @' + String(e.filename || '').slice(-90) + ':' + (e.lineno || '') + ' :: ' + String((e.error && e.error.stack) || '').slice(0, 300));
    });
    window.addEventListener('unhandledrejection', function (e) {
      var r = e.reason;
      osTrap('reject', String((r && (r.stack || r.message)) || r).slice(0, 350));
    });
    var osCE = console.error;
    console.error = function () {
      var a = [];
      for (var i = 0; i < arguments.length; i++) {
        var x = arguments[i];
        try { a.push(x && x.stack ? String(x.stack).slice(0, 250) : JSON.stringify(x)); } catch (e) { a.push(String(x)); }
      }
      osTrap('console.error', a.join(' | ').slice(0, 350));
      return osCE.apply(console, arguments);
    };
    var osCW = console.warn;
    console.warn = function () {
      var a = [];
      for (var i = 0; i < arguments.length; i++) {
        try { a.push(String(arguments[i]).slice(0, 160)); } catch (e) { }
      }
      osTrap('console.warn', a.join(' | ').slice(0, 300));
      return osCW.apply(console, arguments);
    };
  } catch (e) { /* 不破坏页面 */ }


  // ---- 存储隔离：代理页与宿主桌面同源，localStorage/sessionStorage 原生直写
  // 会与宿主互踩——宿主自身会话键（openos 桌面 openos_token）、被代理的嵌套宿主
  // 登录引导页写 token/openos_token（覆盖外层会话，外层登出 clearToken 又连锁杀
  // 内层）、以及站点 SDK 的 localStorage.clear() 清光宿主登录态（美团风控曾踩）。
  // 门面把本代理页可见的存储整体映射到 __px_<appid>:: 前缀：页面代码无感读写
  // 自己的命名空间，clear() 只清本应用。属性式读写（localStorage.x）不覆盖——
  // 标准 API 已覆盖现代站点，保持门面极简；defineProperty 失败时保持原生（隔离
  // 失效但不破坏页面）。----
  function pxStorage(orig, ns) {
    function own() { // 本命名空间键快照（保持底层插入序）
      var out = [];
      for (var i = 0; i < orig.length; i++) {
        var k = orig.key(i);
        if (k.indexOf(ns) === 0) out.push(k.slice(ns.length));
      }
      return out;
    }
    return {
      get length() { return own().length; },
      key: function (i) { var a = own(); return i >= 0 && i < a.length ? a[i] : null; },
      getItem: function (k) { return orig.getItem(ns + k); },
      setItem: function (k, v) { orig.setItem(ns + k, String(v)); },
      removeItem: function (k) { orig.removeItem(ns + k); },
      clear: function () { own().forEach(function (k) { orig.removeItem(ns + k); }); }
    };
  }
  if (APPID) {
    var NS = '__px_' + APPID + '::';
    var oLS = window.localStorage, oSS = window.sessionStorage;
    try {
      Object.defineProperty(window, 'localStorage', {
        configurable: true,
        get: function () { return pxStorage(oLS, NS); }
      });
      Object.defineProperty(window, 'sessionStorage', {
        configurable: true,
        get: function () { return pxStorage(oSS, NS); }
      });
    } catch (e) { /* 原生属性不可覆盖时保持原生（隔离失效但不破坏页面） */ }
  }

  // ---- 跨帧 postMessage 的源语义还原（代理同源化的结构性补偿）----
  // 代理把目标站的全部帧（含其跨域子帧）同源化到本源后，页面间 postMessage 的
  // 两个真实世界语义都被破坏：①发送方 targetOrigin 写真实站域名——收件帧真实
  // 源已是本源，浏览器按源不符【整条丢弃】；②收件方的 e.origin 域名白名单校验
  // （美团 ME 容器 hotel-container-redirect 指令：origin 须 .meituan.com/
  // .sankuai.com 后缀，不通过即静默丢弃）——同源化后 e.origin 恒为本源、校验
  // 全失配，容器收不到路由指令、内容区永远停在默认页（线上 A/B 实证：真实站
  // 正常渲染订单列表，代理内同账号点同一菜单恒 404）。两向分别补偿：
  // ① 发送侧：targetOrigin 为绝对 http(s) 源且非本源 → 改 '*' 投递（消息本就
  //    发给本源帧树内的窗，只是字面源不符；真实跨源目标窗在代理内不存在）；
  // ② 接收侧：同源帧间消息（e.origin===本源）且发送帧是代理帧（地址可解出
  //    目标站源）→ 以 Proxy 把 e.origin 呈现为发送帧的目标站源，页面域名校验
  //    按真实世界语义通过。e.source/其余属性原样（帧匹配类逻辑不受影响）；
  //    自身消息（e.source===window）、无源消息（MessageChannel 端口等）与发送
  //    方非代理帧（宿主桌面等）一律原样透传。
  function pxFrameOrigin(win) {
    try {
      var href = win.location.href;
      var qi = href.indexOf("?"), q = qi >= 0 ? href.slice(qi) : "";
      var mk = /__os_px=[a-z0-9._-]+~([a-z][a-z0-9+.-]*)~([^&#]+)/i.exec(q);
      if (mk) return mk[1].toLowerCase() + "://" + decodeURIComponent(mk[2]);
      var pn = win.location.pathname;
      if (pn.indexOf("/app/") === 0) {
        var m = /^[a-z0-9._-]+\/([a-z][a-z0-9+.-]*)\/([^/?#]+)/i.exec(pn.slice(5));
        if (m) return m[1].toLowerCase() + "://" + m[2];
      }
    } catch (e) { }
    return "";
  }
  // ---- 发送侧：targetOrigin 放行 + 发送帧身份（source）保持 ----
  // 代理把目标站的全部帧同源化到本源后有两个真实世界语义破坏：
  // ① 发送方 targetOrigin 写真实站域名，收件帧真实源已是本源，浏览器按源不符
  //    【整条丢弃】——须改 '*' 投递；
  // ② 更隐蔽：投递必经收件帧的 postMessage（即本包装器，定义于收件帧 realm）。
  //    MessageEvent.source 跟随【执行代码的 realm】（纯净帧三角实测：l1 realm
  //    定义的函数调原生 → source=l1；l0 realm 函数 → source=l0）——包装器在本
  //    帧 realm 内重调原生 postMessage，source 即被记为【收件帧自身】而非真实
  //    发送帧。目标站跨帧协议普遍以 e.source === 期望窗口 作门（美团 @post-me
  //    Messenger 的 D(e,t,n)、listenChildIframePostMessage 的直接子帧校验），
  //    source 失真即被静默拒绝：握手死循环、容器路由指令丢弃、菜单点击无响应
  //    （线上实证：l1 脚本调 parent.postMessage，收件侧 ev.source===l0）。
  //    修复：每个代理帧的 hook 在自身 realm 预埋 __osRelaySend；包装器识别到
  //    「外 realm 调用」（arguments.callee.caller 的 Function 构造器在其定义
  //    realm 求值，返回该 realm 的 window——同源可跨 realm 构造）时，经调用
  //    方的 relay 投递：relay 在调用方 realm 执行，source 恢复为真实发送帧。
  //    识别不了（严格模式 caller 不可见/顶层脚本/原生入口）回落直投——不劣于
  //    旧行为。本 realm 调用（页面对自身发消息）source 本就是自身，直投。
  var opm = window.postMessage.bind(window);
  window.__osNativePM = opm; // 本帧原生 postMessage（hook 在文档起点捕获，先于一切站点代码）
  window.__osRelaySend = function (targetWin, msg, targetOrigin, transfer) {
    try {
      if (targetWin && targetWin.__osNativePM) {
        return targetWin.__osNativePM(msg, targetOrigin, transfer);
      }
    } catch (e) { }
    return targetWin.postMessage(msg, targetOrigin, transfer);
  };
  function pxCallerWindow() {
    // arguments.callee.caller 对严格模式调用方（现代打包产物普遍 strict）访问即
    // 抛，识别恒失败回落直投 → source 失真（线上实证：strict 函数调
    // parent.postMessage，收件侧 source 仍是收件帧、@post-me 握手永不完成）。
    // 改用 V8 栈帧取调用函数对象：CallSite.getFunction() 不受 strict 限制，
    // 函数对象的 constructor 在其定义 realm 求值（同源可跨 realm 构造执行），
    // 返回该 realm 的 window。sites[0] 是本包装器，从 1 起找第一个能解析出
    // 「非本帧 window」的调用帧。非 V8/解析失败返回 null（回落直投不劣于旧）。
    try {
      var orig = Error.prepareStackTrace;
      Error.prepareStackTrace = function (e, sites) { return sites; };
      var err = new Error();
      var sites = err.stack;
      Error.prepareStackTrace = orig;
      if (!sites || !sites.length) return null;
      for (var i = 1; i < sites.length && i < 6; i++) {
        var fn = null;
        try { fn = sites[i].getFunction(); } catch (e) { }
        if (!fn) continue;
        var cw = null;
        try { cw = fn.constructor("return window")(); } catch (e) { }
        if (cw && cw !== window) return cw;
      }
      return null;
    } catch (e) { return null; }
  }
  window.postMessage = function (msg, targetOrigin, transfer) {
    var to = targetOrigin;
    var abs = false;
    try {
      if (typeof targetOrigin === "string" && /^https?:\/\//i.test(targetOrigin) && targetOrigin !== location.origin) {
        to = "*";
        abs = true;
      }
    } catch (e) { /* 保持原参数 */ }
    try {
      var cw = pxCallerWindow();
      if (cw && cw !== window && typeof cw.__osRelaySend === "function") {
        return cw.__osRelaySend(window, msg, to, transfer);
      }
    } catch (e) { /* 回落直投 */ }
    // 绝对源跨帧协议消息（master→slave 回复等）识别失败时的父窗协作回落：
    // 站点 master 以还原态真实源（如 https://eb.meituan.com）回调子帧时经本包装器
    // 投递，识别失败回落 opm 会在本帧 realm 执行 → MessageEvent.source=收件帧自身，
    // 子帧 @post-me 门（e.source===父窗 && e.origin===me源）恒拒——握手每秒重试、
    // 桥永不建立、slave 空白后被降级重定向（线上实证：工作台 getBridgeDescriptors
    // 10s 超时→空白→跳默认页）。绝对源 targetOrigin 只出现在跨帧协议消息里（自发
    // 消息不会写外部绝对源——写了也按源不符丢弃），此时经父窗 __osRelaySend（父
    // realm 执行）投递：source=父窗=真实发送方（美团拓扑 master 直答子帧；万一真
    // 发送方是兄弟帧则 source 失真，但不劣于旧的恒为收件帧自身）。
    if (abs && window.parent !== window) {
      try {
        if (typeof window.parent.__osRelaySend === "function") {
          return window.parent.__osRelaySend(window, msg, to, transfer);
        }
      } catch (e) { /* 回落直投 */ }
    }
    return opm(msg, to, transfer);
  };
  // ---- 发送方协作：子帧把父窗的 postMessage 包一层【本帧 realm】的放行器 ----
  // 目标侧包装器（上面）对严格模式调用方的 realm 识别不可靠（arguments.callee.
  // caller 对 strict 抛、V8 CallSite.getFunction() 对 eval/优化帧返回 null，双路
  // 线上实证均失败）——source 恒失真、@post-me 握手永不完成（美团 slave 应用
  // 空白 20 秒后被「无容器」重定向默认页）。子帧的 hook 主动包父窗（同源可写）：
  // 放行器定义于本帧 realm，经父窗 hook 预埋的 __osNativePM 原生引用投递，
  // source=本帧（真实发送者）✓。①绝对源 targetOrigin 同时放行为 '*'；②父窗
  // 自发（targetOrigin 非绝对源）转交父窗原目标侧包装器（父 realm 执行，
  // source=父窗自身 ✓）。幂等标记防多子帧叠加（后装子帧的发送经先装者的
  // realm，source 失真但不劣于旧；美团拓扑为单子链，先装者即唯一活跃子帧）。
  try {
    if (window.parent !== window) {
      var pxpw = window.parent;
      if (!pxpw.__osPxParentRelax) {
        pxpw.__osPxParentRelax = true;
        var pxpnat = pxpw.__osNativePM || pxpw.postMessage.bind(pxpw);
        var pxpwrap = pxpw.postMessage; // 父窗的目标侧包装器（父 realm，自发消息经此）
        pxpw.postMessage = function (msg, t, tr) {
          var to2 = t;
          try {
            if (typeof t === "string" && /^https?:\/\//i.test(t) && t !== pxpw.location.origin) {
              to2 = "*";
              return pxpnat(msg, to2, tr); // 跨帧协议投递：本帧 realm → source=本帧
            }
          } catch (e) { }
          return pxpwrap.call(pxpw, msg, t, tr); // 父窗自身语义（'*'/同源）：交回父侧包装器
        };
      }
    }
  } catch (e) { /* 父窗跨源或不可写时忽略（本形态必同源，防御性兜底） */ }
  function pxRestoreOrigin(ev) {
    try {
      if (!ev || ev.origin !== location.origin) return ev;
      var src = ev.source;
      if (!src || src === window) return ev;
      var o = pxFrameOrigin(src);
      if (!o) return ev; // 发送方非代理帧（宿主桌面等）：原样
      return new Proxy(ev, {
        get: function (t, k) {
          if (k === "origin") return o;
          var v = t[k];
          return typeof v === "function" ? v.bind(t) : v;
        }
      });
    } catch (e) { return ev; }
  }
  var pxMsgWrap = new WeakMap();
  var oael = window.addEventListener;
  window.addEventListener = function (type, fn, opts) {
    try {
      if (type === "message" && typeof fn === "function") {
        var wrapped = function (ev) { return fn.call(this, pxRestoreOrigin(ev)); };
        pxMsgWrap.set(fn, wrapped);
        return oael.call(window, type, wrapped, opts);
      }
    } catch (e) { /* 回落原生注册 */ }
    return oael.apply(window, arguments);
  };
  var orel = window.removeEventListener;
  window.removeEventListener = function (type, fn, opts) {
    try {
      if (type === "message" && typeof fn === "function" && pxMsgWrap.has(fn)) {
        return orel.call(window, type, pxMsgWrap.get(fn), opts);
      }
    } catch (e) { }
    return orel.apply(window, arguments);
  };

  // pxQuery 把 query 中 OAuth 回跳参数（redirect_uri/redirectUri/continue/
  // continueUrl）的绝对地址改写为代理路径：IdP 前端（Casdoor 等纯前端授权页）会
  // 从文档 URL 解析该参数、拼上 code 后用 location 赋值跳转——绝对地址会跨源
  // 逃逸出代理（iframe 直接导航到真实域名，登录态建立在代理之外）；改写为代理
  // 路径后跳转落回本源，回调经代理完成、会话落在代理路径下。值是宿主自身地址
  // （还原模式下页面 JS 用 location.origin 拼的回跳）先重解释为目标域——直接
  // 包前缀会把宿主地址当目标源代理出去。continue/continueUrl 是美团 ebooking
  // （epassport oauth-login → me.meituan.com callback）的回跳参数。
  //
  // 改写结果保持【宿主绝对 URL】形态（location.origin+代理路径）而非相对路径：
  // 目标站页面 JS 常把该参数值读出放进登录请求体（美团 gw/login/password 的
  // continueUrl 字段），服务端在验密前先校验它是合法绝对 URL——相对路径形态
  // 会被判「系统配置异常-5002」直接拒绝（实测）；绝对形态与还原模式下 JS 用
  // location.origin 拼出的原生形态同构，服务端放行，且跳转导航仍落回本源代理
  // 路径、服务端 restorePxParam（宿主形态还原）与 rewriteLocation（isSelfHost
  // 挂当前域）两条既有链路都对它闭环。已是代理前缀的值跳过（幂等）。服务端
  // 转发上游时会把该参数还原为注册的原始值（IdP 白名单校验不受影响）。
  function pxQuery(u) {
    if (!u || !sm || u.charAt(0) === '#') return u;
    try {
      var i = u.indexOf('#'), h = i >= 0 ? u.slice(i) : '';
      if (i >= 0) u = u.slice(0, i);
      var qi = u.indexOf('?');
      if (qi < 0) return u + h;
      var sp = new URLSearchParams(u.slice(qi + 1));
      var hit = false;
      ['redirect_uri', 'redirectUri', 'continue', 'continueUrl'].forEach(function (k) {
        var v = sp.get(k);
        // 幂等：已是本源绝对+代理前缀形态（本函数上一轮的产物）不再包一层
        if (v && /^https?:\/\//i.test(v) && v.indexOf(location.origin + PREFIX) !== 0) {
          sp.set(k, location.origin + PREFIX + compact(rebase(v)));
          hit = true;
        }
      });
      if (!hit) return u + h;
      return u.slice(0, qi) + '?' + sp.toString() + h;
    } catch (e) { return u; }
  }

  // rebase 把宿主自身绝对地址重解释为目标域地址（带端口与不带端口两种拼法，
  // 与 toProxy 的本源重解释同语义；含跨 scheme 硬编码形态），其余地址原样返回。
  function rebase(v) {
    var o = location.origin;
    if (v.indexOf(o + '/') === 0) return SCHEME + '://' + AUTH + v.slice(o.length);
    var np = location.protocol + '//' + location.hostname + '/';
    if (v.indexOf(np) === 0) return SCHEME + '://' + AUTH + v.slice(np.length - 1);
    var xs = (location.protocol === 'https:' ? 'http' : 'https') + '//' + location.hostname + '/';
    if (v.indexOf(xs) === 0) return SCHEME + '://' + AUTH + v.slice(xs.length - 1);
    return v;
  }

  // withMark 给还原空间的地址补 __os_px 标记（pushState 会整体覆盖 query，须持续
  // 维持标记）。绝对 URL 仅同源形态折回路径再补标记——跨源 replaceState/pushState
  // 本就被浏览器拒绝；同源绝对地址（SPA 用 location.origin+path 拼出，代理页里
  // origin 已是宿主源、浏览器视为同源放行）此前原样透传会进入【无标记】形态：
  // 后续 location 赋值类逃逸的 Referer 无标记，服务端 CatchEscape/PxEscape 回捕
  // 失守，iframe 逃到宿主根路径落 SPA 兜底（美团 ebooking 门户新工作台实测：
  // merchant/home 以绝对地址 pushState 后未登录弹 /login/ 即整窗变「不能在
  // iframe 内打开」守卫页）。折回仅取 path+query，hash 部分原样拼回。
  function withMark(u) {
    if (!u || !PXMARK || u.charAt(0) === '#') return u;
    try {
      if (/^[a-z][a-z0-9+.-]*:\/\//i.test(u)) {
        var o = location.origin;
        var np = location.protocol + '//' + location.hostname + '/';
        var xs = (location.protocol === 'https:' ? 'http' : 'https') + '//' + location.hostname + '/';
        var hit = -1;
        if (u.indexOf(o + '/') === 0) hit = o.length;
        else if (u.indexOf(np) === 0) hit = np.length - 1;
        else if (u.indexOf(xs) === 0) hit = xs.length - 1;
        if (hit < 0) return u; // 跨源：原样（浏览器本就拒绝该类 replaceState）
        u = u.slice(hit) || '/';
      }
      u = pxQuery(u);
      var i = u.indexOf('#'), h = i >= 0 ? u.slice(i) : '';
      if (i >= 0) u = u.slice(0, i);
      if (u.indexOf('__os_px=') >= 0) return u + h;
      return u + (u.indexOf('?') >= 0 ? '&' : '?') + '__os_px=' + PXMARK + h;
    } catch (e) { return u; }
  }

  // 还原文档地址为目标站形态。REALBASE/PREFIX/AUTH 等已在此前算好，不受影响。
  // hash 原样保留：iframe src 携带的业务路由（#/statement-…，美团 ebooking 微前端
  // 挂载链）在 replaceState 整体覆盖地址时会连带清掉，SPA 就以无路由形态初始化
  if (sm && sm[3]) {
    try { history.replaceState(history.state, '', withMark(sm[3]) + (location.hash || '')); } catch (e) { /* 保持原地址 */ }
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
    // 已是代理路径（服务端重写过的属性值）勿再包一层；query 里的回跳参数仍按
    // 本页面目标域改写——服务端只改地址不改 query 参数值，宿主地址形态的
    // continue 在子文档（跨主机 OAuth 表单页）里 rebase 会落到表单域而非发起域
    if (s.indexOf(PREFIX) === 0) return pxQuery(s);
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
        if (sub.indexOf(PREFIX) === 0) return pxQuery(abs);
        abs = SCHEME + '://' + AUTH + sub;
      } else {
        // 无端口的本源形态：页面 JS 常以 location.protocol+'//'+location.hostname
        // 拼绝对地址（hostname 不含端口），如 http://127.0.0.1/discover——不重解释
        // 会代理到本源 80 端口（必拒）。与带端口形态同语义：按目标站路径重新解释
        var noPort = location.protocol + '//' + location.hostname + '/';
        if (abs.indexOf(noPort) === 0) abs = SCHEME + '://' + AUTH + abs.slice(noPort.length - 1);
        // 跨 scheme 同主机无端口形态：生产站恒 https 的前端常硬编码 'https://'+
        // location.hostname 拼绝对地址（美团 ebooking 工作台微前端加载 sub-app
        // 子应用入口即此形态）——代理页是 http 时不匹配上两种形态，漏重解释会
        // 代理到本机 https 端口被 SSRF 防线拒绝，子应用加载 502
        var xScheme = (location.protocol === 'https:' ? 'http' : 'https') + '://' + location.hostname + '/';
        if (abs.indexOf(xScheme) === 0) abs = SCHEME + '://' + AUTH + abs.slice(xScheme.length - 1);
      }
      var hi = abs.indexOf('#');
      // hash 是客户端路由状态（SPA 微前端入口把业务路由放 hash：美团 ebooking
      // SDK 以 iframe.src=tpt…/index.html#/statement-… 挂载业务页），剥掉后业务页
      // 以无路由形态初始化、降级成门户首页并自愈式再挂容器（无限套娃的根因）；
      // hash 不随请求发往代理服务端、纯客户端语义，须原样拼回
      var hh = hi >= 0 ? abs.slice(hi) : '';
      if (hi >= 0) abs = abs.slice(0, hi);
      return pxQuery(PREFIX + compact(abs)) + hh;
    } catch (e) {
      return u;
    }
  }

  // toWs：WebSocket 地址映射，返回【绝对 ws(s)://宿主+代理路径】——构造器不接受
  // 相对地址（曾经的实现返回裸路径，new WebSocket 直接 SyntaxError，等于从未生效），
  // 协议跟页面走（https 页面连 ws:// 会被混合内容拦）。自源形态
  // （ws://<location.host>/…）先重解释为目标域——代理页里 location.host 是代理
  // 宿主，页面 JS（openos 桌面总线等）按 location 拼出的 ws 地址会把连接拨到宿主
  // 自己（回环地址非配置目标被 SSRF 防线拒绝）；重解释后嵌套宿主（grf-hotel 这类
  // 自身就是 openos 宿主的系统）的总线 /ws 经代理回到目标站自己的 /ws，令牌随
  // query 原样透传。相对地址按 REALBASE 补全（兜底不影响主路径）。
  function toWs(u) {
    try {
      var s = String(u);
      if (!/^wss?:\/\//i.test(s)) {
        try { s = new URL(s, REALBASE).href.replace(/^http/i, 'ws'); } catch (e) { return u; }
        if (!/^wss?:\/\//i.test(s)) return u;
      }
      var wsBase = (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + PREFIX;
      var m = /^(ws|wss):\/\/([^\/]+)([\s\S]*)$/i.exec(s);
      if (m && (m[2] === location.host || m[2] === location.hostname)) {
        return wsBase + m[1].toLowerCase() + '/' + AUTH + m[3];
      }
      return wsBase + compact(s);
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
        if (init && typeof init.body === 'string') {
          init = Object.assign({}, init, { body: restoreBody(init.body) });
        }
      } catch (e) { /* 保持原参数 */ }
      return of.call(window, input, init);
    };
  }
  // restoreBody 把请求体（JSON）中 OAuth 回跳字段的代理形态值还原为目标站
  // 原始绝对地址：页面 JS 常把文档 URL 里的 continue/redirect_uri 读出放进登录
  // 请求体（美团 gw/login/password 的 continueUrl 字段），目标站在验密前先校验
  // 它是注册的原始形态——携带代理前缀段（/app/<id>/https/<host>/…）或宿主前缀
  // 的形态会被判「系统配置异常-5002」直接拒绝（实测）。非 JSON / 未命中字段
  // 原样返回。
  function restoreBody(b) {
    if (typeof b !== 'string' || b.charAt(0) !== '{') return b;
    try {
      var obj = JSON.parse(b), hit = false;
      ['redirect_uri', 'redirectUri', 'continue', 'continueUrl'].forEach(function (k) {
        var v = obj[k];
        if (typeof v === 'string') {
          var i = v.indexOf(PREFIX);
          if (i >= 0) {
            var m = /^([a-z][a-z0-9+.-]*)\/(.+)$/i.exec(v.slice(i + PREFIX.length));
            if (m) { obj[k] = m[1] + '://' + m[2]; hit = true; }
          }
        }
      });
      return hit ? JSON.stringify(obj) : b;
    } catch (e) { return b; }
  }

  var oo = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function () {
    try { arguments[1] = toProxy(arguments[1]); } catch (e) { /* 保持原参数 */ }
    return oo.apply(this, arguments);
  };
  var oSend = XMLHttpRequest.prototype.send;
  XMLHttpRequest.prototype.send = function (body) {
    try { body = restoreBody(body); } catch (e) { /* 保持原参数 */ }
    return oSend.call(this, body);
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

  // ---- IndexedDB 库名登记（清除按钮消费）：库名无归属信息，打开即记账 ----
  try {
    if (window.indexedDB && window.IDBFactory && IDBFactory.prototype.open) {
      var oIdbOpen = IDBFactory.prototype.open;
      IDBFactory.prototype.open = function (name) {
        try { pxLog('__osPxIdb', String(name)); } catch (e) { }
        return oIdbOpen.apply(this, arguments);
      };
    }
  } catch (e) { /* indexedDB 不可用时忽略 */ }

  // ---- document.cookie：改写回代理路径 + 剥 domain ----
  // ① 显式 path=/ 会把 cookie 种到整个 OpenOS 源，改写回代理路径；
  // ② domain=<目标站域> 在代理页（host 是 OpenOS 源）会被浏览器整条拒收——
  //    settoken 类「跨子域种会话」页面（美团登录票据 domain=.meituan.com）
  //    写不进任何 Cookie，登录态种不上、queryAccountInfo 判未登录跳回登录页
  //    （与响应侧 rewriteSetCookie 剥 Domain 同一语义）；剥后落本源，作用域由
  //    path 前缀限定到目标站，跨目标站不串；
  // ③ 无显式 path 的补目标站根路径：默认作用域是当前文档目录（如 /settoken/），
  //    同站其它路径（/api/…）带不上，原生跨子域语义靠 domain，剥后须由 path 承担
  try {
    var desc = Object.getOwnPropertyDescriptor(Document.prototype, 'cookie');
    if (desc && desc.set && desc.get) {
      Object.defineProperty(document, 'cookie', {
        configurable: true,
        get: function () { return desc.get.call(document); },
        set: function (v) {
          try {
            var s = String(v);
            // 带 domain= 声明的是跨子域共享语义（美团 eprt 票据 domain=.meituan.com）：
            // 剥域后须把 path 提升到应用级 PREFIX，限在本目标源子路径会让其他子域
            // 的代理请求带不上票据（发票接口 403 根因，与响应侧 rewriteSetCookie
            // 同一语义）
            var hadDomain = /(^|;\s*)domain\s*=\s*[^;]*/i.test(s);
            s = s.replace(/(^|;\s*)domain\s*=\s*[^;]*/ig, function (all, sp) { return sp ? '; ' : ''; });
            var re = /(^|;\s*)path\s*=\s*(\/[^;]*)/i;
            var m = s.match(re);
            if (hadDomain) {
              s = s.replace(re, function (all, sp) { return sp ? '; ' : ''; });
              s = s.replace(/;\s*$/, '');
              s += '; path=' + PREFIX;
            } else if (m) {
              // 显式 path：已是本应用目标源前缀则跳过（幂等，防二次包装）
              if (m[2].indexOf(PREFIX + ORIGIN + '/') !== 0) {
                s = s.replace(re, function (all, sp, p) { return sp + 'path=' + PREFIX + ORIGIN + p; });
              }
            } else {
              s += '; path=' + PREFIX + ORIGIN + '/';
            }
            v = s;
          } catch (e) { /* 保持原值 */ }
          // 改写后的最终路径登记（清除按钮消费：JS 写的 Cookie 不经服务端登记簿）
          try {
            var cnm = String(v).split('=')[0].trim();
            var cpm = /(?:^|;\s*)path\s*=\s*(\/[^;]*)/i.exec(String(v));
            if (cnm && cpm) pxLog('__osPxJsCookies', cnm + '\t' + cpm[1]);
          } catch (e) { }
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
    var isFrameSrc = C === window.HTMLIFrameElement && prop === 'src';
    try {
      Object.defineProperty(C.prototype, prop, {
        configurable: true,
        get: function () { return d.get.call(this); },
        set: function (v) {
          try { v = toProxy(String(v)); } catch (e) { /* 保持原值 */ }
          // 容器自举递归检测（判定细节见文末 MutationObserver 段）：先插空 iframe
          // 占位、后赋 src 的创建流不走 MutationObserver 的 fixFrameSrc（占位轮
          // 已消耗 __osPxSeen 幂等标记），属性赋值路径须自带同款检测——美团 ME
          // 容器业务页自举实测：第二层同名 iframe 恰经此路径漏网
          if (isFrameSrc) {
            try { if (pxInAncestors(pxTargetOf(v))) v = 'about:blank'; } catch (e) { }
          }
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
      if (urlAttrs[n]) {
        value = toProxy(String(value));
        // 容器自举递归检测（同属性 setter 路径）：setAttribute('src') 创建的
        // 自举 iframe 与属性赋值路径同风险，统一拦截
        if (n === 'src' && this.tagName === 'IFRAME') {
          try { if (pxInAncestors(pxTargetOf(value))) value = 'about:blank'; } catch (e) { }
        }
      }
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
  // 另拦截「容器自举递归」：子页面环境判定失败后把整只容器页塞进自己内容区，
  // 容器再加载子页面……无限套娃（美团财务子页实测：同 URL 层层加载 26 次）。
  // 判据：新 iframe 的目标地址与任一祖先窗口的目标地址相同（host+path+query，
  // 剥 hash——SPA hash 漂移不参与容器身份；剥 __os_px——本钩子给祖先 location
  // 追加的还原标记，不代表目标站语义）。query 必须参与比对：美团 ME 微前端的
  // 合法双层结构是同一 index.html 以「带 isMeIframeContainer 参数=容器壳 / 无参
  // =业务页」复用（剥 query 会连第一层合法结构一起杀掉），而自举递归的特征
  // 恰是完整 URL 层层重现。两边都归一到「目标站绝对地址」再比：iframe 的 src
  // 是代理路径形态（/app/…），祖先窗口的 location 已被 replaceState 还原成目标
  // 站形态 + __os_px 标记（直接拿代理路径比还原形态的 location.href 恒不相等，
  // 判据全部落空、套娃照常生长——美团 ME 容器实测）——祖先从 __os_px 标记还原
  // 出目标源，iframe 从代理前缀解出目标源。
  try {
    // 比对键：剥 hash、剥 __os_px 标记参数（含前导 ?/& 的清理），留 query
    var pxKey = function (u) {
      try {
        var s = String(u).split('#')[0]
          .replace(/([?&])__os_px=[^&]*/ig, function (_, lead) { return lead; });
        return s.replace(/[?&]$/, '');
      } catch (e) { return String(u); }
    };
    // 代理路径（/app/<id>/… 或宿主绝对+代理前缀）解出目标站绝对地址；非代理形态返回 ''
    var pxTargetOf = function (u) {
      try {
        var s = String(u), i = s.indexOf(PREFIX);
        if (i < 0) return '';
        var qi = s.indexOf('?', i);
        var rest = s.slice(i + PREFIX.length, qi < 0 ? undefined : qi);
        var q = qi < 0 ? '' : s.slice(qi);
        var m = /^([a-z][a-z0-9+.-]*)\/([^\/?#]+)/i.exec(rest);
        return m ? m[1].toLowerCase() + '://' + m[2] + (rest.slice(m[0].length) || '/') + q : '';
      } catch (e) { return ''; }
    };
    // 祖先窗口的目标站绝对地址：还原形态（__os_px=<id>~<scheme>~<auth> 标记）优先，
    // 未还原的代理前缀形态次之，都不是（宿主桌面页等）返回 null
    var pxAncestorTarget = function (w) {
      var href, search, pathname;
      try { href = w.location.href; search = w.location.search; pathname = w.location.pathname; }
      catch (e) { return null; }
      var t = pxTargetOf(href);
      if (t) return t;
      try {
        var mark = new URLSearchParams(search).get('__os_px') || '';
        var m = /^[^~]+~([a-z][a-z0-9+.-]*)~(.+)$/i.exec(mark);
        if (m) return m[1].toLowerCase() + '://' + m[2] + pathname + search;
      } catch (e) { }
      return null;
    };
    var pxInAncestors = function (tgt) {
      if (!tgt) return false;
      var t = pxKey(tgt);
      // 比对链含 window 自身：「容器/业务页在自己内容区再挂一个与自己同 URL 的
      // 页面」恰是最典型的自举（美团 ME：无参业务页在自身文档里创建无参同名
      // iframe，创建者自身不在 parent 链里会漏判）；从自身起逐级向上（顶层
      // window.parent === window 停）
      try {
        for (var w = window, i = 0; w && i < 17; w = (w === w.parent ? null : w.parent), i++) {
          var at = pxAncestorTarget(w);
          if (at && pxKey(at) === t) return true;
        }
      } catch (e) { }
      return false;
    };
    var fixFrameSrc = function (f) {
      if (f.__osPxSeen) return;
      f.__osPxSeen = 1;
      var raw = f.getAttribute('src');
      if (!raw) return;
      try {
        var mapped = toProxy(raw);
        if (mapped !== raw) f.src = mapped; // 属性 setter 已包装且 toProxy 对代理路径幂等
        // 自举递归检测：以目标站地址比对祖先（非代理目标不构成容器自举，不拦）
        if (pxInAncestors(pxTargetOf(mapped))) {
          f.removeAttribute('src');
          try { f.src = 'about:blank'; } catch (e) { }
        }
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

