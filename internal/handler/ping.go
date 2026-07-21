// Package handler 公共 helper 已在 handler.go 定义；本文件只新增 ping endpoint。
//
// HandlePingV1 是 R-03（2026-06-11）新增的「Starcat 客户端测试连接」专用端点，
// 用于替代之前两阶段探测（`/healthz` + 业务 endpoint）的尴尬方案。
//
// 为什么需要它（设计意图）：
//   - `/healthz` 不带鉴权，无法验证客户端 Bearer Key 是否正确
//   - 之前借用业务 endpoint 作 auth probe 会有奇葩副作用 ——
//     sharing 复用 POST `/api/v1/share` 的 GET 触发 405、wiki 复用 `/api/v1/wikis`
//     缺 owner/repo 返 400，客户端要写各种特殊判定
//   - ping 是契约语义明确的、专给客户端用的"鉴权 + 连通性"双重探测端点
//
// 行为：
//   - GET /api/v1/ping，带 Bearer Auth（由 middleware 保护）
//   - 鉴权通过 → 200 + envelope { data: { service, version, ok: true } }
//   - 鉴权失败 → 401（由 middleware 写）
//   - 服务故障 → 5xx（一般不会，本 handler 几乎不可能失败）
//
// 跨项目同步约定：本文件除 import 路径外，必须在 trending / weekly / sharing / wiki
// 四个 API 项目中保持 **byte-level 一致**（与 `middleware/auth.go` 同款约定）。
// 修改时必须同步 4 份。

package handler

import (
	"net/http"
)

// pingResponse 是 /api/v1/ping 的 data 段结构。
// 用 inline 类型避免污染 model 包；service 字段便于客户端日志看到具体服务名。
type pingResponse struct {
	Service string `json:"service"`
	Version string `json:"version"`
	OK      bool   `json:"ok"`
}

// HandlePingV1 暴露 GET /api/v1/ping，专给 Starcat 客户端「测试连接」按钮用。
//
// service 参数标识当前服务；serviceVersion 来自构建时注入的 Git tag 版本。
func HandlePingV1(service, serviceVersion string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, pingResponse{
			Service: service,
			Version: serviceVersion,
			OK:      true,
		})
	}
}
