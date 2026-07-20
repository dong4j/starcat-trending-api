// Package middleware 提供 Bearer Token 鉴权中间件。
//
// R-01 v1.2: 所有 /api/v1/* 和 /internal/* 端点必须携带
// Authorization: Bearer <api-key> 头。
// 与 trending / sharing byte-level 一致（详见 supports/docs/R-01-总体设计.md §3.4 + §4.1）。
//
// ⚠️ 跨项目共享代码同步约定：本文件必须在 trending / weekly / sharing / wiki 四个 API
// 中 byte-level 一致（仅 package import 路径不同），任何修改都必须同时同步 3 份。
package middleware

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/starcat-app/starcat-trending-api/internal/model"
)

// BearerAuth 持有 API Key 白名单，验证 Bearer Token。
type BearerAuth struct {
	allowedKeys map[string]bool
}

// NewBearerAuth 创建 Bearer 鉴权中间件。
// keys 是从 API_KEYS env 解析的白名单列表（逗号分隔，已 trim 空白）。
func NewBearerAuth(keys []string) *BearerAuth {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k != "" {
			m[k] = true
		}
	}
	log.Printf("[auth] %d keys loaded", len(m))
	return &BearerAuth{allowedKeys: m}
}

// Wrap 返回一个 http.Handler，在执行业务 handler 前验证 Bearer Token。
// 鉴权失败返回 401 + WWW-Authenticate: Bearer + envelope 错误响应。
func (a *BearerAuth) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")

		if authHeader == "" {
			writeAuthError(w, "missing Authorization header")
			return
		}

		if !strings.HasPrefix(authHeader, "Bearer ") {
			writeAuthError(w, "expected 'Bearer <token>' format")
			return
		}

		token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
		if !a.allowedKeys[token] {
			log.Printf("[auth] rejected key %s", maskKey(token))
			writeAuthError(w, "invalid API key")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// writeAuthError 写 401 响应，必须带 WWW-Authenticate: Bearer + envelope 错误 JSON。
func writeAuthError(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(model.ErrorEnvelope{
		SchemaVersion: 1,
		Error: model.ErrorResponse{
			Code:    "UNAUTHORIZED",
			Message: msg,
		},
	})
}

// maskKey 脱敏日志输出：显示前 7 + 末 4 字符，中间星号。
func maskKey(key string) string {
	if len(key) < 16 {
		return "****"
	}
	return key[:7] + "****" + key[len(key)-4:]
}
