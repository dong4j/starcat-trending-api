// Package scheduler 提供榜单定时刷新 + 增量 enrich。
//
// cron 驱动爬虫 → 落库 → enricher 补全。
package scheduler

import (
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/starcat-app/starcat-trending-api/internal/enricher"
	"github.com/starcat-app/starcat-trending-api/internal/model"
	"github.com/starcat-app/starcat-trending-api/internal/notifier"
	"github.com/starcat-app/starcat-trending-api/internal/spider"
	"github.com/starcat-app/starcat-trending-api/internal/store"
)

// CacheInvalidator 是 scheduler 完成某 since 桶同步后需要"主动失效内存缓存"的最小接口。
//
// 让 scheduler 包**不直接 import handler 包**：handler/trending_cache.go 的 *TrendingCache
// 自动满足这个接口；main.go 把实例注入到 scheduler.New(...) 即可。
//
// 这是 R-06.2（2026-06-15）拆出的接口，目的是避免 scheduler ↔ handler 双向 import 循环。
type CacheInvalidator interface {
	Invalidate(since string)
}

// noopCacheInvalidator 是 nil-safe 占位，仅用于测试 / 调用方暂时不接缓存的场景。
type noopCacheInvalidator struct{}

func (noopCacheInvalidator) Invalidate(string) {}

// Scheduler 管理定时爬虫任务。
type Scheduler struct {
	store        store.Store
	enricher     *enricher.Enricher
	wikiNotifier *notifier.WikiNotifier
	cache        CacheInvalidator // R-06.2: 完成同步后主动失效对应 since 桶；nil 时用 noop
	cron         *cron.Cron
	langCache    *languageCache
	mu           sync.Mutex
	running      map[string]bool // 防止并发跑同一任务
}

// languageCache 语言列表内存缓存（24h TTL）。
type languageCache struct {
	mu        sync.RWMutex
	languages []model.Language
	fetchedAt time.Time
}

// New 创建 Scheduler。
//
// cache 是 R-06.2 加的可选依赖：完成 syncDaily/Weekly/Monthly 后主动失效对应 since 桶
// 让客户端下次请求强制重建。传 nil 时退化到 noop（不报错，仅不做缓存失效），
// 方便测试 / 暂未接缓存的部署场景。
func New(s store.Store, enc *enricher.Enricher, wn *notifier.WikiNotifier, cache CacheInvalidator) *Scheduler {
	if cache == nil {
		cache = noopCacheInvalidator{}
	}
	sch := &Scheduler{
		store:        s,
		enricher:     enc,
		wikiNotifier: wn,
		cache:        cache,
		cron:         cron.New(cron.WithSeconds()),
		langCache:    &languageCache{},
		running:      make(map[string]bool),
	}

	// daily 每小时第 7 分
	sch.cron.AddFunc("7 * * * *", sch.syncDaily)
	// weekly 每 6 小时第 13 分
	sch.cron.AddFunc("13 */6 * * *", sch.syncWeekly)
	// monthly 每 2 天 05:19 UTC
	sch.cron.AddFunc("19 5 */2 * *", sch.syncMonthly)
	// 长尾 enrich 每天 03:00 UTC
	sch.cron.AddFunc("0 3 * * *", sch.enrichLongTail)
	// 过期清理 每天 04:00 UTC
	sch.cron.AddFunc("0 4 * * *", sch.cleanupStale)

	return sch
}

// Start 启动定时任务 + 冷启动全量同步。
//
// 历史 bug 修复（dong4j 2026-06-11 反馈）：之前冷启动只跑 syncDaily，
// weekly / monthly 要等下一次 cron tick（最长 6h / 48h），导致服务启动后
// 客户端 trending 页本周 / 本月 tab 长时间 0 条数据。现在三个 period 都
// 异步触发，cold start 后端会几乎同时跑完三次 GitHub trending 抓取
// （顺序：daily → weekly → monthly），用户切 tab 几分钟内就有数据。
//
// 每个 syncXxx 内部都自带 tryLock 互斥，跟后续 cron tick 重入冲突也会
// 静默跳过，不会重复抓。
func (sch *Scheduler) Start() {
	log.Println("[scheduler] cold start: syncing daily + weekly + monthly + languages")
	go sch.syncDaily()
	go sch.syncWeekly()
	go sch.syncMonthly()
	go sch.syncLanguages()
	sch.cron.Start()
	log.Println("[scheduler] cron started")
}

