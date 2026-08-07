package enricher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	kitgithub "github.com/starcat-app/starcat-api-kit/github"
	"github.com/starcat-app/starcat-trending-api/internal/model"
	"github.com/starcat-app/starcat-trending-api/internal/store"
	"github.com/starcat-app/starcat-trending-api/internal/tokenpool"
)

// Enricher 管理 GitHub API 字段补全。
type Enricher struct {
	store     store.Store
	pool      *tokenpool.Pool
	rateLimit *RateLimitHandler
	gh        *kitgithub.Client
	workerCnt int

	inflightMu sync.Mutex
	inflight   map[string]bool // key = full_name + "@" + since
}

// New 创建 Enricher。
func New(s store.Store, p *tokenpool.Pool, rl *RateLimitHandler) *Enricher {
	return &Enricher{
		store:     s,
		pool:      p,
		rateLimit: rl,
		gh: kitgithub.NewClient(kitgithub.Options{
			UserAgent: "starcat-trending-api/1.0",
			Pool:      p,
			Limiter:   rl,
			Timeout:   30 * time.Second,
		}),
		workerCnt: 2,
		inflight:  make(map[string]bool),
	}
}

// tryAcquire 尝试占用某个 repo 的 enrich 处理权。
//
// 防止并发跑同一 repo 的 enrich（enrich 自身并发安全，但 GitHub API rate limit 宝贵）。
func (e *Enricher) tryAcquire(repo *model.TrendingRepo) bool {
	key := repo.FullName + "@" + repo.Since
	e.inflightMu.Lock()
	defer e.inflightMu.Unlock()
	if e.inflight[key] {
		return false
	}
	e.inflight[key] = true
	return true
}

// release 释放占用（必须 defer 调用）。
func (e *Enricher) release(repo *model.TrendingRepo) {
	key := repo.FullName + "@" + repo.Since
	e.inflightMu.Lock()
	delete(e.inflight, key)
	e.inflightMu.Unlock()
}

// EnrichAll 全量 enrich 所有待处理 repo。
func (e *Enricher) EnrichAll() {
	batchSize := 30
	enriched := 0

	for {
		repos, err := e.store.GetUnenrichedRepos(batchSize)
		if err != nil {
			log.Printf("[enricher] GetUnenrichedRepos error: %v", err)
			return
		}
		if len(repos) == 0 {
			break
		}

		for i := range repos {
			if err := e.EnrichOne(&repos[i]); err != nil {
				log.Printf("[enricher] enrich %s failed: %v", repos[i].FullName, err)
				continue
			}
			enriched++
		}
		if enriched%100 == 0 {
			alive, dead, remaining, _ := e.pool.Stats()
			log.Printf("[enricher] progress: %d enriched | [token-pool] alive=%d dead=%d remaining=%d",
				enriched, alive, dead, remaining)
		}
	}

	log.Printf("[enricher] EnrichAll done: %d repos enriched", enriched)
}

// EnrichOne 单 repo enrich；HTTP / 重试 / 限流由 kit github.Client 负责。
func (e *Enricher) EnrichOne(repo *model.TrendingRepo) error {
	if !e.tryAcquire(repo) {
		return nil
	}
	defer e.release(repo)

	owner, name := repo.Owner, repo.Name
	gh, err := e.gh.GetRepo(context.Background(), owner, name)
	if err != nil {
		if errors.Is(err, kitgithub.ErrRepoNotFound) {
			_ = e.store.MarkUnavailable(repo.FullName, repo.Since)
			return nil
		}
		return fmt.Errorf("enrichOne %s/%s: %w", owner, name, err)
	}

	updated := buildEnrichedRepo(repo, gh)
	if err := e.store.UpdateEnriched(repo.FullName, repo.Since, updated); err != nil {
		return fmt.Errorf("update enriched: %w", err)
	}
	return nil
}

// buildEnrichedRepo 将 kit Repo 映射到 TrendingRepo 字段。
func buildEnrichedRepo(repo *model.TrendingRepo, gh *kitgithub.Repo) model.TrendingRepo {
	r := *repo

	r.GhRepoID = &gh.ID
	r.Description = gh.Description
	// Homepage 归一化：GitHub 未填主页时常返回 ""，直接透传会让客户端解 URL 失败。
	if gh.Homepage != nil && strings.TrimSpace(*gh.Homepage) == "" {
		r.Homepage = nil
	} else {
		r.Homepage = gh.Homepage
	}
	r.Watchers = gh.Watchers
	r.Subscribers = gh.Subscribers
	r.IsArchived = gh.Archived
	r.IsFork = gh.Fork
	r.IsPrivate = gh.Private
	r.DefaultBranch = &gh.DefaultBranch
	r.OpenIssues = gh.OpenIssues
	r.PushedAt = &gh.PushedAt
	r.UpdatedAt = &gh.UpdatedAt
	r.CreatedAt = &gh.CreatedAt
	r.Stars = gh.Stars
	r.Forks = gh.Forks
	if gh.Language != nil {
		r.Language = gh.Language
	}

	if gh.LicenseSpdx != nil {
		r.LicenseSpdx = gh.LicenseSpdx
	}
	if gh.OwnerAvatar != nil {
		r.OwnerAvatar = gh.OwnerAvatar
	}

	if len(gh.Topics) > 0 {
		b, _ := json.Marshal(gh.Topics)
		s := string(b)
		r.TopicsJSON = &s
	}

	return r
}
