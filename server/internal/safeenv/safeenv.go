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
// 双写以兼容大小写敏感的工具）。SSL_CERT_*：内网自签 CA 部署需要子进程
//（git/gh 等）信任企业根证书——非秘密，放行。
var allowlist = map[string]bool{
	"PATH": true, "HOME": true, "TERM": true, "LANG": true, "LC_ALL": true,
	"TZ": true, "TMPDIR": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	"http_proxy": true, "https_proxy": true, "all_proxy": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true,
	"NO_PROXY": true, "no_proxy": true,
}

// Env 返回清洗后的子进程环境：仅保留白名单变量，再叠加 overrides
//（overrides 里的同名变量覆盖白名单值）。前缀例外（GODER_GH_ENV_*）见
// EnvPrefixed——按调用方需要单独启用，不进共享白名单（真实终端子进程
// 不应看到透传密钥）。
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

// ghEnvPrefix goder-serve 的 gh 子进程环境透传前缀（goder 侧约定，见
// cmd/goder-serve 的 ghExtraEnvFromEnv）：GODER_GH_ENV_<KEY>=<value> 剥
// 前缀后作为 KEY=<value> 注入每个 gh 子进程，典型用途
// GRF_TURN_WEBHOOK_URL / _SECRET（回合终态事件外发，宿主无头任务由
// 轮询转事件驱动）。
const ghEnvPrefix = "GODER_GH_ENV_"

// GhEnvOverrides 从服务进程环境原样收集 GODER_GH_ENV_* 透传项（前缀
// 保留——goder-serve 侧按同一前缀识别收集，见其 ghExtraEnvFromEnv）。
// 仅供 goder 子进程链使用：值来自部署配置（与白名单同信任级）且不含
// OPENOS_* 秘密；不得并入真实终端子进程的环境。
func GhEnvOverrides() []string {
	var out []string
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(name, ghEnvPrefix) {
			out = append(out, kv)
		}
	}
	return out
}
