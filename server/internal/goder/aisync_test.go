// aisync_test.go — AI 模型配置与 goder 联动的写盘语义（upsert/清除/幂等/保真）。
package goder

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openos/server/internal/store"
)

func writeSettings(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func readSettings(t *testing.T, p string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

const seedSettings = `{
  "default": "GRF/deepseek-v4.1-flash",
  "max_steps": 200,
  "mouse": true,
  "policy": "off",
  "providers": {
    "GRF": {
      "base_url": "https://th.gorun.fun/v1",
      "api_key": "sk-host",
      "models": ["deepseek-v4.1-flash"]
    }
  }
}`

func TestUpsertAIProvider(t *testing.T) {
	cfg := ProviderConfig{BaseURL: "https://open.bigmodel.cn/api/paas/v4", APIKey: "sk-new", Model: "glm-4-flash"}
	upsert := func(p string, c ProviderConfig, force bool) (bool, error) {
		return upsertAIProviders(p, c, nil, nil, force)
	}

	t.Run("无 settings.json 起播（新建目录写入）", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "users", "u1", "grf-home") // 目录不存在
		p := filepath.Join(dir, "settings.json")
		def := store.AIUserProvider{UID: "u1", Name: "TokenHub", BaseURL: "https://th/v1", APIKey: "sk", Models: []string{"m1"}, IsDefault: true}
		wrote, err := upsertAIProviders(p, cfg, []store.AIUserProvider{def}, []string{"TokenHub"}, false)
		if !wrote || err != nil {
			t.Fatalf("期望起播写盘: wrote=%v err=%v", wrote, err)
		}
		m := readSettings(t, p)
		var got string
		json.Unmarshal(m["default"], &got)
		if got != "TokenHub/m1" {
			t.Fatalf("起播 default 不对: %q", got)
		}
		var provs map[string]json.RawMessage
		json.Unmarshal(m["providers"], &provs)
		if provs["TokenHub"] == nil || provs["OpenOS"] == nil {
			t.Fatal("起播应含托管与声明条目")
		}
	})

	t.Run("无文件且无内容不建空文件", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "settings.json")
		wrote, err := upsertAIProviders(p, ProviderConfig{}, nil, nil, false)
		if wrote || err != nil {
			t.Fatalf("空配置不应起播: wrote=%v err=%v", wrote, err)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("不应创建文件")
		}
	})

	t.Run("强制同步设默认并保留其他键", func(t *testing.T) {
		p := writeSettings(t, t.TempDir(), seedSettings)
		wrote, err := upsert(p, cfg, true)
		if !wrote || err != nil {
			t.Fatalf("期望写盘: wrote=%v err=%v", wrote, err)
		}
		m := readSettings(t, p)
		var def string
		json.Unmarshal(m["default"], &def)
		if def != "OpenOS/glm-4-flash" {
			t.Fatalf("default = %q", def)
		}
		var provs map[string]json.RawMessage
		json.Unmarshal(m["providers"], &provs)
		if provs["GRF"] == nil || provs["OpenOS"] == nil {
			t.Fatal("宿主供应商被破坏或托管供应商缺失")
		}
		var entry struct {
			BaseURL string   `json:"base_url"`
			APIKey  string   `json:"api_key"`
			Models  []string `json:"models"`
		}
		json.Unmarshal(provs["OpenOS"], &entry)
		if entry.BaseURL != cfg.BaseURL || entry.APIKey != cfg.APIKey || len(entry.Models) != 1 || entry.Models[0] != cfg.Model {
			t.Fatalf("托管条目内容不对: %+v", entry)
		}
		// 未知/常用键原样保留
		for _, k := range []string{"max_steps", "mouse", "policy"} {
			if m[k] == nil {
				t.Fatalf("键 %q 被丢失", k)
			}
		}
	})

	t.Run("幂等不写盘", func(t *testing.T) {
		p := writeSettings(t, t.TempDir(), seedSettings)
		if _, err := upsert(p, cfg, true); err != nil {
			t.Fatal(err)
		}
		wrote, err := upsert(p, cfg, true)
		if wrote || err != nil {
			t.Fatalf("期望幂等跳过: wrote=%v err=%v", wrote, err)
		}
	})

	t.Run("非强制不打架用户自选默认", func(t *testing.T) {
		p := writeSettings(t, t.TempDir(), seedSettings)
		if _, err := upsert(p, cfg, false); err != nil {
			t.Fatal(err)
		}
		var def string
		json.Unmarshal(readSettings(t, p)["default"], &def)
		if def != "GRF/deepseek-v4.1-flash" {
			t.Fatalf("用户默认被打架: %q", def)
		}
	})

	t.Run("空默认与托管默认跟随同步", func(t *testing.T) {
		no := strings.Replace(seedSettings, `"default": "GRF/deepseek-v4.1-flash"`, `"default": ""`, 1)
		p := writeSettings(t, t.TempDir(), no)
		if _, err := upsert(p, cfg, false); err != nil {
			t.Fatal(err)
		}
		var def string
		json.Unmarshal(readSettings(t, p)["default"], &def)
		if def != "OpenOS/glm-4-flash" {
			t.Fatalf("空默认未跟随: %q", def)
		}
	})

	t.Run("清除托管条目", func(t *testing.T) {
		p := writeSettings(t, t.TempDir(), seedSettings)
		if _, err := upsert(p, cfg, true); err != nil {
			t.Fatal(err)
		}
		wrote, err := upsert(p, ProviderConfig{}, true)
		if !wrote || err != nil {
			t.Fatalf("期望写盘: wrote=%v err=%v", wrote, err)
		}
		m := readSettings(t, p)
		if _, has := m["default"]; has {
			t.Fatal("指向托管条目的 default 应被移除")
		}
		var provs map[string]json.RawMessage
		json.Unmarshal(m["providers"], &provs)
		if provs["OpenOS"] != nil || provs["GRF"] == nil {
			t.Fatal("清除应只移除托管条目")
		}
	})

	t.Run("损坏文件不碰", func(t *testing.T) {
		p := writeSettings(t, t.TempDir(), "{not json")
		wrote, err := upsert(p, cfg, true)
		if wrote || err != nil {
			t.Fatalf("损坏文件应跳过: wrote=%v err=%v", wrote, err)
		}
	})
}

