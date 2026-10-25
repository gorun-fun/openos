// gitlabauth.go — GitLab OAuth password-grant 令牌兑换与平台错误提取（共享件）。
//
// 两个消费方此前各持一份同协议副本（connections.go 的 connBearer 与
// api/pipeline.go 的 GitLabClient.endpoint——同样的表单兑换、2 分钟临期余量、
// 2h 兜底 TTL、同款错误文案，注释还互认「同 pipeline 惯例」），协议侧任何
// 改动都要动两处。收敛于此：兑换按 key 隔离缓存（steward 连接用连接 id、
// pipeline 的 env 兜底用固定键），错误提取兼容 GitLab（{"message"}）与
// GitHub（{"error"}）两种错误体。
package steward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// glTokenMargin password-grant 令牌临期重取余量（两消费方同款惯例）。
const glTokenMargin = 2 * time.Minute

// glTokenEntry 兑换缓存条目。
type glTokenEntry struct {
	token string
	exp   time.Time
}

// GlTokenCache password-grant access_token 缓存（按 key 隔离；PAT 模式不经此）。
type GlTokenCache struct {
	mu     sync.Mutex
	tokens map[string]glTokenEntry
	client *http.Client
}

// NewGlTokenCache 构造（client 为出站 HTTP 客户端）。
func NewGlTokenCache(client *http.Client) *GlTokenCache {
	return &GlTokenCache{tokens: map[string]glTokenEntry{}, client: client}
}

// Exchange 取可用令牌：缓存命中（未进临期余量）直接返回，否则向
// {baseURL}/oauth/token 以 password grant 兑换并缓存（expires_in 缺失兜底 2h）。
func (c *GlTokenCache) Exchange(ctx context.Context, key, baseURL, user, password string) (string, error) {
	c.mu.Lock()
	if tok, ok := c.tokens[key]; ok && time.Now().Before(tok.exp.Add(-glTokenMargin)) {
		c.mu.Unlock()
		return tok.token, nil
	}
	c.mu.Unlock()

	form := url.Values{"grant_type": {"password"}, "username": {user}, "password": {password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GitLab 不可达: %s", ErrShort(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("GitLab 认证失败（%d）：%s", resp.StatusCode, ErrMessage(raw))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.AccessToken == "" {
		return "", errors.New("GitLab 认证响应异常（无 access_token）")
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	c.mu.Lock()
	c.tokens[key] = glTokenEntry{token: out.AccessToken, exp: time.Now().Add(ttl)}
	c.mu.Unlock()
	return out.AccessToken, nil
}

// Invalidate 作废单键缓存（401 强制重兑用）。
func (c *GlTokenCache) Invalidate(key string) {
	c.mu.Lock()
	delete(c.tokens, key)
	c.mu.Unlock()
}

// ErrMessage 提取平台错误响应的可读信息（GitLab {"message"} / GitHub {"error"}
// 或原文截断 200 字；message 可能是校验错误对象，序列化为可读串）。
func ErrMessage(raw []byte) string {
	var e struct {
		Message any `json:"message"`
		Error   any `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil {
		for _, m := range []any{e.Message, e.Error} {
			if m == nil {
				continue
			}
			if s, ok := m.(string); ok && s != "" {
				return s
			}
			if b, err := json.Marshal(m); err == nil {
				return string(b)
			}
		}
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// ErrShort 错误串截短（网络错误满屏 URL 噪音；原先 api/status.go 与
// connections.go 各一份逐字相同，收敛于此）。
func ErrShort(err error) string {
	s := err.Error()
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}
