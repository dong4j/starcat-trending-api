// Package handler 的 endpoint 测试：repos.go
//
// 覆盖 HandleReposV1 的核心 query 场景：
//  1. 正常返回（默认 since=daily, 无 lang, 无 limit）
//  2. since=daily / weekly / monthly
//  3. since 非法值（yearly）→ 400
//  4. source=github 拒绝 → 400 + 引导 weekly-api
//  5. source=zread 拒绝 → 400 + 引导 weekly-api
//  6. lang 过滤透传
//  7. limit 上限 clamp 到 100
//  8. 缓存状态：返回 0 条时 cacheStatus=cold，否则 fresh
//  9. 内部 store 错误 → 500
//
// R-06.2 新增缓存场景：
//  10. cache hit：第二次同 query 不再触达 store（callCount 仍为 1）
//  11. ETag 304：cache hit + 客户端带匹配 If-None-Match → 304 + 无 body
//  12. Invalidate 后强制走 store：cache miss 重建
//  13. Invalidate 的桶仅影响对应 since（清 daily 不动 weekly）
//
// fakeStore 实现 store.Store interface,只覆盖 GetRepos 调用路径，
// 其他方法 panic（不调）。
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dong4j/starcat-trending-api/internal/model"
	"github.com/dong4j/starcat-trending-api/internal/store"
)

// fakeStore 是 store.Store 的最小实现,只支持 GetRepos / GetAggregatedLanguages 的可观测调用。
type fakeStore struct {
	repos       []model.TrendingRepo
	gotSince    string
	gotLang     string
	gotLimit    int
	callCount   int
	forceGetErr error

	// GetAggregatedLanguages 用：fakeStore 本身就是 mock，按 fixture 直接返
	aggregates        []model.LanguageAggregate
	forceAggregateErr error
}

func (f *fakeStore) GetRepos(since, lang string, limit int) ([]model.TrendingRepo, error) {
	f.callCount++
	f.gotSince = since
	f.gotLang = lang
	f.gotLimit = limit
	if f.forceGetErr != nil {
		return nil, f.forceGetErr
	}
	return f.repos, nil
}

// 其它方法不调,panic 提示
func (f *fakeStore) UpsertRepo(r model.TrendingRepo) error {
	panic(fmt.Sprintf("UpsertRepo should not be called in handler test, got %s/%s", r.FullName, r.Since))
}
func (f *fakeStore) GetUnenrichedRepos(limit int) ([]model.TrendingRepo, error) {
	panic("GetUnenrichedRepos not used in handler test")
}
func (f *fakeStore) UpdateEnriched(fullName, since string, r model.TrendingRepo) error {
	panic("UpdateEnriched not used in handler test")
}
func (f *fakeStore) MarkUnavailable(fullName, since string) error {
	panic("MarkUnavailable not used in handler test")
}
func (f *fakeStore) RecomputePriorities(since string) error {
	panic("RecomputePriorities not used in handler test")
}
func (f *fakeStore) ResetAllEnriched() error {
	panic("ResetAllEnriched not used in handler test")
}
func (f *fakeStore) UpsertLanguages(langs []model.Language) error {
	panic("UpsertLanguages not used in handler test")
}
func (f *fakeStore) GetLanguages() ([]model.Language, error) {
	panic("GetLanguages not used in handler test")
}
func (f *fakeStore) GetAggregatedLanguages() ([]model.LanguageAggregate, error) {
	if f.forceAggregateErr != nil {
		return nil, f.forceAggregateErr
	}
	return f.aggregates, nil
}
func (f *fakeStore) Close() error { return nil }

// 不实现的接口：让 fakeStore 类型上仍然实现 store.Store,必须补完所有方法
// 注：上面已经全部定义。

// 编译期断言 fakeStore 实现了 store.Store
var _ store.Store = (*fakeStore)(nil)

