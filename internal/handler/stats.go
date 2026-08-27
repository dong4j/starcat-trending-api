// Package handler 中的 stats.go 实现 GET /internal/stats。
//
// 这个 endpoint 专给本地运维面板和轻量监控读取聚合数据。它不能复用
// /api/v1/repos 的 meta.total，因为 repos endpoint 受 limit 钳制，meta.total
// 表示本次返回条数而不是真实总量。
package handler

import (
	"net/http"
	"time"

	"github.com/starcat-app/starcat-trending-api/internal/model"
	"github.com/starcat-app/starcat-trending-api/internal/store"
)

// TrendingStatsResponse 是 trending-api 的真实统计摘要。
type TrendingStatsResponse struct {
	Repos       map[string]int         `json:"repos"`
	Languages   int                    `json:"languages"`
	Operational store.OperationalStats `json:"operational"`
}

// HandleStatsV1 GET /internal/stats - 返回真实 DB 聚合统计。
func HandleStatsV1(s store.StatsStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		repoCounts, err := s.CountReposBySince()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR",
				"failed to count repos: "+err.Error(), nil)
			return
		}

		languages, err := s.GetAggregatedLanguages()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR",
				"failed to aggregate languages: "+err.Error(), nil)
			return
		}
		operational, err := s.GetOperationalStats()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR",
				"failed to aggregate operational stats: "+err.Error(), nil)
			return
		}

		cacheStatus := "fresh"
		if repoCounts["daily"] == 0 && repoCounts["weekly"] == 0 && repoCounts["monthly"] == 0 {
			cacheStatus = "cold"
		}
		repoCounts["total"] = repoCounts["daily"] + repoCounts["weekly"] + repoCounts["monthly"]

		writeJSONWithMeta(w, TrendingStatsResponse{
			Repos:       repoCounts,
			Languages:   len(languages),
			Operational: operational,
		}, &model.Meta{
			GeneratedAt: time.Now().Format(time.RFC3339),
			CacheStatus: cacheStatus,
		})
	}
}
