// HTML/CSS 地址重写：把目标站文档里的资源地址（相对/根绝对/绝对/跨域）统一映射为
// /app/<id>/<绝对目标地址>，使后续请求继续命中代理路由。采用字节级标签扫描、只替换
// URL 属性值的原样拼接（不重建 DOM），避免整树重排破坏残缺页面；script 原文不动
// （动态行为交给注入的 hook.js），style 块与 style 属性做 CSS url()/@import 重写。
package webproxy

import (
	"bytes"
	"html"
	"net/url"
	"strings"
)

// urlAttrMode 属性改写模式；不在表内的属性原样保留。
var urlAttrMode = map[string]string{
	"href": "url", "src": "url", "action": "url", "formaction": "url",
	"poster": "url", "background": "url", "cite": "url", "longdesc": "url",
	"data": "url", "manifest": "url", "xlink:href": "url",
	"srcset": "srcset", "style": "css",
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' }

func indexCloseTag(body []byte, from int, name string) int {
	pat := []byte("</" + name)
	lowered := bytes.ToLower(body)
	for i := from; ; {
		j := bytes.Index(lowered[i:], pat)
		if j < 0 {
			return -1
		}
		j += i
		if e := bytes.IndexByte(body[j:], '>'); e >= 0 {
			return j + e + 1
		}
		return -1
	}
}

// rewriteHTML 重写 HTML 文档：改写 URL 属性、重写 style、注入 hook 脚本（紧跟
// <head> 之后，早于页面脚本执行；无 head 的残缺文档前置注入）。base 是本文档的
// 目标绝对地址（相对地址解析基准）。
func rewriteHTML(body []byte, prefix string, base *url.URL) []byte {
	var out bytes.Buffer
	injected := false
	i, n := 0, len(body)
	for i < n {
		j := bytes.IndexByte(body[i:], '<')
		if j < 0 {
			out.Write(body[i:])
			break
		}
		j += i
		out.Write(body[i:j])
		rest := body[j:]
		switch {
		case bytes.HasPrefix(rest, []byte("<!--")):
			end := bytes.Index(rest[4:], []byte("-->"))
			if end < 0 {
				out.Write(rest)
				i = n
				continue
			}
			e := 4 + end + 3
			out.Write(rest[:e])
			i = j + e
		case bytes.HasPrefix(rest, []byte("<!")) || bytes.HasPrefix(rest, []byte("<?")):
			end := bytes.IndexByte(rest, '>')
			if end < 0 {
				out.Write(rest)
				i = n
				continue
			}
			out.Write(rest[:end+1])
			i = j + end + 1
		case bytes.HasPrefix(rest, []byte("</")):
			end := bytes.IndexByte(rest, '>')
			if end < 0 {
				out.Write(rest)
				i = n
				continue
			}
			out.Write(rest[:end+1])
			i = j + end + 1
		default:
			// 先写进临时缓冲再决定是否输出：meta CSP 标签整枚丢弃（见 isCSPMeta）
			var tmp bytes.Buffer
			name, tagEnd, ok := writeTag(&tmp, rest, prefix, base)
			if !ok {
				out.Write(rest)
				i = n
				continue
			}
			if !(name == "meta" && isCSPMeta(tmp.Bytes())) {
				out.Write(tmp.Bytes())
			}
			i = j + tagEnd
			if name == "head" && !injected {
				out.WriteString(`<script src="` + prefix + `hook.js"></script>`)
				injected = true
			}
			if name == "script" {
				if k := indexCloseTag(body, i, "script"); k >= 0 {
					out.Write(body[i:k])
					i = k
				} else {
					out.Write(body[i:])
					i = n
				}
			} else if name == "style" {
				end := n
				if k := indexCloseTag(body, i, "style"); k >= 0 {
					end = k
				}
				out.Write(rewriteCSS(body[i:end], prefix, base))
				i = end
			}
		}
	}
	res := out.Bytes()
	if !injected {
		res = append([]byte(`<script src="`+prefix+`hook.js"></script>`), res...)
	}
	return res
}

// writeTag 解析并写出一个开始标签（含改写后的属性值），返回（小写标签名、
// 标签结束位置、是否完整）。残缺标签按原文写出、整体消费。
func writeTag(out *bytes.Buffer, rest []byte, prefix string, base *url.URL) (string, int, bool) {
	n := len(rest)
	p := 1
	for p < n && !isSpace(rest[p]) && rest[p] != '>' && rest[p] != '/' {
		p++
	}
	name := strings.ToLower(string(rest[1:p]))
	out.Write(rest[:p])
	for p < n {
		for p < n && isSpace(rest[p]) {
			out.WriteByte(rest[p])
			p++
		}
		if p >= n {
			return name, n, false
		}
		if rest[p] == '>' {
			out.WriteByte('>')
			return name, p + 1, true
		}
		if rest[p] == '/' { // 自闭合斜杠
			out.WriteByte('/')
			p++
			continue
		}
		ns := p
		for p < n && !isSpace(rest[p]) && rest[p] != '=' && rest[p] != '>' && rest[p] != '/' {
			p++
		}
		attr := strings.ToLower(string(rest[ns:p]))
		if p < n && rest[p] == '=' {
			out.Write(rest[ns : p+1])
			p++
			if p < n && (rest[p] == '"' || rest[p] == '\'') {
				q := rest[p]
				p++
				vs := p
				for p < n && rest[p] != q {
					p++
				}
				if p >= n { // 引号未闭合：残缺标签整体按原文收尾
					out.Write(rest[ns:])
					return name, n, false
				}
				out.WriteByte(q)
				out.Write(rewriteAttrVal(name, attr, rest[vs:p], prefix, base))
				out.WriteByte(q)
				p++
			} else {
				vs := p
				for p < n && !isSpace(rest[p]) && rest[p] != '>' {
					p++
				}
				out.Write(rewriteAttrVal(name, attr, rest[vs:p], prefix, base))
			}
		} else {
			out.Write(rest[ns:p]) // 布尔属性
		}
	}
	return name, n, false
}

// isCSPMeta 判断（已由 writeTag 输出的）meta 标签是否为
// <meta http-equiv="Content-Security-Policy[-Report-Only]">。CSP 的 meta 形态与
// 响应头形态同样拒绝代理嵌入：代理页面的脚本/样式源是 OpenOS 宿主源，目标站配置
// 的源白名单（*.目标域）会把重写后的全部子资源拦成 status 0（美团 ebooking 工作台
// 实测：meta 排在首个外部 script 之前，其后脚本全灭、SPA 白屏）。响应头 CSP 在
// copyRespHeaders 剥除，meta 形态在此剥除，二者对齐。
func isCSPMeta(tag []byte) bool {
	lower := bytes.ToLower(tag)
	hv := bytes.Index(lower, []byte("http-equiv"))
	if hv < 0 {
		return false
	}
	rest := lower[hv+len("http-equiv"):]
	i := 0
	for i < len(rest) && isSpace(rest[i]) {
		i++
	}
	if i >= len(rest) || rest[i] != '=' {
		return false
	}
	i++
	for i < len(rest) && isSpace(rest[i]) {
		i++
	}
	if i >= len(rest) {
		return false
	}
	var v []byte
	if rest[i] == '"' || rest[i] == '\'' {
		q := rest[i]
		i++
		j := bytes.IndexByte(rest[i:], q)
		if j < 0 {
			return false
		}
		v = rest[i : i+j]
	} else {
		j := i
		for j < len(rest) && !isSpace(rest[j]) && rest[j] != '>' {
			j++
		}
		v = rest[i:j]
	}
	v = bytes.TrimSpace(v)
	return bytes.Equal(v, []byte("content-security-policy")) ||
		bytes.Equal(v, []byte("content-security-policy-report-only"))
}

func rewriteAttrVal(tag, attr string, val []byte, prefix string, base *url.URL) []byte {
	if len(val) == 0 {
		return val
	}
	switch urlAttrMode[attr] {
	case "url":
		return rewriteURLVal(val, prefix, base)
	case "srcset":
		return rewriteSrcset(val, prefix, base)
	case "css":
		return rewriteCSS(val, prefix, base)
	}
	// <meta http-equiv=refresh content="N; url=X">：按值形态嗅探（不必预扫 http-equiv）
	if tag == "meta" && attr == "content" {
		s := html.UnescapeString(string(val))
		if i := strings.Index(strings.ToLower(s), "url="); i >= 0 {
			head, tail := s[:i+4], strings.TrimSpace(s[i+4:])
			if tail != "" && (tail[0] == '"' || tail[0] == '\'') {
				q := tail[0]
				inner := strings.Trim(tail, string(q))
				return []byte(head + string(q) + string(rewriteURLVal([]byte(inner), prefix, base)) + string(q))
			}
			return []byte(head + string(rewriteURLVal([]byte(tail), prefix, base)))
		}
	}
	return val
}

// rewriteURLVal 单个地址改写：跳过锚点与非 http(s) 方案，其余按 base 解析为绝对
// 目标地址后映射为紧凑代理路径（<prefix><scheme>/<host>/<路径>，无 // 段）。
// 无法解析时保留原值（含引号内空白）。
func rewriteURLVal(val []byte, prefix string, base *url.URL) []byte {
	s := strings.TrimSpace(html.UnescapeString(string(val)))
	if s == "" || s[0] == '#' {
		return val
	}
	lu := strings.ToLower(s)
	for _, p := range []string{"javascript:", "data:", "mailto:", "tel:", "about:", "blob:", "file:", "chrome:", "ws:", "wss:", "#"} {
		if strings.HasPrefix(lu, p) {
			return val
		}
	}
	u, err := base.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return val
	}
	u.Fragment = ""
	u.User = nil // 极罕见的 userinfo 直接丢弃，避免污染紧凑段
	return []byte(escapeAttr(prefix + compactTarget(u)))
}

