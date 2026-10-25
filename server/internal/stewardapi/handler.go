// Package stewardapi — 「代码管家」App 的 HTTP 面（自 grf-os 宿主上游化，
// v0.0.5x 起）：管理面 /api/steward/*（登录 + admin），对外上报入口
// /api/steward/report 公开注册、X-Report-Key 共享密钥自鉴权。引擎本体在
// internal/steward（后台 worker + 双平台 issue 同步 + AI 转制），流水线工作面
// /api/steward/pipe/* 亦在此（GitLab 客户端见 pipeline.go）。此处只做薄封装。
package stewardapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"openos/server/internal/auth"
	"openos/server/internal/steward"
	"openos/server/internal/webres"
)

// Handler 代码管家 REST 面。
type Handler struct {
	Eng       *steward.Engine // 引擎（nil = 停用：管理面报「未启用」）
	GL        *GitLabClient   // GitLab 流水线客户端（活动连接优先，env 兜底）
	GitLabURL string          // 连接表单的缺省实例地址
}

// apiErr 带状态码的业务错误（handler 返回 error、wrap 统一应答——自 grf-os
// 宿主同构迁移，错误文案与码位不变）。
type apiErr struct {
	code int
	msg  string
}

func (e *apiErr) Error() string { return e.msg }

func errBad(msg string) error       { return &apiErr{http.StatusBadRequest, msg} }
func errNotFound(msg string) error  { return &apiErr{http.StatusNotFound, msg} }
func errUpstream(msg string) error  { return &apiErr{http.StatusBadGateway, msg} }
func errUnauthenticated(msg string) error { return &apiErr{http.StatusUnauthorized, msg} }
func errInternal(msg string) error  { return &apiErr{http.StatusInternalServerError, msg} }

// wrap handler 包装：返回的 apiErr 按码应答（{"error"} 契约，webres），其余
// 错误 500。
func wrap(fn func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			var ae *apiErr
			if errors.As(err, &ae) {
				webres.Error(w, ae.code, ae.msg)
				return
			}
			webres.Error(w, http.StatusInternalServerError, err.Error())
		}
	}
}

func (h *Handler) ok(w http.ResponseWriter, v any) { webres.WriteJSON(w, v) }

// readJSON 解析 JSON 请求体（上限 1MB，空体容忍为零值——confirm 等可选体端点）。
func readJSON[T any](r *http.Request) (T, error) {
	var v T
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return v, errBad("读取请求体失败: " + err.Error())
	}
	if len(raw) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, errBad("请求体解析失败: " + err.Error())
	}
	return v, nil
}

// stewardID 当前请求身份投影（grf-os 宿主 MustClaims 的等价物：Name 用于
// 确认人显示名、UID 用于 goder 会话归属——openos 单一用户名即二者）。
func stewardID(r *http.Request) (id struct{ Name, UID string }) {
	if v := auth.FromContext(r); v != nil {
		id.Name, id.UID = v.Username, v.Username
	}
	return id
}

// requireSteward 引擎可用性守卫。
func (h *Handler) requireSteward() (*steward.Engine, error) {
	if h.Eng == nil {
		return nil, errBad("代码管家引擎未启用（OPENOS_STEWARD=off 或初始化失败，见服务端日志）")
	}
	return h.Eng, nil
}
