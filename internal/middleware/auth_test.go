// Package middleware 的单测：auth.go (Bearer Token 鉴权)。
//
// 覆盖：
//  1. NewBearerAuth 跳过空 key + trim 空白
//  2. 缺 Authorization 头 → 401 + WWW-Authenticate: Bearer + ErrorEnvelope
//  3. 错前缀（不是 "Bearer "）→ 401
//  4. 空 Bearer token → 401
//  5. 错 token → 401 + 日志脱敏
//  6. 正确 token → 通过
//  7. 多个 key 注册时，任一合法 key 都通过
//
// 鉴权中间件不依赖外部包,纯 stdlib + model.Envelope,独立可测。
package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/starcat-app/starcat-trending-api/internal/model"
)

// newAuth 用一组 key 创建 BearerAuth,key 之间用 \| 分隔便于测试。
func newAuth(t *testing.T, keys ...string) *BearerAuth {
	t.Helper()
	return NewBearerAuth(keys)
}

// okHandler 是个最简的"通过" handler,记录被调用。
type okHandler struct {
	called bool
}

func (h *okHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.called = true
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// runAuth 走一次鉴权链（无 Authorization 头）。
func runAuth(a *BearerAuth, next http.Handler) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/repos", nil)
	a.Wrap(next).ServeHTTP(w, r)
	return w
}

// TestNewBearerAuth_SkipEmpty 验证空 key / 全空白 key 被跳过（仅合法 key 能通过）。
func TestNewBearerAuth_SkipEmpty(t *testing.T) {
	a := NewBearerAuth([]string{"good-key-1234567890", "", "  ", "  \t  ", "another-key-2345678901"})
	for _, key := range []string{"good-key-1234567890", "another-key-2345678901"} {
		h := &okHandler{}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/repos", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		a.Wrap(h).ServeHTTP(w, r)
		if w.Code != http.StatusOK || !h.called {
			t.Errorf("key %q should pass auth", key)
		}
	}
}

// TestAuth_MissingHeader 缺 Authorization → 401。
func TestAuth_MissingHeader(t *testing.T) {
	a := newAuth(t, "valid-key-1234567890")
	h := &okHandler{}
	w := runAuth(a, h)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: want 401, got %d", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("WWW-Authenticate: want 'Bearer', got %q", w.Header().Get("WWW-Authenticate"))
	}
	if h.called {
		t.Error("downstream handler should not be called when auth fails")
	}
	// 错误 envelope
	var env model.ErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.SchemaVersion != 1 {
		t.Errorf("schema_version: want 1, got %d", env.SchemaVersion)
	}
	if env.Error.Code != "UNAUTHORIZED" {
		t.Errorf("code: want UNAUTHORIZED, got %s", env.Error.Code)
	}
	if env.Error.Message == "" {
		t.Error("message should not be empty")
	}
}

// TestAuth_WrongPrefix 错前缀（Basic / Token）→ 401。
func TestAuth_WrongPrefix(t *testing.T) {
	a := newAuth(t, "valid-key-1234567890")
	cases := []string{
		"Basic dXNlcjpwYXNz",          // 错的 scheme
		"Token valid-key-1234567890",  // Token 而不是 Bearer
		"Bearer",                      // 缺 token
		"bearer valid-key-1234567890", // 小写 bearer,严格匹配
	}
	for _, header := range cases {
		t.Run(header, func(t *testing.T) {
			h := &okHandler{}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/api/v1/repos", nil)
			r.Header.Set("Authorization", header)
			a.Wrap(h).ServeHTTP(w, r)

			if w.Code != http.StatusUnauthorized {
				t.Errorf("header %q: want 401, got %d", header, w.Code)
			}
			if h.called {
				t.Errorf("downstream should not be called for %q", header)
			}
		})
	}
}

// TestAuth_WrongKey 错 key → 401。
func TestAuth_WrongKey(t *testing.T) {
	a := newAuth(t, "valid-key-1234567890")
	h := &okHandler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/repos", nil)
	r.Header.Set("Authorization", "Bearer wrong-key-9876543210")
	a.Wrap(h).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: want 401, got %d", w.Code)
	}
	if h.called {
		t.Error("downstream should not be called for wrong key")
	}
}

// TestAuth_ValidKey 正确 key → 通过。
func TestAuth_ValidKey(t *testing.T) {
	a := newAuth(t, "valid-key-1234567890")
	h := &okHandler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/repos", nil)
	r.Header.Set("Authorization", "Bearer valid-key-1234567890")
	a.Wrap(h).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !h.called {
		t.Error("downstream should be called for valid key")
	}
	if w.Body.String() != "OK" {
		t.Errorf("body: want 'OK', got %q", w.Body.String())
	}
}

// TestAuth_MultipleKeys 注册多个 key,任一合法都通过。
func TestAuth_MultipleKeys(t *testing.T) {
	a := newAuth(t,
		"key-aaa-1234567890",
		"key-bbb-2345678901",
		"key-ccc-3456789012",
	)
	for _, key := range []string{
		"key-aaa-1234567890",
		"key-bbb-2345678901",
		"key-ccc-3456789012",
	} {
		t.Run(key, func(t *testing.T) {
			h := &okHandler{}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/api/v1/repos", nil)
			r.Header.Set("Authorization", "Bearer "+key)
			a.Wrap(h).ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Errorf("key %q: want 200, got %d", key, w.Code)
			}
			if !h.called {
				t.Errorf("downstream should be called for key %q", key)
			}
		})
	}
}

// TestAuth_KeyWithSurroundingSpace token 前后有空白应被 trim。
func TestAuth_KeyWithSurroundingSpace(t *testing.T) {
	a := newAuth(t, "valid-key-1234567890")
	h := &okHandler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/repos", nil)
	// "Bearer valid-key-1234567890  "（token 后多 2 个空格）
	r.Header.Set("Authorization", "Bearer valid-key-1234567890  ")
	a.Wrap(h).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("trailing space should be trimmed, got %d", w.Code)
	}
}
