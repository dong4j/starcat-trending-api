// Package handler 中的 trending_cache.go 实现 /api/v1/repos 响应的内存缓存。
//
// R-06.2（2026-06-15）：把 trending 接口从"每次请求都查 SQLite + marshal JSON"升级为
// 内存预拼 payload，配合 ETag / Last-Modified 让客户端可以走 304 节省带宽。
//
// 设计要点：
//   - 分桶 TTL：与后端 cron 节奏对齐，让 cache 自然在数据更新前过期。
//     · daily   → 1h （cron `7 * * * *` 每小时第 7 分跑）
//     · weekly  → 6h （cron `13 */6 * * *` 每 6h 第 13 分跑）
//     · monthly → 24h（cron `19 5 */2 * *` 每 2 天 05:19 跑，但 24h cap 比 48h 服务重启安全
//     且 monthly 数据变化慢）
//   - 主动 Invalidate：scheduler 完成 syncDaily / syncWeekly / syncMonthly 后调
//     `Invalidate(since)` 清掉对应桶下所有 key，保证客户端下次请求 100% 拿到新数据
//     （不等 TTL 自然过期）。Invalidate 走 scheduler.CacheInvalidator 接口注入，
//     避免 scheduler 直接 import handler 包形成奇怪的依赖方向。
//   - Key 规则：`since|lang|limit`（lang 为空时记 `*`，避免空串歧义）；不同 limit
//     视为独立 key（客户端少数会传 50 / 100 / 30）
//   - ETag 用 SHA256 前 16 字符 + `W/` weak 前缀（不要求 byte-by-byte 一致，
//     只要逻辑等价）
//   - payload 是 pre-marshaled envelope JSON，cache hit 直接 `w.Write` 跳过 marshal
//   - 锁粒度：`sync.RWMutex` 整张 map，read 走 RLock；entry 量级 3 since ×
//     几十 lang × ≤3 limit ≈ 几百，map 不大无需分桶锁
//
// 不做的事（保持简单）：
//   - 不做 LRU / 不做总容量上限：entry 都很小（≤ 100 KB），全量上限也就 MB 级
//   - 不在 Get 路径写 mtime：TTL 判定用 builtAt 即可，不需要 LRU 时序
//   - 不持久化到磁盘：服务重启后冷启动，下一次 cron tick 自然回填
package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// TrendingCache 是 /api/v1/repos 响应的进程内内存缓存。
//
// 安全使用：所有方法都是 goroutine-safe。
type TrendingCache struct {
	mu    sync.RWMutex
	items map[string]*trendingCacheEntry
}

// trendingCacheEntry 单条 cache entry。
type trendingCacheEntry struct {
	payload      []byte    // pre-marshaled envelope JSON
	etag         string    // weak ETag，形如 `W/"abc123..."`
	lastModified time.Time // = builtAt，写到 Last-Modified header
	builtAt      time.Time // 用来判 TTL
}

// NewTrendingCache 创建一个空缓存。
func NewTrendingCache() *TrendingCache {
	return &TrendingCache{
		items: make(map[string]*trendingCacheEntry),
	}
}

// CacheKey 拼一个稳定的缓存 key。lang 为空记 `*`。
func CacheKey(since, lang string, limit int) string {
	if lang == "" {
		lang = "*"
	}
	return fmt.Sprintf("%s|%s|%d", since, lang, limit)
}

// Get 返回 (entry, true) 表示命中且未过期；否则 (nil, false)。
// 过期判定用各 since 的分桶 TTL（见 TTLFor）。
//
// 调用方拿到 entry 后**不能修改**——entry.payload 是共享 byte slice。
func (c *TrendingCache) Get(key, since string) (*trendingCacheEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if time.Since(entry.builtAt) > TTLFor(since) {
		return nil, false
	}
	return entry, true
}

// Set 写入 entry。重复 key 直接覆盖（典型场景：cache miss 后填）。
func (c *TrendingCache) Set(key string, payload []byte) *trendingCacheEntry {
	now := time.Now()
	entry := &trendingCacheEntry{
		payload:      payload,
		etag:         computeWeakETag(payload),
		lastModified: now,
		builtAt:      now,
	}
	c.mu.Lock()
	c.items[key] = entry
	c.mu.Unlock()
	return entry
}

// Invalidate 清掉所有 key 前缀匹配 `since|` 的 entry。
//
// 用途：scheduler 跑完 syncDaily 后调 `cache.Invalidate("daily")`，主动失效所有
// daily 桶的 entry（含全部 lang × limit 组合），保证下次客户端请求强制重建。
//
// 不抛错——cache miss 也是合法状态（服务启动期 / 上一轮 sync 后未被访问过的 key）。
func (c *TrendingCache) Invalidate(since string) {
	prefix := since + "|"
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.items {
		if strings.HasPrefix(k, prefix) {
			delete(c.items, k)
		}
	}
}

// InvalidateAll 清掉全部 entry。仅供测试用或极端管理场景。
func (c *TrendingCache) InvalidateAll() {
	c.mu.Lock()
	c.items = make(map[string]*trendingCacheEntry)
	c.mu.Unlock()
}

// Size 返回当前 entry 数。仅供测试 / 监控。
func (c *TrendingCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// TTLFor 返回某 since 桶的 TTL。
//
// 与 cron 节奏精确对齐：cache 自然在数据可能更新前过期。客户端拿到的数据最坏
// 滞后 TTL × 2（cron 刚 invalidate 后立即拉了一次新 cache → TTL 满期 → 自然过期重建）。
//
// daily 1h / weekly 6h / monthly 24h 是产品权衡：
//   - monthly 实际 cron 是每 2 天跑，cap 到 24h 让客户端看到的最大滞后控制在 1 天
//   - weekly 6h 与 cron 节奏 1:1 对齐
//   - daily 1h 与 cron 节奏 1:1 对齐
//
// 未知 since（理论上 handler 已经 reject 非法值，但兜底）按 daily 1h 处理。
func TTLFor(since string) time.Duration {
	switch since {
	case "daily":
		return 1 * time.Hour
	case "weekly":
		return 6 * time.Hour
	case "monthly":
		return 24 * time.Hour
	default:
		return 1 * time.Hour
	}
}

// computeWeakETag 用 SHA256 前 16 字符生成 weak ETag。
//
// W/ 前缀表"语义等价"——客户端不必要求 byte-by-byte 一致（HTTP 7232 §2.1）。
// 16 字符（64 bit）冲突概率足够低，比完整 64 字符短的多。
func computeWeakETag(payload []byte) string {
	sum := sha256.Sum256(payload)
	hex := hex.EncodeToString(sum[:8]) // 8 bytes = 16 hex chars
	return `W/"` + hex + `"`
}

// 关于 scheduler / handler 解耦的注：
//
// scheduler 在自己包里定义一个最小接口 `type CacheInvalidator interface { Invalidate(since string) }`，
// main.go 把 *TrendingCache 注入到 scheduler.New(...) 即可（*TrendingCache 自动满足接口）。
// 这样 scheduler 包**不直接 import handler 包**，避免形成奇怪的依赖方向；同时 handler 也不需要 import
// scheduler。两个包都只依赖 store / model。详见 scheduler/cron.go 的 CacheInvalidator 定义。
