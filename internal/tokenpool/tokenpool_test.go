// Package tokenpool 的单测。
//
// 覆盖：
//  1. New: 跳过空字符串 / 全空白
//  2. PickBest:
//     - 全部 dead 返 nil
//     - 有 dead 有 alive 选 alive
//     - 全部 exhausted 返 nil
//     - remaining=-1 随机选
//     - remaining 不同选最大
//     - 同一 token exhausted (remaining=0 且 reset 未到) 不返
//  3. UpdateFromResponse:
//     - X-RateLimit-Remaining 解析
//     - X-RateLimit-Reset 解析（Unix timestamp）
//     - 401 翻 Dead=true
//     - 5xx 累计 ConsecutiveFailures, 5 次翻 Dead
//     - 5xx < 5 次不翻 Dead
//     - 5xx 之后遇到 2xx 复位 ConsecutiveFailures
//  4. EarliestReset: 跨 token 选最早 reset；dead 跳过
//  5. Stats: alive / dead / totalRemaining
//  6. maskToken: 短字符串全脱敏,长字符串前 7 + 后 4
//  7. trimSpace: 处理空格 / 制表符
package tokenpool

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

// TestNew_SkipEmpty 验证空字符串/全空白被跳过。
func TestNew_SkipEmpty(t *testing.T) {
	p := New([]string{"", "  ", "\t", "real-token-1", "real-token-2"})
	if len(p.tokens) != 2 {
		t.Errorf("want 2 tokens, got %d", len(p.tokens))
	}
}

// TestNew_AllEmpty 空列表不 panic。
func TestNew_AllEmpty(t *testing.T) {
	p := New([]string{"", "  "})
	if len(p.tokens) != 0 {
		t.Errorf("want 0 tokens, got %d", len(p.tokens))
	}
	if p.PickBest() != nil {
		t.Error("PickBest on empty pool should return nil")
	}
}

// TestPickBest_AllDead 全部 dead 返 nil。
func TestPickBest_AllDead(t *testing.T) {
	p := New([]string{"t1", "t2", "t3"})
	for _, tok := range p.tokens {
		tok.Dead = true
	}
	if got := p.PickBest(); got != nil {
		t.Errorf("all dead: want nil, got %v", got)
	}
}

// TestPickBest_OneAliveNDead 选 alive。
func TestPickBest_OneAliveNDead(t *testing.T) {
	p := New([]string{"dead1", "alive", "dead2"})
	p.tokens[0].Dead = true
	p.tokens[2].Dead = true
	p.tokens[1].Remaining = 100

	got := p.PickBest()
	if got == nil {
		t.Fatal("want non-nil, got nil")
	}
	if got.Value != "alive" {
		t.Errorf("want 'alive', got %q", got.Value)
	}
}

// TestPickBest_UnknownPreferred 多个 token,有些 remaining 未知 → 选未知（让它去试一次）。
func TestPickBest_UnknownPreferred(t *testing.T) {
	p := New([]string{"t1", "t2", "t3"})
	// t1: known 100, t2: known 50, t3: unknown
	p.tokens[0].Remaining = 100
	p.tokens[1].Remaining = 50
	p.tokens[2].Remaining = -1

	got := p.PickBest()
	if got == nil {
		t.Fatal("want non-nil")
	}
	// 选 unknown 的 — 不一定是 t3(随机)，但 remaining=-1
	if got.Remaining != -1 {
		t.Errorf("want unknown token (remaining=-1), got remaining=%d", got.Remaining)
	}
}

// TestPickBest_KnownMax 全部 known 选 remaining 最大。
func TestPickBest_KnownMax(t *testing.T) {
	p := New([]string{"t1", "t2", "t3"})
	p.tokens[0].Remaining = 100
	p.tokens[1].Remaining = 500 // 最大
	p.tokens[2].Remaining = 50

	got := p.PickBest()
	if got == nil {
		t.Fatal("want non-nil")
	}
	if got.Value != "t2" {
		t.Errorf("want t2 (remaining=500), got %q", got.Value)
	}
}

// TestPickBest_ExhaustedSkipped exhausted token 跳过。
func TestPickBest_ExhaustedSkipped(t *testing.T) {
	p := New([]string{"t1", "t2"})
	// t1: exhausted (remaining=0, reset 1h 后)
	p.tokens[0].Remaining = 0
	p.tokens[0].ResetAt = time.Now().Add(1 * time.Hour)
	// t2: alive with some quota
	p.tokens[1].Remaining = 100

	got := p.PickBest()
	if got == nil {
		t.Fatal("want non-nil")
	}
	if got.Value != "t2" {
		t.Errorf("want t2 (exhausted t1 skipped), got %q", got.Value)
	}
}

