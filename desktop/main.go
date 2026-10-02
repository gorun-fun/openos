// openos-desktop —— OpenOS 桌面客户端（解决网页应用的跨域问题）。
//
// 背景：OpenOS 的网页应用（apps 表 kind=proxy）经服务端反代（webproxy）嵌入同源
// iframe，服务端重写地址、搬运 Cookie，天生难平目标站的 SSO 跳转 / Referer 风控 /
// Cookie 轮换（美团 ebooking 等站点反复踩坑）。桌面客户端换一条根本路径：
//
//	用 CDP 驱动本机 Chrome，以 app 模式（独立窗口、无浏览器外壳）打开 OpenOS，
//	并关闭同源策略与站点隔离——跨源 iframe 不再被同源策略 / X-Frame-Options /
//	CSP frame-ancestors 拦截，网页应用直连真实站点、使用真实浏览器登录态。
//
// 职责：
//  1. 定位 Chrome（-chrome / $OPENOS_CHROME 可覆盖），用专用用户目录拉起
//     --app=<OpenOS 地址>（默认 http://127.0.0.1:8081，-url 可改）；
//  2. 在调试端口起 CDP，连浏览器级 WebSocket，对每个页面目标注入
//     window.__OPENOS_DESKTOP__ 标记——前端 WebApp.vue 检测到该标记后，
//     代理应用 iframe 直连 config.url 真实地址，而非 /app/<id>/ 反代路径；
//  3. 监听 Ctrl+C / 主窗口关闭 / Chrome 退出，随之收尾退出（按进程组清理）。
//
// 安全边界：--disable-web-security 意味着窗口内跨源脚本可互访，且调试端口只绑
// 127.0.0.1；专用 profile 仅用于打开 OpenOS，不要当日常浏览器使用。
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	urlFlag := flag.String("url", "http://127.0.0.1:8081", "OpenOS 地址（开发模式可指 http://127.0.0.1:5173）")
	chromeFlag := flag.String("chrome", "", "Chrome/Chromium/Edge 可执行文件路径（默认自动探测，$OPENOS_CHROME 同效）")
	profileFlag := flag.String("profile", "", "专用用户数据目录（默认 ~/.openos-desktop，登录态持久化于此）")
	portFlag := flag.Int("port", 0, "CDP 调试端口（默认自动选取空闲端口）")
	flag.Parse()

	home, err := os.UserHomeDir()
	if err != nil {
		fatal(err)
	}
	profile := *profileFlag
	if profile == "" {
		profile = filepath.Join(home, ".openos-desktop")
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		fatal(err)
	}

	chromePath, err := findChrome(*chromeFlag)
	if err != nil {
		fatal(err)
	}

	port := *portFlag
	if port == 0 {
		if port, err = freePort(); err != nil {
			fatal(err)
		}
	}

	cmd := exec.Command(chromePath, chromeArgs(*urlFlag, profile, port)...)
	setSysProcAttr(cmd)
	tail := &tailBuf{}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		fatal(err)
	}
	fmt.Printf("OpenOS 桌面客户端已启动\n  浏览器:   %s\n  地址:     %s\n  调试端口: %d（CDP）\n  用户目录: %s\n",
		chromePath, *urlFlag, port, profile)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	wsURL, err := waitDevtools(port, 20*time.Second)
	if err != nil {
		killGroup(cmd)
		fatal(fmt.Errorf("连接 Chrome 调试端口失败: %w\nChrome 输出末尾:\n%s", err, tail.String()))
	}
	conn, err := dialCDP(wsURL)
	if err != nil {
		killGroup(cmd)
		fatal(err)
	}

	done := make(chan struct{}) // 主窗口关闭
	osdOrigin := ""
	if u, err := url.Parse(*urlFlag); err == nil && u.Host != "" {
		osdOrigin = u.Scheme + "://" + u.Host
	}
	go watchTargets(conn, os.Getpid(), port, osdOrigin, docReferrer(), done)

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case <-sigCh:
		fmt.Println("收到退出信号，关闭 Chrome…")
		killGroup(cmd)
		<-exited
	case err := <-exited:
		if err != nil {
			fmt.Fprintf(os.Stderr, "Chrome 异常退出: %v\nChrome 输出末尾:\n%s\n", err, tail.String())
		}
		fmt.Println("桌面客户端已退出。")
	case <-done:
		fmt.Println("主窗口已关闭，桌面客户端退出。")
		killGroup(cmd)
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
		}
	case <-conn.Dead():
		fmt.Println("CDP 连接断开，桌面客户端退出。")
		killGroup(cmd)
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
		}
	}
}