func rewriteSrcset(val []byte, prefix string, base *url.URL) []byte {
	parts := strings.Split(string(val), ",")
	for i, part := range parts {
		t := strings.TrimSpace(part)
		if t == "" {
			continue
		}
		if k := strings.IndexAny(t, " \t"); k >= 0 {
			parts[i] = string(rewriteURLVal([]byte(t[:k]), prefix, base)) + t[k:]
		} else {
			parts[i] = string(rewriteURLVal([]byte(t), prefix, base))
		}
	}
	return []byte(strings.Join(parts, ", "))
}

// escapeAttr 属性值上下文的实体转义（地址中的 & 与引号）。
func escapeAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}

// ---- CSS ----

// rewriteCSS 重写样式表里的 url(...) 与 @import "..." 地址（base 为该样式资源
// 自身的目标绝对地址）。
func rewriteCSS(css []byte, prefix string, base *url.URL) []byte {
	var out bytes.Buffer
	lowered := bytes.ToLower(css)
	i, n := 0, len(css)
	for i < n {
		uIdx := bytes.Index(lowered[i:], []byte("url("))
		iIdx := bytes.Index(lowered[i:], []byte("@import"))
		if uIdx < 0 && iIdx < 0 {
			out.Write(css[i:])
			break
		}
		k := uIdx
		if k < 0 || (iIdx >= 0 && iIdx < uIdx) {
			k = iIdx
		}
		k += i
		out.Write(css[i:k])
		if bytes.HasPrefix(lowered[k:], []byte("@import")) {
			// @import 后若是 url(...) 形态，交给下一轮 url( 扫描处理；这里只处理裸字符串
			q := k + len("@import")
			for q < n && isSpace(css[q]) {
				q++
			}
			if q < n && (css[q] == '"' || css[q] == '\'') {
				quote := css[q]
				qe := q + 1
				for qe < n && css[qe] != quote {
					qe++
				}
				if qe < n {
					out.Write(css[k : q+1])
					out.Write(rewriteURLVal(css[q+1:qe], prefix, base))
					out.WriteByte(quote)
					i = qe + 1
					continue
				}
			}
			out.Write(css[k : k+len("@import")])
			i = k + len("@import")
			continue
		}
		// url( 形态
		vs := k + len("url(")
		for vs < n && isSpace(css[vs]) {
			vs++
		}
		if vs >= n {
			out.Write(css[k:])
			break
		}
		if css[vs] == '"' || css[vs] == '\'' {
			quote := css[vs]
			ve := vs + 1
			for ve < n && css[ve] != quote {
				ve++
			}
			if ve >= n {
				out.Write(css[k:])
				break
			}
			out.WriteString("url(")
			out.WriteByte(quote)
			out.Write(rewriteURLVal(css[vs+1:ve], prefix, base))
			out.WriteByte(quote)
			out.WriteByte(')')
			i = ve + 1
			for i < n && isSpace(css[i]) {
				i++
			}
			if i < n && css[i] == ')' {
				i++
			}
			continue
		}
		ve := vs
		for ve < n && css[ve] != ')' && !isSpace(css[ve]) {
			ve++
		}
		out.WriteString("url(")
		out.Write(rewriteURLVal(css[vs:ve], prefix, base))
		out.WriteByte(')')
		i = ve
		for i < n && isSpace(css[i]) {
			i++
		}
		if i < n && css[i] == ')' {
			i++
		}
	}
	return out.Bytes()
}
