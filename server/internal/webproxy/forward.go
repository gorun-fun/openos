// HTTP 转发主链路：清洗请求 → 拨号上游 → 头改写 → 文本响应读入（限长/gzip 解压）
// → HTML/CSS 地址重写 → 回写。非文本与不可重写的响应流式透传。
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
)

func (p *Proxy) serveHTTPProxy(w http.ResponseWriter, r *http.Request, id string, tu *url.URL, cfg *proxyConfig) {
	cfgHost := cfgURLHost(cfg.URL)
	outReq := r.Clone(context.WithValue(r.Context(), cfgHostCtx{}, cfgHost))
	outReq.URL = tu
	outReq.Host = tu.Host
	outReq.RequestURI = "" // client 发出的请求必须清空
	sanitizeOutRequest(outReq, r, id, cfg)
	// 配置头在剥除 OpenOS 会话/逐跳头之后应用：优先级最高（可覆盖 X-Forwarded-*），
	// 管理员显式写死的目标站凭据（如 Authorization）是本功能的主要用例
	applyCfgHeaders(outReq.Header, cfg.Headers)

	resp, err := p.client.Do(outReq)
	if err != nil {
		http.Error(w, "代理请求失败："+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	secure := r.TLS != nil
	copyRespHeaders(w, resp, "/app/"+id+"/", tu, secure, r.Host)

	ctype := strings.ToLower(resp.Header.Get("Content-Type"))
	isHTML := strings.Contains(ctype, "text/html") || strings.Contains(ctype, "application/xhtml")
	isCSS := !isHTML && strings.Contains(ctype, "text/css")
	if !isHTML && !isCSS {
		w.WriteHeader(resp.StatusCode)
		streamCopy(w, resp.Body)
		return
	}

	enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	body, rewritable, err := readRewritable(resp, enc)
	if err != nil {
		http.Error(w, "读取上游响应失败", http.StatusBadGateway)
		return
	}
	if !rewritable {
		// 超限或无法解压的编码：原文透传（已读入前段 + 剩余流），不重写不注入。
		// 不设 Content-Length（改写不了长度，截断回写会让浏览器拿到残缺文档）
		w.WriteHeader(resp.StatusCode)
		streamCopy(w, io.MultiReader(bytes.NewReader(body), resp.Body))
		return
	}
	if enc == "gzip" {
		w.Header().Del("Content-Encoding")
	}
	if isHTML {
		body = rewriteHTML(body, "/app/"+id+"/", tu)
	} else {
		body = rewriteCSS(body, "/app/"+id+"/", tu)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
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
