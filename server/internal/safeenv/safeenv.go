// Package safeenv 提供子进程环境白名单清洗。
//
// 背景（安全审计 2026-10）：真实终端与 goder 子进程原先继承服务进程的全部
// 环境变量（os.Environ），任意登录用户可在终端里 `printenv OPENOS_EXCHANGE_SECRET`
// 或读 OPENOS_AI_API_KEY 自我提权。本包把子进程可见环境收敛为白名单：
// 白名单之外的一切（含全部 OPENOS_* 秘密）一律丢弃——需要透传的新变量必须
// 显式加入 allowlist 或经 overrides 传入。
package safeenv

import (
	"os"
	"strings"
)

// allowlist 是子进程允许继承的环境变量名（大小写不敏感；代理变量按原大小写
// 双写以兼容大小写敏感的工具）。
var allowlist = map[string]bool{
	"PATH": true, "HOME": true, "TERM": true, "LANG": true, "LC_ALL": true,
	"TZ": true, "TMPDIR": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"http_proxy": true, "https_proxy": true, "all_proxy": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true,
	"NO_PROXY": true, "no_proxy": true,
}

// Env 返回清洗后的子进程环境：仅保留白名单变量，再叠加 overrides
//（overrides 里的同名变量覆盖白名单值）。
func Env(overrides ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "" {
			continue
		}
		if allowlist[name] || allowlist[strings.ToUpper(name)] {
			out = append(out, kv)
		}
	}
	return append(out, overrides...)
}
