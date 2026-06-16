package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dong4j/starcat-trending-api/internal/model"
)

// SQLiteStore 基于 SQLite 的 trending 数据持久化。
//
// 连接策略: WAL + busy_timeout=5000 + MaxOpenConns(1)。
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore 打开 SQLite 数据库并初始化 schema。
func NewSQLiteStore(dsn string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dsn+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	if err := createSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	log.Printf("[store] sqlite opened at %s", dsn)
	return &SQLiteStore{db: db}, nil
}

// UpsertRepo upsert 一条 trending repo 记录。
func (s *SQLiteStore) UpsertRepo(repo model.TrendingRepo) error {
	now := time.Now().Format(time.RFC3339)
	capturedAt := repo.CapturedAt.Format(time.RFC3339)

	_, err := s.db.Exec(`
		INSERT INTO trending_repos
			(full_name, owner, name, desc_text, stars, forks, language, change, build_by_json,
			 since, captured_at, enrich_priority)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(full_name, since) DO UPDATE SET
			desc_text = excluded.desc_text,
			stars = excluded.stars,
			forks = excluded.forks,
			language = excluded.language,
			change = excluded.change,
			build_by_json = excluded.build_by_json,
			captured_at = excluded.captured_at,
			enrich_priority = excluded.enrich_priority
	`, repo.FullName, repo.Owner, repo.Name, repo.DescText, repo.Stars, repo.Forks,
		repo.Language, repo.Change, repo.BuildByJSON,
		repo.Since, capturedAt, repo.EnrichPriority)

	if err != nil {
		return fmt.Errorf("upsert %s/%s: %w", repo.FullName, repo.Since, err)
	}

	// 更新 captured_at（ON CONFLICT 不自动处理）
	_, _ = s.db.Exec(`UPDATE trending_repos SET captured_at = ? WHERE full_name = ? AND since = ?`,
		now, repo.FullName, repo.Since)
	return nil
}

// GetRepos 按条件查询 repo 列表。
//
// `lang` 参数三种语义：
//   - 空字符串：不按语言过滤
//   - `model.UncategorizedLanguageKey`（即 `"__uncategorized__"`）：返回 `language IS NULL OR
//     language = ''` 的行（与 `GetAggregatedLanguages` 的「未分类」桶口径完全一致）
//   - 其它值：按 `language = ?` 严格等值过滤
//
// **不**对 lang 做 case-insensitive 匹配——后端 enricher 已经把 GitHub 返回的语言名规范化
// （Go / Python / TypeScript），客户端从 sidebar 拿到的 key 必然能 1:1 命中。
func (s *SQLiteStore) GetRepos(since, lang string, limit int) ([]model.TrendingRepo, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `SELECT full_name, owner, name, desc_text, stars, forks, language, change, build_by_json,
		gh_repo_id, description, homepage, license_spdx, topics_json, watchers, subscribers,
		owner_avatar, is_archived, is_fork, is_private, default_branch, open_issues,
		pushed_at, updated_at, created_at,
		since, captured_at, enriched_at, is_available, enrich_priority
	FROM trending_repos WHERE is_available = 1 AND enriched_at IS NOT NULL`
	args := []interface{}{}

	if since != "" {
		query += " AND since = ?"
		args = append(args, since)
	}
	if lang == model.UncategorizedLanguageKey {
		// 哨兵值：未分类——`language IS NULL OR language = ''`。
		// 不带占位符（IS NULL 不能用 `?`），直接拼到 SQL 里安全（哨兵是 const 常量，无注入风险）。
		query += " AND (language IS NULL OR language = '')"
	} else if lang != "" {
		query += " AND language = ?"
		args = append(args, lang)
	}
	query += " ORDER BY enrich_priority DESC, captured_at DESC LIMIT ?"
	args = append(args, limit)

	return s.queryRepos(query, args...)
}
// GetUnenrichedRepos 获取待 enrich 的 repo（按 priority desc）。
func (s *SQLiteStore) GetUnenrichedRepos(limit int) ([]model.TrendingRepo, error) {
	if limit <= 0 {
		limit = 30
	}
	return s.queryRepos(
		`SELECT full_name, owner, name, desc_text, stars, forks, language, change, build_by_json,
		 gh_repo_id, description, homepage, license_spdx, topics_json, watchers, subscribers,
		 owner_avatar, is_archived, is_fork, is_private, default_branch, open_issues,
		 pushed_at, updated_at, created_at,
		 since, captured_at, enriched_at, is_available, enrich_priority
		 FROM trending_repos
		 WHERE enriched_at IS NULL AND is_available = 1
		 ORDER BY enrich_priority DESC LIMIT ?`, limit)
}

