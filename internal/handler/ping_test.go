// Package handler 的 ping endpoint 单测。
//
// 覆盖 R-03（2026-06-11）新增的 /api/v1/ping 端点：
//  1. handler 返回 200 + Content-Type JSON
//  2. envelope 结构正确：schema_version=1，data.service=入参，data.ok=true
//  3. 不携带 meta 字段（omitempty 生效）
//  4. method 默认走 GET 即可（mux 路由匹配由 main.go 装配）
//
// 注意：本测试不测 Bearer Auth 行为——那由 middleware/auth_test.go 覆盖。
// HandlePingV1 本身假设上游已经通过 middleware 鉴权（调用即说明鉴权过了）。
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/starcat-app/starcat-trending-api/internal/model"
)

// TestHandlePingV1_OK 验证返回 200 + 正确 envelope。
func TestHandlePingV1_OK(t *testing.T) {
	h := HandlePingV1("trending")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type: want 'application/json; charset=utf-8', got %q", ct)
	}

	// 用 anonymous struct 解码 envelope.data —— 比额外引入 PingResponse 类型简洁。
	type pingData struct {
		Service string `json:"service"`
		OK      bool   `json:"ok"`
	}
	var env model.Envelope[pingData]
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body: %s)", err, w.Body.String())
	}

	if env.SchemaVersion != 1 {
		t.Errorf("schema_version: want 1, got %d", env.SchemaVersion)
	}
	if env.Data.Service != "trending" {
		t.Errorf("data.service: want 'trending', got %q", env.Data.Service)
	}
	if !env.Data.OK {
		t.Errorf("data.ok: want true, got false")
	}
	if env.Meta != nil {
		t.Errorf("meta: want nil for ping, got %+v", env.Meta)
	}
}

// TestHandlePingV1_ServiceNamePassthrough 验证 service 入参原样写入 response，
// 没有把不同服务搞错（防止 4 个项目复制时漏改服务名）。
func TestHandlePingV1_ServiceNamePassthrough(t *testing.T) {
	cases := []string{"trending", "weekly", "sharing", "wiki", "custom-fork"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			h := HandlePingV1(name)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			type pingData struct {
				Service string `json:"service"`
				OK      bool   `json:"ok"`
			}
			var env model.Envelope[pingData]
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if env.Data.Service != name {
				t.Errorf("service: want %q, got %q", name, env.Data.Service)
			}
		})
	}
}
