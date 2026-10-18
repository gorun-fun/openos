// index.go — goder.index 分组镜像的编排写入（hosttasks 无头任务的归组落点）。
//
// 背景：goder 的分组索引（项目 → 会话）在前端是本地态，OpenOS 嵌入态经
// GoderStudio.vue 的 setPersistence 适配器镜像到 user_settings 的 goder.index
// 键（load 服务端优先、save 防抖回写）。本文件的读写格式与前端
// GoderStudio.vue 的 persistence 格式严格耦合——字段名与 goder.js
// persistence.ts 的 ProjectsIndex 一致，未知键（automation 等）原样保真；
// 改动任一字段/结构须两侧同步，否则前端 save 的合并逻辑（防陈旧浏览器副本
// 冲掉代建条目）无法识别代建会话，会被冲回未分组。
//
// hosttasks.go 在会话创建成功后调用 indexSession（best-effort：失败不影响
// 任务执行，会话回落「未分组」仍可见）。
package goder

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// goder.index 镜像键与自动修复分组（字段名与 goder.js persistence.ts 的
// ProjectsIndex 严格一致，未知键（automation 等）原样保真）。
const (
	hostIndexKey     = "goder.index"
	fixerProjectID   = "fixer"
	fixerGroupName   = "自动修复"
	defaultFixModels = 200
)

// indexSession 把会话写进用户的 goder.index（确保分组项目存在）。整体
// map[string]json.RawMessage 保真未知键（automation / lastActiveSessionId 等）。
func (h *HostTasks) indexSession(uid, sid, title, group, model, respID string) error {
	raw, err := h.db.GetUserSetting(uid, hostIndexKey)
	if err != nil {
		return err
	}
	all := map[string]json.RawMessage{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &all); err != nil {
			// 损坏镜像不整体覆盖（用户其余分组宝贵）：跳过归组，会话回落未分组
			return fmt.Errorf("goder.index 损坏，跳过归组: %w", err)
		}
	}
	var idx struct {
		Projects []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			CreatedAt string `json:"createdAt"`
		} `json:"projects"`
		Sessions map[string]json.RawMessage `json:"sessions"`
	}
	_ = json.Unmarshal(all["projects"], &idx.Projects)
	if idx.Sessions == nil {
		if err := json.Unmarshal(all["sessions"], &idx.Sessions); err != nil {
			idx.Sessions = map[string]json.RawMessage{}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	found := false
	for _, p := range idx.Projects {
		if p.ID == fixerProjectID {
			found = true
			break
		}
	}
	if !found {
		idx.Projects = append(idx.Projects, struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			CreatedAt string `json:"createdAt"`
		}{ID: fixerProjectID, Name: group, CreatedAt: now})
	}
	rec, err := json.Marshal(map[string]any{
		"projectId":      fixerProjectID,
		"title":          title,
		"createdAt":      now,
		"lastResponseId": respID,
		"model":          model,
	})
	if err != nil {
		return err
	}
	if idx.Sessions == nil {
		idx.Sessions = map[string]json.RawMessage{}
	}
	idx.Sessions[sid] = rec

	projRaw, err := json.Marshal(idx.Projects)
	if err != nil {
		return err
	}
	sessRaw, err := json.Marshal(idx.Sessions)
	if err != nil {
		return err
	}
	if _, ok := all["version"]; !ok {
		all["version"] = json.RawMessage("1")
	}
	all["projects"] = projRaw
	all["sessions"] = sessRaw
	out, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return h.db.SetUserSetting(uid, hostIndexKey, string(out))
}

// deriveHostTitle 任务首行截断作会话标题（与 goder.js deriveTitle 的 42 字口径接近）。
func deriveHostTitle(task string) string {
	line := task
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	r := []rune(line)
	if len(r) > 42 {
		r = r[:42]
	}
	return string(r)
}
