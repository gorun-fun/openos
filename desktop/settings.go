package main

// 服务器设置：桌面客户端可配置「OpenOS 兼容端」地址（任何 OpenOS 部署，本地或在线），
// 运行中即可切换。三个部件：
//  1. 持久化配置 <profile>/config.json（url=当前服务器，recent=最近使用）；
//  2. 回环设置页（默认 http://127.0.0.1:17700，端口被占自动递增）：表单输入 / 最近
//     列表一键连接，POST /api/connect 校验落盘并导航主窗口；两种形态——独立页
//     （chrome-error 兜底转入选中）与内嵌 iframe（OpenOS 前端「系统设置 → 服务器」
//     分区，桌面模式专属，见 markSource 的 settingsUrl 标记）；
//  3. 目标地址打不开（chrome-error:// 页不跑任何注入脚本）时客户端自动转入设置页
//     兜底（frameNavigated + initialWatch 补附身竞态）。
//
// URL 优先级：-url 参数（显式指定仅本次生效、不落盘）> config.json 的 url >
// http://127.0.0.1:8081。连接的源运行期随切换动态更新：主帧防走失守卫、Fetch
// Referer 改写、Cookie jar 归一化豁免域全部经 serverConf 取值（见 watchTargets）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// recentMax 最近使用列表上限（含当前连接项）
const recentMax = 6

// desktopConfig 持久化配置（<profile>/config.json，0600）
type desktopConfig struct {
	URL    string   `json:"url,omitempty"`
	Recent []string `json:"recent,omitempty"`
}

// loadDesktopConfig 读配置；文件不存在返回零值，损坏则提示后按无配置处理
func loadDesktopConfig(path string) desktopConfig {
	var cfg desktopConfig
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "桌面配置 %s 解析失败（%v），按无配置处理\n", path, err)
		return desktopConfig{}
	}
	return cfg
}

// saveDesktopConfig 原子落盘（临时文件 + rename），权限 0600
func saveDesktopConfig(path string, cfg desktopConfig) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// serverConf 运行期共享的连接状态：持久配置 + 当前实际连接地址（-url 显式传入时
// 与配置不同，此时只读不写）。主帧守卫 / Fetch Referer 改写 / Cookie 归一化豁免
// 都从这里取当前值，切换服务器后自动跟随。
type serverConf struct {
	mu      sync.Mutex
	path    string
	cfg     desktopConfig
	runtime string // 当前实际连接的地址
}

func newServerConf(path string, cfg desktopConfig, runtimeURL string) *serverConf {
	return &serverConf{path: path, cfg: cfg, runtime: runtimeURL}
}

// Current 当前实际连接的地址
func (s *serverConf) Current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtime
}

// Origin 当前连接的源（scheme://host）；解析失败返回空串（守卫等按空串跳过）
func (s *serverConf) Origin() string {
	return originOf(s.Current())
}

// CoversHost 判断 cookie 域是否属于当前 OpenOS 源（源主机本身或其父域）：OpenOS 端
// 的 Cookie 是第一方会话，不参与跨站归一化——http 兼容端的 Cookie 被强加 Secure
// 会直接失效
func (s *serverConf) CoversHost(domain string) bool {
	if domain == "" {
		return false
	}
	s.mu.Lock()
	runtime := s.runtime
	s.mu.Unlock()
	h := hostOf(runtime)
	return h == domain || strings.HasSuffix(h, "."+domain)
}

// recentList 最近使用列表快照
func (s *serverConf) recentList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cfg.Recent...)
}

// Connect 校验并规范化地址、落盘、切换运行态；返回规范化后的地址
func (s *serverConf) Connect(raw string) (string, error) {
	u, err := normalizeTargetURL(raw)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := desktopConfig{URL: u, Recent: dedupeRecent(u, s.cfg.Recent)}
	if err := saveDesktopConfig(s.path, cfg); err != nil {
		return "", fmt.Errorf("配置保存失败: %w", err)
	}
	s.cfg = cfg
	s.runtime = u
	return u, nil
}

// dedupeRecent 新地址置顶 + 旧列表去重，上限 recentMax
func dedupeRecent(u string, old []string) []string {
	out := []string{u}
	for _, x := range old {
		if x == u || len(out) >= recentMax {
			continue
		}
		out = append(out, x)
	}
	return out
}

func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return ""
}

// isLocalHost 本机域名（Cookie jar 归一化豁免：http 站点 Cookie 强加 Secure 会失效）
func isLocalHost(d string) bool {
	return d == "localhost" || d == "[::1]" || d == "::1" || strings.HasPrefix(d, "127.")
}