// watchTargets 对所有页面目标注入桌面端标记，并监听主窗口（首个 page 目标）销毁；
// 同时在每个页面会话上开启 Network/Fetch 域，运行 SameSite Cookie 桥（bridgeCookie）
// 与请求头改写（fetchHandler）。
func watchTargets(c *cdp, pid, port int, osdOrigin, docRef string, done chan<- struct{}) {
	// 自动附身：开启后已存在与新建的 page 目标都会逐个收到 attachedToTarget（flat 会话）
	if _, err := c.call("Target.setAutoAttach", map[string]any{
		"autoAttach":             true,
		"waitForDebuggerOnStart": false,
		"flatten":                true,
	}, ""); err != nil {
		fmt.Fprintf(os.Stderr, "CDP 自动附身失败: %v\n", err)
		close(done)
		return
	}
	mark := markSource(pid, port, docRef)
	shim := cookieShimSource()
	mainTarget := ""
	sess := map[string]*pageSession{} // sessionId → 网络事件关联状态
	sessTarget := map[string]string{} // sessionId → targetId（主帧守卫只认主窗口）
	go fetchHandler(c, osdOrigin)
	// 首个 page 会话供 normalizeLoop 使用（Network.* 只能在页面级会话上调用）
	var sessMu sync.Mutex
	firstPage := ""
	firstSession := func() string { sessMu.Lock(); defer sessMu.Unlock(); return firstPage }
	go normalizeLoop(c, firstSession)
	for ev := range c.Events() {
		switch ev.Method {
		case "Target.attachedToTarget":
			var p struct {
				SessionID  string `json:"sessionId"`
				TargetInfo struct {
					TargetID string `json:"targetId"`
					Type     string `json:"type"`
				} `json:"targetInfo"`
			}
			if json.Unmarshal(ev.Params, &p) != nil {
				continue
			}
			// 跨源 iframe 在新版 Chrome 是独立 target（type=iframe），与 page 同等对待
			if t := p.TargetInfo.Type; t != "page" && t != "iframe" {
				continue
			}
			if mainTarget == "" && p.TargetInfo.Type == "page" {
				mainTarget = p.TargetInfo.TargetID
			}
			if p.TargetInfo.Type == "page" {
				sessMu.Lock()
				if firstPage == "" {
					firstPage = p.SessionID
				}
				sessMu.Unlock()
			}
			// 标记两路齐下：新文档（任何脚本执行前注入，覆盖刷新/跳转）+ 当前文档（立即生效）。
			// 注意 addScriptToEvaluateOnNewDocument 只覆盖主帧（跨源子帧不注入），
			// 子帧靠 Runtime.contextCreated 即时补丁 + 周期 walker 兜底（见下）
			_, _ = c.call("Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": mark}, p.SessionID)
			_, _ = c.call("Runtime.evaluate", map[string]any{"expression": mark}, p.SessionID)
			// Cookie 桥与 realm 补丁需要 Network/Runtime 事件流；主帧导航守卫需要 Page 事件
			if _, err := c.call("Network.enable", nil, p.SessionID); err == nil {
				sess[p.SessionID] = newPageSession()
			}
			sessTarget[p.SessionID] = p.TargetInfo.TargetID
			_, _ = c.call("Page.enable", nil, p.SessionID)
			// 请求级拦截（fetchHandler）：剥 Sec-Fetch-* / OpenOS 源 Referer 改写。
			// 诊断模式：OSD_NO_FETCH=1 时关闭拦截（排查拦截对目标站 SSO 的影响）
			if os.Getenv("OSD_NO_FETCH") == "" {
				_, _ = c.call("Fetch.enable", map[string]any{
					"patterns": []map[string]any{{"urlPattern": "*", "requestStage": "Request"}},
				}, p.SessionID)
			}
			if _, err := c.call("Runtime.enable", nil, p.SessionID); err != nil {
				continue
			}
			// 该目标周期兜底：给所有可达子帧补 cookie shim（新帧的 contextCreated
			// 竞态漏网时由此补上；SOP 已关，contentWindow.eval 可跨 realm 执行）
			go patchLoop(c, p.SessionID, shim)
		case "Runtime.executionContextCreated":
			// 子帧 realm 创建即补丁，赶在帧内脚本写 Cookie 之前
			var p struct {
				Context struct {
					ID      int `json:"id"`
					AuxData struct {
						IsDefault bool `json:"isDefault"`
					} `json:"auxData"`
				} `json:"context"`
			}
			if json.Unmarshal(ev.Params, &p) != nil || !p.Context.AuxData.IsDefault {
				continue
			}
			_, _ = c.call("Runtime.evaluate", map[string]any{
				"expression": shim, "contextId": p.Context.ID,
			}, ev.SessionID)
		case "Target.targetDestroyed":
			var p struct {
				TargetID string `json:"targetId"`
			}
			if json.Unmarshal(ev.Params, &p) == nil && p.TargetID == mainTarget {
				close(done)
				return
			}
		case "Page.frameNavigated":
			// 主帧防走失（等价 webproxy 的 CatchEscape 回捕）：桌面模式下应用 iframe
			// 不带 sandbox（美团等微前端容器需要），第三方页面理论上可 framebusting
			// （top.location 自拔带走整个桌面）。主窗口的主帧一旦离开 OpenOS 源，
			// 立即导航回去（桌面 SPA 状态会丢，但桌面保住）。只认主窗口——
			// 「登录」按钮弹出的顶层窗口本就该导航到目标站，不能误伤
			var p struct {
				Frame struct {
					ParentID string `json:"parentId"`
					URL      string `json:"url"`
				} `json:"frame"`
			}
			if json.Unmarshal(ev.Params, &p) != nil || p.Frame.ParentID != "" || p.Frame.URL == "" {
				continue
			}
			if osdOrigin != "" && mainTarget != "" && sessTarget[ev.SessionID] == mainTarget &&
				!strings.HasPrefix(p.Frame.URL, osdOrigin) {
				fmt.Printf("主帧被导航到 %s，回捕到 %s\n", p.Frame.URL, osdOrigin)
				_, _ = c.call("Page.navigate", map[string]any{"url": osdOrigin + "/"}, ev.SessionID)
			}
		case "Network.responseReceived":
			var p struct {
				RequestID string `json:"requestId"`
				Response  struct {
					URL string `json:"url"`
				} `json:"response"`
			}
			if json.Unmarshal(ev.Params, &p) != nil {
				continue
			}
			if st := sess[ev.SessionID]; st != nil {
				for _, line := range st.takePending(p.RequestID, p.Response.URL) {
					bridgeCookie(c, ev.SessionID, p.Response.URL, line)
				}
			}
		case "Network.responseReceivedExtraInfo":
			// 被拒的 Set-Cookie 会带 blockedReasons 与原始 cookieLine；两个事件到达顺序
			// 不保证（extraInfo 可能先于 responseReceived），URL 未知时先挂起
			var p struct {
				RequestID      string `json:"requestId"`
				BlockedCookies []struct {
					BlockedReasons []string `json:"blockedReasons"`
					CookieLine     string   `json:"cookieLine"`
				} `json:"blockedCookies"`
			}
			if json.Unmarshal(ev.Params, &p) != nil {
				continue
			}
			var lines []string
			for _, bc := range p.BlockedCookies {
				if bc.CookieLine == "" || !sameSiteBlocked(bc.BlockedReasons) {
					continue
				}
				lines = append(lines, bc.CookieLine)
			}
			if len(lines) == 0 {
				continue
			}
			st := sess[ev.SessionID]
			if st == nil {
				continue
			}
			if u := st.urlOf(p.RequestID); u != "" {
				for _, line := range lines {
					bridgeCookie(c, ev.SessionID, u, line)
				}
			} else {
				st.addPending(p.RequestID, lines)
			}
		}
	}
}

