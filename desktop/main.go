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
//     --app=<首开地址>。默认首开本地设置页的启动选择形态（?pick=1，先选服务器
//     再进入）；-url 显式（仅本次生效、不落盘）与 -auto（直连已保存配置）跳过
//     选择页，目标 = -url > <profile>/config.json 的 url > http://127.0.0.1:8081；
//  2. 在调试端口起 CDP，连浏览器级 WebSocket，对每个页面目标注入
//     window.__OPENOS_DESKTOP__ 标记——前端 WebApp.vue 检测到该标记后，
//     代理应用 iframe 直连 config.url 真实地址，而非 /app/<id>/ 反代路径；
//  3. 服务器设置（settings.go）：OpenOS 前端「系统设置 → 服务器」分区（桌面模式
//     专属，内嵌客户端本地设置页）与登录页「切换服务器」入口改连任何 OpenOS 兼容端
//     （在线部署地址）；目标地址打不开时自动转入设置页兜底；
//  4. reset 子命令（reset.go）：清空全部本地数据（登录态、站点数据、服务器配置），
//     先停运行中的实例再删目录；
//  5. 监听 Ctrl+C / 主窗口关闭 / Chrome 退出，随之收尾退出（按进程组清理）。
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
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	// Windows GUI 子系统（-H windowsgui）下挂回父终端：cmd/PowerShell 启动时恢复
	// stdio（启动横幅、reset 的输出与确认输入）；其它平台 no-op
	attachParentConsole()

	// 子命令分派（须在 flag.Parse 之前——位置参数不属于任何 flag，与 openos-server
	// 的 mcp-shim 同一模式）。reset：清空全部本地数据（profile + 服务器配置）
	if len(os.Args) > 1 && os.Args[1] == "reset" {
		os.Exit(runReset(os.Args[2:]))
	}

	urlFlag := flag.String("url", "", "OpenOS 地址（显式指定时跳过启动选择页直连：仅本次生效、不落盘；开发模式可指 http://localhost:5173）")
	autoFlag := flag.Bool("auto", false, "跳过启动选择页，自动连接已保存的服务器地址（默认每次启动先选择服务器再进入）")
	chromeFlag := flag.String("chrome", "", "Chrome/Chromium/Edge 可执行文件路径（默认自动探测，$OPENOS_CHROME 同效）")
	profileFlag := flag.String("profile", "", "专用用户数据目录（默认 ~/.openos-desktop，登录态持久化于此）")
	portFlag := flag.Int("port", 0, "CDP 调试端口（默认自动选取空闲端口）")
	confPortFlag := flag.Int("conf-port", 0, "服务器设置页的回环端口（默认 17700，被占自动递增；$OPENOS_DESKTOP_CONF_PORT 同效）")
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
	// 记录客户端 pid：reset 子命令据此停止运行中的实例（配进程身份校验防 PID 复用）
	_ = os.WriteFile(filepath.Join(profile, "client.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)

	// 服务器配置（OpenOS 兼容端地址）：-url 显式 > 已保存配置 > 默认本地。
	// -url 只覆盖本次运行，不写盘——run.sh 透传的开发地址不会顶掉用户保存的在线端。
	// 未直连时 target 仅作为选择页的「上次连接」预填值展示
	confPath := filepath.Join(profile, "config.json")
	cfg := loadDesktopConfig(confPath)
	target := *urlFlag
	if target == "" {
		target = cfg.URL
	}
	if target == "" {
		target = "http://127.0.0.1:8081"
	}
	if norm, err := normalizeTargetURL(target); err == nil {
		target = norm
	} else if *urlFlag == "" {
		fmt.Fprintf(os.Stderr, "已保存的地址无效（%v），回退默认本地地址\n", err)
		target = "http://127.0.0.1:8081"
	} else {
		fatal(fmt.Errorf("-url 无效: %w", err))
	}
	sw := newServerConf(confPath, cfg, target)
	confPort := *confPortFlag
	if confPort == 0 {
		confPort = 17700
		if v := os.Getenv("OPENOS_DESKTOP_CONF_PORT"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				confPort = n
			}
		}
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

	// 主窗口会话 + 导航回调：设置页「连接」与 ⚙ 入口都经它导航主窗口。设置服务先于
	// Chrome 启动（默认首开就是设置页的选择形态），CDP 连接 / 主窗口会话就绪前先
	// 暂存导航意图、就绪后重试补发（15s 超时）；后到的意图盖掉先到的——快速连点以
	// 最后一次为准
	mw := &mainWindow{}
	var connP atomic.Pointer[cdp]
	var navSeq atomic.Int64
	nav := func(u string) {
		seq := navSeq.Add(1)
		go func() {
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				if seq != navSeq.Load() {
					return
				}
				if c := connP.Load(); c != nil {
					if sid := mw.get(); sid != "" {
						if _, err := c.call("Page.navigate", map[string]any{"url": u}, sid); err == nil {
							return
						}
					}
				}
				time.Sleep(150 * time.Millisecond)
			}
			fmt.Fprintf(os.Stderr, "主窗口导航失败（超时）: %s\n", u)
		}()
	}
	// 回环设置页（服务器设置）：主窗口右下角 ⚙ 进入；目标地址打不开时自动转入
	selfBase := listenSettings(confPort, sw, nav)

	// 首开地址：默认每次启动先选服务器再进入（设置页 pick 形态，选定后落盘并导航）；
	// -url 显式（开发/自动化一次性目标）与 -auto（直连已保存地址）跳过；设置服务
	// 降级（回环端口全被占）时同样退回直连 target
	pickStart := *urlFlag == "" && !*autoFlag
	startURL := target
	if pickStart && selfBase != "" {
		startURL = selfBase + "/?pick=1"
	}

	cmd := exec.Command(chromePath, chromeArgs(startURL, profile, port)...)
	setSysProcAttr(cmd)
	tail := &tailBuf{}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		fatal(err)
	}
	fmt.Printf("OpenOS 桌面客户端已启动\n  浏览器:   %s\n  打开:     %s\n  调试端口: %d\n  用户目录: %s\n",
		chromePath, startURL, port, profile)
	if selfBase != "" {
		fmt.Printf("  设置页:   %s（窗口右下角 ⚙ 进入；连接的地址保存在 %s）\n", selfBase, confPath)
	}

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
	connP.Store(conn)

	done := make(chan struct{}) // 主窗口关闭
	go watchTargets(conn, os.Getpid(), port, sw, docReferrer(), selfBase, mw, done)

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
// 与请求头改写（fetchHandler）。连接的 OpenOS 源（守卫 / Referer 改写 / Cookie 归一化
// 豁免）经 serverConf 动态取值——设置页切换服务器后自动跟随。selfBase 为本地设置页
// 基地址（空 = 设置服务降级；标记对象里的 settingsUrl 亦随之省略，前端不显示入口）；
// mw 记录主窗口会话供设置页导航。
func watchTargets(c *cdp, pid, port int, sw *serverConf, docRef, selfBase string, mw *mainWindow, done chan<- struct{}) {
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
	mark := markSource(pid, port, docRef, selfBase)
	shim := cookieShimSource()
	mainTarget := ""
	sess := map[string]*pageSession{} // sessionId → 网络事件关联状态
	sessTarget := map[string]string{} // sessionId → targetId（主帧守卫只认主窗口）
	go fetchHandler(c, sw)
	// 首个 page 会话供 normalizeLoop 使用（Network.* 只能在页面级会话上调用）
	var sessMu sync.Mutex
	firstPage := ""
	firstSession := func() string { sessMu.Lock(); defer sessMu.Unlock(); return firstPage }
	go normalizeLoop(c, firstSession, sw)
	go mirrorLoop(c, firstSession, sw)
	lastErrNav := time.Time{} // chrome-error 兜底防抖（事件循环单协程访问）
	mainFrameId := ""         // 主窗口顶层 frame 的 frameId（frameRequestedNavigation 只认它，子帧导航不掺和）
	leavePass := false        // 最近一次主帧导航请求的目标在守卫源内（SSO 形态：同源端点 302 后外出）
	away := false             // 主帧外出转场中（登录流），回到守卫源即复位；转场内的一切外部导航放行
	// inGuard 判定 URL 是否守卫源内（OpenOS 源或本地设置页）；origin 经 serverConf 动态取值
	inGuard := func(u string) bool {
		if o := sw.Origin(); o != "" && strings.HasPrefix(u, o) {
			return true
		}
		return selfBase != "" && strings.HasPrefix(u, selfBase)
	}
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
			sessTarget[p.SessionID] = p.TargetInfo.TargetID
			// 标记两路齐下：新文档（任何脚本执行前注入，覆盖刷新/跳转）+ 当前文档（立即生效）。
			// 注意 addScriptToEvaluateOnNewDocument 只覆盖主帧（跨源子帧不注入），
			// 子帧靠 Runtime.contextCreated 即时补丁 + 周期 walker 兜底（见下）
			_, _ = c.call("Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": mark}, p.SessionID)
			_, _ = c.call("Runtime.evaluate", map[string]any{"expression": mark}, p.SessionID)
			// 主窗口会话记录：设置页「连接」的导航经此下发；并补初始导航竞态
			//（附身晚于 Chrome 完成初始导航时 frameNavigated 已错过，见 initialWatch）
			if p.TargetInfo.TargetID == mainTarget {
				mw.set(p.SessionID)
				// 立即取 frame 树钉住主帧 frameId：守卫的 frameRequestedNavigation
				// 判定依赖它，附身晚于初始导航时等 frameNavigated 补会漏掉首个
				// SSO 跳转请求（页面脚本一加载就跳登录端点的形态），先被误回捕一次
				if r, err := c.call("Page.getFrameTree", nil, p.SessionID); err == nil {
					var ft struct {
						FrameTree struct {
							Frame struct {
								ID string `json:"id"`
							} `json:"frame"`
						} `json:"frameTree"`
					}
					if json.Unmarshal(r, &ft) == nil && ft.FrameTree.Frame.ID != "" {
						mainFrameId = ft.FrameTree.Frame.ID
					}
				}
				if selfBase != "" {
					go initialWatch(c, p.SessionID, sw, selfBase)
				}
			}
			// Cookie 桥与 realm 补丁需要 Network/Runtime 事件流；主帧导航守卫需要 Page 事件
			if _, err := c.call("Network.enable", nil, p.SessionID); err == nil {
				sess[p.SessionID] = newPageSession()
			}
			_, _ = c.call("Page.enable", nil, p.SessionID)
			// 请求级拦截（fetchHandler）：剥 Sec-Fetch-* / OpenOS 源 Referer/Origin 改写。
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
		case "Page.frameRequestedNavigation":
			// renderer 发起的主帧导航请求（服务端 302 重定向不触发——那由首个同源
			// 请求代表）。SSO 登录流形态：页面 location.href = 守卫源同源端点
			//（如 /api/v1/auth/sso/login）→ 服务端 302 到身份源再回来；framebusting
			// 则把 top 直接拔到外部 URL。据此发/撤外出通行证（302 链中间跳无事件，
			// 凭此证放行）。
			var p struct {
				FrameID string `json:"frameId"`
				URL     string `json:"url"`
			}
			if json.Unmarshal(ev.Params, &p) != nil || mainFrameId == "" || p.FrameID != mainFrameId {
				continue
			}
			leavePass = navRequestInGuard(p.URL, inGuard)
		case "Page.frameNavigated":
			// 主帧防走失（等价 webproxy 的 CatchEscape 回捕）：桌面模式下应用 iframe
			// 不带 sandbox（美团等微前端容器需要），第三方页面理论上可 framebusting
			// （top.location 自拔带走整个桌面）。判定见 classifyMainNav：守卫源内放行；
			// 同源请求发起的外出（SSO 登录）放行转场；直接拔到外部才回捕。只认主
			// 窗口——「登录」按钮弹出的顶层窗口本就该导航到目标站，不能误伤
			var p struct {
				Frame struct {
					ID       string `json:"id"`
					ParentID string `json:"parentId"`
					URL      string `json:"url"`
				} `json:"frame"`
			}
			if json.Unmarshal(ev.Params, &p) != nil || p.Frame.ParentID != "" || p.Frame.URL == "" {
				continue
			}
			if mainTarget == "" || sessTarget[ev.SessionID] != mainTarget {
				continue
			}
			if p.Frame.ID != "" {
				mainFrameId = p.Frame.ID
			}
			// 服务器地址打不开（DNS 失败 / 拒绝连接等）：chrome-error 页不跑任何注入
			// 脚本、⚙ 入口也无处安放，直接转入本地设置页（带 unreachable 提示），
			// 5s 冷却防抖；设置服务降级时退化为下面的守卫回捕
			if strings.HasPrefix(p.Frame.URL, "chrome-error://") && selfBase != "" {
				if time.Since(lastErrNav) > 5*time.Second {
					lastErrNav = time.Now()
					fmt.Printf("服务器地址打不开（%s），转入设置页\n", sw.Current())
					_, _ = c.call("Page.navigate", map[string]any{
						"url": selfBase + "/?unreachable=" + url.QueryEscape(sw.Current()),
					}, ev.SessionID)
				}
				continue
			}
			switch classifyMainNav(p.Frame.URL, away, leavePass, inGuard) {
			case navHome:
				away = false
			case navAway:
				// 外出转场中（身份源登录页的页内导航/表单提交），等它回来
			case navLeave:
				away = true
				fmt.Printf("主帧经同源请求外出（登录跳转），放行: %s\n", p.Frame.URL)
			case navHijack:
				// 守卫目标跟随 serverConf（设置页里切换服务器后立即生效）；origin 未知
				//（未配置）时不动作
				if origin := sw.Origin(); origin != "" {
					fmt.Printf("主帧被导航到 %s，回捕到 %s\n", p.Frame.URL, origin)
					_, _ = c.call("Page.navigate", map[string]any{"url": origin + "/"}, ev.SessionID)
				}
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

// navDecision 主帧导航守卫决策。
type navDecision int

const (
	navHome   navDecision = iota // 回到守卫源：复位转场态，放行
	navAway                      // 外出转场中（SSO 登录流），放行
	navLeave                     // 主帧以同源请求发起的外出（302 后到外部，SSO 登录形态）：放行并进入转场
	navHijack                    // 主帧被直接拔到外部（iframe framebusting / 未知发起方）：回捕
)

// classifyMainNav 主帧文档导航守卫判定。SSO 登录流形态是「页面导航到守卫源的同源
// 端点（如 /api/v1/auth/sso/login），服务端 302 到身份源，完成后再 302 回来」——
// 导航请求时 URL 在源内（leavePass，见 Page.frameRequestedNavigation），提交时已在外；
// framebusting 则把 top 直接拔到外部 URL（无通行证）。away 表示已进入外出转场，
// 身份源登录页自身的页内导航/表单提交均放行，直到回到守卫源。
func classifyMainNav(url string, away, leavePass bool, inGuard func(string) bool) navDecision {
	if inGuard(url) {
		return navHome
	}
	if away {
		return navAway
	}
	if leavePass {
		return navLeave
	}
	return navHijack
}

// navRequestInGuard 判定 renderer 发起的主帧导航请求是否指向守卫源（发给外出通行证）。
// 相对路径必然落在守卫源内（协议字段通常给绝对 URL，双保险）；空 URL 无从判定，
// 不发证（守卫保守侧：宁可多回捕，不可漏掉 framebusting）。
func navRequestInGuard(u string, inGuard func(string) bool) bool {
	if strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") {
		return true
	}
	return u != "" && inGuard(u)
}

