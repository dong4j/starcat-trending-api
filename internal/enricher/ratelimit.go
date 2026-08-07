// Package enricher 提供 GitHub API 字段补全 + Rate Limit 退避逻辑。
//
// RateLimitHandler 已收敛到 starcat-api-kit/github；本文件保留类型别名以兼容
// server / 测试引用。
package enricher

import (
	"time"

	kitgithub "github.com/starcat-app/starcat-api-kit/github"
)

// RateLimitHandler 透传 kit 限流器。
type RateLimitHandler = kitgithub.RateLimitHandler

// NewRateLimitHandler 创建速率限制处理器。
func NewRateLimitHandler(minInterval time.Duration) *RateLimitHandler {
	return kitgithub.NewRateLimitHandler(minInterval)
}
