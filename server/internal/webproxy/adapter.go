// 站点适配层：针对特定目标站点的特殊处理，从统一转发链路中解耦出来。
//
// 统一链路（engine.go / request.go / response.go）只保留对所有站点语义安全的
// 通用改写；「已知某站点需要、对未知站点属于凭空改写」的行为收敛到本文件的
// 适配器注册表，按目标主机命中后叠加。新增站点特殊处理时：
//  1. 在此追加 SiteAdapter（Hosts 声明适用域，精确段或裸域后缀匹配）
//  2. 钩子内只做增量修正——通用链路已完成的清洗（会话剥离/头剥除）不可回退
//  3. 同步补 adapter_test.go 用例
//
// 钩子时机：
//   - OnRequest：sanitizeOutRequest + applyCfgHeaders 之后、拨号之前
//     （out 为发往上游的请求，orig 为浏览器原始请求）
//   - OnResponse：copyRespHeaders 之后、写状态行之前（h 为最终响应头）
//   - Hook：追加注入到代理页面的 JS 片段（app 模式；紧随通用 hook.js 之后
//     执行，片段内 __PREFIX__ 占位符同样被替换为 /app/<id>/）
//   - RewriteJS：JS 响应体的定点改写（app 模式；gzip 自动解压重压，超限原文
//     透传——与 HTML/CSS 重写同一套限长语义；prefix 为 /app/<id>/ 代理前缀）
package webproxy

import (
	"bytes"
	"net/http"
	"regexp"
)

// reTopGuard 美团 master 引导的顶层守卫（见 meituan-sso-rewrite 注释）。
var reTopGuard = regexp.MustCompile(`window\.top!==window\.self\|\|([a-zA-Z_$][\w$]*)\(`)

type SiteAdapter struct {
	Name string
	// Hosts 适用主机：裸域（"meituan.com"）匹配自身与任意子域；含点号的段
	// （"me.meituan.com"）精确匹配。匹配不区分大小写。
	Hosts []string
	// OnRequest 转发前的请求侧修正（可空）。
	OnRequest func(out, orig *http.Request)
	// OnResponse 响应头最终修正（可空）。
	OnResponse func(h http.Header)
	// Hook 追加注入的客户端脚本片段（可空；IIFE 自包含——通用 hook.js 先于其
	// 执行，但不保证页面脚本已跑完，需要晚绑定行为的自行 load/setTimeout 兜底）
	Hook string
	// RewriteJS JS 响应体定点改写（可空；仅 app 模式对命中的 JS 资源生效）。
	// prefix 为当前代理路径前缀（/app/<id>/，按应用 id 变化），需要产出代理路径
	// 的改写用它拼接。只做确定性的字符串替换——混淆名/结构随版本变化，勿写脆弱
	// 的语法级改写。
	RewriteJS func(body []byte, prefix string) []byte
}