// normalizeTargetURL 规范化用户输入的 OpenOS 兼容端地址：无 scheme 时本地主机补
// http://、其余补 https://（在线端绝大多数是 https）；只接受 http/https、host 非空、
// 去 fragment、限长 2048
func normalizeTargetURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("地址不能为空")
	}
	if !strings.Contains(s, "://") {
		host := s
		if i := strings.IndexAny(s, "/:"); i >= 0 {
			host = s[:i]
		}
		if isLocalHost(host) {
			s = "http://" + s
		} else {
			s = "https://" + s
		}
	}
	if len(s) > 2048 {
		return "", errors.New("地址过长")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("地址须为 http/https URL，例如 https://os.example.com")
	}
	if u.Host == "" {
		return "", errors.New("缺少主机名")
	}
	u.Fragment = ""
	return u.String(), nil
}

// mainWindow 主窗口（首个 page 目标）的 CDP 会话：设置页「连接」的导航经它下发
//（Page.navigate 须在目标会话上调用）
type mainWindow struct {
	mu      sync.Mutex
	session string
}

func (m *mainWindow) set(session string) {
	m.mu.Lock()
	m.session = session
	m.mu.Unlock()
}

func (m *mainWindow) get() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.session
}

// listenSettings 起回环设置页服务（只绑 127.0.0.1）：port 起步被占则递增重试共
// 20 个，全部失败返回空串 = 降级（无 ⚙ 入口与打不开兜底，打开 OpenOS 的主功能
// 不受影响）。返回设置页基地址。
func listenSettings(port int, conf *serverConf, nav func(string)) string {
	for p := port; p < port+20; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue
		}
		base := fmt.Sprintf("http://127.0.0.1:%d", p)
		mux := http.NewServeMux()
		h := &settingsHandler{conf: conf, nav: nav}
		mux.HandleFunc("/", h.page)
		mux.HandleFunc("/api/connect", h.connect)
		go func() { _ = http.Serve(ln, mux) }()
		return base
	}
	fmt.Fprintf(os.Stderr, "设置页服务启动失败（%d 起的 20 个端口均被占用），服务器设置功能不可用\n", port)
	return ""
}

