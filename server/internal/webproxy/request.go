// 转发请求的清洗与改写：逐跳头剥除、OpenOS 会话隔离、Referer 还原、
// OAuth 回跳参数还原、配置请求头应用与 Cookie 合并。
package webproxy

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// hopReq 转发请求时剥掉的逐跳/会话头。Origin/Referer 整头剥除后按需重建
// （见 sanitizeOutRequest：Referer 还原为目标站自身地址，Origin 语义在反代后
// 失真、不重建）。
var hopReq = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	"Authorization", "Referer", "Origin",
}

// sanitizeOutRequest 清洗转发上游的请求：剥逐跳/会话头、压缩协商收窄到 gzip、
// Referer 还原、Sec-Fetch-* 剥除、还原标记与回跳参数改写、会话 Cookie 过滤、
// X-Forwarded-* 补齐。
func sanitizeOutRequest(out, orig *http.Request, id string, cfg *proxyConfig, tu *url.URL) {
	h := out.Header
	for _, k := range hopReq {
		h.Del(k)
	}
	// 只接受 gzip：其余压缩格式（br/zstd）无法在纯 Go 侧解压做地址重写
	h.Set("Accept-Encoding", "gzip")
	// Referer 还原为目标站自身的绝对地址（替代此前的整头剥除）：目标站的敏感
	// 接口常校验 Referer 白名单——微信扫码后的 webwxnewloginpage（发凭证）在无
	// Referer 时被风控拒绝，返回「暂不支持使用网页版微信」提示页。还原值是目标
	// 站自己的地址，不泄露 OpenOS 宿主信息；无法识别的 Referer 仍不发送。
	if ref := orig.Header.Get("Referer"); ref != "" {
		if rr := rewriteReferer(ref, id); rr != "" {
			h.Set("Referer", rr)
		}
	}
	// Referer 兜底重建：反代页里同源请求原生形态本就携带目标站自身地址的
	// Referer（代理前缀/还原标记两种形态上面已还原），识别不出时补目标站根
	// 地址——值是目标站自己的，不泄露宿主信息。
	if h.Get("Referer") == "" {
		h.Set("Referer", tu.Scheme+"://"+tu.Host+"/")
	}
	// Origin 不在此无条件重建：原生 GET 请求不携带 Origin，凭空补上是对未知
	// 站点的多余改写。kdocs/美团系等写接口严判 Origin 的已知站点由 adapter.go
	// 的 origin-strict 适配器按目标主机兜底补齐（与 Referer 重建同一语义）。
	// Sec-Fetch-* 描述浏览器视角的源关系（代理后基准源是 OpenOS 宿主，到达目标站
	// 语义全失真——如跨域跳转显示成 same-origin），整组剥除，服务器按无该头处理
	for k := range h {
		if strings.HasPrefix(k, "Sec-Fetch-") {
			h.Del(k)
		}
	}
	// 剥掉 hook 还原标记：它只服务于文档地址与逃逸回捕，不应到达目标站；
	// 并把回跳参数（redirect_uri，含 hook 改写的代理路径与 JS 拼 host 的宿主
	// 绝对地址两种形态）还原为目标站注册的原始值——白名单/风控校验依赖原始值
	out.URL.RawQuery = restorePxParam(id, stripPxMark(out.URL.RawQuery), cfg, orig.Host)
	// Cookie 过滤：绝不把 OpenOS 会话带给目标站；其余（落在代理路径下的目标会话）放行
	if c := orig.Header.Get("Cookie"); c != "" {
		if kept := keepCookies(c); kept != "" {
			h.Set("Cookie", kept)
		} else {
			h.Del("Cookie")
		}
	}
	if host, _, err := net.SplitHostPort(orig.RemoteAddr); err == nil {
		if xff := h.Get("X-Forwarded-For"); xff != "" {
			h.Set("X-Forwarded-For", xff+", "+host)
		} else {
			h.Set("X-Forwarded-For", host)
		}
	}
	if orig.TLS != nil {
		h.Set("X-Forwarded-Proto", "https")
	} else {
		h.Set("X-Forwarded-Proto", "http")
	}
}