// adapters 站点适配器注册表（命中即全部按序应用）。
var adapters = []SiteAdapter{
	// Origin 严判站点：美团/大众点评系（含 ebooking 商家后台）、金山文档（kdocs，
	// 写接口无 Origin/Referer 直接 403 SessionDeleted）、微信系（OAuth 授权链风控）、
	// 网易云音乐（接口风控）。原生页面自身的 POST 会携带自家 Origin，反代后源
	// 关系失真被剥除——这些站点要求「Origin/Referer 至少一个匹配自家域」，兜底
	// 补目标站自身源（值是目标站自己的，不泄露宿主信息）。未命中站点维持原生
	// 语义（GET 本就不带 Origin，不凭空补）。
	{
		Name:  "origin-strict",
		Hosts: []string{"meituan.com", "meituan.net", "sankuai.com", "dianping.com", "kdocs.cn", "wps.cn", "163.com", "126.net", "weixin.qq.com", "wx.qq.com"},
		OnRequest: func(out, orig *http.Request) {
			if out.Header.Get("Origin") == "" {
				out.Header.Set("Origin", out.URL.Scheme+"://"+out.URL.Host)
			}
		},
	},
	// 微信扫码登录链（music.163.com 经 open.weixin.qq.com 授权、webwxnewloginpage
	// 发凭证）：jslogin 接口实测同时下发 text/javascript 与 text/html 两个
	// Content-Type（上游 bug），浏览器按末值解析会让 <script> 的严格 MIME 检查
	//（配 nosniff）拒绝执行 JSONP——拿不到 uuid、二维码不渲染。收敛为首个。
	{
		Name:  "weixin-double-ct",
		Hosts: []string{"weixin.qq.com", "wx.qq.com", "wx1.qq.com", "wx2.qq.com", "wx3.qq.com", "weixin.com"},
		OnResponse: func(h http.Header) {
			if cts := h.Values("Content-Type"); len(cts) > 1 {
				first := cts[0]
				h.Del("Content-Type")
				h.Set("Content-Type", first)
			}
		},
	},
	// 网易云音乐：站内内层页（g_iframe 里的发现/播放页）以 top.login(...) 呼出
	// 壳页的登录弹窗——原生形态 top 即站点自身壳页，代理下 top 是 OpenOS 宿主
	// 页，TypeError 中断、登录弹窗永远打不开。桥接：壳页（直挂宿主的代理文档）
	// 把自身定义的这组全局函数挂上真 top（代理页面与宿主同源，可写），内层
	// top.login 即回落到壳页实现。login 由站点脚本在解析期定义，load 后与
	// 1.5s 定时各补一次（时序兜底），幂等（已存在不覆盖）。
	{
		Name:  "music163-top-bridge",
		Hosts: []string{"163.com", "126.net"},
		Hook: `(function () {
  if (window.parent !== window.top || window.parent === window) return; // 仅直挂宿主的壳页执行
  // 壳页全局 → top 的转发桥（网易云整站按「top 即站点自身壳页」的假设组织：
  // 内层内容页 g_iframe 经 top.xxx 调用壳页能力——top.player.addTo 播歌、
  // top.GDispatcher.dispatch SPA 路由、top.login 呼登录、top.scrollTopbar 与
  // 底栏/顶栏高度滚动同步。代理下 top 是 OpenOS 桌面页，这些引用全部落空：
  // 播放/导航/登录整链 TypeError。代理页与宿主同源，用 getter/setter 双向
  // 转发挂上 top——惰性求值对站点脚本的定义时序不敏感，写入也回流壳页）。
  var NAMES = ['login', 'logout', 'player', 'GDispatcher', 'scrollTopbar',
               'g_bottomBarHeight', 'g_bottomBarShow', 'g_topBarHeight'];
  function bridge() {
    try {
      var T = window.top;
      for (var i = 0; i < NAMES.length; i++) {
        var k = NAMES[i];
        if (T[k] !== undefined || Object.getOwnPropertyDescriptor(T, k)) continue; // 宿主或其他窗口已占用则不覆盖
        (function (k) {
          Object.defineProperty(T, k, {
            configurable: true,
            get: function () { return window[k]; },
            set: function (v) { window[k] = v; },
          });
        })(k);
      }
    } catch (e) { }
  }
  bridge();
  window.addEventListener('load', bridge);
  setTimeout(bridge, 1500);
  // 登录模块的跨 realm 桥：网易云登录模块（ctWebLogin）以脚本形式注入 top 文档执行
  //（原生形态 top 即站点自身壳页、同一 realm），CtWebLogin / CtWebLoginConfig 因此注册
  // 在 top realm——而壳页点击处理器查的是自身 window.CtWebLogin、模块就绪回调读的
  // CtWebLoginConfig 也是 top 上的另一份，互相不可见：点击登录永远走「设 config 等
  // 加载」分支且模块读不到 config，弹窗从未出现（还会反复向 top 注入脚本）。get/set
  // 双向转发让两个 realm 立即可达：点击即刻命中 top 上的模块，config 写入也被
  // top 侧脚本读到。弹窗 DOM/样式由 top realm 脚本自建（覆盖宿主页，关闭即还原）。
  try {
    var T = window.top;
    if (!Object.getOwnPropertyDescriptor(window, '__osCtBridged')) {
      Object.defineProperty(window, '__osCtBridged', { value: 1 });
      Object.defineProperty(window, 'CtWebLogin', {
        configurable: true,
        get: function () { try { return T.CtWebLogin; } catch (e) { return undefined; } },
        set: function (v) { try { T.CtWebLogin = v; } catch (e) { } },
      });
      Object.defineProperty(window, 'CtWebLoginConfig', {
        configurable: true,
        get: function () { try { return T.CtWebLoginConfig; } catch (e) { return undefined; } },
        set: function (v) { try { T.CtWebLoginConfig = v; } catch (e) { } },
      });
    }
  } catch (e) { }
})();`,
		// JS 定点改写：站点脚本以 window.location.host 做环境检测（非
		// music.163.com 时接口 base 落到 QA 环境 qa.igame.163.com，代理页
		// host 是 OpenOS 宿主必中招——登录二维码 403）。hostname 无法伪造，
		// 把 JS 里的 QA 兜底字面量改写为生产域：检测仍判离线，但 base 落到
		// music.163.com，绝对地址经 hook 映射回代理路径。
		RewriteJS: func(body []byte, prefix string) []byte {
			return bytes.ReplaceAll(body, []byte("qa.igame.163.com"), []byte("music.163.com"))
		},
	},
	// 美团容器菜单伪造 referrer：ebooking 容器页（ebIframe）检测
	// isInIframe() && !isNoCodeSandboxRuntime() 即藏自身菜单（仅豁免 referrer
	// 匹配自家低代码沙箱 nocode.sankuai.com 的场景）——反代形态容器页恒在
	// OpenOS 窗体 iframe 里，菜单永远被藏（登录后只剩内容区）。与桌面客户端
	// 同方案（desktop/main.go docReferrer）：重定义 Document.prototype.referrer
	// getter 伪造为 nocode 沙箱域，只骗页面 JS 的嵌入检测、请求头不动。
	{
		Name:  "meituan-referrer-spoof",
		Hosts: []string{"meituan.com", "meituan.net", "sankuai.com", "dianping.com", "dpfile.com", "51ping.com"},
		Hook: `
try {
  if (!window.__osRefSpoofed) {
    window.__osRefSpoofed = true;
    var rd = Object.getOwnPropertyDescriptor(Document.prototype, 'referrer');
    if (rd && rd.get) {
      Object.defineProperty(Document.prototype, 'referrer', {
        configurable: true, enumerable: rd.enumerable,
        get: function () { return 'https://nocode.sankuai.com/'; }
      });
    }
  }
} catch (e) { /* referrer getter 不可重定义时忽略 */ }`,
	},
	// 美团 ME 容器握手补全（忠实模拟真实拓扑，按帧层级分治）：
	// 门户新工作台（hotel-fe-me）的 iframeMessage 模块启动即向父容器发
	// hotel-query-container 探测、等 hotel-container-response（应答 origin 须过其
	// isValidHost：含 .meituan./.sankuai./.dianping. 串）。真实世界语义：
	// ①顶层页自声明 inWhichSystem="me" 并注册监听、应答其【直接子页】的探测；
	// ②iframe 内的页只探测不监听——其子页（深层业务页 me-spu 等）探测无应答、
	// 落 unknown，按 eb 旧路径语义渲染（me 变体路径如 /ebooking/merchant/spu/
	// gift-list 是门户路由、只在 me.meituan.com 存在；深层页若被误答 me 会拼
	// me 变体相对地址，代理下落到自身代理基（eb.meituan.com）→ 整页 404，线上
	// 实证）。代理把全部帧同源化到宿主源后：宿主源必挂 isValidHost → 最外层
	// 页收不到应答（工作台残缺/登录表单空白），且美团顶层分支的子页应答监听
	//（isValidOrigin 门）在代理内永不触发。本 Hook 分层补全：
	// ①最外层代理页（宿主 shell 直接子帧 = 真实世界顶层页）：拦截发往父窗的
	//   探测、以合成 MessageEvent（origin 呈现为本页目标站源）异步自应答；并
	//   代行容器职责——监听直接子页的探测（真实监听因 isValidOrigin 门失效），
	//   经 e.source.postMessage 应答（子页侧通用 hook 的接收侧源还原把 e.origin
	//   呈现为本页目标站源，校验按真实世界语义通过）。
	// ②其余深层页：不自答不代答，保持真实世界的 unknown 落点与 eb 路径语义。
	// 应答载荷 origin 一律填本页 location.origin（宿主源）：站点后续
	// postMessageToParentWindow 以它作 targetOrigin，代理下父窗源正确命中。
	// 桌面客户端形态的 WebApp onDeskHandshake（SOP 关闭下 e.source.eval 同域源
	// 投递）只覆盖宿主直接子帧一层，与本方案①的第一段同位。
	{
		Name:  "meituan-container-handshake",
		Hosts: []string{"meituan.com", "meituan.net", "sankuai.com", "dianping.com", "dpfile.com", "51ping.com"},
		Hook: `
try {
  if (window.parent !== window && !window.__osMeHandshake) {
    window.__osMeHandshake = true;
    // 本页目标站源（与通用 hook 的帧源解析同源逻辑：__os_px 标记 / 代理前缀路径）
    var own = '';
    try {
      var mk = /__os_px=[a-z0-9._-]+~([a-z][a-z0-9+.-]*)~([^&#]+)/i.exec(location.search);
      if (mk) own = mk[1].toLowerCase() + '://' + decodeURIComponent(mk[2]);
      else if (location.pathname.indexOf('/app/') === 0) {
        var pk = /^[a-z0-9._-]+\/([a-z][a-z0-9+.-]*)\/([^/?#]+)/i.exec(location.pathname.slice(5));
        if (pk) own = pk[1].toLowerCase() + '://' + pk[2];
      }
    } catch (e) { }
    if (own && window.parent === window.top) {
      // ① 最外层代理页（真实世界顶层页）：自声明。探测可能经 parent / self 任意
      //    一个 postMessage 发出（新版 WsIframeMessage 走 parent，留 self 拦截作
      //    兜底），两处都挂拦截，幂等标记防重复应答。（不可用 top===self 判定：
      //    本浏览器 iframe 的 top 为 Unforgeable 不可重定义，假 top 方案无效。）
      var osMeAnswered = {};
      var osMeSelfAnswer = function (msg) {
        try {
          if (msg && msg.eventType === 'hotel-query-container' && msg.signature && !osMeAnswered[msg.signature]) {
            osMeAnswered[msg.signature] = 1;
            var resp = {
              eventType: 'hotel-container-response',
              iframeInSystem: 'me',
              origin: location.origin,
              signature: msg.signature
            };
            // 异步投递：消息事件天然异步，同步合成会干扰「发送后才注册监听」的代码。
            // source 补 window.parent（宿主桌面窗）：新版 WsIframeMessage（美团
            // 2026-10-22 上线的 hotel-fe-me 11305747）应答校验除 origin 外还核
            // source===父容器窗，缺 source 的合成应答被静默拒收（3s 未回应实证）。
            setTimeout(function () {
              try {
                window.dispatchEvent(new MessageEvent('message', { data: resp, origin: own, source: window.parent }));
              } catch (e) { }
            }, 0);
          }
        } catch (e) { }
      };
      var opm = window.parent.postMessage.bind(window.parent);
      window.parent.postMessage = function (msg, t, tr) {
        osMeSelfAnswer(msg);
        return opm(msg, t, tr);
      };
      var ospm = window.postMessage;
      window.postMessage = function (msg, t, tr) {
        osMeSelfAnswer(msg);
        return ospm.call(window, msg, t, tr);
      };
      // ① 代行顶层分支的容器职责：应答直接子页的探测（原生监听被 isValidOrigin
      //    门挡死；以「发送者是本窗直接子帧」代校验——能向本窗 postMessage 的
      //    外部页不构成提权面，应答只影响其渲染分支）
      window.addEventListener('message', function (ev) {
        try {
          var d = ev.data;
          if (!d || d.eventType !== 'hotel-query-container' || !d.signature) return;
          var isChild = false;
          for (var fi = 0; fi < window.frames.length; fi++) {
            if (window.frames[fi] === ev.source) { isChild = true; break; }
          }
          if (!isChild) return;
          ev.source.postMessage({
            eventType: 'hotel-container-response',
            iframeInSystem: 'me',
            origin: location.origin,
            signature: d.signature
          }, '*');
        } catch (e) { }
      });
    }
    // ③ 恢复性 me-spu 导航拦截（上次访问死锁解）：门户按账号服务端记忆恢复
    //    「上次访问」，记录被 me-spu 自锁（每次恢复访问又刷新记录），入口
    //    iUrl 意图（merchant/home 最终跳 ebIframe?iUrl=new-workbench；顶层直开
    //    可带任意 iUrl）被无条件覆盖。wrapper 重建内容 iframe（Vue 换 src/换
    //    元素，非消息指令）落 me-spu 时，改写回 iUrl 意图目标。iUrl 空或本身
    //    指向 me-spu 时不干预。
    try {
      var im = /(?:\?|&)iUrl=([^&#]*)/.exec(location.search);
      var iUrl = im ? decodeURIComponent(im[1]) : '';
      if (iUrl && iUrl.indexOf('me-spu') < 0) {
        var osIUrlTarget = /^https?:\/\//i.test(iUrl)
          ? '__PREFIX__' + 'https/' + iUrl.replace(/^https?:\/\//i, function (m2) { return m2.toLowerCase(); })
          : '__PREFIX__' + 'https/eb.meituan.com' + (iUrl.charAt(0) === '/' ? iUrl : '/' + iUrl);
        var osIUrlKey = osIUrlTarget.split('?')[0].split('#')[0];
        window.__osSpuBlock = 0;
        var osMapSpu = function (v) {
          try {
            if (typeof v === 'string' && v.indexOf('me-spu') >= 0 && v.indexOf(osIUrlKey) < 0) {
              window.__osSpuBlock++;
              return osIUrlTarget;
            }
          } catch (e2) { }
          return v;
        };
        // 三路覆盖 iframe src 的设置路径：property setter（Vue/程序赋值）、
        // setAttribute（模板属性）、MutationObserver（innerHTML/解析器注入）。
        try {
          var osD = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, 'src');
          if (osD && osD.set) {
            Object.defineProperty(HTMLIFrameElement.prototype, 'src', {
              configurable: true, enumerable: osD.enumerable,
              get: function () { return osD.get.call(this); },
              set: function (v) { return osD.set.call(this, osMapSpu(v)); }
            });
          }
        } catch (e2) { }
        try {
          var osSA = Element.prototype.setAttribute;
          Element.prototype.setAttribute = function (n, v) {
            if ((n === 'src' || n === 'SRC') && this.tagName === 'IFRAME') v = osMapSpu(v);
            return osSA.call(this, n, v);
          };
        } catch (e2) { }
        var osScanFrames = function (root) {
          try {
            var frs = root.querySelectorAll ? root.querySelectorAll('iframe') : [];
            for (var fi = 0; fi < frs.length; fi++) {
              var fr = frs[fi];
              var s = fr.getAttribute('src') || '';
              if (s.indexOf('me-spu') >= 0 && s.indexOf(osIUrlKey) < 0) {
                window.__osSpuBlock++;
                fr.setAttribute('src', osIUrlTarget);
              }
            }
          } catch (e2) { }
        };
        osScanFrames(document);
        var osMo = new MutationObserver(function (muts) {
          for (var mi = 0; mi < muts.length; mi++) {
            var nodes = muts[mi].addedNodes;
            for (var nk = 0; nk < nodes.length; nk++) {
              var n = nodes[nk];
              if (n.nodeType !== 1) continue;
              if (n.tagName === 'IFRAME') osScanFrames(n.parentElement || document);
              else osScanFrames(n);
            }
          }
        });
        osMo.observe(document.documentElement || document, { childList: true, subtree: true });
      }
    } catch (e3) { }
    // ② 深层页：不自答不代答（保持真实世界 unknown → eb 路径语义），own 仅作
    //    上面分支的判据，不产生任何行为
  }
} catch (e) { }`,
	},
	// 美团 SSO 登录跳转代理化：ebooking 商家后台的登录入口（美团员工登录等）在
	// 前端 JS 里以字面量拼出 SSO 绝对地址（https://ssosv.sankuai.com/sson/login?…
	// redirect_uri=location.origin+…）并 location.href 直跳——绝对地址导航完全
	// 绕开代理（服务端改写碰不到不经过自己的请求），iframe 直连目标站后：SSO 页
	// 里 location.origin 拼出的回跳是 OpenOS 宿主地址，登录完成 302 回
	// /api/gw/… 等宿主路径时 Referer 已是跨源目标站（逃逸回捕判定不上），最终
	// 落在 OpenOS 自身路由上 404（登录链整体断）。把 JS 里的 SSO 域字面量改写为
	// 代理路径：导航成为同源相对跳转进入代理，redirect_uri 还原（restorePxParam）
	// 与回跳 Location 前缀化（rewriteLocation）链路随之全部生效。
	{
		Name:  "meituan-sso-rewrite",
		Hosts: []string{"meituan.com", "meituan.net", "sankuai.com", "dianping.com", "dpfile.com", "51ping.com"},
		RewriteJS: func(body []byte, prefix string) []byte {
			// 先替换更长的 test/st 域，防 ssosv.sankuai.com 前缀截断后者。
			// epassport（含 fepassport 测试变体）是商家 Epassport 登录分支
			//（login JS 的 EP 分支以 location.href= 字面量直跳——页面自身
			// location 不可包装、hook 拦不住跨源导航）：漏改写时重新登录直接
			// 逃逸到真实域完成，票据（eprt 等 Domain=.meituan.com）种不进代理
			// 路径，发票等严校验接口恒 403「无访问权限」（实测 2026-10-15）
			for _, h := range []string{
				"ssosv.it.st.sankuai.com", "ssosv.it.test.sankuai.com", "ssosv.sankuai.com",
				"fepassport.sjst.test.sankuai.com", "epassport.meituan.com",
			} {
				to := prefix + "https/" + h
				body = bytes.ReplaceAll(body, []byte("https://"+h), []byte(to))
			}
			// 门户新工作台 master 引导（slave-bootstrap-master）的 ME 容器初始化
			// 被顶层守卫禁用：`window.top!==window.self||b(E)` —— 真实世界 ebIframe
			// 宿主页是顶层页（守卫假 → 执行 b(E) 装配 window.me 容器、挂菜单处理、
			// @post-me master 侧）；代理把它嵌进宿主桌面后 top=桌面（守卫真 → 短路
			// 跳过 b(E)）→ 菜单点击无响应、@post-me 握手 master 永不应答、single-spa
			// 子应用超时死亡（线上实证）。window.top 为 Unforgeable 不可运行时伪造，
			// 唯一通道是此处字节改写：守卫改恒假，让容器初始化在嵌套语境照常执行。
			// 变量名（b/E 为压缩产物）随版本变，模式锚定 window.top!==window.self||
			// 后随函数调用；仅命中此一处形态（证书检查的同形判定是独立 if 语句，
			// 无 || 函数调用尾，不受影响）。
			body = reTopGuard.ReplaceAll(body, []byte("!1||$1("))
			return body
		},
	},
}

// matchAdapters 返回适用于目标主机的全部适配器（注册序）。
func matchAdapters(host string) []*SiteAdapter {
	if host == "" {
		return nil
	}
	host = toLowerASCII(host)
	if i := indexByteASCII(host, ':'); i >= 0 { // 去端口
		host = host[:i]
	}
	var out []*SiteAdapter
	for i := range adapters {
		for _, pat := range adapters[i].Hosts {
			p := toLowerASCII(pat)
			if host == p || (len(host) > len(p) && host[len(host)-len(p)-1] == '.' && host[len(host)-len(p):] == p) {
				out = append(out, &adapters[i])
				break
			}
		}
	}
	return out
}

func toLowerASCII(s string) string {
	hasUpper := false
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			hasUpper = true
			break
		}
	}
	if !hasUpper {
		return s
	}
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func indexByteASCII(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