// fetchHandler 处理请求级拦截（Fetch 域，Request 阶段）：
//   - 剥 Sec-Fetch-* 整组头：跨站 iframe 发出的请求带 Sec-Fetch-Site: cross-site 语义，
//     部分目标站（美团 ebooking 实测：ebIframe 直达 302 → /ebooking/404，同源 fetch
//     则 200）按其做服务端风控，反代 webproxy 早已验证过剥除是正确姿势；
//   - Referer 指向 OpenOS 源（如 http://127.0.0.1:8081）时改写为目标站自身源：
//     iframe 文档导航的 Referer 是嵌入页地址，对按 Referer 白名单风控的接口
//     （微信 webwxnewloginpage 同款）等于自曝。其余 Referer（目标站自身页面间）原样。
//
// 每个事件必须 continue：暂停中的请求不继续，页面会挂死（authRequired 走默认处理）。
func fetchHandler(c *cdp, osdOrigin string) {
	for ev := range c.FetchEvents() {
		switch ev.Method {
		case "Fetch.requestPaused":
			var p struct {
				RequestID string `json:"requestId"`
				Request   struct {
					URL     string            `json:"url"`
					Headers map[string]string `json:"headers"`
				} `json:"request"`
			}
			_ = json.Unmarshal(ev.Params, &p)
			cont := map[string]any{"requestId": p.RequestID}
			if hdrs, changed := rewriteHeaders(p.Request.Headers, p.Request.URL, osdOrigin); changed {
				cont["headers"] = hdrs
			}
			_, _ = c.call("Fetch.continueRequest", cont, ev.SessionID)
		case "Fetch.authRequired":
			var p struct {
				RequestID string `json:"requestId"`
			}
			if json.Unmarshal(ev.Params, &p) == nil {
				_, _ = c.call("Fetch.continueWithAuth", map[string]any{
					"requestId":             p.RequestID,
					"authChallengeResponse": map[string]any{"response": "default"},
				}, ev.SessionID)
			}
		default:
			// Target 附身/销毁等关键事件：阻塞转投通用通道，保证 watchTargets 收到
			c.events <- ev
		}
	}
}