// rewriteReferer 把代理页面的 Referer 还原为目标站自身的绝对地址，两种形态：
//  1. 路径携带代理前缀：/app/<id>/<scheme>/<host>/…（前缀段切分见 parseAppReferrer，
//     完整目标重建走 decodeTarget）
//  2. hook 还原模式（SPA 客户端路由）：查询串携带 __os_px=<id>~<scheme>~<host>
//     标记（见 parsePxMark），pathname 即目标站路径
//
// 无法识别（非本应用代理页面）返回空串，调用方按不发 Referer 处理。
func rewriteReferer(ref, id string) string {
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	if pid, scheme, host, rest, pok := parseAppReferrer(u); pok && pid == id {
		tu, err := decodeTarget(scheme + "/" + host + "/" + rest)
		if err != nil {
			return ""
		}
		tu.RawQuery = stripPxMark(u.RawQuery)
		return tu.String()
	}
	mid, scheme, host, ok := parsePxMark(u.Query())
	if !ok || mid != id {
		return ""
	}
	vals := u.Query()
	vals.Del("__os_px")
	return (&url.URL{Scheme: scheme, Host: host, Path: u.Path, RawQuery: vals.Encode()}).String()
}

// restorePxParam 把转发上游的 query 中 OAuth 回跳参数
// （redirect_uri/redirectUri/continue/continueUrl）还原为目标站注册的原始绝对
// 地址，两种命中形态：
//  1. 代理路径值（/app/<id>/…）：hook 会把文档地址里该参数改写为代理路径（防 IdP
//     前端解析参数、拼 code 后 location 赋值跳转时跨源逃逸出代理），上游（Casdoor
//     等）校验 redirect_uri 白名单需要注册的原始值；
//  2. 宿主自身绝对地址（http(s)://<宿主>/…）：目标站 JS 读 location.origin 拼接的
//     回跳（代理还原模式下 origin 是 OpenOS 宿主），还原为管理员配置的目标域——
//     微信 jslogin 即此形态，注册成宿主地址的回跳会被服务端风控拒绝（扫码后
//     webwxnewloginpage 返回「暂不支持使用网页版微信」）。
//
// continue/continueUrl 是美团 ebooking 的回跳参数（epassport oauth-login 的
// continue 指向 me.meituan.com 的 callback）：还原缺失时上游拿到宿主地址，302
// Location 被 rewriteLocation 按「宿主地址挂当前请求域」处理——跨主机回跳被挂到
// 表单所在域（epassport）而非 continue 语义域（me.meituan.com），callback 落在
// 错误主机上 404，登录链断（Chrome「表单显示 404」实测）。continue 是通用参数名
// （Google/微软系登录同用），仅对「值是宿主/代理前缀形态」的命中改写，站内相对
// 值不受扰动。
//
// 无命中时 query 原样返回。注意预检不能按明文前缀匹配 query——参数值经
// encodeURIComponent 编码后是 %2Fapp%2F<id>%2F 形态，判断须在解码后进行。
func restorePxParam(id, q string, cfg *proxyConfig, selfHost string) string {
	if q == "" || (!strings.Contains(q, "redirect") && !strings.Contains(q, "continue")) {
		return q
	}
	vals, err := url.ParseQuery(q)
	if err != nil {
		return q
	}
	cfgURL, cfgErr := url.Parse(cfg.URL)
	changed := false
	for _, k := range []string{"redirect_uri", "redirectUri", "continue", "continueUrl"} {
		v := vals.Get(k)
		if v == "" {
			continue
		}
		if strings.HasPrefix(v, "/app/"+id+"/") {
			if tu, err := decodeTarget(strings.TrimPrefix(v, "/app/"+id+"/")); err == nil {
				vals.Set(k, tu.String())
				changed = true
			}
			continue
		}
		if u, err := url.Parse(v); err == nil && u.Host != "" && cfgErr == nil && isSelfHost(u.Host, selfHost) {
			if tu, ok := selfRefTarget(u, id); ok {
				// hook pxQuery 产物：宿主绝对地址 + path 携带 /app/<id>/ 前缀——
				// 按前缀还原出完整目标（比 cfg 域更准，跨主机引用也正确）
				vals.Set(k, tu.String())
				changed = true
				continue
			}
			u.Scheme, u.Host = cfgURL.Scheme, cfgURL.Host
			vals.Set(k, u.String())
			changed = true
		}
	}
	if !changed {
		return q
	}
	return vals.Encode()
}

