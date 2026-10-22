// 统一转发引擎：应用反代（/app/<id>/…，HTML/CSS 重写 + hook 注入）与通用转发
// （/px/<scheme>/<host>/…，纯透传）两种形态共用同一条链路——请求清洗、拨号、
// 响应头改写、站点适配器钩子完全一致，仅 rewrite（地址重写与 hook 注入）按模式
// 开关。这是「统一代理」的核心：两处入口不会出现清洗规则分叉。
package webproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// forwardOpts 一次转发的模式参数。
type forwardOpts struct {
	// prefix 响应改写的代理路径前缀（app 模式 /app/<id>/，通用模式 /px/）。
	// Set-Cookie 收归、Location/Refresh 前缀化都以它为基准。
	prefix string
	// appID 应用模式的应用 id（通用模式为空串）：OAuth 回跳参数还原与
	// Referer 还原按 id 匹配代理前缀，空串时这两类还原自然不命中。
	appID string
	// cfg 应用配置（通用模式为 nil：无管理员自定义头、无配置目标主机放行）。
	cfg *proxyConfig
	// rewrite 是否做 HTML/CSS 地址重写并注入 hook（应用模式 true；通用转发
	// 面向 API 请求，重写与注入既无必要也避免被当页面嵌入使用）。
	rewrite bool
}

// serveForward 统一转发主链路：清洗请求 → 拨号上游 → 头改写 →（模式开启时）
// 文本响应读入重写 → 回写；非文本与不可重写响应流式透传。
// 幂等 GET 的传输层失败（拨号错误/读体中途连接重置）在写响应头前单次透明重试
// （见 serveForwardOnce 的返回值语义）：长连接池空闲超时后首个请求常撞死连接
// （H2 GOAWAY 未及感知/RST），上游 ~200ms 返回连接错误 → 502，页面侧无重试即
// 整轮启动失败（线上实证：每轮子页启动首请求 menus/chunk-vendors 恒 502、同一
// URL 立即重取恒 200——新连接即好）。已开始写响应（流式透传段）不可重试。
func (p *Proxy) serveForward(w http.ResponseWriter, r *http.Request, tu *url.URL, opts forwardOpts) {
	for try := 0; ; try++ {
		if !p.serveForwardOnce(w, r, tu, opts) && try < 2 {
			// 重试前清空首次尝试积累的未发送响应头（copyRespHeaders 全 Add 语义，
			// 不清会双份 Set-Cookie 等）。带退避：首请求集中失败实证为冷 DNS 并发
			// 解析雪崩（容器初起/空闲后首轮突发，瞬时再试仍在冷窗内），150/250ms
			// 让解析落地。
			time.Sleep(time.Duration(100+try*100) * time.Millisecond)
			h := w.Header()
			for k := range h {
				delete(h, k)
			}
			continue
		}
		return
	}
}