// docReferrer 嵌入帧 document.referrer 的伪装值：目标站的容器类应用常在被嵌入时
// 关闭自身菜单/外壳（美团 ebooking：isInIframe() && !isNoCodeSandboxRuntime() →
// no-menu），仅对自家低代码沙箱（document.referrer 匹配 nocode.sankuai.com）豁免。
// document.referrer 是 Document.prototype 上可重定义的 getter，注入 shim 对嵌入帧
// 伪造其返回值即可（只骗过页面 JS 的嵌入检测，请求头不动——实测 Fetch 域对文档
// 导航根本不触发 requestPaused，头改写路径无效）。
// OPENOS_DESKTOP_DOC_REFERRER 可改写，默认美团 nocode 沙箱域。
func docReferrer() string {
	if v := os.Getenv("OPENOS_DESKTOP_DOC_REFERRER"); v != "" {
		return v
	}
	return "https://nocode.sankuai.com/"
}

// rewriteHeaders 产出 continueRequest 的 headers 数组：剥 Sec-Fetch-*、改写指向
// OpenOS 源的 Referer 为目标站自身源（防按 Referer 风控的接口自曝）。changed=false
// 表示无需改写（continueRequest 不带 headers 原样放行）
func rewriteHeaders(in map[string]string, reqURL, osdOrigin string) ([]map[string]string, bool) {
	if len(in) == 0 {
		return nil, false
	}
	var out []map[string]string
	changed := false
	for k, v := range in {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "sec-fetch-") {
			changed = true
			continue
		}
		if lk == "referer" && osdOrigin != "" && strings.HasPrefix(v, osdOrigin) {
			if u, err := url.Parse(reqURL); err == nil && u.Host != "" {
				v = u.Scheme + "://" + u.Host + "/"
				changed = true
			}
		}
		out = append(out, map[string]string{"name": k, "value": v})
	}
	return out, changed
}