// initialWatch 补初始导航的竞态：CDP 附身可能晚于 Chrome 完成初始导航（本地端口
// 拒绝是毫秒级），frameNavigated 已错过，主窗口停在 chrome-error 页——那里不跑
// 任何注入脚本、⚙ 入口无处安放。附身主窗口后轮询 location.href：打不开（chrome-error）
// 转设置页，打开成功（回到 OpenOS 源）即止，最长盯 15s。
func initialWatch(c *cdp, sessionId string, sw *serverConf, selfBase string) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		r, err := c.call("Runtime.evaluate", map[string]any{
			"expression": "location.href", "returnByValue": true,
		}, sessionId)
		if err != nil {
			return
		}
		var p struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if json.Unmarshal(r, &p) == nil && p.Result.Value != "" {
			href := p.Result.Value
			if strings.HasPrefix(href, "chrome-error://") {
				fmt.Printf("服务器地址打不开（%s），转入设置页\n", sw.Current())
				_, _ = c.call("Page.navigate", map[string]any{
					"url": selfBase + "/?unreachable=" + url.QueryEscape(sw.Current()),
				}, sessionId)
				return
			}
			if strings.HasPrefix(href, sw.Origin()) {
				return // 打开成功
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// settingsHandler 设置页与连接接口。回环明文服务、无鉴权——与 CDP 调试端口同一
// 威胁模型（仅本机进程可达，能打到本口的进程早就能打 CDP 口直接操控浏览器）。
type settingsHandler struct {
	conf *serverConf
	nav  func(string) // 导航主窗口
}

func (h *settingsHandler) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page := renderSettingsPage(h.conf.Current(), h.conf.recentList(), r.URL.Query().Get("unreachable"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, page)
}

func (h *settingsHandler) connect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, `{"ok":false,"error":"仅支持 POST"}`)
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"ok":false,"error":"请求体不是合法 JSON"}`)
		return
	}
	u, err := h.conf.Connect(body.URL)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		b, _ := json.Marshal(map[string]any{"ok": false, "error": err.Error()})
		_, _ = w.Write(b)
		return
	}
	h.nav(u)
	b, _ := json.Marshal(map[string]any{"ok": true, "url": u})
	_, _ = w.Write(b)
}

// renderSettingsPage 渲染设置页：动态值以占位符替换——__DATA__（页面初始数据 JSON，
// encoding/json 默认转义 <>&，内嵌 <script> 安全）、__UNREACHABLE_BLOCK__（地址
// 打不开提示块，query 参数带出，html.EscapeString 转义）
func renderSettingsPage(current string, recent []string, unreachable string) string {
	data, _ := json.Marshal(map[string]any{"current": current, "recent": recent})
	page := strings.Replace(settingsPageHTML, "__DATA__", string(data), 1)
	block := ""
	if unreachable != "" {
		block = `<div class="warn">⚠ 无法打开 <code>` + html.EscapeString(unreachable) + `</code>——请检查地址与网络，或改连其它服务器。</div>`
	}
	return strings.Replace(page, "__UNREACHABLE_BLOCK__", block, 1)
}

// settingsPageHTML 设置页静态骨架（无外部资源；JS 不用模板字符串，整段可安全放在
// Go 原始字符串里）
const settingsPageHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>OpenOS · 服务器设置</title>
<script>
// 内嵌形态（OpenOS「系统设置 → 服务器」分区的 iframe）：融入窗口背景并跟随宿主
// 主题。桌面模式已关同源策略，可跨源读父窗口 html.dark（读不到则维持默认深色）
try{
if(window.self!==window.top){
  document.documentElement.className+=' embed';
  try{
    var pc=window.parent.document.documentElement.classList;
    if(pc&&pc.contains&&pc.contains('dark'))document.documentElement.className+=' dark';
  }catch(e){}
}
}catch(e){}
</script>
<style>
  *{box-sizing:border-box;margin:0;padding:0}
  html,body{min-height:100%}
  body{font-family:-apple-system,BlinkMacSystemFont,"PingFang SC","Segoe UI","Microsoft YaHei",sans-serif;
    background:radial-gradient(1200px 700px at 70% -10%,#2c3e55 0%,#15161a 55%,#0c0d10 100%);
    color:#e8e8ea;display:flex;align-items:center;justify-content:center;padding:24px}
  .card{width:100%;max-width:460px;background:rgba(38,39,44,.92);border:1px solid rgba(255,255,255,.1);
    border-radius:16px;padding:32px 30px;box-shadow:0 20px 60px rgba(0,0,0,.5);backdrop-filter:blur(10px)}
  .logo{width:44px;height:44px;border-radius:11px;background:linear-gradient(135deg,#3f8cff,#7a5cff);
    display:flex;align-items:center;justify-content:center;font-weight:700;font-size:15px;color:#fff;margin-bottom:16px}
  h1{font-size:19px;font-weight:600}
  .sub{color:#9a9aa2;font-size:13px;margin-top:6px;line-height:1.6}
  .warn{margin-top:18px;padding:10px 12px;border-radius:9px;background:rgba(255,159,10,.12);
    border:1px solid rgba(255,159,10,.35);color:#ffb340;font-size:13px;line-height:1.6;word-break:break-all}
  .lbl{margin-top:22px;font-size:12px;color:#9a9aa2;letter-spacing:.04em}
  .row{display:flex;gap:10px;margin-top:8px}
  input{flex:1;min-width:0;background:rgba(0,0,0,.35);border:1px solid rgba(255,255,255,.16);color:#eee;
    border-radius:9px;padding:10px 12px;font-size:14px;outline:none;font-family:ui-monospace,Menlo,monospace}
  input:focus{border-color:#3f8cff}
  input::placeholder{color:#666a72}
  button.go{background:#0a84ff;color:#fff;border:none;border-radius:9px;padding:0 20px;font-size:14px;
    cursor:pointer;font-weight:600;flex-shrink:0}
  button.go:hover{background:#2b95ff}
  button.go:disabled{opacity:.5;cursor:default}
  .cur{margin-top:8px;padding:9px 12px;border-radius:9px;background:rgba(0,0,0,.25);
    border:1px solid rgba(255,255,255,.09);font-family:ui-monospace,Menlo,monospace;font-size:13px;
    color:#b9bac2;word-break:break-all}
  .err{color:#ff6961;font-size:13px;margin-top:10px;min-height:18px;line-height:1.5}
  .pills{margin-top:10px;display:flex;flex-direction:column;gap:8px}
  .pill{display:flex;align-items:center;gap:8px;text-align:left;background:rgba(255,255,255,.05);
    border:1px solid rgba(255,255,255,.12);color:#cfd0d6;border-radius:9px;padding:9px 12px;font-size:13px;
    cursor:pointer;font-family:ui-monospace,Menlo,monospace;word-break:break-all;transition:background .12s}
  .pill:hover{background:rgba(255,255,255,.11)}
  .pill.on{border-color:rgba(10,132,255,.55);color:#7db8ff}
  .dot{width:7px;height:7px;border-radius:50%;background:#30d158;flex-shrink:0}
  .hint{margin-top:22px;color:#7c7d85;font-size:12px;line-height:1.7}
  code{font-family:ui-monospace,Menlo,monospace}
  /* 内嵌形态：去独立页外壳（背景/卡片/logo/标题），融入设置窗口 */
  html.embed body{background:transparent;display:block;padding:0;overflow-y:auto}
  html.embed .card{max-width:none;background:transparent;border:none;box-shadow:none;backdrop-filter:none;padding:0;border-radius:0}
  html.embed .logo,html.embed h1,html.embed .sub{display:none}
  html.embed #addrLbl{margin-top:0}
  /* 内嵌 + 宿主浅色主题：深色控件换浅色（dark 类由头部脚本从父窗口带入） */
  html.embed:not(.dark) body{color:#1d1d1f}
  html.embed:not(.dark) .lbl{color:#8a8a90}
  html.embed:not(.dark) .cur{background:rgba(0,0,0,.04);border-color:rgba(0,0,0,.08);color:#494950}
  html.embed:not(.dark) input{background:rgba(0,0,0,.04);border-color:rgba(0,0,0,.14);color:#1d1d1f}
  html.embed:not(.dark) input::placeholder{color:#8a8a90}
  html.embed:not(.dark) .pill{background:rgba(0,0,0,.03);border-color:rgba(0,0,0,.1);color:#3a3a40}
  html.embed:not(.dark) .pill:hover{background:rgba(0,0,0,.07)}
  html.embed:not(.dark) .pill.on{border-color:rgba(10,132,255,.5);color:#0a84ff}
  html.embed:not(.dark) .hint{color:#8a8a90}
</style>
</head>
<body>
<main class="card">
  <div class="logo">OS</div>
  <h1>OpenOS 桌面客户端</h1>
  <p class="sub">连接 OpenOS 兼容端——任何 OpenOS 部署的地址（本地或在线均可），随时可切换。</p>
  __UNREACHABLE_BLOCK__
  <div class="lbl" id="addrLbl">服务器地址</div>
  <form class="row" id="f">
    <input id="u" placeholder="https://your-openos.example.com" spellcheck="false" autocomplete="off">
    <button class="go" id="go" type="submit">连接</button>
  </form>
  <div class="err" id="err"></div>
  <div class="lbl" id="curLbl">当前连接</div>
  <div class="cur" id="cur"></div>
  <div class="lbl" id="recLbl">最近使用</div>
  <div class="pills" id="rec"></div>
  <p class="hint">地址保存在 <code>~/.openos-desktop/config.json</code>，下次启动自动连接；<br>
  窗口内等于本机真实浏览器会话，勿连接不可信的 OpenOS 实例。</p>
</main>
<script>
var DATA=__DATA__;
var $=function(id){return document.getElementById(id)};
$('cur').textContent=DATA.current||'（未配置）';
if(DATA.current){$('u').placeholder=DATA.current}
var rec=$('rec');
(DATA.recent||[]).forEach(function(u){
  var b=document.createElement('button');
  b.type='button';b.className='pill'+(u===DATA.current?' on':'');
  var d=document.createElement('span');d.className='dot';b.appendChild(d);
  b.appendChild(document.createTextNode(u));
  b.onclick=function(){go(u)};
  rec.appendChild(b);
});
if(!(DATA.recent||[]).length){rec.style.display='none';$('recLbl').style.display='none'}
if(!DATA.current){$('cur').style.display='none';$('curLbl').style.display='none'}
var err=$('err'),btn=$('go');
function reset(){btn.disabled=false;btn.textContent='连接'}
function go(u){
  u=(u||'').trim();if(!u){err.textContent='请输入服务器地址';return}
  err.textContent='';btn.disabled=true;btn.textContent='连接中…';
  fetch('/api/connect',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({url:u})})
    .then(function(r){return r.json()})
    .then(function(j){
      if(!j.ok){err.textContent=j.error||'连接失败';reset();return}
      err.style.color='#30d158';err.textContent='正在打开 '+j.url+' …';
    })
    .catch(function(e){err.textContent=''+e;reset()});
}
$('f').onsubmit=function(e){e.preventDefault();go($('u').value)};
</script>
</body>
</html>`
