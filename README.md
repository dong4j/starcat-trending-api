# Starcat Trending API

<!-- starcat-promo:start -->
<div align="center">
<a href="https://starcat.ink"><img src="https://raw.githubusercontent.com/starcat-app/starcat-pro/main/banner.webp" width="100%" alt="Starcat" /></a>

<p><strong>Self-hostable support API for Starcat GitHub Trending data.</strong></p>
<p>Starcat is a native macOS app that turns GitHub Stars into a searchable, organized and AI-assisted local knowledge base. Version 1.4.0 includes README rendering, knowledge-base RAG, GitHub notifications, My Projects, library and repository insights, macOS desktop widgets, tags and private notes, release tracking, repository health signals, AI summaries, semantic search, browser plugins, Alfred / uTools / Raycast search integrations, and self-hostable support APIs.</p>

<a href="https://github.com/starcat-app/homebrew-starcat"><img src="https://img.shields.io/badge/Install%20with-Homebrew-FBBF24?style=for-the-badge&logo=homebrew&logoColor=white" width="220" alt="Install with Homebrew"/></a>
<br/>
<sub><a href="./README-ZH.md">中文说明</a></sub>
</div>

<div align="center">
<a href="https://starcat.ink"><img src="https://img.shields.io/badge/website-starcat.ink-38BDF8?style=flat&color=blue" alt="website"/></a>
<a href="https://github.com/starcat-app/starcat-pro"><img src="https://img.shields.io/badge/support-starcat--pro-lightgrey.svg?style=flat&color=blue" alt="support"/></a>
<a href="https://github.com/starcat-app/homebrew-starcat"><img src="https://img.shields.io/badge/install-homebrew-lightgrey.svg?style=flat&color=blue" alt="homebrew"/></a>
<a href="https://github.com/starcat-app/starcat-localization"><img src="https://img.shields.io/badge/localization-open-lightgrey.svg?style=flat&color=blue" alt="localization"/></a>
</div>

<div align="center">
<img width="900" src="https://raw.githubusercontent.com/starcat-app/starcat-pro/main/main.webp" alt="Starcat main window"/>
</div>

**Preferred install method:**

```bash
brew tap starcat-app/starcat
brew trust starcat-app/starcat
brew install --cask starcat
```

**Useful links:**

