// index.go — 无头任务会话归组的落点（经 goder-serve 管理面）。
//
// 会话分组索引（项目 → 会话）的权威存储在 goder-serve（每用户
// platform/index.json）：POST /admin/users/{uid}/index/sessions 做保真追加
//（未知键原样保留，读改写在服务端一把锁内完成，消掉浏览器整存与代建追加
// 并发时的丢更新窗口）；浏览器侧 GoderStudio 的 setPersistence 适配器
// load/save 走 /goder-api/index（见 GoderStudio.vue）。历史实现里 openos
// 把镜像写进 user_settings 的 goder.index 键、前端 save 靠读改写合并防冲的
// 三方格式耦合（本文件 ↔ GoderStudio ↔ goder.js persistence.ts）由此收敛：
// 字段形状由 goder-serve 独家维护（ProjectsIndex），openos 只传业务参数。
//
// hosttasks.go 在会话创建成功后调用 indexSession（best-effort：失败不影响
// 任务执行，会话回落「未分组」仍可见）。
package goder

import (
	"context"
	"strings"
)

// 归组默认值（业务语义：grf-os 自动修复的稳定分组；宿主可经请求字段覆盖）。
const (
	fixerProjectID   = "fixer"
	fixerGroupName   = "自动修复"
	defaultFixSteps  = 200
)

// indexSession 把会话追加进用户的 goder 会话索引（确保分组项目存在；
// projectId 与 projectName 由调用方给出，稳定性与展示名解耦——同一业务
// 复用稳定 id，改名不重建分组）。
func (h *HostTasks) indexSession(ctx context.Context, uid, sid, projectID, groupName, title, model, respID string) error {
	return h.m.adminAppendIndexSession(ctx, uid, map[string]any{
		"sid":              sid,
		"project_id":       projectID,
		"project_name":     groupName,
		"title":            title,
		"model":            model,
		"last_response_id": respID,
	})
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
