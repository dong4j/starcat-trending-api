// Package tokenpool 提供 GitHub PAT 多 token 池管理。
//
// R-01 v1.2: Quota-aware 选择 + 401/5xx 死 token 检测。
// 与 trending / weekly / sharing byte-level 一致（详见 supports/docs/R-01-总体设计.md §3.7 + §4.1）。
//
// ⚠️ 跨项目共享代码同步约定：本文件必须在 trending / weekly / sharing 三个
// API 中 byte-level 一致，任何修改都必须同时同步 3 份。
package tokenpool

import (
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const defaultTemporaryDisable = 60 * time.Second

// TokenState 单个 PAT 的运行时状态。
type TokenState struct {
	Value               string
	Remaining           int       // X-RateLimit-Remaining; -1 未知
	ResetAt             time.Time // X-RateLimit-Reset
	DisabledUntil       time.Time // 临时不可用，到 GitHub reset / Retry-After 后懒恢复
	Dead                bool
	LastUsedAt          time.Time
	ConsecutiveFailures int
}

// Pool GitHub Token 池。
type Pool struct {
	mu     sync.Mutex
	tokens []*TokenState
}

// New 从 token 字符串列表创建 Pool。
// 自动 trim 空白 + skip 空字符串，避免把误配的空 token 加入池。
func New(tokenValues []string) *Pool {
	tokens := make([]*TokenState, 0, len(tokenValues))
	for _, tv := range tokenValues {
		tv = trimSpace(tv)
		if tv == "" {
			continue
		}
		tokens = append(tokens, &TokenState{
			Value:     tv,
			Remaining: -1,
		})
	}
	log.Printf("[token-pool] loaded %d tokens from GITHUB_TOKENS env", len(tokens))
	return &Pool{tokens: tokens}
}

// PickBest Quota-aware 选 token。
//
// 算法：
//  1. 懒恢复已过 DisabledUntil 的 token
//  2. 过滤 dead、临时禁用和已耗尽（remaining==0 且 reset 还没到）的 token
//  3. 若有 remaining 未知（-1）的 token，从中随机选一个（让它去试一次拿到 quota）
//  4. 否则选 remaining 最高的
//
// 返回 nil 表示所有 token 都不可用，上层应该 sleep 到 EarliestReset。
func (p *Pool) PickBest() *TokenState {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	var alive []*TokenState
	for _, t := range p.tokens {
		if t.Dead {
			continue
		}
		p.recoverIfReady(t, now)
		if t.DisabledUntil.After(now) {
			continue
		}
		if t.Remaining == 0 && now.Before(t.ResetAt) {
			continue
		}
		alive = append(alive, t)
	}

	if len(alive) == 0 {
		log.Printf("[token-pool] all tokens dead or exhausted")
		return nil
	}

	// 优先选 remaining 未知的
	var unknowns []*TokenState
	for _, t := range alive {
		if t.Remaining == -1 {
			unknowns = append(unknowns, t)
		}
	}
	if len(unknowns) > 0 {
		return unknowns[rand.Intn(len(unknowns))]
	}

	// 选 remaining 最高的
	best := alive[0]
	for _, t := range alive[1:] {
		if t.Remaining > best.Remaining {
			best = t
		}
	}
	return best
}

// UpdateFromResponse 从 HTTP 响应头更新 token 状态。
// 必须在每次 GitHub API 调用后调用，否则池无法感知配额变化和死 token。
func (p *Pool) UpdateFromResponse(token *TokenState, resp *http.Response) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if r := resp.Header.Get("X-RateLimit-Remaining"); r != "" {
		token.Remaining, _ = strconv.Atoi(r)
	}
	if r := resp.Header.Get("X-RateLimit-Reset"); r != "" {
		ts, _ := strconv.ParseInt(r, 10, 64)
		token.ResetAt = time.Unix(ts, 0)
	}
	token.LastUsedAt = time.Now()
	if token.Remaining == 0 && token.ResetAt.After(token.LastUsedAt) {
		p.disableUntilLocked(token, token.ResetAt, "quota exhausted")
	}

	if resp.StatusCode == 401 {
		token.Dead = true
		log.Printf("[token-pool] %s marked DEAD (401)", maskToken(token.Value))
	}
	if resp.StatusCode >= 500 {
		token.ConsecutiveFailures++
		if token.ConsecutiveFailures >= 5 {
			token.Dead = true
			log.Printf("[token-pool] %s marked DEAD (5 consecutive 5xx)", maskToken(token.Value))
		}
	} else if resp.StatusCode < 500 {
		token.ConsecutiveFailures = 0
	}
}

// DisableUntil 临时禁用 token。到期恢复不需要定时器，由 PickBest 懒执行。
func (p *Pool) DisableUntil(token *TokenState, until time.Time, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.disableUntilLocked(token, until, reason)
}

// EarliestReset 返回池中最早的 reset 时间。
// 当 PickBest 返回 nil 时，上层 sleep 到此时间再 retry。
func (p *Pool) EarliestReset() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()

	var earliest time.Time
	now := time.Now()
	for _, t := range p.tokens {
		if t.Dead {
			continue
		}
		p.recoverIfReady(t, now)
		candidate := t.DisabledUntil
		if candidate.IsZero() {
			candidate = t.ResetAt
		}
		if candidate.IsZero() {
			continue
		}
		if earliest.IsZero() || candidate.Before(earliest) {
			earliest = candidate
		}
	}
	return earliest
}

// Stats 返回监控用统计。
func (p *Pool) Stats() (alive, dead, totalRemaining int, nextReset time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, t := range p.tokens {
		if t.Dead {
			dead++
		} else {
			alive++
			if t.Remaining > 0 {
				totalRemaining += t.Remaining
			}
		}
	}
	nextReset = p.earliestResetUnsafe()
	return
}

func (p *Pool) disableUntilLocked(token *TokenState, until time.Time, reason string) {
	if token == nil || token.Dead {
		return
	}
	now := time.Now()
	if until.IsZero() || !until.After(now) {
		until = now.Add(defaultTemporaryDisable)
	}
	if until.After(token.DisabledUntil) {
		token.DisabledUntil = until
		log.Printf("[token-pool] %s disabled until %s (%s)", maskToken(token.Value), until.Format(time.RFC3339), reason)
	}
}

func (p *Pool) recoverIfReady(token *TokenState, now time.Time) {
	if token.DisabledUntil.IsZero() || token.DisabledUntil.After(now) {
		return
	}
	token.DisabledUntil = time.Time{}
	if token.ResetAt.IsZero() || !token.ResetAt.After(now) {
		token.Remaining = -1
	}
}

func (p *Pool) earliestResetUnsafe() time.Time {
	var earliest time.Time
	for _, t := range p.tokens {
		if t.Dead || t.ResetAt.IsZero() {
			continue
		}
		if earliest.IsZero() || t.ResetAt.Before(earliest) {
			earliest = t.ResetAt
		}
	}
	return earliest
}

// maskToken 脱敏 PAT 字符串，仅留前 7 + 末 4 字符。
func maskToken(key string) string {
	if len(key) < 16 {
		return "****"
	}
	return key[:7] + "****" + key[len(key)-4:]
}

// trimSpace 轻量级 trim（只处理空格 / 制表符，避免引入 strings 包以减少 import）。
func trimSpace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	j := len(s) - 1
	for j >= i && (s[j] == ' ' || s[j] == '\t') {
		j--
	}
	return s[i : j+1]
}