// UpdateEnriched 更新 enricher 补全字段。
func (s *SQLiteStore) UpdateEnriched(fullName, since string, repo model.TrendingRepo) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.Exec(`
		UPDATE trending_repos SET
			gh_repo_id = ?, description = ?, homepage = ?, license_spdx = ?,
			topics_json = ?, watchers = ?, subscribers = ?, owner_avatar = ?,
			is_archived = ?, is_fork = ?, is_private = ?, default_branch = ?,
			open_issues = ?, pushed_at = ?, updated_at = ?, created_at = ?,
			language = COALESCE(?, language),
			stars = CASE WHEN ? > stars THEN ? ELSE stars END,
			enriched_at = ?
		WHERE full_name = ? AND since = ?
	`, repo.GhRepoID, repo.Description, repo.Homepage, repo.LicenseSpdx,
		repo.TopicsJSON, repo.Watchers, repo.Subscribers, repo.OwnerAvatar,
		boolToInt(repo.IsArchived), boolToInt(repo.IsFork), boolToInt(repo.IsPrivate), repo.DefaultBranch,
		repo.OpenIssues, repo.PushedAt, repo.UpdatedAt, repo.CreatedAt,
		repo.Language, repo.Stars, repo.Stars,
		now, fullName, since)
	return err
}

// ResetAllEnriched 把所有 repo 的 enriched_at 置 NULL，让 enricher 把全表
// 当成"未 enrich"状态重跑（详见 Store 接口注释）。
//
// 注意：不动 stars / forks / change / language 等 spider 字段，也不动
// enrich_priority，避免重 enrich 间隙客户端看到空卡片。
func (s *SQLiteStore) ResetAllEnriched() error {
	_, err := s.db.Exec(`UPDATE trending_repos SET enriched_at = NULL`)
	return err
}

// MarkUnavailable 标记 repo 404。
func (s *SQLiteStore) MarkUnavailable(fullName, since string) error {
	_, err := s.db.Exec(`UPDATE trending_repos SET is_available = 0 WHERE full_name = ? AND since = ?`,
		fullName, since)
	return err
}