// TestPickBest_ExhaustedResetPassed reset 过了 → 重新可用。
func TestPickBest_ExhaustedResetPassed(t *testing.T) {
	p := New([]string{"t1"})
	// remaining=0 但 reset 已过去 → 应当被视为可用（虽然 remaining 还是 0，但不被过滤）
	// 看实现:过滤条件是 `t.Remaining == 0 && time.Now().Before(t.ResetAt)`
	// reset 在过去时,Before 返 false → 不过滤
	p.tokens[0].Remaining = 0
	p.tokens[0].ResetAt = time.Now().Add(-1 * time.Hour) // 1h 前

	got := p.PickBest()
	if got == nil {
		t.Fatal("reset passed: token should be available, got nil")
	}
}

func TestPickBest_DisabledUntilSkippedAndRecovered(t *testing.T) {
	p := New([]string{"t1", "t2"})
	p.tokens[0].Remaining = 100
	p.tokens[1].Remaining = 50
	p.DisableUntil(p.tokens[0], time.Now().Add(1*time.Hour), "test")

	got := p.PickBest()
	if got == nil || got.Value != "t2" {
		t.Fatalf("disabled token should be skipped, got %#v", got)
	}

	p.tokens[0].DisabledUntil = time.Now().Add(-1 * time.Second)
	got = p.PickBest()
	if got == nil || got.Value != "t1" {
		t.Fatalf("expired disabled token should recover, got %#v", got)
	}
}

// TestUpdateFromResponse_RateLimitHeaders 验证响应头解析。
func TestUpdateFromResponse_RateLimitHeaders(t *testing.T) {
	p := New([]string{"tok"})
	tok := p.tokens[0]

	resetUnix := time.Now().Add(1 * time.Hour).Unix()
	hdr := http.Header{}
	hdr.Set("X-RateLimit-Remaining", "4999")
	hdr.Set("X-RateLimit-Reset", strconv.FormatInt(resetUnix, 10))
	resp := &http.Response{StatusCode: 200, Header: hdr}

	p.UpdateFromResponse(tok, resp)

	if tok.Remaining != 4999 {
		t.Errorf("Remaining: want 4999, got %d", tok.Remaining)
	}
	// ResetAt 应当接近 resetUnix(秒级精度)
	if delta := tok.ResetAt.Unix() - resetUnix; delta < -2 || delta > 2 {
		t.Errorf("ResetAt: want ~%d, got %d (delta=%d)", resetUnix, tok.ResetAt.Unix(), delta)
	}
}

func TestUpdateFromResponse_RemainingZeroDisablesUntilReset(t *testing.T) {
	p := New([]string{"tok-eeeeeeeeeeeeeeeeee", "tok-ffffffffffffffffff"})
	tok := p.tokens[0]
	resetAt := time.Now().Add(1 * time.Hour)
	hdr := http.Header{}
	hdr.Set("X-RateLimit-Remaining", "0")
	hdr.Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))

	p.UpdateFromResponse(tok, &http.Response{StatusCode: 200, Header: hdr})

	if tok.DisabledUntil.IsZero() {
		t.Fatal("remaining=0 should temporarily disable token")
	}
	got := p.PickBest()
	if got == nil || got.Value != p.tokens[1].Value {
		t.Fatalf("disabled exhausted token should be skipped, got %#v", got)
	}
}

// TestUpdateFromResponse_401MarksDead 401 翻 Dead。
func TestUpdateFromResponse_401MarksDead(t *testing.T) {
	p := New([]string{"tok-aaaaaaaaaaaaaaaaaa"})
	tok := p.tokens[0]
	resp := &http.Response{StatusCode: 401, Header: http.Header{}}
	p.UpdateFromResponse(tok, resp)
	if !tok.Dead {
		t.Error("401 should mark token Dead")
	}
	// 401 后 PickBest 应跳过
	if got := p.PickBest(); got != nil {
		t.Errorf("dead token should be skipped, got %v", got)
	}
}

// TestUpdateFromResponse_5xxAccumulate 5xx 累计 5 次翻 Dead。
func TestUpdateFromResponse_5xxAccumulate(t *testing.T) {
	p := New([]string{"tok-bbbbbbbbbbbbbbbbbb"})
	tok := p.tokens[0]
	for i := 0; i < 4; i++ {
		p.UpdateFromResponse(tok, &http.Response{StatusCode: 502, Header: http.Header{}})
		if tok.Dead {
			t.Errorf("after %d 5xx, should not be dead yet", i+1)
		}
	}
	p.UpdateFromResponse(tok, &http.Response{StatusCode: 503, Header: http.Header{}})
	if !tok.Dead {
		t.Error("after 5 5xx, token should be dead")
	}
}

