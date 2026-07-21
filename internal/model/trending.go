// Package model 定义 trending 数据库层结构。
//
// TrendingRepo 直接映射 SQLite trending_repos 表，与 DB 列一一对齐。
// R-01 v1.2: 替代旧的 internal/models/models.go 中的 RepoItem。
package model

import "time"

// TrendingRepo 是 trending_repos 表中一条记录的 Go 表示。
//
// R-02 v0.2（2026-06-10）：删 v0.3 引入的 zread 维度（9 字段）+ Source 字段。
// trending-api 重新收敛到 GitHub 单源数据；zread 数据已迁到 weekly-api。
type TrendingRepo struct {
	FullName    string
	Owner       string
	Name        string
	DescText    *string // 爬虫原始描述
	Stars       int
	Forks       int
	Language    *string
	Change      int
	BuildByJSON *string // contributors JSON array

	// enricher 补全字段
	GhRepoID      *int64
	Description   *string // GitHub 官方 description，覆盖 desc_text
	Homepage      *string
	LicenseSpdx   *string
	TopicsJSON    *string
	Watchers      int
	Subscribers   int
	OwnerAvatar   *string
	IsArchived    bool
	IsFork        bool
	IsPrivate     bool
	DefaultBranch *string
	OpenIssues    int
	PushedAt      *string // RFC3339
	UpdatedAt     *string
	CreatedAt     *string

	// 元数据
	Since          string
	CapturedAt     time.Time
	EnrichedAt     *time.Time
	IsAvailable    bool
	EnrichPriority int
}

// Language 语言列表项。
type Language struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// UncategorizedLanguageKey 是「未分类」语言的哨兵 key。
//
// 用途：trending_repos.language 为 NULL / 空字符串的 repo 在聚合接口里归为这一项，
// 客户端 sidebar 也用这个值作为筛选 key（详见 GET /api/v1/repos?lang=__uncategorized__）。
//
// 选用双下划线包裹的字符串避免与任何真实语言名（如 "C" / "Go" / "Rust"）冲突；
// GitHub 历史上没有任何语言名以下划线开头，所以这是一个永久安全的哨兵值。
const UncategorizedLanguageKey = "__uncategorized__"

// UncategorizedLanguageLabel 是「未分类」在响应里的默认 label。
//
// 注意：客户端最终展示给用户的文案应该走客户端自己的 i18n 系统（如 Starcat 的
// String Catalog），后端这里只是给一个英文兜底，方便不做客户端的运维 / curl 调试时阅读。
const UncategorizedLanguageLabel = "Uncategorized"

// LanguageAggregate 是 GET /api/v1/languages 聚合响应里的单项。
//
// 与 `Language` 的区别：
//   - `Language` 只承载「GitHub trending 页面有哪些可选语言」（爬虫快照），
//     现在仅供历史 / debug 参考，不再驱动客户端 sidebar
//   - `LanguageAggregate` 承载「trending_repos 表里实际有数据的语言 + 每语言 repo 数量」，
//     客户端 sidebar 改用本结构（含 `__uncategorized__` 一项）
//
// JSON 字段命名走 envelope schema_version=1 的 snake_case 约定。
type LanguageAggregate struct {
	// Key 是语言的稳定标识（如 "Go" / "Python"）；
	// 对应「未分类」时取 `UncategorizedLanguageKey`（即 `"__uncategorized__"`）。
	// 客户端把它作为 GET /api/v1/repos?lang=<Key> 的查询参数。
	Key string `json:"key"`

	// Label 是语言的展示名（如 "Go" / "Python" / "Uncategorized"）。
	// 当前实现里 Key == Label（都是 GitHub 上规范的语言名）。
	// 「未分类」走 `UncategorizedLanguageLabel`；客户端可选择展示自己 i18n 的本地化文案。
	Label string `json:"label"`

	// Count 是该语言下当前 trending_repos 表中可用且已 enrich 的 repo 数量。
	// 不区分 since（daily/weekly/monthly），是三个 period 的并集。
	// 客户端 sidebar 在标签后展示该数字（与 Tags / Languages section 视觉一致）。
	Count int `json:"count"`
}

// Developer 开发者列表项（保持与旧 UserItem 兼容）。
type Developer struct {
	Avatar     string `json:"avatar"`
	Name       string `json:"name"`
	GitHubName string `json:"github_name"`
	Popular    *struct {
		Repo string `json:"repo"`
		Desc string `json:"desc"`
	} `json:"popular,omitempty"`
}
