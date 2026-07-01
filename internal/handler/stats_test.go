package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dong4j/starcat-trending-api/internal/model"
)

func doStatsReq(f *fakeStore) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/internal/stats", nil)
	HandleStatsV1(f)(w, r)
	return w
}

func TestStats_ReturnsRealRepoCountsAndLanguageCount(t *testing.T) {
	f := &fakeStore{
		counts: map[string]int{"daily": 137, "weekly": 82, "monthly": 41},
		aggregates: []model.LanguageAggregate{
			{Key: "Go", Label: "Go", Count: 20},
			{Key: "Swift", Label: "Swift", Count: 12},
		},
	}

	w := doStatsReq(f)
	if w.Code != http.StatusOK {
		t.Fatalf("stats status: want 200, got %d body=%s", w.Code, w.Body.String())
	}

	env := decodeEnvelope[TrendingStatsResponse](t, w)
	if env.Data.Repos["daily"] != 137 || env.Data.Repos["weekly"] != 82 || env.Data.Repos["monthly"] != 41 || env.Data.Repos["total"] != 260 {
		t.Fatalf("repo counts mismatch: %+v", env.Data.Repos)
	}
	if env.Data.Languages != 2 {
		t.Fatalf("languages count: want 2, got %d", env.Data.Languages)
	}
	if env.Meta == nil || env.Meta.CacheStatus != "fresh" {
		t.Fatalf("meta cache_status: want fresh, got %+v", env.Meta)
	}
}

func TestStats_ColdWhenRepoCountsEmpty(t *testing.T) {
	f := &fakeStore{
		counts:     map[string]int{"daily": 0, "weekly": 0, "monthly": 0},
		aggregates: []model.LanguageAggregate{},
	}

	w := doStatsReq(f)
	env := decodeEnvelope[TrendingStatsResponse](t, w)
	if env.Meta == nil || env.Meta.CacheStatus != "cold" {
		t.Fatalf("meta cache_status: want cold, got %+v", env.Meta)
	}
}

func TestStats_CountError(t *testing.T) {
	f := &fakeStore{forceCountErr: errors.New("db down")}
	w := doStatsReq(f)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", w.Code)
	}
	env := decodeErrorEnv(t, w)
	if env.Error.Code != "INTERNAL_ERROR" {
		t.Fatalf("error code: want INTERNAL_ERROR, got %s", env.Error.Code)
	}
}
