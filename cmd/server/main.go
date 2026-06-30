// Package main 是 starcat-trending-api 的入口。
//
// R-01 v1.2: 从无状态爬虫升级为三层架构
//
//	spider（爬虫）→ store（SQLite）→ enricher（GitHub API 补全）→ scheduler（cron）
//	+ Bearer Token 鉴权 + Token Pool + /api/v1/* 契约。
package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/dong4j/starcat-trending-api/internal/enricher"
	"github.com/dong4j/starcat-trending-api/internal/handler"
	"github.com/dong4j/starcat-trending-api/internal/middleware"
	"github.com/dong4j/starcat-trending-api/internal/notifier"
	"github.com/dong4j/starcat-trending-api/internal/scheduler"
	"github.com/dong4j/starcat-trending-api/internal/store"
	"github.com/dong4j/starcat-trending-api/internal/tokenpool"
)

func main() {
	// 加载 .env
	if err := godotenv.Load(); err != nil {
		log.Printf("[env] no .env file found, using OS environment only")
	} else {
		log.Printf("[env] .env loaded")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "5002"
	}

	storeFile := os.Getenv("STORE_FILE")
	if storeFile == "" {
		storeFile = "./trending.db"
	}

	apiKeysStr := os.Getenv("API_KEYS")
	if apiKeysStr == "" {
		log.Fatal("API_KEYS env is required")
	}
	apiKeys := strings.Split(apiKeysStr, ",")

	tokensStr := os.Getenv("GITHUB_TOKENS")
	if tokensStr == "" {
		log.Fatal("GITHUB_TOKENS env is required (at least 1 GitHub PAT)")
	}
	tokens := strings.Split(tokensStr, ",")

	// SQLite Store
	sqliteStore, err := store.NewSQLiteStore(storeFile)
	if err != nil {
		log.Fatalf("Failed to initialize SQLite: %v", err)
	}
	defer sqliteStore.Close()

	// Token Pool
	pool := tokenpool.New(tokens)

	// Rate Limit Handler（5000 req/h → 720ms/req 兜底间隔）
	rateLimitHandler := enricher.NewRateLimitHandler(720 * time.Millisecond)

	// Enricher
	enc := enricher.New(sqliteStore, pool, rateLimitHandler)

	// Enrich Queue（后台 worker pool，持续处理 priority 最高的待 enrich repo）
	enrichQueue := enricher.NewEnrichQueue(enc, 2)
	enrichQueue.Start()
	defer enrichQueue.Stop()

	// Wiki Notifier（增量预热 wiki-api 缓存，通过 WIKI_API_KEY 控制开关）
	wikiNotifier := notifier.NewWikiNotifier()

	// R-06.2: trending 内存缓存（分桶 TTL daily 1h / weekly 6h / monthly 24h + ETag 304）。
	// 单例，由 handler.HandleReposV1 读 + scheduler 写（写=Invalidate 桶）。
	trendingCache := handler.NewTrendingCache()

	// Scheduler — 把 trendingCache 当作 CacheInvalidator 注入，cron 跑完同步后失效对应 since 桶。
	sch := scheduler.New(sqliteStore, enc, wikiNotifier, trendingCache)

	// Bearer 鉴权中间件
	authMW := middleware.NewBearerAuth(apiKeys)

	// 路由
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthzHandler)
	// R-03 (2026-06-11): /api/v1/ping 专门给 Starcat 客户端「测试连接」按钮用，
	// 在 middleware 后面挂——同时验证服务可达 + Bearer Key 正确。详见 handler/ping.go。
	mux.Handle("GET /api/v1/ping", authMW.Wrap(handler.HandlePingV1("trending")))
	mux.Handle("GET /api/v1/repos", authMW.Wrap(handler.HandleReposV1(sqliteStore, trendingCache)))
	// /api/v1/languages 现在直接读 store 聚合（trending_repos 维度），不再走 scheduler 的
	// langCache（langCache 抓的是 GitHub trending 页面菜单，与实际数据无关）。详见
	// handler/languages.go 顶部的「历史演进」注释。
	mux.Handle("GET /api/v1/languages", authMW.Wrap(handler.HandleLanguagesV1(sqliteStore)))
	mux.Handle("GET /api/v1/users", authMW.Wrap(handler.HandleUsersV1()))
	mux.Handle("POST /internal/sync/repos", authMW.Wrap(handler.HandleAdminSyncRepos(sch)))
	mux.Handle("POST /internal/sync/languages", authMW.Wrap(handler.HandleAdminSyncLanguages(sch)))
	mux.Handle("POST /internal/sync/users", authMW.Wrap(handler.HandleAdminSyncUsers()))
	// 2026-06-11 dong4j 反馈：trending 卡片缺 language / 详情字段。enricher
	// 字段映射演进时（如 R-05 加 10 字段），老 enricher 处理过的历史行不会被
	// 新版字段覆盖。此 endpoint 一键 reset enriched_at + 触发 EnrichAll 全表重
	// 跑，省得手动删 trending.db。详见 handler.HandleEnrichForce 注释。
	mux.Handle("POST /internal/enrich/force", authMW.Wrap(handler.HandleEnrichForce(sqliteStore, enc)))

	// 优雅关闭
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("Received %v, shutting down...", sig)
		sch.Stop()
		enrichQueue.Stop()
		sqliteStore.Close()
		os.Exit(0)
	}()

	// 冷启动：爬 daily + 语言列表
	go sch.Start()

	log.Printf("starcat-trending-api starting on port %s", port)
	log.Printf("Endpoints:")
	log.Printf("  GET  /api/v1/ping           - Connectivity probe for Starcat client (auth required)")
	log.Printf("  GET  /api/v1/repos          - Trending repos (auth required)")
	log.Printf("  GET  /api/v1/languages      - Languages list (auth required)")
	log.Printf("  GET  /api/v1/users          - Trending developers (auth required)")
	log.Printf("  POST /internal/sync/repos    - Manual sync all periods (daily+weekly+monthly+languages) (auth required)")
	log.Printf("  POST /internal/sync/languages - Languages refresh (auth required)")
	log.Printf("  POST /internal/sync/users    - Developers refresh (auth required)")
	log.Printf("  POST /internal/enrich/force  - Force re-enrich all data (clear enriched_at + dispatch EnrichAll) (auth required)")
	log.Printf("  GET  /healthz               - Health check (public)")
	handler := middleware.CORS(mux)
	log.Fatal(http.ListenAndServe(":"+port, handler))
}

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}