// 辅助：构造一条 enriched repo（UpsertRepo 不传 is_available,这里手工设置）
func makeRepo(name, lang string, stars int) model.TrendingRepo {
	langPtr := &lang
	desc := "desc of " + name
	enriched := time.Now()
	return model.TrendingRepo{
		FullName:   "owner/" + name,
		Owner:      "owner",
		Name:       name,
		DescText:   &desc,
		Stars:      stars,
		Forks:      stars / 10,
		Language:   langPtr,
		Change:     1,
		Since:      "daily",
		CapturedAt: time.Now(),
		EnrichedAt: &enriched,
		IsAvailable: true,
	}
}

// doReq 调 HandleReposV1 走一次 HTTP，每个调用用一个**独立的新 cache**——
// 让原有用例与 cache 状态无关（避免跨用例污染）。
func doReq(s store.Store, query string) *httptest.ResponseRecorder {
	return doReqWith(s, NewTrendingCache(), query, nil)
}

// doReqWith 让需要复用同一缓存 / 自定义 header 的测试用例显式注入 cache 和 header。
// headers nil 时不加任何额外 header。
func doReqWith(s store.Store, c *TrendingCache, query string, headers http.Header) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/repos?"+query, nil)
	for k, vv := range headers {
		for _, v := range vv {
			r.Header.Add(k, v)
		}
	}
	HandleReposV1(s, c)(w, r)
	return w
}

// decodeEnvelope 解码 envelope 到具体 data 类型。
func decodeEnvelope[T any](t *testing.T, w *httptest.ResponseRecorder) model.Envelope[T] {
	t.Helper()
	var env model.Envelope[T]
	if err := decodeJSON(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body: %s)", err, w.Body.String())
	}
	return env
}

func decodeErrorEnv(t *testing.T, w *httptest.ResponseRecorder) model.ErrorEnvelope {
	t.Helper()
	var env model.ErrorEnvelope
	if err := decodeJSON(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v (body: %s)", err, w.Body.String())
	}
	return env
}

// TestRepos_DefaultSince 验证默认 since=daily。
func TestRepos_DefaultSince(t *testing.T) {
	f := &fakeStore{
		repos: []model.TrendingRepo{makeRepo("r1", "go", 100)},
	}
	w := doReq(f, "")
	env := decodeEnvelope[[]model.StarcatRepoCardDTO](t, w)
	if env.Meta == nil || env.Meta.Since != "daily" {
		t.Errorf("default since: want daily, got %+v", env.Meta)
	}
	if f.gotSince != "daily" {
		t.Errorf("store got since: want daily, got %s", f.gotSince)
	}
}

// TestRepos_ValidSince 验证 3 个合法 since 都通过。
func TestRepos_ValidSince(t *testing.T) {
	for _, since := range []string{"daily", "weekly", "monthly"} {
		t.Run(since, func(t *testing.T) {
			f := &fakeStore{}
			w := doReq(f, "since="+since)
			if w.Code != http.StatusOK {
				t.Errorf("since=%s should be accepted, got %d", since, w.Code)
			}
			if f.gotSince != since {
				t.Errorf("store got since: want %s, got %s", since, f.gotSince)
			}
		})
	}
}

// TestRepos_InvalidSince 验证非法 since → 400。
func TestRepos_InvalidSince(t *testing.T) {
	f := &fakeStore{}
	w := doReq(f, "since=yearly")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("since=yearly: want 400, got %d", w.Code)
	}
	env := decodeErrorEnv(t, w)
	if env.Error.Code != "BAD_REQUEST" {
		t.Errorf("code: want BAD_REQUEST, got %s", env.Error.Code)
	}
	details, ok := env.Error.Details.(map[string]interface{})
	if !ok {
		t.Fatalf("details type: %T", env.Error.Details)
	}
	if details["param"] != "since" {
		t.Errorf("details.param: want since, got %v", details["param"])
	}
	if details["got"] != "yearly" {
		t.Errorf("details.got: want yearly, got %v", details["got"])
	}
	if f.callCount != 0 {
		t.Errorf("store should not be called on invalid since, got %d calls", f.callCount)
	}
}

