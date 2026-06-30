// Package store 定义 trending 数据持久化接口。
//
// R-01 v1.2: 从零建立 SQLite 持久化层。
// R-02 v2.1（P2 清理,2026-06-10）: 删 v0.3 zread 集成残留的 source 参数。trending-api 重新
// 收敛到 GitHub 单源数据，所有 Store 方法不再接受 source 入参。
package store

import (
	"github.com/dong4j/starcat-trending-api/internal/model"
)

// Store 定义 trending 数据访问接口。
type Store interface {
	// --- Trending Repos ---

	// UpsertRepo 按 full_name + since 幂等 upsert。
	UpsertRepo(repo model.TrendingRepo) error

	// GetRepos 按 since / lang / limit 查询 repo 列表。
	//
	// R-02 v2.1：删除 source 参数，trending-api 固定走 GitHub 单源。
	GetRepos(since, lang string, limit int) ([]model.TrendingRepo, error)

	// CountReposBySince 按 since 统计真实可见 repo 数量，不受列表接口 limit 影响。
	//
	// 口径与 GetRepos 保持一致：仅统计 is_available=1 且 enriched_at IS NOT NULL 的行。
	// local admin 面板用它展示真实 daily / weekly / monthly 总数，避免把分页返回数误当总量。
	CountReposBySince() (map[string]int, error)

	// GetUnenrichedRepos 获取待 enrich 的 repo（按 priority desc）。
	GetUnenrichedRepos(limit int) ([]model.TrendingRepo, error)

	// UpdateEnriched 写入 enricher 补全字段 + 标记 enriched_at。
	//
	// R-02 v2.1：删除 source 参数。
	UpdateEnriched(fullName, since string, repo model.TrendingRepo) error

	// MarkUnavailable 标记 repo 不可用（404）。
	//
	// R-02 v2.1：删除 source 参数。
	MarkUnavailable(fullName, since string) error

	// RecomputePriorities 重新计算 enrich 优先级。
	//
	// R-02 v2.1：删除 source 参数，固定走全表 since 维度。
	RecomputePriorities(since string) error

	// ResetAllEnriched 把所有 repo 的 enriched_at 置为 NULL，让下一次
	// `enricher.EnrichAll()` 把全表当成"未 enrich"状态重新跑一遍 GitHub API。
	//
	// 设计动机（dong4j 2026-06-11）：
	// enricher 字段映射 / GitHub API 返回字段集会随版本演进（如 R-05 加 10 个
	// 详情字段），但 EnrichAll 流程是「只补 enriched_at IS NULL 的行」—— 已经
	// 用老版 enricher 处理过的历史数据**永远不会被新版字段覆盖**，新加字段会
	// 长期为空。这个方法配合 HandleEnrichForce admin endpoint 提供「一键全表重
	// enrich」能力，省得每次升级 enricher 字段都要手动删 trending.db。
	//
	// 关键约束：
	//   - 只清 enriched_at，不动 spider 抓的 stars / forks / change / language 等
	//     基础字段，避免在重 enrich 间隙客户端看到空卡片
	//   - 不重置 enrich_priority，让 enricher 仍按现有优先级倒序处理
	//   - 调用方需注意：会让所有行重新进入 enricher 队列，token 消耗 = 全表行数 × 1 req
	ResetAllEnriched() error

	// --- Languages ---

	// UpsertLanguages 批量 upsert 语言列表（来自 GitHub trending 页面爬虫快照）。
	//
	// 注意：本方法写入的 `trending_languages` 表只是「GitHub 页面菜单上可选的全部语言名」，
	// 与当前 `trending_repos` 实际有哪些 repo **完全无关**。客户端 sidebar 不再使用这张表，
	// 改用 `GetAggregatedLanguages()`（基于 trending_repos 实际数据聚合）。
	// 本表保留供 debug / 未来「未抓到的语言菜单」使用。
	UpsertLanguages(langs []model.Language) error

	// GetLanguages 获取语言列表（GitHub trending 页面爬虫快照，与实际数据无关）。
	//
	// 历史接口，**已不再驱动客户端 sidebar**。请使用 GetAggregatedLanguages()。
	GetLanguages() ([]model.Language, error)

	// GetAggregatedLanguages 基于 trending_repos 表聚合实际有数据的语言列表。
	//
	// 与 `GetLanguages()` 的区别：
	//   - `GetLanguages()`：返回 trending_languages 表里 GitHub 页面的全部语言菜单（700+ 项），
	//     **大部分语言下没有任何 repo**，客户端展示这个列表对用户无意义
	//   - `GetAggregatedLanguages()`：返回 trending_repos 实际有 repo 的语言 + 每语言 repo 数量，
	//     这才是 sidebar 真正需要的列表
	//
	// 数据策略（dong4j 2026-06-11 决策）：
	//   - 三个 period（daily / weekly / monthly）合并聚合，不分维度。前端切 period 不需要重新拉，
	//     避免「切到 weekly 发现某语言突然消失」的列表抖动
	//   - 仅统计 `is_available = 1 AND enriched_at IS NOT NULL` 的行，
	//     与 `GetRepos` 的可见性规则保持一致（客户端在 sidebar 看到的语言一定能在列表页查到 repo）
	//   - `language IS NULL OR language = ''` 的 repo 归到 `model.UncategorizedLanguageKey`（哨兵值
	//     `"__uncategorized__"`）一项；空表 / 无未分类时不出现该项
	//   - 排序：未分类**永远排在最后**，其它语言按 count DESC，count 相同时按 key ASC（稳定输出）
	//
	// 永不返错——空结果直接返 `[]`，方便客户端 zero-state 处理。
	// 真正的 DB 错误（连接断 / schema 异常）通过 error 返回，handler 层翻成 500。
	GetAggregatedLanguages() ([]model.LanguageAggregate, error)

	// Close 关闭数据库连接。
	Close() error
}
