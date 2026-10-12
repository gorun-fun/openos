// safeenv_test.go — 白名单与 GODER_GH_ENV_* 透传通道的边界守护:
// 透传项只经 GhEnvOverrides 显式进入 goder 子进程链(前缀原样保留,
// goder-serve 侧按前缀识别),不进共享 Env()——真实终端子进程不应看到
// webhook 密钥类透传值。
package safeenv

import (
	"slices"
	"strings"
	"testing"
)

func TestGhEnvOverridesIsolatedFromEnv(t *testing.T) {
	t.Setenv("GODER_GH_ENV_GRF_TURN_WEBHOOK_URL", "http://127.0.0.1/hook")
	t.Setenv("GODER_GH_ENV_GRF_TURN_WEBHOOK_SECRET", "s3cret")
	t.Setenv("OPENOS_EXCHANGE_SECRET", "must-not-leak")

	overrides := GhEnvOverrides()
	if !slices.Contains(overrides, "GODER_GH_ENV_GRF_TURN_WEBHOOK_URL=http://127.0.0.1/hook") ||
		!slices.Contains(overrides, "GODER_GH_ENV_GRF_TURN_WEBHOOK_SECRET=s3cret") {
		t.Fatalf("GhEnvOverrides 应原样收齐透传项: %v", overrides)
	}
	for _, kv := range overrides {
		if !strings.HasPrefix(kv, "GODER_GH_ENV_") {
			t.Fatalf("透传项应保留前缀: %v", overrides)
		}
	}

	// 共享 Env() 不得包含透传项与 OPENOS_* 秘密(终端子进程防线)。
	for _, kv := range Env() {
		if kv == "GODER_GH_ENV_GRF_TURN_WEBHOOK_SECRET=s3cret" {
			t.Fatalf("Env() 不应包含透传密钥(终端子进程可见): %s", kv)
		}
		if kv == "OPENOS_EXCHANGE_SECRET=must-not-leak" {
			t.Fatalf("Env() 不应包含 OPENOS_* 秘密: %s", kv)
		}
	}
}
