package spider

import "testing"

// fixtureTrendingArticleHTML 摘自 2026-06-18 GitHub trending 页真实 DOM（截断到单条 article）。
const fixtureTrendingArticleHTML = `
<div data-hpc>
  <article class="Box-row">
    <h2 class="h3 lh-condensed">
      <a href="/DeusData/codebase-memory-mcp">DeusData / codebase-memory-mcp</a>
    </h2>
    <p class="col-9 color-fg-muted my-1 tmp-pr-4">High-performance code intelligence MCP server.</p>
    <div class="f6 color-fg-muted mt-2">
      <span class="tmp-mr-3 d-inline-block ml-0 tmp-ml-0">
        <span class="repo-language-color" style="background-color: #555555"></span>
        <span itemprop="programmingLanguage">C</span>
      </span>
      <a href="/DeusData/codebase-memory-mcp/stargazers">6,364</a>
      <a href="/DeusData/codebase-memory-mcp/forks">525</a>
      <span class="d-inline-block float-sm-right">
        371 stars today
      </span>
    </div>
  </article>
  <article class="Box-row">
    <h2 class="h3 lh-condensed">
      <a href="/n0-computer/iroh">n0-computer / iroh</a>
    </h2>
    <p class="col-9 color-fg-muted my-1 tmp-pr-4">Rust network stack.</p>
    <div class="f6 color-fg-muted mt-2">
      <span itemprop="programmingLanguage">Rust</span>
      <a href="/n0-computer/iroh/stargazers">12,345</a>
      <a href="/n0-computer/iroh/forks">678</a>
      <span class="d-inline-block float-sm-right">120 stars today</span>
    </div>
  </article>
</div>
`

func TestRepoSpider_Parse_LanguageAndStats(t *testing.T) {
	sp := NewRepoSpider("daily", "")
	items := sp.Parse(fixtureTrendingArticleHTML)
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}

	first := items[0]
	if first.Repo != "DeusData/codebase-memory-mcp" {
		t.Errorf("repo: want DeusData/codebase-memory-mcp, got %q", first.Repo)
	}
	if first.Lang != "C" {
		t.Errorf("lang: want C, got %q", first.Lang)
	}
	if first.Stars != 6364 {
		t.Errorf("stars: want 6364, got %d", first.Stars)
	}
	if first.Forks != 525 {
		t.Errorf("forks: want 525, got %d", first.Forks)
	}
	if first.Change != 371 {
		t.Errorf("change: want 371, got %d", first.Change)
	}

	second := items[1]
	if second.Lang != "Rust" {
		t.Errorf("lang: want Rust, got %q", second.Lang)
	}
}
