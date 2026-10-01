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

	t.Run("无 settings.json 跳过", func(t *testing.T) {
		wrote, err := upsertAIProvider(filepath.Join(t.TempDir(), "settings.json"), cfg, true)
		if wrote || err != nil {
			t.Fatalf("期望跳过，得到 wrote=%v err=%v", wrote, err)
		}
	})

	t.Run("强制同步设默认并保留其他键", func(t *testing.T) {
		p := writeSettings(t, t.TempDir(), seedSettings)
		wrote, err := upsertAIProvider(p, cfg, true)
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
		if _, err := upsertAIProvider(p, cfg, true); err != nil {
			t.Fatal(err)
		}
		wrote, err := upsertAIProvider(p, cfg, true)
		if wrote || err != nil {
			t.Fatalf("期望幂等跳过: wrote=%v err=%v", wrote, err)
		}
	})

	t.Run("非强制不打架用户自选默认", func(t *testing.T) {
		p := writeSettings(t, t.TempDir(), seedSettings)
		if _, err := upsertAIProvider(p, cfg, false); err != nil {
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
		if _, err := upsertAIProvider(p, cfg, false); err != nil {
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
		if _, err := upsertAIProvider(p, cfg, true); err != nil {
			t.Fatal(err)
		}
		wrote, err := upsertAIProvider(p, ProviderConfig{}, true)
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
		wrote, err := upsertAIProvider(p, cfg, true)
		if wrote || err != nil {
			t.Fatalf("损坏文件应跳过: wrote=%v err=%v", wrote, err)
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
	m := &Manager{}
	body := `{"path":"/x/settings.json","default":"OpenOS/glm-4-flash","writable":true,"maxSteps":200,"providers":[{"name":"GRF","baseUrl":"https://x","apiKey":"","models":["a"]},{"name":"OpenOS","baseUrl":"https://y","apiKey":"","models":["glm-4-flash"]}]}`
	resp := fakeJSONResponse("GET", "/goder-api/v1/agent/models", body)
	if err := m.markManagedProvider(resp); err != nil {
		t.Fatal(err)
	}
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
	if out.Providers[0]["managed"] != nil {
		t.Fatal("非托管供应商不应有 managed")
	}
	if out.Providers[1]["managed"] != true {
		t.Fatal("托管供应商缺 managed:true")
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
