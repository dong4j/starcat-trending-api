// Package model 定义 StarcatRepoCardDTO 统一卡片结构。
//
// R-01 v1.2: 字段严格分两层 —
//
//	核心字段（顶层）: GitHub /repos/{o}/{r} 原生语义
//	扩展段（嵌套子对象）: trending / weekly / sharing 场景发现型语义
//
// 硬边界规则（同时刻在 supports/docs/R-01-总体设计.md §3.9）:
//
//	红线1: 非 Repo metadata 字段不能放顶层
//	红线2: 不能把扩展段字段提升到顶层
//	红线3: 不能在扩展段塞非本场景语义的字段
package model

// StarcatRepoCardDTO 统一卡片数据，所有 /api/v1/repos /api/v1/projects 共用。
type StarcatRepoCardDTO struct {
	GhRepoID      int64              `json:"gh_repo_id"`
	FullName      string             `json:"full_name"`
	Owner         string             `json:"owner"`
	Repo          string             `json:"repo"`
	OwnerAvatar   *string            `json:"owner_avatar"`
	Description   *string            `json:"description"`
	Language      *string            `json:"language"`
	Stars         int                `json:"stars"`
	Forks         int                `json:"forks"`
	Watchers      int                `json:"watchers"`
	Subscribers   int                `json:"subscribers"`
	Topics        []string           `json:"topics"`
	Homepage      *string            `json:"homepage"`
	LicenseSpdx   *string            `json:"license_spdx"`
	IsArchived    bool               `json:"is_archived"`
	IsFork        bool               `json:"is_fork"`
	IsPrivate     bool               `json:"is_private"`
	DefaultBranch *string            `json:"default_branch"`
	OpenIssues    int                `json:"open_issues"`
	PushedAt      *string            `json:"pushed_at"`
	UpdatedAt     *string            `json:"updated_at"`
	CreatedAt     *string            `json:"created_at"`
	HTMLURL       *string            `json:"html_url"`
	Trending      *TrendingExtension `json:"trending,omitempty"`
	Weekly        *WeeklyExtension   `json:"weekly,omitempty"`
}

// TrendingExtension trending 场景发现型字段。
//
// R-02 v0.2（2026-06-10）：删 v0.3 引入的 zread 字段（DescriptionZh / ZreadWikiID）。
// trending-api 重新收敛到 GitHub 单源数据；zread 数据已迁到 weekly-api。
type TrendingExtension struct {
	Change       int                   `json:"change"`
	Contributors []TrendingContributor `json:"contributors"`
}

// TrendingContributor 贡献者简要信息。
type TrendingContributor struct {
	Avatar string `json:"avatar"`
	Login  string `json:"login"`
}

// WeeklyExtension weekly 场景发现型字段（trending API 不填充，但 struct 保留用于 shared model）。
type WeeklyExtension struct {
	FirstIssue int    `json:"first_issue"`
	IssueURL   string `json:"issue_url"`
}