// RecomputePriorities 按榜单位置重算 priority。
func (s *SQLiteStore) RecomputePriorities(since string) error {
	// 先全置 0
	_, _ = s.db.Exec(`UPDATE trending_repos SET enrich_priority = 0 WHERE since = ?`, since)

	// top 30 priority += 100
	_, err := s.db.Exec(`
		UPDATE trending_repos SET enrich_priority = 100
		WHERE full_name IN (
			SELECT full_name FROM trending_repos
			WHERE since = ? AND enriched_at IS NULL AND is_available = 1
			ORDER BY captured_at DESC LIMIT 30
		) AND since = ?
	`, since, since)
	if err != nil {
		return err
	}

	// next 70 priority += 50
	_, err = s.db.Exec(`
		UPDATE trending_repos SET enrich_priority = 50
		WHERE full_name IN (
			SELECT full_name FROM trending_repos
			WHERE since = ? AND enriched_at IS NULL AND is_available = 1 AND enrich_priority = 0
			ORDER BY captured_at DESC LIMIT 70
		) AND since = ?
	`, since, since)
	if err != nil {
		return err
	}

	// 其余 +10
	_, err = s.db.Exec(`UPDATE trending_repos SET enrich_priority = 10 WHERE since = ? AND enrich_priority = 0 AND enriched_at IS NULL`, since)
	return err
}
// UpsertLanguages 批量覆写语言列表。
func (s *SQLiteStore) UpsertLanguages(langs []model.Language) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM trending_languages"); err != nil {
		return err
	}

	now := time.Now().Format(time.RFC3339)
	stmt, err := tx.Prepare("INSERT INTO trending_languages (key, label, captured_at) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, l := range langs {
		if _, err := stmt.Exec(l.Key, l.Label, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetLanguages 获取语言列表（GitHub trending 页面爬虫快照）。
//
// 历史接口，**已不再驱动客户端 sidebar**——客户端用 `GetAggregatedLanguages()`。
// 本表仍由 `syncLanguages` cron 任务定时刷新，保留供 debug / 未来扩展。
func (s *SQLiteStore) GetLanguages() ([]model.Language, error) {
	rows, err := s.db.Query("SELECT key, label FROM trending_languages ORDER BY label")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var langs []model.Language
	for rows.Next() {
		var l model.Language
		if err := rows.Scan(&l.Key, &l.Label); err != nil {
			return nil, err
		}
		langs = append(langs, l)
	}
	return langs, rows.Err()
}

// GetAggregatedLanguages 基于 trending_repos 实际数据聚合语言列表。
//
// 实现要点：
//  1. `COALESCE(NULLIF(language, ''), '__uncategorized__')` 把 NULL / 空串都归一到哨兵 key
//  2. 仅统计 `is_available = 1 AND enriched_at IS NOT NULL`，与 GetRepos 可见性一致
//  3. 排序（dong4j 2026-06-16 调整）：先按 `is_uncategorized DESC`（**未分类排第 1 位**），
//     再按 `cnt DESC`，最后按 key ASC 兜底稳定。客户端 sidebar / picker 会在前面 prepend
//     「全部」哨兵,所以最终展示顺序是: 全部 → 未分类 → count 最多 → ... → count 最少。
//     **历史**: 之前是 `is_uncategorized ASC`(未分类排最后), dong4j 反馈用户找「未分类」要
//     滚到底太吃力, 改为放在前列与「全部」紧邻, 让用户一眼能看到这个特殊选项。
//  4. **不**按 since 维度切片——三个 period 合并；前端切 period 不需要重拉
//
// 关键约束（已踩过的坑）：
//   - 不要直接 `GROUP BY language` 然后 if NULL 转 key——SQLite 在 GROUP BY 阶段就会把 NULL
//     和 '' 分到两个 group 里（NULL 走默认 NULL 比较），需要 COALESCE 在 GROUP BY 之前归一
//   - `NULLIF(language, '')` 把空串转 NULL，再 COALESCE 把所有 NULL 转哨兵——两步合一缺一不可
//   - 不在 SQL 里写死哨兵字符串，从 Go 的 `model.UncategorizedLanguageKey` 注入，
//     避免后端 const 改了 SQL 漏改
func (s *SQLiteStore) GetAggregatedLanguages() ([]model.LanguageAggregate, error) {
	// 把哨兵值通过参数注入，避免 SQL/Go 两边 const 漂移；
	// 同时通过 `key = ?` 计算 `is_uncategorized` 标记，让排序「未分类排最前」与哨兵 key 解耦。
	query := `
		SELECT
			COALESCE(NULLIF(language, ''), ?) AS key,
			COUNT(*) AS cnt,
			CASE WHEN COALESCE(NULLIF(language, ''), ?) = ? THEN 1 ELSE 0 END AS is_uncategorized
		FROM trending_repos
		WHERE is_available = 1 AND enriched_at IS NOT NULL
		GROUP BY key
		ORDER BY is_uncategorized DESC, cnt DESC, key ASC
	`
	rows, err := s.db.Query(query,
		model.UncategorizedLanguageKey,
		model.UncategorizedLanguageKey,
		model.UncategorizedLanguageKey,
	)
	if err != nil {
		return nil, fmt.Errorf("aggregate languages: %w", err)
	}
	defer rows.Close()

	// 显式初始化空切片（而不是 nil）：
	// envelope 里 `Data []LanguageAggregate` 序列化时 nil 会输出 `null`，空切片输出 `[]`，
	// 客户端解析时 `[]` 比 `null` 友好。
	aggs := []model.LanguageAggregate{}
	for rows.Next() {
		var key string
		var count int
		var isUncategorized int
		if err := rows.Scan(&key, &count, &isUncategorized); err != nil {
			return nil, fmt.Errorf("scan aggregated language row: %w", err)
		}

		// label 默认 = key（GitHub 规范化的语言名直接展示）；
		// 哨兵 key 给一个英文 label 做调试 / 非 i18n 客户端兜底，最终展示由客户端 i18n 决定。
		label := key
		if key == model.UncategorizedLanguageKey {
			label = model.UncategorizedLanguageLabel
		}

		aggs = append(aggs, model.LanguageAggregate{
			Key:   key,
			Label: label,
			Count: count,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate aggregated language rows: %w", err)
	}
	return aggs, nil
}

// Close 关闭数据库连接。
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// queryRepos 执行查询并返回 TrendingRepo 列表。
func (s *SQLiteStore) queryRepos(query string, args ...interface{}) ([]model.TrendingRepo, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var repos []model.TrendingRepo
	for rows.Next() {
		var r model.TrendingRepo
		var capturedAtStr, enrichedAtStr sql.NullString

		err := rows.Scan(
			&r.FullName, &r.Owner, &r.Name, &r.DescText, &r.Stars, &r.Forks, &r.Language, &r.Change, &r.BuildByJSON,
			&r.GhRepoID, &r.Description, &r.Homepage, &r.LicenseSpdx, &r.TopicsJSON,
			&r.Watchers, &r.Subscribers, &r.OwnerAvatar,
			&r.IsArchived, &r.IsFork, &r.IsPrivate, &r.DefaultBranch, &r.OpenIssues,
			&r.PushedAt, &r.UpdatedAt, &r.CreatedAt,
			&r.Since, &capturedAtStr, &enrichedAtStr, &r.IsAvailable, &r.EnrichPriority,
		)
		if err != nil {
			return nil, fmt.Errorf("scan trending_repos row: %w", err)
		}

		if capturedAtStr.Valid {
			r.CapturedAt, _ = time.Parse(time.RFC3339, capturedAtStr.String)
		}
		if enrichedAtStr.Valid {
			t, _ := time.Parse(time.RFC3339, enrichedAtStr.String)
			r.EnrichedAt = &t
		}

		repos = append(repos, r)
	}
	return repos, rows.Err()
}

// TrendingRepoToCardDTO 将 DB 行转为统一卡片 DTO。
func TrendingRepoToCardDTO(r model.TrendingRepo) model.StarcatRepoCardDTO {
	htmlURL := "https://github.com/" + r.FullName

	dto := model.StarcatRepoCardDTO{
		GhRepoID:      ptrToInt64(r.GhRepoID),
		FullName:      r.FullName,
		Owner:         r.Owner,
		Repo:          r.Name,
		OwnerAvatar:   r.OwnerAvatar,
		Description:   coalesceString(r.Description, r.DescText),
		Language:      r.Language,
		Stars:         r.Stars,
		Forks:         r.Forks,
		Watchers:      r.Watchers,
		Subscribers:   r.Subscribers,
		Topics:        jsonToStringSlice(r.TopicsJSON),
		Homepage:      r.Homepage,
		LicenseSpdx:   r.LicenseSpdx,
		IsArchived:    r.IsArchived,
		IsFork:        r.IsFork,
		IsPrivate:     r.IsPrivate,
		DefaultBranch: r.DefaultBranch,
		OpenIssues:    r.OpenIssues,
		PushedAt:      r.PushedAt,
		UpdatedAt:     r.UpdatedAt,
		CreatedAt:     r.CreatedAt,
		HTMLURL:       &htmlURL,
		Trending:      buildTrendingExtension(r),
	}
	return dto
}

// buildTrendingExtension 构造 trending 扩展段。
func buildTrendingExtension(r model.TrendingRepo) *model.TrendingExtension {
	ext := &model.TrendingExtension{
		Change: r.Change,
	}

	if r.BuildByJSON != nil {
		var contributors []struct {
			By     string `json:"by"`
			Avatar string `json:"avatar"`
		}
		if err := json.Unmarshal([]byte(*r.BuildByJSON), &contributors); err == nil {
			for _, c := range contributors {
				// 从 "/user" 格式提取 login
				login := strings.TrimPrefix(c.By, "/")
				ext.Contributors = append(ext.Contributors, model.TrendingContributor{
					Avatar: c.Avatar,
					Login:  login,
				})
			}
		}
	}
	return ext
}
func ptrToInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func coalesceString(a, b *string) *string {
	if a != nil && *a != "" {
		return a
	}
	return b
}

func jsonToStringSlice(j *string) []string {
	if j == nil {
		return []string{}
	}
	var s []string
	if err := json.Unmarshal([]byte(*j), &s); err != nil {
		return []string{}
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
