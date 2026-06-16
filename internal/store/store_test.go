// Package store 的单测。
//
// 覆盖范围：
//  1. createSchema 建表 + 4 个索引存在性
//  2. UpsertRepo 幂等：同 (full_name, since) 第二次写入会更新字段
//  3. GetRepos 多条件：since / lang / limit / 默认 limit 100 / 过滤 is_available=0
//     / 只返回 enriched_at 非空
//  4. GetUnenrichedRepos 倒序按 priority
//  5. UpdateEnriched 字段映射 + stars 单调递增（不会回退）
//  6. MarkUnavailable 翻转 is_available
//  7. RecomputePriorities 三档：top30=100, next70=50, 其余=10
//  8. UpsertLanguages 事务：批量覆写不留旧 / GetLanguages 按 label 排序
//  9. queryRepos Scan 全字段往返（enricher 17 字段 null/空值/正常值都覆盖）
//
// 测试用 in-memory SQLite (`file::memory:?cache=shared`) 即可，无需磁盘 fixture。
// 每个测试独立开新 store,互不污染。
package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dong4j/starcat-trending-api/internal/model"
)

// newTestStore 创建一个临时文件 SQLite + 已建 schema 的 store。
//
// 为什么不用 `:memory:` ?
//
// 	modernc.org/sqlite 的 in-memory 模式在 sql.Open("sqlite", ":memory:") 下，
// 	每次得到的 db handle 是各自独立连接。Store 内部用 MaxOpenConns(1) + 独立 db 实例
// 	能跑通测试，但共享 :memory: 模式需要带 cache=shared + 名字，多线程下行为不稳。
// 	为了简单可靠，统一用 t.TempDir() 起一个 *.db 文件，测试结束自动清理。
func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "trending_test.db")
	s, err := NewSQLiteStore(dsn)
	if err != nil {
		t.Fatalf("NewSQLiteStore(%q) failed: %v", dsn, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// openTestDB 单独打开一份 DB 连接（不通过 store），用于索引存在性等元数据校验。
func openTestDB(t *testing.T, s *SQLiteStore) *sql.DB {
	t.Helper()
	return s.db
}

// TestCreateSchema_Indexes 验证 createSchema 建出的 4 个索引齐全。
//
// 反向索引名 (idx_trending_*) 在 migrations.go 里固定，rename 时这里要同步改。
func TestCreateSchema_Indexes(t *testing.T) {
	s := newTestStore(t)
	db := openTestDB(t, s)

	want := []string{
		"idx_trending_since_captured",
		"idx_trending_gh_repo_id",
		"idx_trending_unenriched",
		"idx_trending_language_since",
	}
	for _, idx := range want {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, idx,
		).Scan(&name)
		if err == sql.ErrNoRows {
			t.Errorf("index %q missing", idx)
		} else if err != nil {
			t.Errorf("query index %q: %v", idx, err)
		}
	}
}

// TestUpsertRepo_InsertAndUpdate 覆盖：
//  1. 首次插入
//  2. 同 (full_name, since) 第二次写入会 UPDATE 字段（不增行）
//  3. 不同 since 的同一 repo 是独立行
func TestUpsertRepo_InsertAndUpdate(t *testing.T) {
	s := newTestStore(t)

	desc1 := "v1 description"
	repo1 := model.TrendingRepo{
		FullName: "owner/repo1", Owner: "owner", Name: "repo1",
		DescText: &desc1, Stars: 100, Forks: 10,
		Language: ptrStr("Go"), Change: 5, Since: "daily",
		CapturedAt: time.Now(), IsAvailable: true,
	}
	if err := s.UpsertRepo(repo1); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// 第二次 upsert:同 (full_name, since) 改 stars
	desc2 := "v2 description"
	repo1Updated := repo1
	repo1Updated.DescText = &desc2
	repo1Updated.Stars = 200
	if err := s.UpsertRepo(repo1Updated); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	// 用 GetUnenrichedRepos 看是否还是 1 行(upsert 不增行)
	un, _ := s.GetUnenrichedRepos(10)
	if len(un) != 1 {
		t.Fatalf("after upsert, want 1 row, got %d", len(un))
	}
	if un[0].Stars != 200 {
		t.Errorf("Stars want=200 got=%d (upsert did not update)", un[0].Stars)
	}
	if un[0].DescText == nil || *un[0].DescText != "v2 description" {
		t.Errorf("DescText want=v2 got=%v", un[0].DescText)
	}

	// enrich 后 GetRepos 才能看到
	enrichedAt := time.Now()
	_ = s.UpdateEnriched("owner/repo1", "daily", model.TrendingRepo{EnrichedAt: &enrichedAt})
	repos, err := s.GetRepos("daily", "", 100)
	if err != nil {
		t.Fatalf("GetRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("GetRepos want 1, got %d", len(repos))
	}

	// 不同 since 应该是独立行
	repo1Weekly := repo1
	repo1Weekly.Since = "weekly"
	repo1Weekly.Stars = 300
	if err := s.UpsertRepo(repo1Weekly); err != nil {
		t.Fatalf("weekly upsert: %v", err)
	}
	// weekly 还没 enrich,只在 unenriched 里看得到
	allUn, _ := s.GetUnenrichedRepos(10)
	if len(allUn) != 1 || allUn[0].Since != "weekly" {
		t.Errorf("weekly upsert: want [weekly], got %v", namesOf(allUn))
	}
}

// TestGetRepos_Filters 验证：
//  1. since 不匹配的不返回
//  2. lang 不匹配的不返回
//  3. is_available=0 的不返回
//  4. enriched_at IS NULL 的不返回
//  5. limit 默认 100
//  6. limit 上限不报错（小于等于 0 走默认 100）
func TestGetRepos_Filters(t *testing.T) {
	s := newTestStore(t)

	// 准备：5 条记录
	// a: daily, go, available, enriched
	// b: daily, swift, available, enriched
	// c: weekly, go, available, enriched
	// d: daily, go, available, NOT enriched (enriched_at IS NULL)
	// e: daily, go, NOT available (MarkUnavailable 后)
	now := time.Now()
	for _, args := range []struct {
		fullName, since, lang string
		enriched              bool
	}{
		{"a/a", "daily", "go", true},
		{"b/b", "daily", "swift", true},
		{"c/c", "weekly", "go", true},
		{"d/d", "daily", "go", false},
		{"e/e", "daily", "go", true},
	} {
		r := model.TrendingRepo{
			FullName: args.fullName, Owner: "o", Name: "n",
			Language: ptrStr(args.lang), Since: args.since,
			CapturedAt: now, IsAvailable: true,
		}
		if err := s.UpsertRepo(r); err != nil {
			t.Fatalf("upsert %s: %v", args.fullName, err)
		}
		if args.enriched {
			enrichedAt := now
			if err := s.UpdateEnriched(args.fullName, args.since, model.TrendingRepo{
				EnrichedAt: &enrichedAt,
			}); err != nil {
				t.Fatalf("update enriched %s: %v", args.fullName, err)
			}
		}
	}
	// e 调 MarkUnavailable 翻成 0
	if err := s.MarkUnavailable("e/e", "daily"); err != nil {
		t.Fatalf("MarkUnavailable e: %v", err)
	}

	// (1) since=daily → a, b (d 不在 enriched, e 被 mark unavailable)
	got, _ := s.GetRepos("daily", "", 100)
	if len(got) != 2 {
		t.Errorf("since=daily want 2, got %d (got: %v)", len(got), namesOf(got))
	}

	// (2) lang=go + since=daily → a (b 是 swift, c 是 weekly, d/e 都不满足)
	got, _ = s.GetRepos("daily", "go", 100)
	if len(got) != 1 || got[0].FullName != "a/a" {
		t.Errorf("since=daily lang=go want [a/a], got %v", namesOf(got))
	}

	// (3) is_available=0 (e) 不返
	all, _ := s.GetRepos("", "", 100)
	for _, r := range all {
		if !r.IsAvailable {
			t.Errorf("is_available=0 leaked: %s", r.FullName)
		}
	}

	// (4) enriched_at IS NULL (d) 不返
	for _, r := range all {
		if r.FullName == "d/d" {
			t.Errorf("unenriched row leaked: %s", r.FullName)
		}
	}

	// (5)(6) limit <= 0 走默认 100
	if _, err := s.GetRepos("daily", "", 0); err != nil {
		t.Errorf("limit=0 should fall back to default, got %v", err)
	}
	if _, err := s.GetRepos("daily", "", -1); err != nil {
		t.Errorf("limit=-1 should fall back to default, got %v", err)
	}
}

// TestGetUnenrichedRepos 验证只返 enriched_at IS NULL，按 priority desc。
func TestGetUnenrichedRepos(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()

	// 3 条都未 enrich,priority 分别是 0/50/100
	for i, p := range []int{0, 50, 100} {
		r := model.TrendingRepo{
			FullName: "owner/r" + itoa(i), Owner: "owner", Name: "r" + itoa(i),
			Since: "daily", CapturedAt: now, IsAvailable: true,
			EnrichPriority: p,
		}
		if err := s.UpsertRepo(r); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	got, _ := s.GetUnenrichedRepos(10)
	if len(got) != 3 {
		t.Fatalf("want 3 unenriched, got %d", len(got))
	}
	// priority 100 应在第一位
	if got[0].EnrichPriority != 100 {
		t.Errorf("first should be priority=100, got %d", got[0].EnrichPriority)
	}
}

// TestUpdateEnriched 验证字段映射 + stars 单调递增保护。
func TestUpdateEnriched(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()

	// 先 upsert 一条 stars=100
	r := model.TrendingRepo{
		FullName: "owner/repo", Owner: "owner", Name: "repo",
		Stars: 100, Since: "daily", CapturedAt: now, IsAvailable: true,
	}
	if err := s.UpsertRepo(r); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// enrich 后传 stars=80（应该不覆盖,因为 CASE WHEN ? > stars THEN ?）
	enriched := model.TrendingRepo{
		GhRepoID:      ptrInt64(12345),
		Description:   ptrStr("github desc"),
		Homepage:      ptrStr("https://example.com"),
		LicenseSpdx:   ptrStr("MIT"),
		Watchers:      50,
		Subscribers:   5,
		OwnerAvatar:   ptrStr("https://avatars.githubusercontent.com/u/1"),
		IsArchived:    false,
		IsFork:        false,
		IsPrivate:     false,
		DefaultBranch: ptrStr("main"),
		OpenIssues:    3,
		PushedAt:      ptrStr("2026-06-10T00:00:00Z"),
		UpdatedAt:     ptrStr("2026-06-10T00:00:00Z"),
		CreatedAt:     ptrStr("2025-01-01T00:00:00Z"),
		Language:      ptrStr("Rust"),
		Stars:         80, // 比 100 小,应该不覆盖
	}
	if err := s.UpdateEnriched("owner/repo", "daily", enriched); err != nil {
		t.Fatalf("UpdateEnriched: %v", err)
	}

	got, _ := s.GetUnenrichedRepos(10)
	if len(got) != 0 {
		// enriched_at 应该被设置了
	}

	// 用 GetRepos 强制只返 enriched 行,确认 enrich 字段写进去了
	repos, _ := s.GetRepos("daily", "", 10)
	if len(repos) != 1 {
		t.Fatalf("want 1 enriched, got %d", len(repos))
	}
	r1 := repos[0]
	if r1.Stars != 100 {
		t.Errorf("stars should stay 100 (CASE WHEN protect), got %d", r1.Stars)
	}
	if r1.GhRepoID == nil || *r1.GhRepoID != 12345 {
		t.Errorf("gh_repo_id want 12345, got %v", r1.GhRepoID)
	}
	if r1.Description == nil || *r1.Description != "github desc" {
		t.Errorf("description want 'github desc', got %v", r1.Description)
	}
	if r1.LicenseSpdx == nil || *r1.LicenseSpdx != "MIT" {
		t.Errorf("license_spdx want MIT, got %v", r1.LicenseSpdx)
	}
	if r1.EnrichedAt == nil {
		t.Errorf("enriched_at should be set")
	}

	// 第二轮 enrich:stars=150 应该覆盖
	enriched2 := enriched
	enriched2.Stars = 150
	if err := s.UpdateEnriched("owner/repo", "daily", enriched2); err != nil {
		t.Fatalf("UpdateEnriched round 2: %v", err)
	}
	repos, _ = s.GetRepos("daily", "", 10)
	if repos[0].Stars != 150 {
		t.Errorf("stars should update to 150, got %d", repos[0].Stars)
	}
}

// TestMarkUnavailable 翻转 is_available=0。
func TestMarkUnavailable(t *testing.T) {
	s := newTestStore(t)
	r := model.TrendingRepo{
		FullName: "x/y", Owner: "x", Name: "y",
		Since: "daily", CapturedAt: time.Now(), IsAvailable: true,
	}
	if err := s.UpsertRepo(r); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// 先 enrich 让它能在 GetRepos 出现
	now := time.Now()
	_ = s.UpdateEnriched("x/y", "daily", model.TrendingRepo{EnrichedAt: &now, Description: ptrStr("d")})

	pre, _ := s.GetRepos("daily", "", 10)
	if len(pre) != 1 {
		t.Fatalf("setup: want 1 enriched row, got %d", len(pre))
	}

	if err := s.MarkUnavailable("x/y", "daily"); err != nil {
		t.Fatalf("MarkUnavailable: %v", err)
	}

	post, _ := s.GetRepos("daily", "", 10)
	if len(post) != 0 {
		t.Errorf("after MarkUnavailable, GetRepos should return 0, got %d (%v)", len(post), namesOf(post))
	}
}

// TestRecomputePriorities 三档：top30=100, next70=50, 其余=10。
func TestRecomputePriorities(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()

	// 准备 5 条未 enrich
	for i := 0; i < 5; i++ {
		r := model.TrendingRepo{
			FullName: "owner/r" + itoa(i), Owner: "owner", Name: "r" + itoa(i),
			Since: "daily", CapturedAt: now.Add(time.Duration(i) * time.Second),
			IsAvailable: true,
		}
		if err := s.UpsertRepo(r); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	// 5 条 < 30,理论上全部 100
	if err := s.RecomputePriorities("daily"); err != nil {
		t.Fatalf("RecomputePriorities: %v", err)
	}
	got, _ := s.GetUnenrichedRepos(10)
	if len(got) != 5 {
		t.Fatalf("want 5 unenriched, got %d", len(got))
	}
	for _, r := range got {
		if r.EnrichPriority != 100 {
			t.Errorf("with 5 rows, all should be 100, got %d for %s", r.EnrichPriority, r.FullName)
		}
	}
}

// TestUpsertLanguages 事务覆写 + GetLanguages 按 label 排序。
func TestUpsertLanguages(t *testing.T) {
	s := newTestStore(t)

	// 第一批
	if err := s.UpsertLanguages([]model.Language{
		{Key: "go", Label: "Go"},
		{Key: "python", Label: "Python"},
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// 第二批（覆写,不留旧）
	if err := s.UpsertLanguages([]model.Language{
		{Key: "rust", Label: "Rust"},
		{Key: "swift", Label: "Swift"},
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, _ := s.GetLanguages()
	if len(got) != 2 {
		t.Fatalf("want 2 langs, got %d", len(got))
	}
	// 按 label 排序:Rust, Swift
	if got[0].Label != "Rust" || got[1].Label != "Swift" {
		t.Errorf("want [Rust, Swift], got [%s, %s]", got[0].Label, got[1].Label)
	}
}

// TestGetAggregatedLanguages_BasicGrouping 验证：
//  1. 仅返回 enriched + available 的 repo 对应的语言
//  2. NULL / "" language 归到 __uncategorized__ 一桶
//  3. 排序（dong4j 2026-06-16 调整）：**未分类排第 1 位**；其余按 count DESC, key ASC
//  4. 同一 repo 在 daily / weekly 两个 since 都有行 → 仍按行数累加（这是当前设计：
//     一个 repo 上多个 since 的命中视为多次 trending 出现，count 叠加）
func TestGetAggregatedLanguages_BasicGrouping(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()

	// fixture：构造覆盖以下场景的数据
	//   - Go / Python 各 2 条（count = 2）
	//   - Rust 1 条（count = 1）
	//   - language=NULL 1 条 + language="" 1 条（uncategorized count = 2）
	//   - 1 条 unenriched + 1 条 unavailable，不应该计入聚合
	type seed struct {
		fullName    string
		lang        *string // nil = NULL；&"" 表示 ""
		enriched    bool
		isAvailable bool
		since       string
	}
	emptyStr := ""
	seeds := []seed{
		{"a/go1", ptrStr("Go"), true, true, "daily"},
		{"a/go2", ptrStr("Go"), true, true, "daily"},
		{"a/py1", ptrStr("Python"), true, true, "daily"},
		{"a/py2", ptrStr("Python"), true, true, "weekly"},
		{"a/rs1", ptrStr("Rust"), true, true, "daily"},
		{"a/un1", nil, true, true, "daily"},          // NULL
		{"a/un2", &emptyStr, true, true, "weekly"},   // 空串
		{"a/un3", nil, false, true, "daily"},         // 未 enrich → 不计
		{"a/un4", ptrStr("Java"), true, false, "daily"}, // unavailable → 不计
	}
	for _, sd := range seeds {
		r := model.TrendingRepo{
			FullName: sd.fullName, Owner: "a", Name: sd.fullName[2:],
			Language: sd.lang, Since: sd.since,
			CapturedAt: now, IsAvailable: true,
		}
		if err := s.UpsertRepo(r); err != nil {
			t.Fatalf("seed %s: %v", sd.fullName, err)
		}
		if sd.enriched {
			enrichedAt := now
			// UpdateEnriched 会写 language = COALESCE(?, language)：传 nil 不改、传值会覆盖
			// 这里需要保留 fixture 的 language（特别是 NULL / "" 这两种「未分类」case）。
			// 关键：enricher 字段里 Language=nil 会跳过 COALESCE 这条，保留 spider 入库时的值；
			// fixture 用 emptyStr 时也要原样保留——直接传 sd.lang 即可。
			if err := s.UpdateEnriched(sd.fullName, sd.since, model.TrendingRepo{
				EnrichedAt: &enrichedAt,
				Language:   sd.lang,
			}); err != nil {
				t.Fatalf("update enriched %s: %v", sd.fullName, err)
			}
		}
		if !sd.isAvailable {
			if err := s.MarkUnavailable(sd.fullName, sd.since); err != nil {
				t.Fatalf("mark unavailable %s: %v", sd.fullName, err)
			}
		}
	}

	got, err := s.GetAggregatedLanguages()
	if err != nil {
		t.Fatalf("GetAggregatedLanguages: %v", err)
	}

	// 期望排序（SQL 排序键: is_uncategorized DESC, count DESC, key ASC）：
	//   - is_uncategorized=1 的排在最前面（未分类永远第 1 位 — dong4j 2026-06-16 调整）
	//   - 普通语言里按 count DESC：Go(2), Python(2), Rust(1)
	//   - count 相同时按 key ASC：Go < Python（字典序）
	// 最终：__uncategorized__(2) → Go(2) → Python(2) → Rust(1)
	wantKeys := []string{model.UncategorizedLanguageKey, "Go", "Python", "Rust"}
	wantCounts := []int{2, 2, 2, 1}
	if len(got) != len(wantKeys) {
		t.Fatalf("want %d aggregates, got %d (%+v)", len(wantKeys), len(got), got)
	}
	for i := range wantKeys {
		if got[i].Key != wantKeys[i] {
			t.Errorf("aggregates[%d].Key: want %q, got %q", i, wantKeys[i], got[i].Key)
		}
		if got[i].Count != wantCounts[i] {
			t.Errorf("aggregates[%d].Count for %q: want %d, got %d",
				i, wantKeys[i], wantCounts[i], got[i].Count)
		}
	}

	// 未分类项的 label 必须是 model.UncategorizedLanguageLabel(2026-06-16 起排第 1 位)
	first := got[0]
	if first.Key != model.UncategorizedLanguageKey {
		t.Errorf("first item should be uncategorized, got key=%q", first.Key)
	}
	if first.Label != model.UncategorizedLanguageLabel {
		t.Errorf("uncategorized label: want %q, got %q",
			model.UncategorizedLanguageLabel, first.Label)
	}

	// 普通语言的 label 应该等于 key(取 got[1]: 第 2 位是普通语言里 count 最大的 Go)
	if got[1].Label != got[1].Key {
		t.Errorf("non-uncategorized label should equal key, got key=%q label=%q",
			got[1].Key, got[1].Label)
	}
}

// TestGetAggregatedLanguages_EmptyTable 验证空表返 [] 而非 nil。
func TestGetAggregatedLanguages_EmptyTable(t *testing.T) {
	s := newTestStore(t)
	got, err := s.GetAggregatedLanguages()
	if err != nil {
		t.Fatalf("GetAggregatedLanguages: %v", err)
	}
	if got == nil {
		t.Errorf("GetAggregatedLanguages on empty table should return [], got nil")
	}
	if len(got) != 0 {
		t.Errorf("empty table: want 0 aggregates, got %d (%+v)", len(got), got)
	}
}

// TestGetAggregatedLanguages_OnlyUncategorized 验证表里全是 NULL 时仅返一项 uncategorized。
func TestGetAggregatedLanguages_OnlyUncategorized(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	for i := 0; i < 3; i++ {
		r := model.TrendingRepo{
			FullName: "a/u" + itoa(i), Owner: "a", Name: "u" + itoa(i),
			Language: nil, Since: "daily",
			CapturedAt: now, IsAvailable: true,
		}
		if err := s.UpsertRepo(r); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		enrichedAt := now
		if err := s.UpdateEnriched(r.FullName, "daily", model.TrendingRepo{
			EnrichedAt: &enrichedAt,
		}); err != nil {
			t.Fatalf("enrich: %v", err)
		}
	}
	got, err := s.GetAggregatedLanguages()
	if err != nil {
		t.Fatalf("GetAggregatedLanguages: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 aggregate, got %d (%+v)", len(got), got)
	}
	if got[0].Key != model.UncategorizedLanguageKey {
		t.Errorf("only item should be uncategorized, got %q", got[0].Key)
	}
	if got[0].Count != 3 {
		t.Errorf("count: want 3, got %d", got[0].Count)
	}
}

// TestGetRepos_UncategorizedSentinel 验证 lang=__uncategorized__ 触发 NULL/'' 过滤。
func TestGetRepos_UncategorizedSentinel(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	emptyStr := ""

	// 准备：3 条记录
	//   a: language=Go     enriched
	//   b: language=NULL   enriched
	//   c: language=""     enriched
	cases := []struct {
		name string
		lang *string
	}{
		{"a/go", ptrStr("Go")},
		{"b/null", nil},
		{"c/empty", &emptyStr},
	}
	for _, c := range cases {
		r := model.TrendingRepo{
			FullName: c.name, Owner: "x", Name: "n",
			Language: c.lang, Since: "daily",
			CapturedAt: now, IsAvailable: true,
		}
		if err := s.UpsertRepo(r); err != nil {
			t.Fatalf("upsert %s: %v", c.name, err)
		}
		enrichedAt := now
		if err := s.UpdateEnriched(c.name, "daily", model.TrendingRepo{
			EnrichedAt: &enrichedAt,
			Language:   c.lang, // 保留 NULL / ""
		}); err != nil {
			t.Fatalf("enrich %s: %v", c.name, err)
		}
	}

	// (1) lang="" → 不过滤，3 条都返
	all, err := s.GetRepos("daily", "", 100)
	if err != nil {
		t.Fatalf("GetRepos all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("lang='' should return all 3, got %d (%v)", len(all), namesOf(all))
	}

	// (2) lang="Go" → 仅 a/go
	goRepos, err := s.GetRepos("daily", "Go", 100)
	if err != nil {
		t.Fatalf("GetRepos Go: %v", err)
	}
	if len(goRepos) != 1 || goRepos[0].FullName != "a/go" {
		t.Errorf("lang=Go: want [a/go], got %v", namesOf(goRepos))
	}

	// (3) lang=__uncategorized__ → b/null + c/empty
	uncat, err := s.GetRepos("daily", model.UncategorizedLanguageKey, 100)
	if err != nil {
		t.Fatalf("GetRepos uncategorized: %v", err)
	}
	if len(uncat) != 2 {
		t.Errorf("lang=uncategorized: want 2, got %d (%v)", len(uncat), namesOf(uncat))
	}
	gotNames := map[string]bool{}
	for _, r := range uncat {
		gotNames[r.FullName] = true
	}
	if !gotNames["b/null"] || !gotNames["c/empty"] {
		t.Errorf("lang=uncategorized: want {b/null, c/empty}, got %v", namesOf(uncat))
	}
}

// TestUpsertLanguages_Empty 不报错,清空表。
func TestUpsertLanguages_Empty(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertLanguages([]model.Language{{Key: "go", Label: "Go"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.UpsertLanguages(nil); err != nil {
		t.Fatalf("empty upsert: %v", err)
	}
	got, _ := s.GetLanguages()
	if len(got) != 0 {
		t.Errorf("empty upsert should clear table, got %d", len(got))
	}
}

// TestQueryRepos_FieldRoundTrip 端到端字段往返：upsert 复杂行 → 查回 → 所有字段一致。
//
// 覆盖 enrich 17 字段的所有 *string / *int64 / 普通类型的 NULL/正常值。
func TestQueryRepos_FieldRoundTrip(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()

	ghID := int64(9999)
	desc := "github description"
	homepage := "https://example.com"
	license := "Apache-2.0"
	topics := `["cli","parser"]`
	avatar := "https://avatars.githubusercontent.com/u/1"
	defBranch := "main"
	pushed := "2026-06-09T00:00:00Z"
	updated := "2026-06-10T00:00:00Z"
	created := "2024-01-01T00:00:00Z"
	lang := "Go"

	r := model.TrendingRepo{
		FullName: "owner/repo", Owner: "owner", Name: "repo",
		DescText: ptrStr("raw desc"), Stars: 100, Forks: 10,
		Language: &lang, Change: 5,
		BuildByJSON: ptrStr(`[{"by":"/alice","avatar":"https://x/a.png"}]`),
		// enricher 字段
		GhRepoID:      &ghID,
		Description:   &desc,
		Homepage:      &homepage,
		LicenseSpdx:   &license,
		TopicsJSON:    &topics,
		Watchers:      200,
		Subscribers:   20,
		OwnerAvatar:   &avatar,
		IsArchived:    true,
		IsFork:        false,
		IsPrivate:     true,
		DefaultBranch: &defBranch,
		OpenIssues:    7,
		PushedAt:      &pushed,
		UpdatedAt:     &updated,
		CreatedAt:     &created,
		// 元数据
		Since:       "daily",
		CapturedAt:  now,
		IsAvailable: true,
	}
	if err := s.UpsertRepo(r); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// enrich
	enrichedAt := now
	if err := s.UpdateEnriched("owner/repo", "daily", r); err != nil {
		t.Fatalf("UpdateEnriched: %v", err)
	}
	_ = enrichedAt

	got, _ := s.GetRepos("daily", "", 10)
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	g := got[0]

	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"FullName", g.FullName, "owner/repo"},
		{"Owner", g.Owner, "owner"},
		{"Name", g.Name, "repo"},
		{"DescText", derefStr(g.DescText), "raw desc"},
		{"Stars", g.Stars, 100},
		{"Forks", g.Forks, 10},
		{"Language", derefStr(g.Language), "Go"},
		{"Change", g.Change, 5},
		{"GhRepoID", derefInt64(g.GhRepoID), int64(9999)},
		{"Description", derefStr(g.Description), "github description"},
		{"Homepage", derefStr(g.Homepage), "https://example.com"},
		{"LicenseSpdx", derefStr(g.LicenseSpdx), "Apache-2.0"},
		{"TopicsJSON", derefStr(g.TopicsJSON), `["cli","parser"]`},
		{"Watchers", g.Watchers, 200},
		{"Subscribers", g.Subscribers, 20},
		{"OwnerAvatar", derefStr(g.OwnerAvatar), "https://avatars.githubusercontent.com/u/1"},
		{"IsArchived", g.IsArchived, true},
		{"IsFork", g.IsFork, false},
		{"IsPrivate", g.IsPrivate, true},
		{"DefaultBranch", derefStr(g.DefaultBranch), "main"},
		{"OpenIssues", g.OpenIssues, 7},
		{"PushedAt", derefStr(g.PushedAt), pushed},
		{"UpdatedAt", derefStr(g.UpdatedAt), updated},
		{"CreatedAt", derefStr(g.CreatedAt), created},
		{"Since", g.Since, "daily"},
		{"IsAvailable", g.IsAvailable, true},
		{"BuildByJSON", derefStr(g.BuildByJSON), `[{"by":"/alice","avatar":"https://x/a.png"}]`},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: want %v, got %v", c.name, c.want, c.got)
		}
	}
}

// TestClose 验证 Close 后再操作会报错（连接已断）。
func TestClose(t *testing.T) {
	s := newTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.UpsertRepo(model.TrendingRepo{FullName: "x/y", Since: "daily"}); err == nil {
		t.Error("UpsertRepo after Close should fail")
	}
}

// --- helpers ---

func ptrStr(s string) *string { return &s }
func ptrInt64(i int64) *int64 { return &i }
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
func namesOf(repos []model.TrendingRepo) []string {
	out := make([]string, 0, len(repos))
	for _, r := range repos {
		out = append(out, r.FullName)
	}
	return out
}
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	const digits = "0123456789"
	if i < 0 {
		return "-" + itoa(-i)
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = digits[i%10]
		i /= 10
	}
	return string(buf[pos:])
}
