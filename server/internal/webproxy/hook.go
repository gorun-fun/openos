// 注入脚本下发：rewriteHTML 把 <script src="/app/<id>/hook.js"> 注入每个代理
// 页面，本文件按应用 id 替换占位符后下发（go:embed，改动 hook.js 后必须
// 重新编译服务端）。请求的 Referer 携带页面地址（代理前缀或还原标记形态），
// 据此解析目标主机并追加命中的站点适配器 Hook 片段（见 adapter.go）；再追加
// 应用级 config.hook 脚本（宿主平台的业务接管管道，见 config.go）——无 Referer
// / 解析失败时只下发通用 hook 与应用级脚本。
package webproxy

import (
	"bytes"
	_ "embed"
	"net/http"
	"strconv"
)

//go:embed hook.js
var hookJS []byte

const (
	// maxRewriteBody 需要重写（HTML/CSS）的响应体上限，超出按原文透传
	maxRewriteBody = 16 << 20
	hookPath       = "hook.js"
)

func serveHook(w http.ResponseWriter, r *http.Request, id string, cfg *proxyConfig) {
	// 全量替换：源文件注释与代码中均出现占位符，只换首个会漏掉 PREFIX 赋值
	body := bytes.ReplaceAll(hookJS, []byte("__PREFIX__"), []byte("/app/"+id+"/"))
	if host, ok := refererTargetHost(r.Referer()); ok {
		for _, a := range matchAdapters(host) {
			if a.Hook != "" {
				body = append(body, '\n')
				body = append(body, bytes.ReplaceAll([]byte(a.Hook), []byte("__PREFIX__"), []byte("/app/"+id+"/"))...)
			}
		}
	}
	// 应用级脚本（config.hook）最后追加：通用 hook 与适配器先就位，业务脚本
	// 可依赖已包装的 fetch/XHR 等入口做接口判据
	if cfg != nil && cfg.Hook != "" {
		body = append(body, '\n')
		body = append(body, bytes.ReplaceAll([]byte(cfg.Hook), []byte("__PREFIX__"), []byte("/app/"+id+"/"))...)
	}
	h := w.Header()
	h.Set("Content-Type", "application/javascript; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
