package spider

import (
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/dong4j/starcat-trending-api/pkg/utils"
)

// RepoSpider 仓库 trending 爬虫
// 对应 Python 版本的 RepoSpider
type RepoSpider struct {
	*BaseRequest
	Since string
	Lang  string
}

// NewRepoSpider 创建仓库爬虫
func NewRepoSpider(since, lang string) *RepoSpider {
	return &RepoSpider{
		BaseRequest: NewBaseRequest(),
		Since:       since,
		Lang:        lang,
	}
}

// GetURL 获取仓库列表 URL
// URL 格式: https://github.com/trending/{lang}?since={since}
func (r *RepoSpider) GetURL() string {
	base := r.BaseURL
	if r.Lang != "" {
		// URL 编码 lang 参数，如 C# -> c%23
		langEncoded := encodeLangParam(r.Lang)
		base = fmt.Sprintf("%s/%s", base, langEncoded)
	}
	return fmt.Sprintf("%s?since=%s", base, r.Since)
}

// encodeLangParam 编码语言参数
// Python 版本没有显式编码，但 Go 的 URL 构建需要处理特殊字符
func encodeLangParam(lang string) string {
	// C# 在 URL 中显示为 c%23
	if lang == "C#" {
		return "c%23"
	}
	return lang
}

// Parse 解析仓库列表页
func (r *RepoSpider) Parse(html string) []RepoItem {
	var items []RepoItem

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return items
	}

	// 找到 [data-hpc] 下的 article 标签
	doc.Find("[data-hpc]").Find("article").Each(func(i int, article *goquery.Selection) {
		item := RepoItem{}

		// 获取 repo 路径 (href)
		//
		// 关键约束（2026-06-11 修复）：
		// GitHub 的 trending HTML 里 `<a href="/owner/repo">`，href 是 *带前导斜杠*
		// 的根路径。如果直接赋给 item.Repo，下游 scheduler 的
		// `strings.SplitN(item.Repo, "/", 2)` 会拆成 ["", "owner/repo"]，
		// owner 取到空字符串，fullName 落库时变成 "/owner/repo"。
		// enricher 接着拼 https://api.github.com/repos//owner/repo（双斜杠），
		// GitHub 一律返回 404，触发 MarkUnavailable，结果整张表 is_available=0、
		// enriched_at=NULL，前端 /api/v1/repos 永远空数组、cache_status=cold。
		//
		// 此处必须 strip 前导 "/"，让 item.Repo 保持 "owner/repo" 干净格式。
		// 不能在下游补救（scheduler 也加了 defensive check 兜底，但根因应在源头修）。
		h2 := article.Find("h2")
		repo := h2.Find("a").AttrOr("href", "")
		item.Repo = strings.TrimPrefix(repo, "/")
		if item.Repo == "" {
			return
		}

		// 获取描述
		var descParts []string
		article.Find("p").Each(func(_ int, p *goquery.Selection) {
			descParts = append(descParts, p.Text())
		})
		item.Desc = strings.TrimSpace(strings.Join(descParts, " "))

		// GitHub trending 页脚元数据行（2026-06-18 结构）：
		// `<div class="f6 color-fg-muted mt-2">` 内含 language / stars / forks / change。
		// 旧版用「article 第 3 个 div + 三层嵌套 span」解析 language，DOM 改版后
		// 全部落空，spider 入库 language=""，进而冲掉 enricher 补全值（见 store UpsertRepo）。
		metaRow := article.Find("div.f6.color-fg-muted").First()
		if metaRow.Length() == 0 {
			// 兜底：部分 A/B 页可能没有 f6 class，退到含 programmingLanguage 的容器
			metaRow = article.Find(`span[itemprop="programmingLanguage"]`).First().Closest("div")
		}

		// 语言：GitHub 现用 schema.org microdata
		if langText := strings.TrimSpace(article.Find(`span[itemprop="programmingLanguage"]`).First().Text()); langText != "" {
			item.Lang = langText
		}

		// stars / forks：直接挂在 meta 行下的 <a href=".../stargazers|forks">
		metaRow.Find("a").Each(func(_ int, a *goquery.Selection) {
			href, _ := a.Attr("href")
			text := strings.TrimSpace(a.Text())
			switch {
			case strings.HasSuffix(href, "/forks"):
				item.Forks = utils.GetListNum([]string{text})
			case strings.HasSuffix(href, "/stargazers"):
				item.Stars = utils.GetListNum([]string{text})
			}
		})

		// change：浮动在右侧的「NNN stars today / this week / this month」
		article.Find("span").EachWithBreak(func(_ int, span *goquery.Selection) bool {
			text := strings.TrimSpace(span.Text())
			if strings.Contains(text, "stars today") ||
				strings.Contains(text, "stars this week") ||
				strings.Contains(text, "stars this month") {
				item.Change = utils.GetListNum([]string{text})
				return false
			}
			return true
		})

		// build_by：2026-06 起 trending 列表页已移除贡献者头像行；保留空切片即可。

		items = append(items, item)
	})

	return items
}

// GetItems 获取 trending 仓库列表
func (r *RepoSpider) GetItems() []RepoItem {
	html, err := r.Fetch(r.GetURL())
	if err != nil {
		return []RepoItem{}
	}
	return r.Parse(html)
}