// serveForwardOnce 单次转发尝试；返回 false 表示幂等 GET 在写响应头前遭遇传输
// 错误、可整段安全重试（此时未向 w 写入任何字节）。
func (p *Proxy) serveForwardOnce(w http.ResponseWriter, r *http.Request, tu *url.URL, opts forwardOpts) (retryable bool) {
	var cfgURL string
	if opts.cfg != nil {
		cfgURL = opts.cfg.URL
	}
	outReq := r.Clone(context.WithValue(r.Context(), cfgHostCtx{}, cfgURLHost(cfgURL)))
	outReq.URL = tu
	outReq.Host = tu.Host
	outReq.RequestURI = "" // client 发出的请求必须清空
	sanitizeOutRequest(outReq, r, opts.appID, opts.cfg, tu)
	// 配置头在剥除 OpenOS 会话/逐跳头之后应用：优先级最高（可覆盖 X-Forwarded-*），
	// 管理员显式写死的目标站凭据（如 Authorization）是本功能的主要用例
	if opts.cfg != nil {
		applyCfgHeaders(outReq.Header, opts.cfg.Headers)
	}
	// 站点适配器（按目标主机命中的特殊处理，见 adapter.go）：请求侧钩子在
	// 通用清洗与配置头之后叠加，站点规则可以对最终请求头做最后修正；
	// 本请求命中的适配器一次算出（下方请求/响应/JS 改写多处消费）
	adapters := matchAdapters(tu.Host)
	for _, a := range adapters {
		if a.OnRequest != nil {
			a.OnRequest(outReq, r)
		}
	}
	// RewriteJS 适配器命中时剥条件请求头：JS 改写会变更响应体，上游 ETag/
	// Last-Modified 对改写后的体不再成立——放行条件请求会拿到 304 透传，
	// 浏览器继续用改写前的缓存体（QA 域字面量永远清不掉），必须强制全量取回
	if opts.rewrite {
		for _, a := range adapters {
			if a.RewriteJS != nil {
				outReq.Header.Del("If-None-Match")
				outReq.Header.Del("If-Modified-Since")
				break
			}
		}
	}

	resp, err := p.client.Do(outReq)
	if err != nil {
		// 上下文已取消（页面导航走了）不重试；非幂等 GET 不重试
		if r.Method == http.MethodGet && r.Context().Err() == nil && outReq.Body == nil {
			return false
		}
		http.Error(w, "代理请求失败："+err.Error(), http.StatusBadGateway)
		return true
	}
	defer resp.Body.Close()

	secure := r.TLS != nil
	copyRespHeaders(w, resp, opts.prefix, tu, secure, r.Host, p.ck, opts.appID)
	// 响应侧适配器钩子：对最终下发给浏览器的响应头做站点修正
	for _, a := range adapters {
		if a.OnResponse != nil {
			a.OnResponse(w.Header())
		}
	}

	ctype := strings.ToLower(resp.Header.Get("Content-Type"))
	isHTML := strings.Contains(ctype, "text/html") || strings.Contains(ctype, "application/xhtml")
	isCSS := !isHTML && strings.Contains(ctype, "text/css")
	// 站点适配器的 JS 定点改写（如网易云音乐登录模块按 location.host 落 QA 环境
	// 的 base 改写）：与 HTML/CSS 重写同一套读入/解压/限长语义
	var jsRewrite func([]byte) []byte
	if opts.rewrite && !isHTML {
		for _, a := range adapters {
			if a.RewriteJS != nil {
				fn, prefix := a.RewriteJS, opts.prefix
				jsRewrite = func(b []byte) []byte { return fn(b, prefix) }
				break
			}
		}
	}
	isJS := !isHTML && !isCSS && jsRewrite != nil &&
		(strings.Contains(ctype, "javascript") || strings.Contains(ctype, "ecmascript"))
	if !opts.rewrite || (!isHTML && !isCSS && !isJS) {
		w.WriteHeader(resp.StatusCode)
		streamCopy(w, resp.Body)
		return true
	}

	enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	body, rewritable, err := readRewritable(resp, enc)
	if err != nil {
		// 读体中途连接重置：GET 未写出任何字节，可整段重试一次
		if r.Method == http.MethodGet && r.Context().Err() == nil {
			return false
		}
		http.Error(w, "读取上游响应失败", http.StatusBadGateway)
		return true
	}
	if !rewritable {
		// 超限或无法解压的编码：原文透传（已读入前段 + 剩余流），不重写不注入。
		// 不设 Content-Length（改写不了长度，截断回写会让浏览器拿到残缺文档）
		w.WriteHeader(resp.StatusCode)
		streamCopy(w, io.MultiReader(bytes.NewReader(body), resp.Body))
		return true
	}
	if enc == "gzip" {
		w.Header().Del("Content-Encoding")
	}
	if isHTML {
		body = rewriteHTML(body, opts.prefix, tu)
	} else if isCSS {
		body = rewriteCSS(body, opts.prefix, tu)
	} else {
		body = jsRewrite(body)
	}
	// 改写后的体与上游校验器不再匹配：剥掉 ETag/Last-Modified 并要求再验证，
	// 防止浏览器用旧缓存的原始体（304 协商命中的体是改写前的）
	wh := w.Header()
	wh.Del("ETag")
	wh.Del("Last-Modified")
	wh.Del("Expires")
	wh.Set("Cache-Control", "no-cache")
	wh.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
	return true
}

// serveHTTPProxy 应用模式入口（/app/<id>/）：HTML/CSS 重写 + hook 注入。
// 保留为独立入口便于与通用转发模式对照及单测直调。
func (p *Proxy) serveHTTPProxy(w http.ResponseWriter, r *http.Request, id string, tu *url.URL, cfg *proxyConfig) {
	p.serveForward(w, r, tu, forwardOpts{
		prefix:  "/app/" + id + "/",
		appID:   id,
		cfg:     cfg,
		rewrite: true,
	})
}

// readRewritable 读入需要地址重写的文本响应体（限长 maxRewriteBody）并按需解压
// gzip。返回值：
//   - ok=true：限内的明文体，调用方可安全重写（gzip 已解压，调用方须删
//     Content-Encoding 头）
//   - ok=false：不可重写（超限 / gzip 流损坏 / 无法解压的压缩编码），body 为
//     已读入的原文前段，调用方应连同剩余流原文透传
//   - err：上游读取失败
//
// 解压后同样限长：压缩态限长挡不住解压放大（gzip 压缩炸弹），内存上界必须是
// 解压后的字节数而非压缩字节数。
func readRewritable(resp *http.Response, enc string) (body []byte, ok bool, err error) {
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxRewriteBody+1))
	if err != nil {
		return nil, false, err
	}
	if len(body) > maxRewriteBody {
		return body, false, nil
	}
	switch enc {
	case "", "identity":
		return body, true, nil
	case "gzip":
		zr, zerr := gzip.NewReader(bytes.NewReader(body))
		if zerr != nil {
			return body, false, nil // 损坏的 gzip 流：原文透传，改写只会破坏
		}
		ub, uerr := io.ReadAll(io.LimitReader(zr, maxRewriteBody+1))
		if uerr != nil || len(ub) > maxRewriteBody {
			return body, false, nil // 解压超限（炸弹/超大文档）：按压缩态透传
		}
		return ub, true, nil
	default:
		// 其余压缩编码（br/zstd 等）纯 Go 侧无法解压：原文透传
		return body, false, nil
	}
}

// streamCopy 流式透传并逐块冲刷（SSE/大文件/视频等需要即时下发）。
func streamCopy(w http.ResponseWriter, src io.Reader) {
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if rerr != nil {
			return
		}
	}
}