func TestUpsertUserProviders(t *testing.T) {
	cfg := ProviderConfig{BaseURL: "https://open.bigmodel.cn/api/paas/v4", APIKey: "sk-new", Model: "glm-4-flash"}
	tokenHub := store.AIUserProvider{
		UID: "u1", Name: "TokenHub",
		BaseURL: "https://th.gorun.fun/v1", APIKey: "sk-th-x",
		Models: []string{"deepseek-v4.1-flash", "glm-4.7"},
	}
	// 带 TokenHub 残留条目的种子（模拟上一轮声明留下的旧内容）
	seedTH := `{
  "default": "GRF/deepseek-v4.1-flash",
  "max_steps": 200,
  "providers": {
    "TokenHub": {
      "base_url": "https://old/v1",
      "api_key": "sk-old",
      "models": ["stale"]
    },
    "GRF": {
      "base_url": "https://th.gorun.fun/v1",
      "api_key": "sk-host",
      "models": ["deepseek-v4.1-flash"]
    }
  }
}`

	t.Run("声明条目写入且 default 优先于平台托管", func(t *testing.T) {
		// 宿主 Replace 声明后以 force=true 渲染（宿主是声明权威）
		p := writeSettings(t, t.TempDir(), seedSettings)
		def := tokenHub
		def.IsDefault = true
		if _, err := upsertAIProviders(p, cfg, []store.AIUserProvider{def}, []string{"TokenHub"}, true); err != nil {
			t.Fatal(err)
		}
		m := readSettings(t, p)
		var got string
		json.Unmarshal(m["default"], &got)
		if got != "TokenHub/deepseek-v4.1-flash" {
			t.Fatalf("声明 default 未生效: %q", got)
		}
		var provs map[string]json.RawMessage
		json.Unmarshal(m["providers"], &provs)
		var entry struct {
			Models []string `json:"models"`
		}
		json.Unmarshal(provs["TokenHub"], &entry)
		if len(entry.Models) != 2 || entry.Models[1] != "glm-4.7" {
			t.Fatalf("声明条目模型清单不对: %+v", entry)
		}
	})

	t.Run("残留声明条目按 names 全集清理", func(t *testing.T) {
		p := writeSettings(t, t.TempDir(), seedTH)
		if _, err := upsertAIProviders(p, cfg, nil, []string{"TokenHub"}, true); err != nil {
			t.Fatal(err)
		}
		var provs map[string]json.RawMessage
		json.Unmarshal(readSettings(t, p)["providers"], &provs)
		if provs["TokenHub"] != nil || provs["GRF"] == nil || provs["OpenOS"] == nil {
			t.Fatal("残留声明条目未清理或破坏了其他条目")
		}
	})

	t.Run("default 指向被清条目时回落托管默认", func(t *testing.T) {
		base := strings.Replace(seedTH, `"default": "GRF/deepseek-v4.1-flash"`, `"default": "TokenHub/stale"`, 1)
		p := writeSettings(t, t.TempDir(), base)
		if _, err := upsertAIProviders(p, cfg, nil, []string{"TokenHub"}, false); err != nil {
			t.Fatal(err)
		}
		var got string
		json.Unmarshal(readSettings(t, p)["default"], &got)
		if got != "OpenOS/glm-4-flash" {
			t.Fatalf("受管 default 未回落: %q", got)
		}
	})

	t.Run("声明 default 不打架用户自选的非受管默认", func(t *testing.T) {
		p := writeSettings(t, t.TempDir(), seedSettings)
		def := tokenHub
		def.IsDefault = true
		if _, err := upsertAIProviders(p, cfg, []store.AIUserProvider{def}, []string{"TokenHub"}, false); err != nil {
			t.Fatal(err)
		}
		var got string
		json.Unmarshal(readSettings(t, p)["default"], &got)
		if got != "GRF/deepseek-v4.1-flash" {
			t.Fatalf("用户自选默认被打架: %q", got)
		}
	})
}