// cookieShimSource 只含 realm 级 document.cookie shim + 身份全局桥（不带桌面端标记），
// 用于子帧 realm 补丁：跨站 iframe 里未标注 SameSite 的 JS 写 Cookie 默认按 Lax
// 处理而被静默丢弃，shim 给这类写入自动补 SameSite=None（https 帧再补 Secure）。
func cookieShimSource() string {
	return `(function(){try{
if(window.__OSD_COOKIE_SHIM__)return;
Object.defineProperty(window,'__OSD_COOKIE_SHIM__',{value:true});
var d=Object.getOwnPropertyDescriptor(Document.prototype,'cookie');
if(d&&d.set){
Object.defineProperty(Document.prototype,'cookie',{configurable:true,enumerable:d.enumerable,get:d.get,
set:function(v){try{
if(typeof v==='string'&&v&&!/(^|;\s*)samesite\s*=/i.test(v)){
if(!/(^|;\s*)secure($|;|\s)/i.test(v)&&location.protocol==='https:'){v=v.replace(/\s+$/,'')+'; Secure';}
v=v.replace(/\s+$/,'')+'; SameSite=None';
}
}catch(e){}return d.set.call(this,v);}});
}
}catch(e){}})();` + globalBridgeSource()
}

// globalBridgeSource 身份全局桥（ME 系微前端的菜单命门）：美团 WebShell 的
// bridge.getGlobal 合并读取 window.GLOBAL；宿主嵌入时这条数据链不存在（原本由
// 顶层宿主/自举流程供给），菜单 saga 拿不到 partnerId 早退 → 渲染为 no-menu。
// 本桥两件事：①文档启动时把持久值（本域 localStorage + 顶层 __OSD_GLOBALS__[host]）
// 预种进 window.GLOBAL；②监听子页 update-me-master-poi-info 上报，捕获 partnerId/
// poiId 持久化，值有变化时置 __dirty 标记（前端巡检到即刷新一次本窗，让菜单
// 初始化拿到数据；同值不置脏，防刷新循环）。
func globalBridgeSource() string {
	return `(function(){try{
if(window.__OSD_GLOBAL_BRIDGE__)return;
Object.defineProperty(window,'__OSD_GLOBAL_BRIDGE__',{value:true});
var KEY='__osd_globals__';
var TOPG=null;try{TOPG=window.top.__OSD_GLOBALS__||(window.top.__OSD_GLOBALS__={})}catch(e){}
var read=function(){var o={};try{o=JSON.parse(localStorage.getItem(KEY)||'{}')}catch(e){}
if(TOPG&&TOPG[location.host]){for(var k in TOPG[location.host]){o[k]=TOPG[location.host][k]}}
return o};
var write=function(o){try{localStorage.setItem(KEY,JSON.stringify(o))}catch(e){}
if(TOPG){TOPG[location.host]=o}};
var g=read();
if(g&&(g.partnerId!=null||g.poiId!=null)){window.GLOBAL=Object.assign({},window.GLOBAL||{},g)}
try{
if(window.top!==window.self){
var FR=null;try{FR=window.top.__OSD_DOC_REFERRER__}catch(e){}
if(FR){
var rd=Object.getOwnPropertyDescriptor(Document.prototype,'referrer');
if(rd&&rd.get){Object.defineProperty(Document.prototype,'referrer',{configurable:true,enumerable:rd.enumerable,get:function(){return FR}})}
}
}
}catch(e){}
window.addEventListener('message',function(e){
try{var d=e.data;
if(d&&d.type==='update-me-master-poi-info'&&d.data&&(d.data.partnerId!=null||d.data.poiId!=null)){
var o=read(),ch=false;
if(d.data.partnerId!=null&&String(o.partnerId)!==String(d.data.partnerId)){o.partnerId=String(d.data.partnerId);ch=true}
if(d.data.poiId!=null&&String(o.poiId)!==String(d.data.poiId)){o.poiId=String(d.data.poiId);ch=true}
if(ch){write(o);if(TOPG){TOPG.__dirty=location.host}}
}
}catch(x){}
});
}catch(e){}})();`
}

