// 注入脚本下发：rewriteHTML 把 <script src="/app/<id>/hook.js"> 注入每个代理
// 页面，本文件负责按应用 id 替换占位符后下发（go:embed，改动 hook.js 后必须
// 重新编译服务端）。
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

func serveHook(w http.ResponseWriter, id string) {
	// 全量替换：源文件注释与代码中均出现占位符，只换首个会漏掉 PREFIX 赋值
	body := bytes.ReplaceAll(hookJS, []byte("__PREFIX__"), []byte("/app/"+id+"/"))
	h := w.Header()
	h.Set("Content-Type", "application/javascript; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