- Home and downloads: https://starcat.ink
- Mac App Store: search for Starcat for GitHub
- Current Direct build: https://starcat.ink/downloads/Starcat-1.4.0-arm64.dmg
- Public support and release notes: https://github.com/starcat-app/starcat-pro
- Starcat App Homebrew tap: https://github.com/starcat-app/homebrew-starcat
- CLI / MCP: [starcat-cli](https://github.com/starcat-app/starcat-cli) / [Homebrew tap](https://github.com/starcat-app/homebrew-starcat-cli)
- AI Agent Skill: https://github.com/starcat-app/starcat-skill
- Browser plugins: [Chrome](https://github.com/starcat-app/starcat-chrome-plugin) / [Safari](https://github.com/starcat-app/starcat-safari-plugin)
- Launcher integrations: [Alfred](https://github.com/starcat-app/starcat-alfred-workflow) / [uTools](https://github.com/starcat-app/starcat-utools-plugin) / [Raycast](https://github.com/starcat-app/starcat-raycast-extension)
- Documentation: https://github.com/starcat-app/starcat-docs
- Website source: https://github.com/starcat-app/starcat-site
- Localization: https://github.com/starcat-app/starcat-localization

**Self-hostable support APIs:**

- [starcat-sharing-api](https://github.com/starcat-app/starcat-sharing-api)
- [starcat-trending-api](https://github.com/starcat-app/starcat-trending-api)
- [starcat-weekly-api](https://github.com/starcat-app/starcat-weekly-api)
- [starcat-wiki-api](https://github.com/starcat-app/starcat-wiki-api)
- [starcat-recommend-api](https://github.com/starcat-app/starcat-recommend-api)
- [starcat-discovery-api](https://github.com/starcat-app/starcat-discovery-api)

> Starcat provides hosted defaults for normal users. This API is open source so advanced users can inspect it, run it locally, or deploy their own instance.
<!-- starcat-promo:end -->

GitHub Trending data API written in Go with a consistent response envelope.

> trending-api uses **GitHub as its only source**. [`starcat-weekly-api`](../starcat-weekly-api/)
> provides zread weekly rankings through `GET /api/v1/trending/zread`; this service does not expose them.

## Features

- **Three-layer architecture**: spider (HTML crawler) → store (SQLite) → enricher (GitHub API metadata)
- **Bearer Token authentication**: required for every `/api/v1/*` and `/internal/*` endpoint
- **Token Pool**: multiple redundant GitHub PATs with quota-aware selection and failover
- **Rate Limit backoff**: reads the `X-RateLimit-Remaining` header and slows down when quota is low
- **Priority queue**: enriches top-ranked entries first (`enrich_priority DESC`)
- **Admin endpoints**: manually trigger synchronization through `/internal/sync/*`

## Quick Start

### Requirements

- Go 1.25+

### Run Locally

```bash
cp .env.example .env
# Edit .env and set API_KEYS and GITHUB_TOKENS
cd starcat-trending-api
go run ./cmd/server/
```

The default port is `5002`.

### .env Configuration

| Variable | Description | Default |
|------|------|--------|
| `PORT` | Server port | `5002` |
| `STORE_FILE` | SQLite database path | `./trending.db` |
| `METRICS_STORE_FILE` | Dedicated request metrics SQLite path | `./trending-metrics.db` |
| `API_KEYS` | Comma-separated Bearer Token allowlist | Required |
| `GITHUB_TOKENS` | Comma-separated GitHub PAT pool | Required |

## API Endpoints

Every data endpoint requires an `Authorization: Bearer <api-key>` header.

### `GET /api/v1/ping` (Authentication Required)

Returns the service identity and the build version injected from the release tag:

```json
{"schema_version":1,"data":{"service":"trending","version":"1.2.3","ok":true}}
```

### `GET /api/v1/repos?lang=&since=&limit=` (Authentication Required)

Returns a list of trending repositories with fields enriched through the GitHub API.

| Parameter | Type | Default | Description |
|------|------|--------|------|
| `lang` | string | — | Language filter, such as `Go` or `Python` |
| `since` | string | `daily` | `daily` / `weekly` / `monthly` |
| `limit` | int | 100 | Maximum number of results (up to 100) |

**Note**: The endpoint does not accept a `source=*` parameter. trending-api uses GitHub as its only source.
For zread data, use weekly-api `GET /api/v1/trending/zread`.

See `StarcatRepoCardDTO` in `internal/model/repo_card.go` for the response format.

### `GET /api/v1/languages` (Authentication Required)

Returns a language list with repository counts, **aggregated from the `trending_repos` table at request
time**. The list contains **only languages with current repositories** plus one `__uncategorized__`
entry for repositories whose language is NULL or empty.

> Before 2026-06-11, v1 returned the complete language menu scraped from GitHub Trending (more than
> 700 entries, most with no data in our database). The endpoint now aggregates languages from available
> data. The `key` and `label` response fields remain backward-compatible, and the response adds `count`.
> The client sidebar uses this endpoint to populate its trending language list.

Example response:

```json
{
  "schema_version": 1,
  "data": [
    { "key": "Python", "label": "Python", "count": 42 },
    { "key": "Go", "label": "Go", "count": 31 },
    { "key": "TypeScript", "label": "TypeScript", "count": 18 },
    { "key": "__uncategorized__", "label": "Uncategorized", "count": 5 }
  ],
  "meta": {
    "total": 4,
    "generated_at": "2026-06-11T12:00:00Z",
    "cache_status": "fresh"
  }
}
```

Field descriptions:

- `key`: Stable language identifier using GitHub's normalized language name, such as `Go` or `Python`.
  Uncategorized entries always use `__uncategorized__`, which can be passed to
  `GET /api/v1/repos?lang=__uncategorized__`.
- `label`: Display name. Regular languages use `key`; uncategorized entries use `Uncategorized`.
  Clients may override the label through their own i18n.
- `count`: Number of available, enriched repositories for the language in the current `trending_repos`
  table, combined across all three periods.

Sort order: Uncategorized entries **always appear last**. Other languages use `count DESC` with
`key ASC` as a stable tiebreaker.

#### Meaning of the `__uncategorized__` Sentinel in `/api/v1/repos`

`GET /api/v1/repos?lang=__uncategorized__` is equivalent to querying
`language IS NULL OR language = ''`. It returns every trending repository for which GitHub did not
identify a primary language and neither the spider nor the enricher could fill the value.

### `GET /api/v1/users?lang=&since=&sponsorable=` (Authentication Required)

Returns a list of trending developers.

### Admin Endpoints (Authentication Required)

| Endpoint | Description |
|------|------|
| `POST /internal/sync/repos` | Manually trigger a full recrawl and enrichment |
| `POST /internal/sync/languages` | Manually refresh the language list cache |
| `POST /internal/sync/users` | Manually recrawl the developer rankings |

### `GET /healthz` (Public)

Health check that returns `ok`.

## Authentication

Every `/api/v1/*` and `/internal/*` endpoint requires an
`Authorization: Bearer <api-key>` header.

Generate a new key:

```bash
bash ../scripts/gen-api-key.sh
```

## Project Structure

```
starcat-trending-api/
├── cmd/server/main.go              # Entry point: wires the three layers, scheduler, and authentication
├── internal/
│   ├── spider/                     # HTML crawler (goquery)
│   ├── store/                      # SQLite persistence
│   ├── enricher/                   # GitHub API field enrichment and rate limiting
│   ├── tokenpool/                  # GitHub Token Pool
│   ├── scheduler/                  # cron scheduling
│   ├── handler/                    # HTTP handlers (v1 and admin)
│   ├── middleware/                 # Bearer authentication middleware
│   └── model/                      # Data models (DB, DTO, and Envelope)
├── .env.example                    # Configuration template
├── fly.toml                        # Fly.io deployment configuration
├── Dockerfile
└── Makefile
```

## Deployment (Fly.io)

```bash
fly secrets set \
  API_KEYS="sk-starcat-prodKey1,sk-starcat-prodKey2" \
  GITHUB_TOKENS="ghp_token1,ghp_token2,ghp_token3" \
  STORE_FILE="/data/trending.db" \
  METRICS_STORE_FILE="/data/trending-metrics.db" \
  -a starcat-trending-api

fly deploy -a starcat-trending-api
```

## Technology

- **net/http**: Go standard library with no framework dependency
- **goquery**: HTML parsing with jQuery-like selectors
- **SQLite**: modernc.org/sqlite (pure Go, no CGO)
- **cron**: robfig/cron/v3 for scheduled jobs
- **godotenv**: loads environment variables from `.env`