func TestGrfHomePath清洗(t *testing.T) {
	got := GrfHomePath("/data", "we ird!")
	want := filepath.Join("/data", "goder", "serve-data", "users", "we_ird_", "grf-home")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMarkManagedProvider(t *testing.T) {
	body := `{"path":"/x/settings.json","default":"OpenOS/glm-4-flash","writable":true,"maxSteps":200,"providers":[{"name":"GRF","baseUrl":"https://x","apiKey":"","models":["a"]},{"name":"OpenOS","baseUrl":"https://y","apiKey":"","models":["glm-4-flash"]},{"name":"TokenHub","baseUrl":"https://z/v1","apiKey":"sk","models":["m1"]}]}`

	t.Run("平台托管名", func(t *testing.T) {
		m := &Manager{}
		resp := fakeJSONResponse("GET", "/goder-api/v1/agent/models", body)
		if err := m.markManagedProvider(resp); err != nil {
			t.Fatal(err)
		}
		assertManaged(t, resp, map[int]bool{1: true, 2: false})
	})

	t.Run("含宿主声明名", func(t *testing.T) {
		m := &Manager{}
		m.managedNames = []string{"TokenHub"}
		resp := fakeJSONResponse("GET", "/goder-api/v1/agent/models", body)
		if err := m.markManagedProvider(resp); err != nil {
			t.Fatal(err)
		}
		assertManaged(t, resp, map[int]bool{0: false, 1: true, 2: true})
	})
}

func assertManaged(t *testing.T, resp *http.Response, want map[int]bool) {
	t.Helper()
	var out struct {
		Default   string           `json:"default"`
		MaxSteps  int              `json:"maxSteps"`
		Providers []map[string]any `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Default != "OpenOS/glm-4-flash" || out.MaxSteps != 200 {
		t.Fatalf("顶层键未保真: %+v", out)
	}
	for i, w := range want {
		got := out.Providers[i]["managed"] == true
		if got != w {
			t.Fatalf("providers[%d].managed = %v, want %v", i, got, w)
		}
	}
}

// fakeJSONResponse 构造带请求上下文的 JSON 响应（markManagedProvider 依赖
// resp.Request 判定路径）。
func fakeJSONResponse(method, path, body string) *http.Response {
	req, _ := http.NewRequest(method, path, nil)
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}
