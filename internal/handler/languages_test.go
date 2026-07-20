// Package handler 的 endpoint 测试：languages.go
//
// 覆盖 HandleLanguagesV1 的 4 个场景：
//  1. 正常返回聚合列表 + meta.cache_status=fresh
//  2. 空表返 [] + meta.cache_status=cold
//  3. store 错误 → 500
//  4. 响应结构兼容性：data 是 []LanguageAggregate（含 key/label/count）
package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/starcat-app/starcat-trending-api/internal/model"
)

func doLanguagesReq(s *fakeStore) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/languages", nil)
	HandleLanguagesV1(s)(w, r)
	return w
}

// TestLanguages_Fresh 验证有数据时 cache_status=fresh，data 透传 store 聚合结果。
func TestLanguages_Fresh(t *testing.T) {
	f := &fakeStore{
		aggregates: []model.LanguageAggregate{
			{Key: "Go", Label: "Go", Count: 10},
			{Key: "Python", Label: "Python", Count: 7},
			{Key: model.UncategorizedLanguageKey, Label: model.UncategorizedLanguageLabel, Count: 2},
		},
	}
	w := doLanguagesReq(f)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	env := decodeEnvelope[[]model.LanguageAggregate](t, w)
	if env.Meta == nil {
		t.Fatalf("meta should be present")
	}
	if env.Meta.CacheStatus != "fresh" {
		t.Errorf("cache_status: want fresh, got %q", env.Meta.CacheStatus)
	}
	if env.Meta.Total != 3 {
		t.Errorf("meta.total: want 3, got %d", env.Meta.Total)
	}
	if len(env.Data) != 3 {
		t.Fatalf("data: want 3 items, got %d", len(env.Data))
	}
	// 顺序保留（store 已经排好）
	if env.Data[0].Key != "Go" || env.Data[1].Key != "Python" {
		t.Errorf("order: want [Go, Python, ...], got [%s, %s, ...]", env.Data[0].Key, env.Data[1].Key)
	}
	// 未分类项 label 不应被覆盖
	last := env.Data[len(env.Data)-1]
	if last.Key != model.UncategorizedLanguageKey {
		t.Errorf("last item should be uncategorized, got %q", last.Key)
	}
	if last.Label != model.UncategorizedLanguageLabel {
		t.Errorf("uncategorized label: want %q, got %q",
			model.UncategorizedLanguageLabel, last.Label)
	}
	if last.Count != 2 {
		t.Errorf("uncategorized count: want 2, got %d", last.Count)
	}
}

// TestLanguages_Cold 验证空表返 [] 和 cache_status=cold。
func TestLanguages_Cold(t *testing.T) {
	f := &fakeStore{aggregates: []model.LanguageAggregate{}}
	w := doLanguagesReq(f)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	env := decodeEnvelope[[]model.LanguageAggregate](t, w)
	if env.Meta == nil || env.Meta.CacheStatus != "cold" {
		t.Errorf("cache_status: want cold, got %+v", env.Meta)
	}
	if len(env.Data) != 0 {
		t.Errorf("data: want 0 items, got %d", len(env.Data))
	}
}

// TestLanguages_StoreError 验证 store 出错时返 500 + INTERNAL_ERROR。
func TestLanguages_StoreError(t *testing.T) {
	f := &fakeStore{forceAggregateErr: errors.New("db locked")}
	w := doLanguagesReq(f)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", w.Code)
	}
	env := decodeErrorEnv(t, w)
	if env.Error.Code != "INTERNAL_ERROR" {
		t.Errorf("code: want INTERNAL_ERROR, got %q", env.Error.Code)
	}
}