// selfRefTarget 解析「宿主自身绝对地址且 path 携带本应用代理前缀」的复合形态
//（hook 的 pxQuery 把回跳参数改写为 location.origin+/app/<id>/… 的绝对 URL，
// 防目标站把相对路径形态的回跳值判为非法——美团 gw/login/password 请求体的
// continueUrl 实测 5002）：命中时返回前缀里的完整目标地址；非复合形态返回
// !ok，调用方回落到「挂配置域」的原有语义。
func selfRefTarget(u *url.URL, id string) (*url.URL, bool) {
	rest := strings.TrimPrefix(u.Path, "/app/"+id+"/")
	if len(rest) == len(u.Path) {
		return nil, false
	}
	tu, err := decodeTarget(rest)
	if err != nil {
		return nil, false
	}
	if u.RawQuery != "" {
		tu.RawQuery = u.RawQuery
	}
	return tu, true
}

// applyCfgHeaders 把管理员配置的自定义请求头应用到上游请求（在 sanitize 之后，
// 配置优先级最高）；Cookie 特殊处理为合并——同名时保留浏览器侧（代理路径下）的
// 目标站会话值：目标站经 Set-Cookie 主动轮换过的会话更新鲜，配置里的静态值
// 若排在前会顶掉轮换后的会话（美团 ebooking 等按首现取值的栈会反复掉登录）；
// 目标站从未下发过的名字仍附上配置值兜底（首次注入会话的主用例）。
func applyCfgHeaders(h http.Header, cfg map[string]string) {
	for k, v := range cfg {
		if strings.EqualFold(k, "Cookie") {
			if old := h.Get("Cookie"); old != "" {
				h.Set("Cookie", mergeCookies(v, old))
				continue
			}
		}
		h.Set(k, v)
	}
}

// mergeCookies 合并配置 Cookie 与浏览器侧会话：jar 在前（同名取 jar 值），
// 配置中未被 jar 覆盖的名字追加在后。两侧均按分号拆分并修剪空白。
func mergeCookies(cfg, jar string) string {
	seen := map[string]bool{}
	jarParts := make([]string, 0, 8)
	for _, part := range strings.Split(jar, ";") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		jarParts = append(jarParts, p)
		name := strings.SplitN(p, "=", 2)[0]
		if name != "" {
			seen[name] = true
		}
	}
	for _, part := range strings.Split(cfg, ";") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		name := strings.SplitN(p, "=", 2)[0]
		if name != "" && seen[name] {
			continue
		}
		jarParts = append(jarParts, p)
	}
	return strings.Join(jarParts, "; ")
}

// keepCookies 剥掉 OpenOS 自身会话 Cookie（openos_token 与 openos_* 系列）。
func keepCookies(cookieHeader string) string {
	var kept []string
	for _, part := range strings.Split(cookieHeader, ";") {
		name := strings.TrimSpace(strings.SplitN(part, "=", 2)[0])
		if name == "" || name == "openos_token" || strings.HasPrefix(name, "openos_") {
			continue
		}
		kept = append(kept, strings.TrimSpace(part))
	}
	return strings.Join(kept, "; ")
}