// patchLoop 周期性给页面所有可达子帧补 cookie shim：addScriptToEvaluateOnNewDocument
// 不进跨源子帧、contextCreated 即时补丁存在毫秒级竞态，此 walker 兜底漏网 realm
// （SOP 已关，contentWindow.eval 可跨 realm 执行）。会话失效（页面关闭）即退出。
func patchLoop(c *cdp, sessionId, shim string) {
	t := time.NewTicker(1200 * time.Millisecond)
	defer t.Stop()
	expr := "(function(){try{var n=0;var W=function(w){try{if(!w.__OSD_COOKIE_SHIM__){w.eval(" +
		strconv.Quote(shim) + ");n++}" +
		"[].forEach.call(w.document.querySelectorAll('iframe'),function(f){try{W(f.contentWindow)}catch(e){}})}" +
		"}catch(e){}};W(window);return n}catch(e){return -1}})()"
	for range t.C {
		if _, err := c.call("Runtime.evaluate", map[string]any{"expression": expr}, sessionId); err != nil {
			return
		}
	}
}

// normalizeLoop 周期性把 Cookie jar 归一为 SameSite=None（发送侧修复）：顶层 1P 登录
// （「登录」按钮 window.open）落地的未标注 SameSite Cookie 按 Lax 语义不会附到跨站
// iframe 请求上，统一改写为 None+Secure 后 iframe 内即为登录态。本地域（OpenOS 自身）
// 不动——http 站点的 Cookie 被强加 Secure 会直接失效。
func normalizeLoop(c *cdp, firstSession func() string) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
		sid := firstSession()
		if sid == "" {
			continue
		}
		r, err := c.call("Storage.getCookies", nil, sid)
		if err != nil {
			continue
		}
		var p struct {
			Cookies []struct {
				Name     string  `json:"name"`
				Value    string  `json:"value"`
				Domain   string  `json:"domain"`
				Path     string  `json:"path"`
				Expires  float64 `json:"expires"`
				HTTPOnly bool    `json:"httpOnly"`
				Secure   bool    `json:"secure"`
				SameSite string  `json:"sameSite"`
			} `json:"cookies"`
		}
		if json.Unmarshal(r, &p) != nil {
			continue
		}
		for _, ck := range p.Cookies {
			if ck.SameSite == "None" {
				continue
			}
			d := strings.TrimPrefix(ck.Domain, ".")
			if d == "localhost" || d == "127.0.0.1" || strings.HasPrefix(d, "127.") || d == "[::1]" {
				continue
			}
			path := ck.Path
			if path == "" {
				path = "/"
			}
			params := map[string]any{
				"name": ck.Name, "value": ck.Value, "domain": ck.Domain, "path": path,
				"secure": true, "sameSite": "None",
				"url": "https://" + d + path,
			}
			if ck.HTTPOnly {
				params["httpOnly"] = true
			}
			if ck.Expires > 0 {
				params["expires"] = ck.Expires
			}
			_, _ = c.call("Network.setCookie", params, sid)
		}
	}
}

// sameSiteBlocked 判定被拒原因是否属于 SameSite 语境（未标注按 Lax 拒收 / Lax/Strict
// 上下文拦截）。其余原因（域不匹配等）属站点自身问题，不动。
func sameSiteBlocked(reasons []string) bool {
	for _, r := range reasons {
		if strings.Contains(r, "SameSite") {
			return true
		}
	}
	return false
}

// pageSession 维护单个页面会话上 requestId→URL 的关联与待回填 Cookie（两事件乱序兜底）
type pageSession struct {
	mu       sync.Mutex
	urlByReq map[string]string
	pending  map[string][]string
}

func newPageSession() *pageSession {
	return &pageSession{urlByReq: map[string]string{}, pending: map[string][]string{}}
}

func (s *pageSession) urlOf(reqID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.urlByReq[reqID]
}

func (s *pageSession) addPending(reqID string, lines []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[reqID] = append(s.pending[reqID], lines...)
}

// takePending 取出该请求挂起的 Cookie 行并记录 URL
func (s *pageSession) takePending(reqID, u string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.urlByReq[reqID] = u
	lines := s.pending[reqID]
	delete(s.pending, reqID)
	return lines
}