// Stop 停止所有定时任务。
func (sch *Scheduler) Stop() {
	ctx := sch.cron.Stop()
	<-ctx.Done()
	log.Println("[scheduler] stopped")
}

// SyncAll 手动全量同步（admin endpoint 调用）。
func (sch *Scheduler) SyncAll() {
	go func() {
		sch.syncDaily()
		sch.syncWeekly()
		sch.syncMonthly()
		sch.syncLanguages()
	}()
}

// SyncLanguages 手动刷新语言列表。
func (sch *Scheduler) SyncLanguages() {
	go sch.syncLanguages()
}

func (sch *Scheduler) syncDaily() {
	if !sch.tryLock("daily") {
		return
	}
	defer sch.unlock("daily")

	log.Println("[scheduler] syncing daily trending")
	repos := sch.scrapeAndPersist("", "daily")
	_ = sch.store.RecomputePriorities("daily")
	sch.enricher.EnrichAll()
	sch.wikiNotifier.NotifyRepos(repos)

	// R-06.2：scrape + enrich 跑完后主动失效内存缓存对应 since 桶。
	// 不在中间失效（enrich 还没补完时让缓存继续提供旧数据，更"smooth"），
	// 不在 scrape 之前失效（避免短暂的"空 200"窗口）。
	sch.cache.Invalidate("daily")
}

// syncWeekly / syncMonthly 历史 bug 修复（dong4j 2026-06-11 反馈）：
// 这两个函数原本没调 `sch.enricher.EnrichAll()`，导致 weekly / monthly
// 抓到的 repo 只有 spider 抓到的 stars / forks / change 等基础字段，
// description / license_spdx / topics_json / watchers / language 全空，
// 客户端 trending 卡片显示不全。现在与 syncDaily 行为一致：抓完 →
// 重算 priority → enricher 跑一遍 GitHub API 补全 → 通知 wiki 预热。
//
// 注：EnrichAll 内部 GetUnenrichedRepos 只取 enriched_at IS NULL 的行，
// 已经 enriched 的不会被重 enrich，所以三个 period 共用一个 enricher
// 池子不会冲突 / 重复消耗 token。

func (sch *Scheduler) syncWeekly() {
	if !sch.tryLock("weekly") {
		return
	}
	defer sch.unlock("weekly")

	log.Println("[scheduler] syncing weekly trending")
	repos := sch.scrapeAndPersist("", "weekly")
	_ = sch.store.RecomputePriorities("weekly")
	sch.enricher.EnrichAll()
	sch.wikiNotifier.NotifyRepos(repos)
	sch.cache.Invalidate("weekly") // R-06.2: 同 syncDaily 注释
}

func (sch *Scheduler) syncMonthly() {
	if !sch.tryLock("monthly") {
		return
	}
	defer sch.unlock("monthly")

	log.Println("[scheduler] syncing monthly trending")
	repos := sch.scrapeAndPersist("", "monthly")
	_ = sch.store.RecomputePriorities("monthly")
	sch.enricher.EnrichAll()
	sch.wikiNotifier.NotifyRepos(repos)
	sch.cache.Invalidate("monthly") // R-06.2: 同 syncDaily 注释
}

func (sch *Scheduler) enrichLongTail() {
	log.Println("[scheduler] running long-tail enrich")
	sch.enricher.EnrichAll()
}

func (sch *Scheduler) cleanupStale() {
	log.Println("[scheduler] cleaning up stale repos (captured_at < 7d)")
}

