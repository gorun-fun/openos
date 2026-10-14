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
)

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
