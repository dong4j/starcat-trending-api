// Package server 导出 trending-api 的可装配 HTTP 服务。
//
// 单仓部署走 cmd/server；聚合部署（starcat-api）import 本包并挂到网关。
// 业务实现仍在 internal/，本包负责 env 装配、路由注册与 enrich/scheduler 生命周期。
package server

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	kitenv "github.com/starcat-app/starcat-api-kit/env"
	kitmetrics "github.com/starcat-app/starcat-api-kit/metrics"
	"github.com/starcat-app/starcat-trending-api/internal/enricher"
	"github.com/starcat-app/starcat-trending-api/internal/handler"
	"github.com/starcat-app/starcat-trending-api/internal/middleware"
	"github.com/starcat-app/starcat-trending-api/internal/notifier"
	"github.com/starcat-app/starcat-trending-api/internal/scheduler"
	"github.com/starcat-app/starcat-trending-api/internal/store"
	"github.com/starcat-app/starcat-trending-api/internal/tokenpool"
	"github.com/starcat-app/starcat-trending-api/internal/version"
)

const defaultPort = "5002"

// Options 控制 trending 服务装配。
type Options struct {
	Port             string
	StoreFile        string
	MetricsStoreFile string
	APIKeys          []string
	Tokens           []string
	// SkipListenLogEndpoints 为 true 时不打印 endpoint 清单（聚合网关挂载时用）。
	SkipListenLogEndpoints bool
}

// Service 是已装配的 trending HTTP 服务。
type Service struct {
	opts        Options
	handler     http.Handler
	store       *store.SQLiteStore
	scheduler   *scheduler.Scheduler
	enrichQueue *enricher.EnrichQueue
	metrics     *kitmetrics.Collector

	closeOnce sync.Once
}

// Name 返回聚合网关识别用的稳定服务名。
func Name() string { return "trending" }

// DefaultPort 返回单仓默认监听端口。
func DefaultPort() string { return defaultPort }

// FromEnv 从环境变量装配服务（缺失必填项时返回 error，不 log.Fatal）。
func FromEnv() (*Service, error) {
	apiKeys, err := kitenv.RequiredCSV("API_KEYS")
	if err != nil {
		return nil, fmt.Errorf("API_KEYS env is required")
	}
	tokens, err := kitenv.RequiredCSV("GITHUB_TOKENS")
	if err != nil {
		return nil, fmt.Errorf("GITHUB_TOKENS env is required (at least 1 GitHub PAT)")
	}

	return New(Options{
		Port:             kitenv.OrDefault("PORT", defaultPort),
		StoreFile:        kitenv.OrDefault("STORE_FILE", "./trending.db"),
		MetricsStoreFile: kitenv.OrDefault("METRICS_STORE_FILE", "./trending-metrics.db"),
		APIKeys:          apiKeys,
		Tokens:           tokens,
	})
}