// TestUpdateFromResponse_5xxResetOnSuccess 5xx 之后 2xx 复位。
func TestUpdateFromResponse_5xxResetOnSuccess(t *testing.T) {
	p := New([]string{"tok-cccccccccccccccccc"})
	tok := p.tokens[0]
	for i := 0; i < 3; i++ {
		p.UpdateFromResponse(tok, &http.Response{StatusCode: 500, Header: http.Header{}})
	}
	if tok.ConsecutiveFailures != 3 {
		t.Errorf("want 3 failures, got %d", tok.ConsecutiveFailures)
	}
	// 2xx 复位
	p.UpdateFromResponse(tok, &http.Response{StatusCode: 200, Header: http.Header{}})
	if tok.ConsecutiveFailures != 0 {
		t.Errorf("after 2xx, ConsecutiveFailures should reset to 0, got %d", tok.ConsecutiveFailures)
	}
}

// TestUpdateFromResponse_4xxNotCounted 4xx (除 401) 不计入 ConsecutiveFailures。
func TestUpdateFromResponse_4xxNotCounted(t *testing.T) {
	p := New([]string{"tok-dddddddddddddddddd"})
	tok := p.tokens[0]
	// 429 / 403 不算 5xx,不应累计 failures
	for i := 0; i < 10; i++ {
		p.UpdateFromResponse(tok, &http.Response{StatusCode: 429, Header: http.Header{}})
	}
	if tok.ConsecutiveFailures != 0 {
		t.Errorf("4xx (not 5xx) should not count as ConsecutiveFailures, got %d", tok.ConsecutiveFailures)
	}
	if tok.Dead {
		t.Error("4xx should not mark Dead")
	}
}

// TestEarliestReset 选所有 alive token 中最早 reset。
func TestEarliestReset(t *testing.T) {
	p := New([]string{"t1", "t2", "t3"})
	p.tokens[0].ResetAt = time.Now().Add(2 * time.Hour)
	p.tokens[1].ResetAt = time.Now().Add(30 * time.Minute) // 最早
	p.tokens[2].ResetAt = time.Now().Add(5 * time.Hour)
	p.tokens[2].Dead = true // dead 跳过

	got := p.EarliestReset()
	if delta := time.Until(got); delta < 25*time.Minute || delta > 35*time.Minute {
		t.Errorf("EarliestReset: want ~30min, got %v", delta)
	}
}

// TestEarliestReset_AllDeadOrNoReset 全部 dead 或 resetAt 零值。
func TestEarliestReset_AllDeadOrNoReset(t *testing.T) {
	p := New([]string{"t1", "t2"})
	p.tokens[0].Dead = true
	p.tokens[1].ResetAt = time.Time{} // 零值
	got := p.EarliestReset()
	if !got.IsZero() {
		t.Errorf("all dead/no-reset: want zero time, got %v", got)
	}
}

// TestStats 验证 Stats 计数。
func TestStats(t *testing.T) {
	p := New([]string{"t1", "t2", "t3", "t4"})
	p.tokens[0].Dead = true
	p.tokens[1].Remaining = 100
	p.tokens[2].Remaining = 50
	p.tokens[3].Remaining = 0 // 不计 totalRemaining

	alive, dead, remaining, _ := p.Stats()
	if alive != 3 {
		t.Errorf("alive: want 3, got %d", alive)
	}
	if dead != 1 {
		t.Errorf("dead: want 1, got %d", dead)
	}
	if remaining != 150 {
		t.Errorf("totalRemaining: want 150, got %d", remaining)
	}
}

// TestMaskToken 脱敏逻辑。
func TestMaskToken(t *testing.T) {
	// 16 字符：12 + 4
	key16 := "abcdefghijkl" + "mnop"
	// 26 字符
	keyLong := "abcdefghijklmnopqrstuvwxyz"

	cases := []struct {
		in, want string
	}{
		{"short", "****"}, // < 16 → 全脱敏
		{key16, "abcdefg****mnop"},
		{keyLong, "abcdefg****wxyz"},
	}
	for _, c := range cases {
		got := maskToken(c.in)
		if got != c.want {
			t.Errorf("maskToken(%q): want %q, got %q", c.in, c.want, got)
		}
	}
}

// TestTrimSpace 验证 trim 处理空格 + 制表符。
func TestTrimSpace(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"\t\t", ""},
		{" \t hello \t ", "hello"},
		{"hello", "hello"},
		{"  hello world  ", "hello world"},
	}
	for _, c := range cases {
		got := trimSpace(c.in)
		if got != c.want {
			t.Errorf("trimSpace(%q): want %q, got %q", c.in, c.want, got)
		}
	}
}