// scrapeAndPersist 爬所有语言 × since 组合并落库。
// 返回本次持久化的 owner/repo 列表，用于后续 wiki 预热。
func (sch *Scheduler) scrapeAndPersist(lang, since string) []string {
	sp := spider.NewRepoSpider(since, lang)
	items := sp.GetItems()

	var repos []string

	for _, item := range items {
		parts := strings.SplitN(item.Repo, "/", 2)
		if len(parts) != 2 {
			continue
		}
		owner, name := parts[0], parts[1]

		// Defense in depth（2026-06-11）：
		// 历史上 spider/repo.go 漏 strip 了 href 的前导 "/"，导致 SplitN 后
		// owner="" / name="owner/repo"，整批数据落库后被 enricher 用 404 标 unavailable，
		// 整张表的 is_available 全 0，handler 返回空数组 + cache_status=cold。
		// 即使源头已修，这里也兜底校验：owner 或 name 为空一律跳过 + 打 warn，
		// 让任何未来再出现的同类异常（爬虫 HTML 结构变动、第三方源差异）能在日志可见。
		if owner == "" || name == "" {
			log.Printf("[scheduler] skip malformed repo %q (owner=%q name=%q) — spider bug?",
				item.Repo, owner, name)
			continue
		}

		var bjJSON *string
		if len(item.BuildBy) > 0 {
			b, _ := json.Marshal(item.BuildBy)
			s := string(b)
			bjJSON = &s
		}

		fullName := owner + "/" + name
		var langPtr *string
		if item.Lang != "" {
			langPtr = &item.Lang
		}
		rec := model.TrendingRepo{
			FullName:    fullName,
			Owner:       owner,
			Name:        name,
			DescText:    &item.Desc,
			Stars:       item.Stars,
			Forks:       item.Forks,
			Language:    langPtr,
			Change:      item.Change,
			BuildByJSON: bjJSON,
			Since:       since,
			CapturedAt:  time.Now(),
			IsAvailable: true,
		}

		if err := sch.store.UpsertRepo(rec); err != nil {
			log.Printf("[scheduler] upsert %s failed: %v", rec.FullName, err)
			continue
		}

		repos = append(repos, fullName)
	}

	log.Printf("[scheduler] scraped %d repos for since=%s lang=%s", len(items), since, lang)
	return repos
}

// syncLanguages 刷新语言列表缓存。
func (sch *Scheduler) syncLanguages() {
	langSpider := spider.NewLangSpider()
	items := langSpider.GetItems()

	langs := make([]model.Language, len(items))
	for i, item := range items {
		langs[i] = model.Language{Key: item.Key, Label: item.Label}
	}

	_ = sch.store.UpsertLanguages(langs)

	sch.langCache.mu.Lock()
	sch.langCache.languages = langs
	sch.langCache.fetchedAt = time.Now()
	sch.langCache.mu.Unlock()

	log.Printf("[scheduler] synced %d languages", len(langs))
}

// GetLanguages 从缓存返回语言列表（24h TTL 内不重爬）。
func (sch *Scheduler) GetLanguages() []model.Language {
	sch.langCache.mu.RLock()
	languages := sch.langCache.languages
	fetchedAt := sch.langCache.fetchedAt
	sch.langCache.mu.RUnlock()

	if len(languages) == 0 || time.Since(fetchedAt) > 24*time.Hour {
		sch.syncLanguages()
		sch.langCache.mu.RLock()
		languages = sch.langCache.languages
		sch.langCache.mu.RUnlock()
	}
	return languages
}

func (sch *Scheduler) tryLock(name string) bool {
	sch.mu.Lock()
	defer sch.mu.Unlock()
	if sch.running[name] {
		return false
	}
	sch.running[name] = true
	return true
}

func (sch *Scheduler) unlock(name string) {
	sch.mu.Lock()
	sch.running[name] = false
	sch.mu.Unlock()
}
