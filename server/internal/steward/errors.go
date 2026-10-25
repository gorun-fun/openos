// errors.go — 引擎对外哨兵错误。
//
// api 层用 errors.Is 分类映射 404/400——此前靠 err.Error() == "连接不存在" 字符串
// 比较（8 处），引擎侧改一个文案 API 层就静默变 500。
package steward

import "errors"

var (
	// ErrConnNotFound 账号连接不存在（含已被删除/未激活等场景的 404 语义）。
	ErrConnNotFound = errors.New("连接不存在")
	// ErrRepoNotFound 已接管仓库不存在。
	ErrRepoNotFound = errors.New("仓库不存在")
	// ErrProjectNameRequired 远程建仓缺项目名。
	ErrProjectNameRequired = errors.New("项目名必填")
)
