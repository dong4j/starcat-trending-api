package enricher

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dong4j/starcat-trending-api/internal/model"
	"github.com/dong4j/starcat-trending-api/internal/store"
	"github.com/dong4j/starcat-trending-api/internal/tokenpool"
)

// githubRepoResponse 是 GET /repos/{o}/{r} 的完整响应。
type githubRepoResponse struct {
	ID          int64    `json:"id"`
	FullName    string   `json:"full_name"`
	Description *string  `json:"description"`
	Stargazers  int      `json:"stargazers_count"`
	Forks       int      `json:"forks_count"`
	Watchers    int      `json:"watchers_count"`
	Subscribers int      `json:"subscribers_count"`
	Language    *string  `json:"language"`
	Topics      []string `json:"topics"`
	Homepage    *string  `json:"homepage"`
	License     *struct {
		SpdxID *string `json:"spdx_id"`
	} `json:"license"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	OpenIssues    int    `json:"open_issues_count"`
	PushedAt      string `json:"pushed_at"`
	UpdatedAt     string `json:"updated_at"`
	CreatedAt     string `json:"created_at"`
	Owner         *struct {
		AvatarURL *string `json:"avatar_url"`
	} `json:"owner"`
	Message string `json:"message"`
}

// Enricher 管理 GitHub API 字段补全。
type Enricher struct {
	store     store.Store
	pool      *tokenpool.Pool
	rateLimit *RateLimitHandler
	client    *http.Client
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
		client:    &http.Client{Timeout: 30 * time.Second},
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

// EnrichOne 单 repo enrich，最多重试 3 次。
func (e *Enricher) EnrichOne(repo *model.TrendingRepo) error {
	if !e.tryAcquire(repo) {
		return nil
	}
	defer e.release(repo)

	owner, name := repo.Owner, repo.Name

	for attempt := 0; attempt < 3; attempt++ {
		token := e.pool.PickBest()
		if token == nil {
			resetAt := e.pool.EarliestReset()
			if !resetAt.IsZero() {
				sleepDuration := time.Until(resetAt)
				if sleepDuration > 0 {
					log.Printf("[enricher] all tokens exhausted, sleeping until %s", resetAt.Format(time.RFC3339))
					time.Sleep(sleepDuration)
				}
			}
			continue
		}

		e.rateLimit.Wait()

		url := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, name)
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+token.Value)
		req.Header.Set("User-Agent", "starcat-trending-api/1.0")
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := e.client.Do(req)
		if err != nil {
			return fmt.Errorf("http GET %s: %w", url, err)
		}
		e.pool.UpdateFromResponse(token, resp)

		switch resp.StatusCode {
		case 200:
			var gh githubRepoResponse
			if err := json.NewDecoder(resp.Body).Decode(&gh); err != nil {
				resp.Body.Close()
				return fmt.Errorf("decode response: %w", err)
			}
			resp.Body.Close()

			updated := buildEnrichedRepo(repo, &gh)
			if err := e.store.UpdateEnriched(repo.FullName, repo.Since, updated); err != nil {
				return fmt.Errorf("update enriched: %w", err)
			}
			return nil

		case 404:
			resp.Body.Close()
			_ = e.store.MarkUnavailable(repo.FullName, repo.Since)
			return nil

		case 401:
			resp.Body.Close()
			continue

		case 429, 403:
			resp.Body.Close()
			e.pool.DisableUntil(token, retryUntil(resp, token.ResetAt), fmt.Sprintf("rate limited status %d", resp.StatusCode))
			continue

		case 502, 503:
			resp.Body.Close()
			continue

		default:
			resp.Body.Close()
			if resp.StatusCode >= 500 {
				continue
			}
			return fmt.Errorf("unexpected status %d", resp.StatusCode)
		}
	}
	return fmt.Errorf("enrichOne %s/%s failed after 3 attempts", owner, name)
}

func retryUntil(resp *http.Response, resetAt time.Time) time.Time {
	until := resetAt
	if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
		if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds > 0 {
			retryAt := time.Now().Add(time.Duration(seconds) * time.Second)
			if retryAt.After(until) {
				until = retryAt
			}
		}
	}
	return until
}

// buildEnrichedRepo 将 GitHub API 响应映射到 TrendingRepo 字段。
func buildEnrichedRepo(repo *model.TrendingRepo, gh *githubRepoResponse) model.TrendingRepo {
	r := *repo

	r.GhRepoID = &gh.ID
	r.Description = gh.Description
	// Homepage 归一化（2026-06-11）：
	// GitHub 的 /repos API 在仓库未填主页时返回 "" 而不是 null。直接透传会让
	// 下游（特别是 Swift / TS / Kotlin 客户端解 URL 类型）抛 dataCorrupted 错，
	// 整批响应解码崩。这里把 "" 归一化为 nil，让 JSON envelope 输出 null，
	// 与 description / topics 这类「业务上等同空值」的字段保持一致。
	// 客户端 StarcatRepoCardDTO.decodeOptionalURL 也加了防御层，两边都修是为了
	// 让 fly.dev 旧版本 / 本地新版本两套部署对所有客户端都行为一致。
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
	r.Stars = gh.Stargazers
	r.Forks = gh.Forks
	if gh.Language != nil {
		r.Language = gh.Language
	}

	if gh.License != nil && gh.License.SpdxID != nil {
		r.LicenseSpdx = gh.License.SpdxID
	}
	if gh.Owner != nil && gh.Owner.AvatarURL != nil {
		r.OwnerAvatar = gh.Owner.AvatarURL
	}

	if len(gh.Topics) > 0 {
		b, _ := json.Marshal(gh.Topics)
		s := string(b)
		r.TopicsJSON = &s
	}

	return r
}
