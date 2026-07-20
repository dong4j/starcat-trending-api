// Package handler 提供 HTTP handler 公共工具。
//
// R-01 v1.2: envelope 包装器 + JSON 响应 + 错误响应统一输出。
package handler

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/starcat-app/starcat-trending-api/internal/model"
)

// writeJSON 将任意类型包装成 envelope 写入 200 响应。
func writeJSON[T any](w http.ResponseWriter, data T) {
	writeJSONWithMeta(w, data, nil)
}

// writeJSONWithMeta 将任意类型包装成 envelope + meta 写入 200 响应。
func writeJSONWithMeta[T any](w http.ResponseWriter, data T, meta *model.Meta) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	env := model.Envelope[T]{
		SchemaVersion: 1,
		Data:          data,
		Meta:          meta,
	}
	if err := json.NewEncoder(w).Encode(env); err != nil {
		log.Printf("[handler] failed to encode envelope: %v", err)
	}
}

// writeError 写统一 error envelope 响应。
// status 是 HTTP 状态码（如 400 / 404 / 500），code 是 SCREAMING_SNAKE_CASE 错误码。
// details 是结构化补充信息（map / struct / nil），按错误码自由扩展。
func writeError(w http.ResponseWriter, status int, code, message string, details interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	env := model.ErrorEnvelope{
		SchemaVersion: 1,
		Error: model.ErrorResponse{
			Code:    code,
			Message: message,
			Details: details,
		},
	}
	if err := json.NewEncoder(w).Encode(env); err != nil {
		log.Printf("[handler] failed to encode error envelope: %v", err)
	}
}
