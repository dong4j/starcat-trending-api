package handler

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/starcat-app/starcat-trending-api/internal/enricher"
	"github.com/starcat-app/starcat-trending-api/internal/scheduler"
	"github.com/starcat-app/starcat-trending-api/internal/store"
)

// HandleAdminSyncRepos POST /internal/sync/repos - 手动触发全量同步。
func HandleAdminSyncRepos(sch *scheduler.Scheduler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID := fmt.Sprintf("task-%s-repos-%d", time.Now().Format("2006-01-02T15:04:05Z"), time.Now().UnixNano()%1000)

		go sch.SyncAll()

		writeJSON(w, map[string]string{
			"task_id":    taskID,
			"started_at": time.Now().Format(time.RFC3339),
			"status":     "running",
		})
	}
}

// HandleAdminSyncLanguages POST /internal/sync/languages - 手动触发热刷新语言列表。
func HandleAdminSyncLanguages(sch *scheduler.Scheduler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID := fmt.Sprintf("task-%s-languages-%d", time.Now().Format("2006-01-02T15:04:05Z"), time.Now().UnixNano()%1000)

		go sch.SyncLanguages()

		writeJSON(w, map[string]string{
			"task_id":    taskID,
			"started_at": time.Now().Format(time.RFC3339),
			"status":     "running",
		})
	}
}

// HandleAdminSyncUsers POST /internal/sync/users - 「重爬开发者榜单」管理触发器。
//
// ⚠️ 已知约束（R-01 v1.2 设计意图）：
//
// trending 开发者数据（GET /api/v1/users）当前是**按需实时爬取**（无落库 / 无缓存），
// 因此本 admin endpoint 没有「触发后台爬虫 + 写库」的工作可做，只回 task_id 表示请求被接受。
//
// 之所以保留这个 endpoint，是为了：
//  1. 与 /internal/sync/{repos,languages} 的接口形态一致（前端 / 运维脚本可统一处理）
//  2. 未来如果把开发者数据改为「定时爬 + 落库」（前端 N4 演进或运维需求），不需要改 endpoint 路径
//  3. 监控 / 日志侧能区分「手动 sync users」与「业务请求 users」两条流量
//
// 后续如果接入真实爬取，把下面注释里的 spider.UserSync(...) 启动起来即可。
// 详见 supports/docs/R-01-trending-api-改造方案.md §6.2.3 + TODO.md「中优」条目。
// HandleEnrichForce POST /internal/enrich/force - 强制重 enrich 所有数据。
//
// 设计动机（dong4j 2026-06-11）：
// enricher 字段映射 / GitHub API 返回字段集会随版本演进（如 R-05 加 10 个详情字段），
// 但 EnrichAll 流程是「只补 enriched_at IS NULL 的行」—— 已经用老版 enricher 处理过的
// 历史数据不会被新版字段覆盖，新加字段会长期为空。
//
// 流程：① `store.ResetAllEnriched()` 把全表 enriched_at 清空 → ② 立即异步触发
// `enricher.EnrichAll()` 重新跑一遍 GitHub API → ③ 同步返回 task_id，不阻塞 HTTP
//
// 重要约束：
//   - 操作 cost = 全表行数 × 1 个 GitHub API token quota（token-pool 自带 rate limit
//     兜底，不会瞬时打爆 GitHub），数百行规模 1 ~ 2 分钟跑完
//   - reset 与 EnrichAll 之间客户端 GET /api/v1/repos 会看到原有 spider 字段
//     （stars / forks / change / language），description / license / topics 等
//     enricher 补全字段会瞬时为空，UI 会有轻微抖动；这是预期 tradeoff
//   - 调用方式（admin curl）：
//       curl -X POST -H "Authorization: Bearer $KEY" http://127.0.0.1:5002/internal/enrich/force
func HandleEnrichForce(s store.Store, enc *enricher.Enricher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID := fmt.Sprintf("task-%s-enrich-force-%d", time.Now().Format("2006-01-02T15:04:05Z"), time.Now().UnixNano()%1000)

		if err := s.ResetAllEnriched(); err != nil {
			log.Printf("[admin] /internal/enrich/force reset failed: %v", err)
			writeError(w, http.StatusInternalServerError, "RESET_FAILED", err.Error(), nil)
			return
		}

		log.Printf("[admin] /internal/enrich/force: enriched_at reset, dispatching EnrichAll")
		go enc.EnrichAll()

		writeJSON(w, map[string]string{
			"task_id":    taskID,
			"started_at": time.Now().Format(time.RFC3339),
			"status":     "running",
			"note":       "全表 enriched_at 已置空，enricher 正在后台重 enrich，预计 1-2 分钟完成",
		})
	}
}

func HandleAdminSyncUsers() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID := fmt.Sprintf("task-%s-users-%d", time.Now().Format("2006-01-02T15:04:05Z"), time.Now().UnixNano()%1000)

		// TODO(R-01 / P2): 若 users 数据改为落库模式，在此处启动后台爬取 goroutine。
		// 例：go sch.SyncUsers()

		log.Printf("[admin] /internal/sync/users invoked (no-op: users are fetched on-demand, not persisted)")

		writeJSON(w, map[string]string{
			"task_id":    taskID,
			"started_at": time.Now().Format(time.RFC3339),
			"status":     "accepted", // 不是 "running"——实际没有后台任务在跑
		})
	}
}
