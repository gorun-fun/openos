// ttl.go — 极简 TTL 缓存（stewardapi/steward 两包共用）。
//
// 收敛此前四份手写的同款逻辑：pipeline 聚合/详情/日志缓存 ×3 + connections
// 仓库浏览缓存 ×1，均为「mu + map + 写入时刻 + 容量超限时清一轮过期项仍超则
// 整体清空」。取数一律无锁外发（先 Get 后回填 Put）——需要串行化取数的场景
// （如聚合一轮全量）由调用方自己加协调，勿在缓存锁内做网络请求（不同 key 的
// 并发取数会被无谓串行化，pipeline 详情/日志缓存曾因此互相阻塞）。
package cache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	at time.Time
	v  V
}

// TTL 带 TTL 的并发安全 map。limit 为容量上限：Put 时超限先清一轮过期项，
// 仍超则整体清空（防翻页类场景无界增长）。
type TTL[K comparable, V any] struct {
	mu    sync.Mutex
	m     map[K]entry[V]
	ttl   time.Duration
	limit int
}

// NewTTL 构造（ttl ≤0 视为永不过期，仅受 limit 约束）。
func NewTTL[K comparable, V any](ttl time.Duration, limit int) *TTL[K, V] {
	if limit <= 0 {
		limit = 64
	}
	return &TTL[K, V]{m: map[K]entry[V]{}, ttl: ttl, limit: limit}
}

// Get 命中且未过期才返回。
func (c *TTL[K, V]) Get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || (c.ttl > 0 && time.Since(e.at) > c.ttl) {
		var zero V
		return zero, false
	}
	return e.v, true
}

// Put 写入（带容量收敛）。
func (c *TTL[K, V]) Put(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.limit {
		now := time.Now()
		for kk, ee := range c.m {
			if c.ttl > 0 && now.Sub(ee.at) > c.ttl {
				delete(c.m, kk)
			}
		}
		if len(c.m) >= c.limit {
			clear(c.m)
		}
	}
	c.m[k] = entry[V]{at: time.Now(), v: v}
}

// Delete 删除单键。
func (c *TTL[K, V]) Delete(k K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, k)
}

// Clear 全量清空。
func (c *TTL[K, V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = map[K]entry[V]{}
}