// bridgeCookie 把被 SameSite 拒收的 Set-Cookie 行以 SameSite=None 直写 Cookie jar
// （Network.setCookie 绕过上下文检查）。这是桌面端跨站登录的第二层桥：
// 响应头侧；JS document.cookie 写入由注入脚本的 shim 补 SameSite（markSource）。
func bridgeCookie(c *cdp, sessionId, pageURL, line string) {
	// 标准库没有公开的 Set-Cookie 行解析器，借 ReadResponse 走一遍响应解析
	resp, err := http.ReadResponse(bufio.NewReader(strings.NewReader("HTTP/1.1 200 OK\r\nSet-Cookie: "+line+"\r\n\r\n")), nil)
	if err != nil {
		return
	}
	u, err := url.Parse(pageURL)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" {
		return
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "" {
			continue
		}
		target := *u
		if ck.Path != "" {
			target.Path = ck.Path
		} else {
			target.Path = "/"
		}
		params := map[string]any{
			"name":   ck.Name,
			"value":  ck.Value,
			"url":    target.String(),
			"secure": u.Scheme == "https",
		}
		if ck.Domain != "" {
			params["domain"] = ck.Domain
		}
		if ck.Path != "" {
			params["path"] = ck.Path
		}
		if ck.HttpOnly {
			params["httpOnly"] = true
		}
		if !ck.Expires.IsZero() && ck.Expires.After(time.Now()) {
			params["expires"] = ck.Expires.Unix()
		}
		// 关键改写：强制 SameSite=None（Chrome 要求 None 须伴 Secure，https 下已带）
		params["sameSite"] = "None"
		if _, err := c.call("Network.setCookie", params, sessionId); err != nil {
			fmt.Fprintf(os.Stderr, "Cookie 桥回填失败（%s）: %v\n", ck.Name, err)
		}
	}
}

// markSource 生成注入脚本，做两件事：
//  1. 在 window 上钉死只读标记 __OPENOS_DESKTOP__——前端（web/src/apps/web/WebApp.vue）
//     据此让代理应用直连真实地址；
//  2. SameSite Cookie shim：跨站 iframe 里未标注 SameSite 的 document.cookie 写入
//     默认按 Lax 处理而被静默丢弃（美团 settoken 实测），shim 给这类写入自动补
//     SameSite=None（https 帧再补 Secure，Chrome 要求 None 须伴 Secure）。
//     注入发生在每个新文档任何脚本执行前，主文档与全部子帧生效。
func markSource(pid, port int, docRef string) string {
	v, _ := json.Marshal(map[string]any{"client": "openos-desktop", "pid": pid, "debugPort": port})
	return `;(function(){
try{
  if(!window.__OPENOS_DESKTOP__){
    Object.defineProperty(window,'__OPENOS_DESKTOP__',{value:` + string(v) + `,configurable:false,enumerable:false,writable:false});
  }
  if(!window.__OSD_DOC_REFERRER__&&` + strconv.Quote(docRef) + `){
    Object.defineProperty(window,'__OSD_DOC_REFERRER__',{value:` + strconv.Quote(docRef) + `,configurable:false,enumerable:false,writable:false});
  }
}catch(e){}
try{
  if(!window.__OSD_COOKIE_SHIM__){
    Object.defineProperty(window,'__OSD_COOKIE_SHIM__',{value:true});
    var d=Object.getOwnPropertyDescriptor(Document.prototype,'cookie');
    if(d&&d.set){
      Object.defineProperty(Document.prototype,'cookie',{
        configurable:true,enumerable:d.enumerable,get:d.get,
        set:function(v){
          try{
            if(typeof v==='string'&&v&&!/(^|;\s*)samesite\s*=/i.test(v)){
              if(!/(^|;\s*)secure($|;|\s)/i.test(v)&&location.protocol==='https:'){v=v.replace(/\s+$/,'')+'; Secure';}
              v=v.replace(/\s+$/,'')+'; SameSite=None';
            }
          }catch(e){}
          return d.set.call(this,v);
        }
      });
    }
  }
}catch(e){}
})();` + globalBridgeSource()
}

