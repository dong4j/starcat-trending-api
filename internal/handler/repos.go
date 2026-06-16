// Package handler 包含 HTTP handler 实现。
//
// trending-api 只走 GitHub 单源；zread 数据请走 weekly-api /api/v1/trending/zread。
package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/dong4j/starcat-trending-api/internal/model"
	"github.com/dong4j/starcat-trending-api/internal/store"
)

// HandleReposV1 GET /api/v1/repos - 返回 GitHub trending repo 卡片列表。
//
// query 参数：
//   - since: daily | weekly | monthly（默认 daily）
//   - lang: 语言过滤（如 go / python / swift）
//   - limit: 1-100（默认 100）
//
// 不接受 source=* 参数。trending-api 固定走 GitHub 单源；zread 数据请改用
// weekly-api /api/v1/trending/zread。
//
// R-06.2（2026-06-15）：接入 *TrendingCache 内存缓存：
//   - 缓存 key = `since|lang|limit`，TTL 与 cron 节奏对齐（daily 1h / weekly 6h / monthly 24h）
//   - 缓存命中 + 客户端带 If-None-Match 等同 ETag → 304 Not Modified（无 body）
//   - 缓存命中 + 不带 If-None-Match → 200 + 预拼 payload（跳过 SQLite + JSON marshal）
//   - cache miss / TTL 过期 → 查 SQLite + marshal envelope + cache.Set + 200 + ETag / Last-Modified
//   - scheduler 跑完同步 invalidate 对应 since 桶，保证一致性（详见 trending_cache.go）
//
// 关键约束：cache 是必传参数（不接受 nil），main.go 单例注入。这样测试也用 `NewTrendingCache()`
// 显式构造，避免运行时 cache 被悄悄关掉的风险。
func HandleReposV1(s store.Store, cache *TrendingCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lang := r.URL.Query().Get("lang")
		since := r.URL.Query().Get("since")
		if since == "" {
			since = "daily"
		}

		if since != "daily" && since != "weekly" && since != "monthly" {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST",
				"since must be one of: daily, weekly, monthly",
				map[string]interface{}{
					"param":   "since",
					"got":     since,
					"allowed": []string{"daily", "weekly", "monthly"},
				})
			return
		}

		// 显式拒绝任何 source=* 参数，引导客户端改用 weekly-api。
		if src := r.URL.Query().Get("source"); src != "" {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST",
				fmt.Sprintf("source=%s is not supported by trending-api. "+
					"trending-api is github-only; use weekly-api /api/v1/trending/zread for zread data.", src),
				map[string]interface{}{
					"param":   "source",
					"got":     src,
					"allowed": []string{"(none — trending-api is github-only)"},
					"see":     "https://github.com/dong4j/starcat-weekly-api (GET /api/v1/trending/zread)",
				})
			return
		}

		limit := 100
		if l := r.URL.Query().Get("limit"); l != "" {
			fmt.Sscanf(l, "%d", &limit)
			if limit > 100 {
				limit = 100
			}
		}

		key := CacheKey(since, lang, limit)

		// 1) cache 命中分支
		if entry, ok := cache.Get(key, since); ok {
			writeCachedResponse(w, r, entry)
			return
		}

		// 2) cache miss / 过期：查 store + 构造 envelope + 写 cache
		repos, err := s.GetRepos(since, lang, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR",
				"failed to query repos: "+err.Error(), nil)
			return
		}

		cards := make([]model.StarcatRepoCardDTO, len(repos))
		for i, repo := range repos {
			cards[i] = store.TrendingRepoToCardDTO(repo)
		}

		cacheStatus := "fresh"
		if len(cards) == 0 {
			cacheStatus = "cold"
		}

		env := model.Envelope[[]model.StarcatRepoCardDTO]{
			SchemaVersion: 1,
			Data:          cards,
			Meta: &model.Meta{
				Since:       since,
				Language:    lang,
				Total:       len(cards),
				GeneratedAt: time.Now().Format(time.RFC3339),
				CacheStatus: cacheStatus,
			},
		}
		payload, err := json.Marshal(env)
		if err != nil {
			// 极少数情况；envelope 内字段都可序列化，几乎不会走到
			log.Printf("[handler] failed to marshal repos envelope: %v", err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR",
				"failed to encode response: "+err.Error(), nil)
			return
		}

		entry := cache.Set(key, payload)
		writeCachedResponse(w, r, entry)
	}
}

// writeCachedResponse 把 cache entry 写到响应。
//
// 流程：
//  1. 设 ETag / Last-Modified / Content-Type header
//  2. 若客户端 If-None-Match 与 entry.etag 匹配 → 304（无 body）
//  3. 否则 200 + 直接 Write payload
//
// 不读 If-Modified-Since（客户端用 ETag 即可，避免双重判定语义冲突）。
func writeCachedResponse(w http.ResponseWriter, r *http.Request, entry *trendingCacheEntry) {
	w.Header().Set("ETag", entry.etag)
	w.Header().Set("Last-Modified", entry.lastModified.UTC().Format(http.TimeFormat))
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if match := r.Header.Get("If-None-Match"); match != "" && match == entry.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(entry.payload); err != nil {
		// 客户端断连等场景；不再做兜底（响应头已写出去）
		log.Printf("[handler] failed to write cached payload: %v", err)
	}
}
