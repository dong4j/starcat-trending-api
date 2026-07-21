// Package handler 中的 languages.go 实现 GET /api/v1/languages。
//
// 历史演进（2026-06-11 dong4j 反馈）：
//   - v1（R-01 v1.2）：返回 GitHub trending 页面爬虫快照（trending_languages 表，700+ 语言）
//     —— 客户端不用，因为绝大多数语言下没有任何 repo
//   - v2（当前）：改用 store.GetAggregatedLanguages()，**基于 trending_repos 实际数据聚合**，
//     返回 [{key, label, count}]，含一项 `__uncategorized__`（language 为 NULL 或空字符串）。
//     客户端 sidebar 改用本接口驱动语言列表
//
// 兼容性：响应类型从 `[]Language` 变成 `[]LanguageAggregate`（多了 count 字段，
// key/label 字段保持兼容），envelope schema_version 仍为 1。客户端可以平滑升级。
package handler

import (
	"net/http"
	"time"

	"github.com/starcat-app/starcat-trending-api/internal/model"
	"github.com/starcat-app/starcat-trending-api/internal/store"
)

// HandleLanguagesV1 GET /api/v1/languages - 返回基于 trending_repos 聚合的语言列表。
//
// 响应（envelope schema_version=1）：
//
//	{
//	  "schema_version": 1,
//	  "data": [
//	    { "key": "__uncategorized__", "label": "Uncategorized", "count": 5 },
//	    { "key": "Python", "label": "Python", "count": 42 },
//	    { "key": "Go", "label": "Go", "count": 31 },
//	    ...
//	  ],
//	  "meta": { "total": N, "generated_at": "...", "cache_status": "fresh" }
//	}
//
// 关键约束：
//   - 实时聚合：每次请求都打一次 SQL（trending_repos 通常 ≤ 几百行，COUNT GROUP BY 微秒级）
//   - 排序（dong4j 2026-06-16 调整）：**未分类排第 1 位**，其它按 count DESC、key ASC
//     (详见 SQLiteStore.GetAggregatedLanguages 注释)。客户端 sidebar / picker 会在前面
//     prepend「全部」哨兵, 所以最终展示顺序: 全部 → 未分类 → count 最多 → ... → count 最少。
//   - cache_status：聚合数据来源是「已 enrich + is_available」的 repo，
//     有数据返 `fresh`，空表返 `cold`（与 /api/v1/repos 的语义对齐）
//   - 错误：只在 store DB 层异常时返 500，正常的「空结果」返 200 + 空数组
func HandleLanguagesV1(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		aggs, err := s.GetAggregatedLanguages()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR",
				"failed to aggregate languages: "+err.Error(), nil)
			return
		}

		cacheStatus := "fresh"
		if len(aggs) == 0 {
			cacheStatus = "cold"
		}

		writeJSONWithMeta(w, aggs, &model.Meta{
			Total:       len(aggs),
			GeneratedAt: time.Now().Format(time.RFC3339),
			CacheStatus: cacheStatus,
		})
	}
}