// TestRepos_SourceRejected 验证 source= 任何值都拒绝。
func TestRepos_SourceRejected(t *testing.T) {
	cases := []string{"github", "zread", "merged", ""} // 注意 "" 是默认值,不会拒绝
	for _, src := range cases[:3] {                      // github / zread / merged
		t.Run("source="+src, func(t *testing.T) {
			f := &fakeStore{}
			w := doReq(f, "source="+src)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("source=%s: want 400, got %d", src, w.Code)
			}
			env := decodeErrorEnv(t, w)
			if env.Error.Code != "BAD_REQUEST" {
				t.Errorf("code: want BAD_REQUEST, got %s", env.Error.Code)
			}
			// 错误信息必须引导 weekly-api
			if !strings.Contains(env.Error.Message, "weekly-api") {
				t.Errorf("error message should mention weekly-api, got: %q", env.Error.Message)
			}
			if f.callCount != 0 {
				t.Errorf("store should not be called on rejected source, got %d calls", f.callCount)
			}
		})
	}
}

// TestRepos_LangFilter 透传 lang 参数。
func TestRepos_LangFilter(t *testing.T) {
	f := &fakeStore{}
	w := doReq(f, "lang=swift")
	if w.Code != http.StatusOK {
		t.Fatalf("lang=swift: want 200, got %d", w.Code)
	}
	if f.gotLang != "swift" {
		t.Errorf("store got lang: want swift, got %s", f.gotLang)
	}
	env := decodeEnvelope[[]model.StarcatRepoCardDTO](t, w)
	if env.Meta == nil || env.Meta.Language != "swift" {
		t.Errorf("meta.language: want swift, got %+v", env.Meta)
	}
}

// TestRepos_LimitClampTo100 验证 limit > 100 被 clamp。
func TestRepos_LimitClampTo100(t *testing.T) {
	f := &fakeStore{}
	w := doReq(f, "limit=500")
	if w.Code != http.StatusOK {
		t.Fatalf("limit=500: want 200, got %d", w.Code)
	}
	if f.gotLimit != 100 {
		t.Errorf("limit should clamp to 100, store got %d", f.gotLimit)
	}
}

// TestRepos_LimitCustom 验证 limit 正常值透传。
func TestRepos_LimitCustom(t *testing.T) {
	f := &fakeStore{}
	doReq(f, "limit=30")
	if f.gotLimit != 30 {
		t.Errorf("limit=30: store got %d, want 30", f.gotLimit)
	}
}

// TestRepos_CacheStatusFresh 验证有数据时 cacheStatus=fresh。
func TestRepos_CacheStatusFresh(t *testing.T) {
	f := &fakeStore{
		repos: []model.TrendingRepo{makeRepo("r1", "go", 100)},
	}
	w := doReq(f, "")
	env := decodeEnvelope[[]model.StarcatRepoCardDTO](t, w)
	if env.Meta == nil || env.Meta.CacheStatus != "fresh" {
		t.Errorf("cacheStatus: want fresh, got %+v", env.Meta)
	}
}

// TestRepos_CacheStatusCold 验证 0 条时 cacheStatus=cold。
func TestRepos_CacheStatusCold(t *testing.T) {
	f := &fakeStore{repos: nil}
	w := doReq(f, "")
	env := decodeEnvelope[[]model.StarcatRepoCardDTO](t, w)
	if env.Meta == nil || env.Meta.CacheStatus != "cold" {
		t.Errorf("cacheStatus: want cold, got %+v", env.Meta)
	}
}

// TestRepos_StoreError 验证 store 错误 → 500。
func TestRepos_StoreError(t *testing.T) {
	f := &fakeStore{forceGetErr: errors.New("db locked")}
	w := doReq(f, "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("store error: want 500, got %d", w.Code)
	}
	env := decodeErrorEnv(t, w)
	if env.Error.Code != "INTERNAL_ERROR" {
		t.Errorf("code: want INTERNAL_ERROR, got %s", env.Error.Code)
	}
}

