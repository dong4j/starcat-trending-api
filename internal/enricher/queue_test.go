// Package enricher 的 EnrichQueue 单测。
//
// 覆盖：
//  1. NewEnrichQueue: workerCnt <= 0 走默认 2
//  2. NewEnrichQueue: workerCnt > 0 透传
//  3. Start 启动 N 个 worker（workerCount = workerCnt）
//  4. 重复调 Start 不会启两轮 worker（幂等）
//  5. Stop 关闭所有 worker,等待当前任务完成
//  6. Stats 透传 enrich 计数
//
// 注意：不测 worker 实际拉数据 / 调 EnrichOne 的行为（那是 enricher/github.go 的范畴，
// 那个需要 mock GitHub API 服务器，单独一个测试文件做）。
package enricher

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/starcat-app/starcat-trending-api/internal/store"
)

// TestNewEnrichQueue_DefaultWorkerCount 验证 workerCnt <= 0 走默认 2。
func TestNewEnrichQueue_DefaultWorkerCount(t *testing.T) {
	for _, n := range []int{0, -1, -100} {
		q := NewEnrichQueue(nil, n)
		if q.workerCnt != 2 {
			t.Errorf("NewEnrichQueue(_, %d): want workerCnt=2, got %d", n, q.workerCnt)
		}
	}
}

// TestNewEnrichQueue_CustomWorkerCount 验证 workerCnt > 0 透传。
func TestNewEnrichQueue_CustomWorkerCount(t *testing.T) {
	q := NewEnrichQueue(nil, 4)
	if q.workerCnt != 4 {
		t.Errorf("want workerCnt=4, got %d", q.workerCnt)
	}
}

// TestEnrichQueue_StartStop 验证 Start 启动 worker + Stop 关闭。
//
// 需要给 enricher 配一个真实 store（worker 第一行就 deref enricher.store），
// 用空 SQLite 表模拟：worker 拉不到 repo,进 sleep 循环,等 Stop 关闭。
func TestEnrichQueue_StartStop(t *testing.T) {
	q := makeTestQueue(t, 2)
	q.Start()
	time.Sleep(100 * time.Millisecond) // 让 worker 跑一会儿

	// Stop 必须能正常返回,不能 hang
	done := make(chan struct{})
	go func() {
		q.Stop()
		close(done)
	}()
	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s")
	}
}

// TestEnrichQueue_StartIdempotent 重复调 Start 不会启两轮 worker。
//
// 通过查看 running 状态验证：第二调用是 no-op,worker 数不翻倍。
func TestEnrichQueue_StartIdempotent(t *testing.T) {
	q := makeTestQueue(t, 1)
	q.Start()
	q.Start() // 第二次
	q.Start() // 第三次

	time.Sleep(50 * time.Millisecond)

	q.mu.Lock()
	running := q.running
	q.mu.Unlock()
	if !running {
		t.Error("queue should be running after Start")
	}

	// 关键验证:Stop 必须能正确结束(只有一轮 worker,否则 wait group 等不到)
	done := make(chan struct{})
	go func() {
		q.Stop()
		close(done)
	}()
	select {
	case <-done:
		// OK — Stop 在 2s 内返回
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after 3x Start (extra workers leaked)")
	}
}

// TestEnrichQueue_StopWithoutStart 验证 Stop 在没 Start 时不 panic。
func TestEnrichQueue_StopWithoutStart(t *testing.T) {
	q := makeTestQueue(t, 2)
	// 不调 Start,直接 Stop,应当立即返回
	done := make(chan struct{})
	go func() {
		q.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop without Start should be instant")
	}
}

// TestEnrichQueue_Stats 验证 Stats 返回计数（初始 0）。
func TestEnrichQueue_Stats(t *testing.T) {
	q := makeTestQueue(t, 1)
	e, f := q.Stats()
	if e != 0 || f != 0 {
		t.Errorf("initial stats: want 0/0, got %d/%d", e, f)
	}
}

// TestEnrichQueue_StartStopConcurrent 验证 Start 和 Stop 并发不竞态。
func TestEnrichQueue_StartStopConcurrent(t *testing.T) {
	q := makeTestQueue(t, 3)

	var ops int32
	done := make(chan struct{})

	// 一个 goroutine 反复 Start
	go func() {
		for i := 0; i < 20; i++ {
			q.Start()
			atomic.AddInt32(&ops, 1)
		}
	}()

	// 另一个等会儿后 Stop
	time.Sleep(50 * time.Millisecond)
	go func() {
		q.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatalf("concurrent Start/Stop did not finish (ops=%d)", atomic.LoadInt32(&ops))
	}
}

// makeTestQueue 构造一个 EnrichQueue + 配 nil-safe enricher + 临时 SQLite store。
//
// worker 内部会调 q.enricher.store.GetUnenrichedRepos,所以 enricher.store 必须非 nil。
// enricher 自身的 tryAcquire / EnrichOne 在没有 repo 的情况下不会触发。
func makeTestQueue(t *testing.T, workerCnt int) *EnrichQueue {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "queue_test.db")
	st, err := store.NewSQLiteStore(dsn)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rl := NewRateLimitHandler(1 * time.Millisecond) // 测试时压缩间隔
	enc := New(st, nil, rl)
	return NewEnrichQueue(enc, workerCnt)
}