// New 按 Options 装配服务；enrich 队列与 scheduler 在此启动（与历史 main 一致）。
func New(opt Options) (*Service, error) {
	if strings.TrimSpace(opt.Port) == "" {
		opt.Port = defaultPort
	}
	if strings.TrimSpace(opt.StoreFile) == "" {
		opt.StoreFile = "./trending.db"
	}
	if strings.TrimSpace(opt.MetricsStoreFile) == "" {
		opt.MetricsStoreFile = ":memory:"
	}
	if len(opt.APIKeys) == 0 {
		return nil, fmt.Errorf("APIKeys is required")
	}
	if len(opt.Tokens) == 0 {
		return nil, fmt.Errorf("GitHub tokens are required")
	}

	sqliteStore, err := store.NewSQLiteStore(opt.StoreFile)
	if err != nil {
		return nil, fmt.Errorf("initialize SQLite: %w", err)
	}

	pool := tokenpool.New(opt.Tokens)
	rateLimitHandler := enricher.NewRateLimitHandler(720 * time.Millisecond)
	enc := enricher.New(sqliteStore, pool, rateLimitHandler)

	enrichQueue := enricher.NewEnrichQueue(enc, 2)
	enrichQueue.Start()

	wikiNotifier := notifier.NewWikiNotifier()
	trendingCache := handler.NewTrendingCache()
	sch := scheduler.New(sqliteStore, enc, wikiNotifier, trendingCache)

	authMW := middleware.NewBearerAuth(opt.APIKeys)
	metricsStore, err := kitmetrics.OpenSQLite(opt.MetricsStoreFile)
	if err != nil {
		enrichQueue.Stop()
		_ = sqliteStore.Close()
		return nil, fmt.Errorf("initialize metrics SQLite: %w", err)
	}
	metricsCollector, err := kitmetrics.NewCollector(kitmetrics.Config{Service: Name(), Store: metricsStore})
	if err != nil {
		_ = metricsStore.Close()
		enrichQueue.Stop()
		_ = sqliteStore.Close()
		return nil, fmt.Errorf("initialize metrics collector: %w", err)
	}
	metricsHandler := kitmetrics.NewHandler(Name(), metricsCollector.Store())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthzHandler)
	mux.Handle("GET /api/v1/ping", authMW.Wrap(handler.HandlePingV1(version.Service, version.Version)))
	mux.Handle("GET /internal/stats", authMW.Wrap(handler.HandleStatsV1(sqliteStore)))
	mux.Handle("GET /api/v1/repos", authMW.Wrap(handler.HandleReposV1(sqliteStore, trendingCache)))
	mux.Handle("GET /api/v1/languages", authMW.Wrap(handler.HandleLanguagesV1(sqliteStore)))
	mux.Handle("GET /api/v1/users", authMW.Wrap(handler.HandleUsersV1()))
	mux.Handle("POST /internal/sync/repos", authMW.Wrap(handler.HandleAdminSyncRepos(sch)))
	mux.Handle("POST /internal/sync/languages", authMW.Wrap(handler.HandleAdminSyncLanguages(sch)))
	mux.Handle("POST /internal/sync/users", authMW.Wrap(handler.HandleAdminSyncUsers()))
	mux.Handle("POST /internal/enrich/force", authMW.Wrap(handler.HandleEnrichForce(sqliteStore, enc)))
	mux.Handle("GET /internal/metrics/summary", authMW.Wrap(http.HandlerFunc(metricsHandler.HandleSummary)))
	mux.Handle("GET /internal/metrics/timeseries", authMW.Wrap(http.HandlerFunc(metricsHandler.HandleTimeseries)))
	mux.Handle("GET /internal/metrics/routes", authMW.Wrap(http.HandlerFunc(metricsHandler.HandleRoutes)))
	mux.Handle("GET /internal/metrics/status-codes", authMW.Wrap(http.HandlerFunc(metricsHandler.HandleStatusCodes)))

	// 冷启动 cron + 三档 period 同步与历史 main 相同，必须在独立 goroutine 里跑。
	go sch.Start()

	if !opt.SkipListenLogEndpoints {
		log.Printf("starcat-trending-api %s endpoints ready", version.Version)
		log.Printf("  GET  /api/v1/ping           - Connectivity probe for Starcat client (auth required)")
		log.Printf("  GET  /internal/stats        - Aggregated DB stats for admin panel (auth required)")
		log.Printf("  GET  /api/v1/repos          - Trending repos (auth required)")
		log.Printf("  GET  /api/v1/languages      - Languages list (auth required)")
		log.Printf("  GET  /api/v1/users          - Trending developers (auth required)")
		log.Printf("  POST /internal/sync/repos    - Manual sync all periods (auth required)")
		log.Printf("  POST /internal/sync/languages - Languages refresh (auth required)")
		log.Printf("  POST /internal/sync/users    - Developers refresh (auth required)")
		log.Printf("  POST /internal/enrich/force  - Force re-enrich all data (auth required)")
		log.Printf("  GET  /healthz               - Health check (public)")
	}

	return &Service{
		opts:        opt,
		handler:     metricsCollector.Wrap(middleware.CORS(mux)),
		store:       sqliteStore,
		scheduler:   sch,
		enrichQueue: enrichQueue,
		metrics:     metricsCollector,
	}, nil
}

// Handler 返回已包 CORS 的根 handler。
func (s *Service) Handler() http.Handler { return s.handler }

// Addr 返回建议监听地址（":port"）。
func (s *Service) Addr() string { return ":" + s.opts.Port }

// Close 停止 scheduler、enrich 队列并关闭 SQLite（与历史 signal handler 顺序一致）。
func (s *Service) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		if s.scheduler != nil {
			s.scheduler.Stop()
		}
		if s.enrichQueue != nil {
			s.enrichQueue.Stop()
		}
		if s.metrics != nil {
			closeErr = s.metrics.Close()
		}
		if s.store != nil {
			if err := s.store.Close(); closeErr == nil {
				closeErr = err
			}
		}
	})
	return closeErr
}

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
