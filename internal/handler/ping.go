// Package handler 的 ping 端点已收敛到 starcat-api-kit/httputil。
package handler

import (
	"net/http"

	"github.com/starcat-app/starcat-api-kit/httputil"
)

// HandlePingV1 暴露 GET /api/v1/ping，专给 Starcat 客户端「测试连接」按钮用。
func HandlePingV1(service, serviceVersion string) http.HandlerFunc {
	return httputil.HandlePingV1(service, serviceVersion)
}