// TestRepos_CardDTOConversion 验证 repo → card DTO 转换（含 contributors 解析）。
func TestRepos_CardDTOConversion(t *testing.T) {
	buildBy := `[{"by":"/alice","avatar":"https://x/a.png"},{"by":"/bob","avatar":"https://x/b.png"}]`
	r := makeRepo("r1", "go", 100)
	r.BuildByJSON = &buildBy
	r.Description = ptrStr("github official desc") // enricher 写的 description
	f := &fakeStore{repos: []model.TrendingRepo{r}}
	w := doReq(f, "")
	env := decodeEnvelope[[]model.StarcatRepoCardDTO](t, w)
	if len(env.Data) != 1 {
		t.Fatalf("want 1 card, got %d", len(env.Data))
	}
	card := env.Data[0]
	if card.FullName != "owner/r1" {
		t.Errorf("full_name: want owner/r1, got %s", card.FullName)
	}
	if card.Language == nil || *card.Language != "go" {
		t.Errorf("language: want go, got %v", card.Language)
	}
	if card.Description == nil || *card.Description != "github official desc" {
		t.Errorf("description should use github desc, got %v", card.Description)
	}
	if card.HTMLURL == nil || *card.HTMLURL != "https://github.com/owner/r1" {
		t.Errorf("html_url: want https://github.com/owner/r1, got %v", card.HTMLURL)
	}
	// Trending 扩展段
	if card.Trending == nil {
		t.Fatalf("trending extension should be present")
	}
	if card.Trending.Change != 1 {
		t.Errorf("trending.change: want 1, got %d", card.Trending.Change)
	}
	if len(card.Trending.Contributors) != 2 {
		t.Errorf("contributors: want 2, got %d", len(card.Trending.Contributors))
	}
	// By 字段 "/alice" 去掉前缀成 "alice"
	if card.Trending.Contributors[0].Login != "alice" {
		t.Errorf("contributor[0].login: want alice, got %s", card.Trending.Contributors[0].Login)
	}
}

// TestRepos_DescriptionFallbackToDescText 验证 description 空时回退到 desc_text。
func TestRepos_DescriptionFallbackToDescText(t *testing.T) {
	r := makeRepo("r1", "go", 100)
	r.Description = nil // enricher 还没写
	f := &fakeStore{repos: []model.TrendingRepo{r}}
	w := doReq(f, "")
	env := decodeEnvelope[[]model.StarcatRepoCardDTO](t, w)
	card := env.Data[0]
	if card.Description == nil || *card.Description != "desc of r1" {
		t.Errorf("description should fall back to desc_text, got %v", card.Description)
	}
}

// --- R-06.2 cache 行为测试 ---

// TestRepos_CacheHitSkipsStore：同 query 第二次请求应该 cache 命中，
// 不再触达 store（callCount 维持 1）。
func TestRepos_CacheHitSkipsStore(t *testing.T) {
	f := &fakeStore{repos: []model.TrendingRepo{makeRepo("r1", "go", 100)}}
	c := NewTrendingCache()

	w1 := doReqWith(f, c, "since=daily&lang=go&limit=10", nil)
	if w1.Code != http.StatusOK {
		t.Fatalf("first call: want 200, got %d", w1.Code)
	}
	if f.callCount != 1 {
		t.Fatalf("first call should hit store once, got callCount=%d", f.callCount)
	}

	w2 := doReqWith(f, c, "since=daily&lang=go&limit=10", nil)
	if w2.Code != http.StatusOK {
		t.Fatalf("second call: want 200, got %d", w2.Code)
	}
	if f.callCount != 1 {
		t.Errorf("second call should NOT hit store (cache hit), callCount=%d", f.callCount)
	}
	// body 一致
	if w1.Body.String() != w2.Body.String() {
		t.Errorf("cache-hit response body should be identical to first call")
	}
	// 必带 ETag / Last-Modified header
	if w2.Header().Get("ETag") == "" {
		t.Errorf("cache-hit response should expose ETag header")
	}
	if w2.Header().Get("Last-Modified") == "" {
		t.Errorf("cache-hit response should expose Last-Modified header")
	}
}

