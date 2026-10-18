// Package webres 统一 /api/* 的 JSON 响应与错误输出。此前 writeJSON/jsonError
// 在 main/auth/ai/vfs 各自复制一份，错误契约随拷贝逐渐分叉（纯文本 vs JSON、
// 文案不一致），收敛到这里单一实现。
package webres

import (
	"encoding/json"
	"net/http"
)

// WriteJSON 写出 JSON 响应（Content-Type 统一 application/json; charset=utf-8）。
func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

// Error 写出 {"error": msg} 形态的错误响应——前端 api/client.ts 按该契约解析
// 错误文案向用户展示（http.Error 纯文本的具体消息到不了用户面前）。
func Error(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