// chromeArgs 组装 Chrome 启动参数。核心三件套：
//   - --app：独立应用窗口（无浏览器地址栏/标签页外壳）
//   - --disable-web-security：关闭同源策略，跨源 iframe 可嵌入（X-Frame-Options /
//     CSP frame-ancestors 不再拦截），父页面与 iframe 脚本可互访
//   - 关闭站点隔离（隔离试验 / site-per-process / IsolateOrigins），iframe 与宿主
//     同进程渲染，不再被当独立站点隔离
//
// SameSite 三连是跨站登录的关键：OpenOS 页面（如 localhost）里嵌入的 me.meituan.com
// 属第三方上下文，目标站未显式标注 SameSite 的会话 Cookie（默认按 Lax 处理）会被
// 整体拒收（Set-Cookie 与 document.cookie 双双落空，美团 ebooking 实测：epassport
// 显式 SameSite=None 的 eprt/eplt 能落地、me.meituan.com 全域为空导致 settoken 后
// 仍弹回登录页）。关闭 SameSiteByDefaultCookies 回到「未标注=跨站可用」的宽松语义。
// ThirdPartyStoragePartitioning 一并关闭，嵌入态第三方存储不分区（可信 profile 下
// 兼容性优先）。
//
// 其余为降噪（首跑向导/默认浏览器提示/「不受支持的开关」提示条）与调试端口。
func chromeArgs(url, profile string, port int) []string {
	return []string{
		"--app=" + url,
		"--user-data-dir=" + profile,
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-debugging-address=127.0.0.1",
		"--remote-allow-origins=*",
		"--disable-web-security",
		"--disable-site-isolation-trials",
		"--disable-features=" + strings.Join([]string{
			"site-per-process",
			"IsolateOrigins",
			"ThirdPartyStoragePartitioning",
			"SameSiteByDefaultCookies",
			"CookiesWithoutSameSiteMustBeSecure",
			"SameSiteDefaultChecksMethodRigorously",
		}, ","),
		"--no-first-run",
		"--no-default-browser-check",
		"--test-type",
		"--window-size=1440,860",
	}
}

// findChrome 定位本机浏览器：-chrome 参数 > $OPENOS_CHROME > 平台常见路径 > PATH。
// Edge/Chromium 与 Chrome 同内核，启动参数与 CDP 兼容。
func findChrome(flagVal string) (string, error) {
	for _, p := range []string{flagVal, os.Getenv("OPENOS_CHROME")} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("指定的浏览器不存在: %s", p)
		}
		return p, nil
	}
	home, _ := os.UserHomeDir()
	var cands []string
	switch runtime.GOOS {
	case "darwin":
		cands = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			filepath.Join(home, "Applications", "Google Chrome.app", "Contents", "MacOS", "Google Chrome"),
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	case "windows":
		cands = []string{
			filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("LocalAppData"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		}
	default:
		cands = []string{
			"/usr/bin/google-chrome", "/usr/bin/google-chrome-stable",
			"/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/microsoft-edge",
			"/snap/bin/chromium",
		}
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	for _, name := range []string{"chrome", "google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("未找到 Chrome/Chromium/Edge，请用 -chrome 指定可执行文件路径")
}

// freePort 随机取一个空闲 TCP 端口（先听后放，存在极小竞争窗口，此处够用）
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitDevtools 轮询 /json/version 直至 CDP 就绪，返回浏览器级 WebSocket 地址
func waitDevtools(port int, timeout time.Duration) (string, error) {
	u := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	client := &http.Client{Timeout: time.Second}
	var lastErr error
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(u)
		if err == nil {
			var v struct {
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			err = json.NewDecoder(resp.Body).Decode(&v)
			resp.Body.Close()
			if err == nil && v.WebSocketDebuggerURL != "" {
				return v.WebSocketDebuggerURL, nil
			}
		}
		lastErr = err
		time.Sleep(150 * time.Millisecond)
	}
	return "", fmt.Errorf("超时（%v）: %w", timeout, lastErr)
}

// tailBuf 只保留末尾若干字节的写缓冲（Chrome stderr 诊断用）
type tailBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if excess := t.buf.Len() - 8192; excess > 0 {
		t.buf.Next(excess)
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "openos-desktop:", err)
	os.Exit(1)
}
