# Starcat Trending API

<!-- starcat-promo:start -->
<div align="center">
<a href="https://starcat.ink"><img src="https://raw.githubusercontent.com/starcat-app/starcat-pro/main/banner.webp" width="100%" alt="Starcat" /></a>

<p><strong>这是 Starcat GitHub Trending 数据的可自部署支撑服务。</strong></p>
<p>Starcat 是一款原生 macOS 应用，可以把 GitHub Stars 变成可搜索、可整理、可用 AI 追问的本地知识库，并通过桌面客户端、插件、CLI 与可自部署服务组成完整生态。</p>

<a href="https://github.com/starcat-app/homebrew-starcat"><img src="https://img.shields.io/badge/Install%20with-Homebrew-FBBF24?style=for-the-badge&logo=homebrew&logoColor=white" width="220" alt="Install with Homebrew"/></a>
<br/>
<sub><a href="./README.md">English</a></sub>
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

**首选 Homebrew 安装：**

```bash
brew tap starcat-app/starcat
brew trust starcat-app/starcat
brew install --cask starcat
```

**相关链接：**

- 官网与下载: https://starcat.ink
- Mac App Store: 搜索 Starcat for GitHub
- 公开支持与发布说明: https://github.com/starcat-app/starcat-pro
- Starcat App Homebrew tap: https://github.com/starcat-app/homebrew-starcat
- CLI / MCP: [starcat-cli](https://github.com/starcat-app/starcat-cli) / [Homebrew tap](https://github.com/starcat-app/homebrew-starcat-cli)
- AI Agent Skill: https://github.com/starcat-app/starcat-skill
- 浏览器插件: [Chrome](https://github.com/starcat-app/starcat-chrome-plugin) / [Safari](https://github.com/starcat-app/starcat-safari-plugin)
- 启动器集成: [Alfred](https://github.com/starcat-app/starcat-alfred-workflow) / [uTools](https://github.com/starcat-app/starcat-utools-plugin) / [Raycast](https://github.com/starcat-app/starcat-raycast-extension)
- 官方文档: https://github.com/starcat-app/starcat-docs
- 官网源码: https://github.com/starcat-app/starcat-site
- 本地化: https://github.com/starcat-app/starcat-localization

**可自部署支撑 API：**

- [starcat-sharing-api](https://github.com/starcat-app/starcat-sharing-api)
- [starcat-trending-api](https://github.com/starcat-app/starcat-trending-api)
- [starcat-weekly-api](https://github.com/starcat-app/starcat-weekly-api)
- [starcat-wiki-api](https://github.com/starcat-app/starcat-wiki-api)
- [starcat-recommend-api](https://github.com/starcat-app/starcat-recommend-api)
- [starcat-discovery-api](https://github.com/starcat-app/starcat-discovery-api)

> Starcat 为普通用户提供默认托管服务。这个 API 开源出来，是为了让进阶用户可以审查实现、本地运行，或部署自己的实例。
<!-- starcat-promo:end -->

GitHub Trending 数据 API，使用 Go 语言实现，输出统一 envelope 格式。

> trending-api **只走 GitHub 单源**。zread 周榜数据由 [`starcat-weekly-api`](../starcat-weekly-api/)
> 的 `GET /api/v1/trending/zread` 提供，不在本服务暴露。

## 特性

- **三层架构**：spider（HTML 爬虫）→ store（SQLite）→ enricher（GitHub API 补全）
- **Bearer Token 鉴权**：所有 `/api/v1/*` 和 `/internal/*` 端点强制鉴权
- **Token Pool**：多 GitHub PAT 冗余 + Quota-aware 选择 + 故障切换
- **Rate Limit 退避**：主动读 `X-RateLimit-Remaining` 头，低配额时自动减速
- **优先级队列**：榜单前排优先 enrich（`enrich_priority DESC`）
- **Admin endpoint**：手动触发同步（`/internal/sync/*`）

## 快速开始

### 环境要求

- Go 1.25+

### 本地运行

```bash
cp .env.example .env
# 编辑 .env，填入 API_KEYS 和 GITHUB_TOKENS
cd starcat-trending-api
go run ./cmd/server/
```

默认端口 `5002`。

### .env 配置

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `PORT` | 服务端口 | `5002` |
| `STORE_FILE` | SQLite 数据库路径 | `./trending.db` |
| `METRICS_STORE_FILE` | 独立请求指标 SQLite 路径 | `./trending-metrics.db` |
| `API_KEYS` | Bearer Token 白名单（逗号分隔） | 必填 |
| `GITHUB_TOKENS` | GitHub PAT 池（逗号分隔） | 必填 |

## API 接口

所有数据接口需要 `Authorization: Bearer <api-key>` 头。

### `GET /api/v1/ping`（需鉴权）

返回服务标识，以及由发布 tag 注入的构建版本：

```json
{"schema_version":1,"data":{"service":"trending","version":"1.2.3","ok":true}}
```

### `GET /api/v1/repos?lang=&since=&limit=`（需鉴权）

返回 trending 仓库列表（含 GitHub API 补全字段）。

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `lang` | string | — | 语言过滤（如 `Go`、`Python`） |
| `since` | string | `daily` | `daily` / `weekly` / `monthly` |
| `limit` | int | 100 | 返回数量上限（最大 100） |

**注意**：不接受 `source=*` 参数。trending-api 固定走 GitHub 单源；zread 数据请改用
weekly-api `GET /api/v1/trending/zread`。

响应示例见 `internal/model/repo_card.go` 中的 `StarcatRepoCardDTO`。

### `GET /api/v1/languages`（需鉴权）

返回**基于 `trending_repos` 表实时聚合**的语言列表（含 repo 数量），仅包含**当前真有 repo
的语言** + 一项 `__uncategorized__`（语言为 NULL/空的 repo 集合）。

> 历史 v1（2026-06-11 前）返回的是 GitHub trending 页面爬到的全量语言菜单（700+ 项，绝大多数
> 在我们库里没数据），现已改为按实际数据聚合。响应字段在 `key` / `label` 上向后兼容，新增
> `count` 字段。客户端 sidebar 直接用本接口驱动 trending 语言列表。

响应示例：

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

字段说明：

- `key`：语言稳定标识（GitHub 规范化语言名，如 `Go` / `Python`）；
  「未分类」恒为 `__uncategorized__`，可作为 `GET /api/v1/repos?lang=__uncategorized__` 查询参数
- `label`：展示名（普通语言 = `key`；未分类 = `Uncategorized`，客户端可用自己的 i18n 覆盖）
- `count`：该语言下当前 trending_repos 表中可用且已 enrich 的 repo 数量（三个 period 合并）

排序规则：未分类**永远排在最后**，其它语言按 `count DESC` + `key ASC` 兜底稳定。

#### `__uncategorized__` 哨兵在 `/api/v1/repos` 的语义

`GET /api/v1/repos?lang=__uncategorized__` 等价于查询 `language IS NULL OR language = ''`，
返回所有 GitHub 没识别到主语言的 trending repo（spider/enricher 都补不全的 case）。

### `GET /api/v1/users?lang=&since=&sponsorable=`（需鉴权）

返回 trending 开发者列表。

### Admin Endpoints（需鉴权）

| 端点 | 说明 |
|------|------|
| `POST /internal/sync/repos` | 手动触发全量重爬 + enrich |
| `POST /internal/sync/languages` | 手动刷新语言列表缓存 |
| `POST /internal/sync/users` | 手动触发重爬开发者榜单 |

### `GET /healthz`（公开）

健康检查，返回 `ok`。

## 运营与调用指标

- `GET /internal/stats`：周期规模、可见性、补全积压、不可用记录和数据新鲜度。
- `GET /internal/metrics/{summary,timeseries,routes,status-codes}`：鉴权后的聚合调用指标。
- 现有 `GET /api/v1/repos` 与 `GET /api/v1/languages` 作为 Admin Console 的受限数据视图。

指标只保留路由模板，不保存凭据、查询串、请求体、客户端地址或真实路径参数。

## 鉴权

所有 `/api/v1/*` 和 `/internal/*` 端点需要 `Authorization: Bearer <api-key>` 头。

生成新 key：

```bash
bash ../scripts/gen-api-key.sh
```

## 项目结构

```
starcat-trending-api/
├── cmd/server/main.go              # 入口：装配三层 + scheduler + 鉴权
├── internal/
│   ├── spider/                     # HTML 爬虫（goquery）
│   ├── store/                      # SQLite 持久化
│   ├── enricher/                   # GitHub API 字段补全 + Rate Limit
│   ├── tokenpool/                  # GitHub Token Pool
│   ├── scheduler/                  # cron 定时调度
│   ├── handler/                    # HTTP handler（v1 + admin）
│   ├── middleware/                 # Bearer 鉴权中间件
│   └── model/                      # 数据模型（DB + DTO + Envelope）
├── .env.example                    # 配置模板
├── fly.toml                        # Fly.io 部署配置
├── Dockerfile
└── Makefile
```

## 部署（Fly.io）

```bash
fly secrets set \
  API_KEYS="sk-starcat-prodKey1,sk-starcat-prodKey2" \
  GITHUB_TOKENS="ghp_token1,ghp_token2,ghp_token3" \
  STORE_FILE="/data/trending.db" \
  METRICS_STORE_FILE="/data/trending-metrics.db" \
  -a starcat-trending-api

fly deploy -a starcat-trending-api
```

## 技术选型

- **net/http**：Go 标准库，无框架依赖
- **goquery**：HTML 解析（类 jQuery 选择器）
- **SQLite**：modernc.org/sqlite（纯 Go，无 CGO）
- **cron**：robfig/cron/v3（定时调度）
- **godotenv**：.env 文件加载