// fetchHandler 处理请求级拦截（Fetch 域，Request 阶段）：
//   - 剥 Sec-Fetch-* 整组头：跨站 iframe 发出的请求带 Sec-Fetch-Site: cross-site 语义，
//     部分目标站（美团 ebooking 实测：ebIframe 直达 302 → /ebooking/404，同源 fetch
//     则 200）按其做服务端风控，反代 webproxy 早已验证过剥除是正确姿势；
//   - Referer 指向 OpenOS 源（如 http://127.0.0.1:8081）时改写为目标站自身源：
//     iframe 文档导航的 Referer 是嵌入页地址，对按 Referer 白名单风控的接口
//     （微信 webwxnewloginpage 同款）等于自曝。其余 Referer（目标站自身页面间）原样。
//
// OpenOS 源经 serverConf 动态取值（设置页切换服务器后跟随）。每个事件必须 continue：
// 暂停中的请求不继续，页面会挂死（authRequired 走默认处理）。
func fetchHandler(c *cdp, sw *serverConf) {
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
			if hdrs, changed := rewriteHeaders(p.Request.Headers, p.Request.URL, sw.Origin()); changed {
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
// 表示无需改写（continueRequest 不带 headers 原样放行）。
//
// 注意 Origin 改不到：Chrome 的 requestPaused 暂停视图不含 Origin/Sec-Fetch/Cookie
// 之外的网络层头，Origin 由网络栈在拦截之后附加（kdocs 实测：其接口按 Origin 判
// 异源直接 403，跨站 iframe 的 fetch 必带 OpenOS 源 Origin，continueRequest 无法
// 剥除）。此类站点桌面端只能走 desktopProxy 反代（webproxy 转发时整头剥 Origin）。
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
		// 仅当请求发往 OpenOS 源之外（目标站）才改写其 OpenOS 源 Referer：发往
		// OpenOS 自身的请求（服务端反代 /app/*、REST API）Referer 本就应指向
		// OpenOS，且 continueRequest 的 headers 是整组替换——requestPaused 暂停
		// 视图不含 Cookie/Sec-Fetch 等网络栈后挂头，替换会让这类请求裸奔丢
		// Cookie（kdocs 代理页实测：创建接口一律 403 SessionDeleted）。发往目标
		// 站的文档导航（iframe src 直连真实地址）仍按原样改写防 Referer 自曝。
		if lk == "referer" && osdOrigin != "" && strings.HasPrefix(v, osdOrigin) && !strings.HasPrefix(reqURL, osdOrigin) {
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
}catch(e){}})();` + swGuardSource() + globalBridgeSource() + keyForwardSource() + openTabSource()
}

// openTabSource iframe 内新标签页导航收编（桌面端直连形态）：kdocs 等站点把
// 「新建文档/打开文档」一律开新标签页——window.open、动态 <a target=_blank> 的
// 程序化 click()、真实点击 _blank 锚点的默认行为三种通道（kdocs 实测走第二条：
// 处理器动态建锚点后 a.click()），在直连 iframe 里都会弹成真实 Chrome 窗口逃出
// OpenOS 桌面。三条通道统一收编为所属 WebApp 窗体内的新 tab：沿 frameElement
// 跨文档上溯找到窗体根上的 __openosPxHost（webproxy hook.js 的同一宿主协议；
// SOP 已关可跨 realm 直调）。找不到宿主链（不在任何 WebApp 窗体内的帧，如独立
// 弹窗的子帧）回落原生行为；反代帧（hook.js 已接管，__openosPx 标记在调用期
// 存在）不夺权。
func openTabSource() string {
	return `(function(){try{
if(window.__OSD_OPENTAB__)return;
if(window.top===window.self)return;
Object.defineProperty(window,'__OSD_OPENTAB__',{value:true});
var findHost=function(){
try{
var fe=window.frameElement;
while(fe){
var root=fe.closest?fe.closest('[data-os-webapp]'):null;
if(root&&root.__openosPxHost&&root.__openosPxHost.openTab)return root.__openosPxHost;
var w2=fe.ownerDocument.defaultView;
if(!w2||w2===w2.top)break;
fe=w2.frameElement;
}
}catch(e){}
return null;
};
var asTab=function(url,name){
try{
var host=findHost();
if(host){host.openTab(url,name);return true}
}catch(e){}
return false;
};
var realUrl=function(u){
try{
var s=String(u||'');
if(!s||s==='about:blank'||s.indexOf('javascript:')===0)return '';
var a=document.createElement('a');a.href=s;
return a.href;
}catch(e){return ''}
};
var oo=window.open.bind(window);
window.open=function(u,n,f){
try{
var url=realUrl(u);
if(url&&!window.__openosPx&&asTab(url,n))return {closed:false,opener:window,close:function(){},focus:function(){},postMessage:function(){},location:{href:url},document:document};
}catch(e){}
return oo(u,n,f);
};
var ac=window.HTMLAnchorElement.prototype.click;
window.HTMLAnchorElement.prototype.click=function(){
try{
if(String(this.target||'')==='_blank'&&!window.__openosPx){
var url=realUrl(this.href);
if(url&&asTab(url))return;
}
}catch(e){}
return ac.apply(this,arguments);
};
document.addEventListener('click',function(e){
try{
if(!e.isTrusted)return;
var a=e.target&&e.target.closest?e.target.closest('a[target=_blank]'):null;
if(!a||window.__openosPx)return;
var url=realUrl(a.href);
if(url&&asTab(url))e.preventDefault();
}catch(x){}
},true);
}catch(e){}})();`
}

// swGuardSource Service Worker 守卫（桌面客户端专用）：目标站的 SW 在客户端
// 上下文里是毒药——kdocs 的 SW 拦截自家 API 请求后以「SessionDeleted」拒绝
// （同 Cookie 同源在无 SW 的干净 Chrome 一切正常，实锤归因），且其缓存让
// 僵尸会话的页面照常渲染、极具迷惑性。判定用运行时帧拓扑而非模板化源：帧自身
// 与顶层同源（OpenOS 页/设置页自身，或顶层即目标站的独立弹窗内的子帧）不设防，
// 其余（嵌入应用帧、目标站弹窗顶层）禁掉 navigator.serviceWorker.register 并
// 异步卸载已注册的——OpenOS 自身的 PWA SW（sw.js 直通清单）不受影响。须在
// 文档任何脚本前执行（随 markSource 注入主文档与全部子帧；SOP 已关故
// window.top.location 可跨源读取，读不到时保守不设防）。
func swGuardSource() string {
	return `(function(){try{
if(window.__OSD_SWGUARD__)return;
Object.defineProperty(window,'__OSD_SWGUARD__',{value:true});
var top=null;try{top=window.top.location.origin}catch(e){}
var foreign=top&&location.origin!==top;
if(!top&&!foreign)return;
var sw=navigator.serviceWorker;
if(!sw)return;
var isProxyScope=function(u){try{var s=String(u==null?'':u);return s.indexOf('/app/')>=0}catch(e){return false}};
try{
	var orig=Object.getPrototypeOf(sw).register;
	Object.defineProperty(sw,'register',{configurable:true,writable:true,value:function(a,o){
		if(foreign||isProxyScope(a)||isProxyScope(o&&o.scope)){
			return Promise.reject(new Error('openos-desktop: 目标站 Service Worker 注册已禁用'));
		}
		return orig.call(sw,a,o);
	}});
}catch(e){}
try{
	sw.getRegistrations().then(function(rs){rs.forEach(function(r){
		var sc='';try{sc=r.scope}catch(e){}
		if(foreign||isProxyScope(sc)){try{r.unregister()}catch(e){}}
	})}).catch(function(){});
}catch(e){}
}catch(e){}})();`
}

// keyForwardSource iframe 内全局快捷键转发（SOP 已关、子帧直连真实站点）：
// 帧内聚焦后宿主 window 收不到按键，把系统级组合（F4 启动台 / ⌘·Ctrl+Space
// 聚焦搜索 / Win·⌘ 单击松开切换启动台）经 postMessage 转发给父页（WebApp 按
// contentWindow 校验来源后交统一键盘中枢 ui/keys.ts）。普通按键与打字内容
// 绝不外发；主帧（OpenOS 自身）不安装。
func keyForwardSource() string {
	return `(function(){try{
if(window.__OSD_KEYFWD__)return;
Object.defineProperty(window,'__OSD_KEYFWD__',{value:true});
if(window.top===window.self)return;
var solo=false;
var isMeta=function(e){return e.key==='Meta'||e.key==='OS'||e.code==='MetaLeft'||e.code==='MetaRight'};
var send=function(ev,e,extra){
try{
var m={type:'openos:key',ev:ev,key:e.key,code:e.code,alt:e.altKey,ctrl:e.ctrlKey,meta:e.metaKey,shift:e.shiftKey};
if(extra)for(var k in extra)m[k]=extra[k];
window.parent.postMessage(m,'*');
}catch(x){}
};
document.addEventListener('keydown',function(e){
solo=isMeta(e)&&!e.altKey&&!e.ctrlKey&&!e.shiftKey;
if(e.key==='F4')send('down',e);
else if(e.code==='Space'&&(e.metaKey||e.ctrlKey||e.altKey))send('down',e);
},true);
document.addEventListener('keyup',function(e){
var s=solo&&isMeta(e);solo=false;
if(s)send('up',e,{soloMeta:true});
},true);
window.addEventListener('blur',function(){solo=false});
}catch(e){}})();`
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
// iframe 请求上，统一改写为 None+Secure 后 iframe 内即为登录态。本地域与当前
// OpenOS 源不动——http 站点（本地服务端 / http 兼容端）的 Cookie 被强加 Secure 会
// 直接失效，且 OpenOS 自身会话是第一方、无需归一化。OpenOS 源经 serverConf 动态
// 取值（设置页切换服务器后跟随）。
// mirrorLoop 周期把目标站域的 Cookie jar 镜像到 desktopProxy 应用的代理路径。
// desktopProxy（桌面端也走服务端反代）解决「跨站 iframe 请求带 OpenOS 源 Origin
// 被目标站风控拒绝」的问题（kdocs 创建接口 403 SessionDeleted——requestPaused
// 暂停视图不含 Origin，客户端层无法剥除，见 rewriteHeaders 注释），但反代页的
// 会话 Cookie 落在代理路径（127.0.0.1 的 /app/<id>/ 下），真实域 jar 里经顶层
// 「登录」辅助窗口建立的会话不会自动流转过去。本循环：经页面 fetch /api/apps
// 取 desktopProxy 应用清单（随登录态），Storage.getCookies 全量取 jar，按目标
// 主机与注册域后缀（取 hostname 末两段，覆盖 .kdocs.cn 一类父域 Cookie）匹配，
// 值有变化才 Network.setCookie 推到 <openos源>/app/<id> 路径（http 本地域不
// Secure、SameSite=Lax——代理页与 OpenOS 同源，第一方语境照常携带）。只在 jar
// 值变化时推送：代理路径上目标站主动轮换出的新值不会被 jar 里的旧值回写覆盖。
func mirrorLoop(c *cdp, firstSession func() string, sw *serverConf) {
	type appHost struct {
		id   string
		host string
		reg  string
	}
	lastPushed := map[string]string{} // "<id>\x00<cookie名>" → 上次推送值
	t := time.NewTicker(2500 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		sid := firstSession()
		if sid == "" {
			continue
		}
		origin := sw.Origin()
		if origin == "" {
			continue
		}
		// 应用清单从页面取（带登录态；失败下轮重试）
		r, err := c.call("Runtime.evaluate", map[string]any{
			"expression":    `(async()=>{try{const r=await fetch('/api/apps',{credentials:'include'});if(!r.ok)return '';const list=await r.json();const out=[];for(const a of list){let cfg={};try{cfg=typeof a.config==='string'?JSON.parse(a.config):(a.config||{})}catch(e){}if(cfg&&cfg.kind==='proxy'&&cfg.desktopProxy&&cfg.url){let u;try{u=new URL(cfg.url)}catch(e){continue}out.push({id:a.id,host:u.hostname})}}return JSON.stringify(out)}catch(e){return ''}})()`,
			"awaitPromise":  true,
			"returnByValue": true,
		}, sid)
		if err != nil {
			continue
		}
		var ev struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if json.Unmarshal(r, &ev) != nil || ev.Result.Value == "" {
			continue
		}
		var raw []struct {
			ID   string `json:"id"`
			Host string `json:"host"`
		}
		if json.Unmarshal([]byte(ev.Result.Value), &raw) != nil {
			continue
		}
		var apps []appHost
		for _, a := range raw {
			if a.ID == "" || a.Host == "" {
				continue
			}
			labels := strings.Split(strings.Trim(a.Host, "."), ".")
			reg := a.Host
			if len(labels) >= 2 {
				reg = strings.Join(labels[len(labels)-2:], ".")
			}
			apps = append(apps, appHost{id: a.ID, host: a.Host, reg: reg})
		}
		if len(apps) == 0 {
			continue
		}
		jr, err := c.call("Storage.getCookies", nil, sid)
		if err != nil {
			continue
		}
		var jc struct {
			Cookies []struct {
				Name     string  `json:"name"`
				Value    string  `json:"value"`
				Domain   string  `json:"domain"`
				Path     string  `json:"path"`
				Expires  float64 `json:"expires"`
				HTTPOnly bool    `json:"httpOnly"`
			} `json:"cookies"`
		}
		if json.Unmarshal(jr, &jc) != nil {
			continue
		}
		for _, app := range apps {
			for _, ck := range jc.Cookies {
				d := strings.Trim(strings.ToLower(ck.Domain), ".")
				if d != app.host && d != app.reg && !strings.HasSuffix(d, "."+app.reg) {
					continue
				}
				key := app.id + "\x00" + ck.Name
				if lastPushed[key] == ck.Value {
					continue
				}
				params := map[string]any{
					"name":     ck.Name,
					"value":    ck.Value,
					"url":      origin + "/app/" + app.id,
					"path":     "/app/" + app.id,
					"httpOnly": ck.HTTPOnly,
					"secure":   false,
					"sameSite": "Lax",
				}
				if ck.Expires > 0 {
					params["expires"] = int64(ck.Expires)
				}
				if _, err := c.call("Network.setCookie", params, sid); err == nil {
					lastPushed[key] = ck.Value
				}
			}
		}
	}
}

func normalizeLoop(c *cdp, firstSession func() string, sw *serverConf) {
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
			if isLocalHost(d) || sw.CoversHost(d) {
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
//     据此让代理应用直连真实地址；标记对象带 settingsUrl（本地设置页地址）时，
//     前端「系统设置 → 服务器」分区与登录页「切换服务器」入口据此显示（桌面模式
//     专属；设置服务降级时无该字段，前端不显示入口）；
//  2. SameSite Cookie shim：跨站 iframe 里未标注 SameSite 的 document.cookie 写入
//     默认按 Lax 处理而被静默丢弃（美团 settoken 实测），shim 给这类写入自动补
//     SameSite=None（https 帧再补 Secure，Chrome 要求 None 须伴 Secure）。
//     注入发生在每个新文档任何脚本执行前，主文档与全部子帧生效。
func markSource(pid, port int, docRef, settingsURL string) string {
	v, _ := json.Marshal(struct {
		Client      string `json:"client"`
		Pid         int    `json:"pid"`
		DebugPort   int    `json:"debugPort"`
		SettingsURL string `json:"settingsUrl,omitempty"`
	}{Client: "openos-desktop", Pid: pid, DebugPort: port, SettingsURL: settingsURL})
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
})();` + globalBridgeSource() + keyForwardSource() + openTabSource()
}

// chromeArgs 组装 Chrome 启动参数。核心三件套：
//   - --app：独立应用窗口（无浏览器地址栏/标签页外壳），url 为首开地址——默认本地
//     设置页的启动选择形态（先选服务器再进入），-url/-auto 时为 OpenOS 地址
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
	// Windows 双击启动（GUI 子系统、无控制台）时 stderr 不可见，弹错误框兜底
	fatalDialog(err)
	os.Exit(1)
}