// TestRepos_ETagReturns304：客户端带匹配的 If-None-Match 时返回 304 + 无 body。
func TestRepos_ETagReturns304(t *testing.T) {
	f := &fakeStore{repos: []model.TrendingRepo{makeRepo("r1", "go", 100)}}
	c := NewTrendingCache()

	w1 := doReqWith(f, c, "since=daily", nil)
	etag := w1.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("first call should set ETag")
	}

	headers := http.Header{}
	headers.Set("If-None-Match", etag)
	w2 := doReqWith(f, c, "since=daily", headers)

	if w2.Code != http.StatusNotModified {
		t.Errorf("matching If-None-Match: want 304, got %d", w2.Code)
	}
	if w2.Body.Len() != 0 {
		t.Errorf("304 response should have empty body, got %d bytes", w2.Body.Len())
	}
	if f.callCount != 1 {
		t.Errorf("304 path should still come from cache, store callCount=%d", f.callCount)
	}
}

// TestRepos_InvalidateForcesRefetch：Invalidate 后下次请求强制走 store 重建 cache。
func TestRepos_InvalidateForcesRefetch(t *testing.T) {
	f := &fakeStore{repos: []model.TrendingRepo{makeRepo("r1", "go", 100)}}
	c := NewTrendingCache()

	_ = doReqWith(f, c, "since=daily", nil)
	if f.callCount != 1 {
		t.Fatalf("first call: callCount=%d, want 1", f.callCount)
	}

	c.Invalidate("daily")

	_ = doReqWith(f, c, "since=daily", nil)
	if f.callCount != 2 {
		t.Errorf("after Invalidate: store should be hit again, callCount=%d (want 2)", f.callCount)
	}
}

// TestRepos_InvalidateOtherBucketUnaffected：Invalidate("daily") 不应清掉 weekly 的 cache。
func TestRepos_InvalidateOtherBucketUnaffected(t *testing.T) {
	f := &fakeStore{repos: []model.TrendingRepo{makeRepo("r1", "go", 100)}}
	c := NewTrendingCache()

	_ = doReqWith(f, c, "since=daily", nil)
	_ = doReqWith(f, c, "since=weekly", nil)
	if f.callCount != 2 {
		t.Fatalf("after two fresh calls: callCount=%d, want 2", f.callCount)
	}

	c.Invalidate("daily")

	// weekly 应仍然命中
	_ = doReqWith(f, c, "since=weekly", nil)
	if f.callCount != 2 {
		t.Errorf("weekly should still be cached after Invalidate(daily), callCount=%d (want 2)", f.callCount)
	}
	// daily 应需要重新查 store
	_ = doReqWith(f, c, "since=daily", nil)
	if f.callCount != 3 {
		t.Errorf("daily should refetch after Invalidate(daily), callCount=%d (want 3)", f.callCount)
	}
}

// TestCacheTTLFor 验证各 since 的 TTL。
func TestCacheTTLFor(t *testing.T) {
	cases := []struct {
		since string
		want  time.Duration
	}{
		{"daily", 1 * time.Hour},
		{"weekly", 6 * time.Hour},
		{"monthly", 24 * time.Hour},
		{"unknown", 1 * time.Hour}, // fallback
	}
	for _, tc := range cases {
		t.Run(tc.since, func(t *testing.T) {
			if got := TTLFor(tc.since); got != tc.want {
				t.Errorf("TTLFor(%s) = %s, want %s", tc.since, got, tc.want)
			}
		})
	}
}

// TestCacheInvalidateAll 验证 InvalidateAll 清空所有 entry。
func TestCacheInvalidateAll(t *testing.T) {
	c := NewTrendingCache()
	c.Set("daily|*|100", []byte(`{"data":[]}`))
	c.Set("weekly|*|100", []byte(`{"data":[]}`))
	if c.Size() != 2 {
		t.Fatalf("after 2 sets, size=%d", c.Size())
	}
	c.InvalidateAll()
	if c.Size() != 0 {
		t.Errorf("InvalidateAll: size=%d, want 0", c.Size())
	}
}

// --- helpers ---

func ptrStr(s string) *string { return &s }

func decodeJSON(b []byte, v interface{}) error {
	return json.Unmarshal(b, v)
}
