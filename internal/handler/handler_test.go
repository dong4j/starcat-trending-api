// Package handler 的单测（envelope + error 响应工具）。
//
// 覆盖：
//  1. writeJSON: 顶层 envelope {schema_version, data}（meta=nil 时不输出 meta 字段）
//  2. writeJSONWithMeta: 顶层 envelope {schema_version, data, meta}，meta 字段按 omitempty
//  3. writeError: 非 2xx 响应，schema_version=1，error 包装 {code, message, details}
//  4. writeError: details=nil 时不输出 details 字段（omitempty 生效）
//  5. writeError: 任意 details 类型（map / slice / struct）正确序列化
//
// 注意：本包是 internal/handler,不依赖 store/spider,所以可以独立单测。
// 不测具体的 endpoint 行为（repos.go / languages.go 等），那些放 repos_test.go。
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/starcat-app/starcat-trending-api/internal/model"
)

// TestWriteJSON_NilMeta 验证 meta=nil 时不输出 meta 字段。
func TestWriteJSON_NilMeta(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, []string{"a", "b"})

	if w.Code != http.StatusOK {
		t.Errorf("status: want 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type: want 'application/json; charset=utf-8', got %q", ct)
	}

	// 解码 envelope
	var env model.Envelope[[]string]
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body: %s)", err, w.Body.String())
	}
	if env.SchemaVersion != 1 {
		t.Errorf("schema_version: want 1, got %d", env.SchemaVersion)
	}
	if len(env.Data) != 2 || env.Data[0] != "a" || env.Data[1] != "b" {
		t.Errorf("data: want [a, b], got %v", env.Data)
	}
	if env.Meta != nil {
		t.Errorf("meta: want nil, got %+v", env.Meta)
	}

	// 原始 JSON 应该不包含 "meta" 字段
	raw := w.Body.String()
	if containsField(raw, "meta") {
		t.Errorf("raw JSON should not contain 'meta' field when nil, got: %s", raw)
	}
}

// TestWriteJSONWithMeta_AllFields 验证 meta 全字段写出。
func TestWriteJSONWithMeta_AllFields(t *testing.T) {
	w := httptest.NewRecorder()
	nextPage := 2
	writeJSONWithMeta(w, []int{1, 2, 3}, &model.Meta{
		Page:        1,
		PageSize:    10,
		Total:       3,
		NextPage:    &nextPage,
		Since:       "daily",
		Language:    "go",
		GeneratedAt: "2026-06-10T12:00:00Z",
		CacheStatus: "fresh",
		FetchedAt:   "2026-06-10T11:00:00Z",
	})

	if w.Code != http.StatusOK {
		t.Errorf("status: want 200, got %d", w.Code)
	}

	var env model.Envelope[[]int]
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.SchemaVersion != 1 {
		t.Errorf("schema_version: want 1, got %d", env.SchemaVersion)
	}
	if env.Meta == nil {
		t.Fatalf("meta should not be nil")
	}
	m := env.Meta
	if m.Page != 1 || m.PageSize != 10 || m.Total != 3 {
		t.Errorf("pagination: want 1/10/3, got %d/%d/%d", m.Page, m.PageSize, m.Total)
	}
	if m.NextPage == nil || *m.NextPage != 2 {
		t.Errorf("next_page: want 2, got %v", m.NextPage)
	}
	if m.Since != "daily" || m.Language != "go" {
		t.Errorf("since/language: want daily/go, got %s/%s", m.Since, m.Language)
	}
	if m.CacheStatus != "fresh" {
		t.Errorf("cache_status: want fresh, got %s", m.CacheStatus)
	}
}

// TestWriteJSONWithMeta_Omitempty 验证 zero-value 字段不输出。
func TestWriteJSONWithMeta_Omitempty(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSONWithMeta(w, map[string]string{"k": "v"}, &model.Meta{
		// 全部留空（zero value）
	})

	var env model.Envelope[map[string]string]
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Meta == nil {
		t.Fatalf("meta should not be nil (写出来的是 {} 不会变 nil)")
	}

	// 原始 JSON 应该不包含 since / language / total 等字段
	raw := w.Body.String()
	for _, field := range []string{"page", "page_size", "total", "next_page", "since", "language", "generated_at", "cache_status", "fetched_at"} {
		if containsField(raw, field) {
			t.Errorf("raw JSON should not contain zero-value field %q, got: %s", field, raw)
		}
	}
}

// TestWriteError_Basic 验证 4xx/5xx envelope 形态。
func TestWriteError_Basic(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusBadRequest, "BAD_REQUEST", "since invalid", map[string]interface{}{
		"param":   "since",
		"got":     "yearly",
		"allowed": []string{"daily", "weekly", "monthly"},
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: want 400, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type: want 'application/json; charset=utf-8', got %q", ct)
	}

	var env model.ErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.SchemaVersion != 1 {
		t.Errorf("schema_version: want 1, got %d", env.SchemaVersion)
	}
	if env.Error.Code != "BAD_REQUEST" {
		t.Errorf("code: want BAD_REQUEST, got %s", env.Error.Code)
	}
	if env.Error.Message != "since invalid" {
		t.Errorf("message: want 'since invalid', got %q", env.Error.Message)
	}
	// details 是 map,断言关键字段
	detailsMap, ok := env.Error.Details.(map[string]interface{})
	if !ok {
		t.Fatalf("details type: want map, got %T", env.Error.Details)
	}
	if detailsMap["param"] != "since" {
		t.Errorf("details.param: want 'since', got %v", detailsMap["param"])
	}
}

// TestWriteError_NilDetails 验证 details=nil 时不输出 details 字段。
func TestWriteError_NilDetails(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "boom", nil)

	var env model.ErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Details != nil {
		t.Errorf("details: want nil, got %v", env.Error.Details)
	}
	raw := w.Body.String()
	if containsField(raw, "details") {
		t.Errorf("raw JSON should not contain 'details' when nil, got: %s", raw)
	}
}

// TestWriteError_DifferentStatusCodes 验证 status code 透传。
func TestWriteError_DifferentStatusCodes(t *testing.T) {
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusBadRequest, "BAD_REQUEST"},
		{http.StatusUnauthorized, "UNAUTHORIZED"},
		{http.StatusForbidden, "FORBIDDEN"},
		{http.StatusNotFound, "NOT_FOUND"},
		{http.StatusInternalServerError, "INTERNAL_ERROR"},
		{http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
	}
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeError(w, c.status, c.code, "test message", nil)
			if w.Code != c.status {
				t.Errorf("status: want %d, got %d", c.status, w.Code)
			}
		})
	}
}

// containsField 简单判断 JSON 字符串里是否含 "field": 字段。
//
// 用 string.Contains 是够用的近似,因为我们只测自己写的 envelope,不会出现奇怪的 key。
func containsField(jsonStr, field string) bool {
	// 简单匹配 "field":
	return contains(jsonStr, `"`+field+`":`)
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
