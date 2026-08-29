# AGENTS.md — starcat-trending-api

> **唯一协作规范源**：本仓库根目录 `AGENTS.md` 是本项目协作规范的唯一正文维护源。
> 开工前还必须阅读并遵守上级 [`../AGENTS.md`](../AGENTS.md) 的跨仓规则。

## 项目概述

GitHub Trending 数据 API：爬虫抓取 trending 页 → SQLite 入库 → GitHub API enrich 补全字段。输出统一 envelope 格式，供 Starcat Trending 视图使用。**只走 GitHub 单源**；zread 周榜由 `starcat-weekly-api` 的 `GET /api/v1/repos?source=zread` 提供（聚合调用时带 `X-SC-Svc: weekly`），本服务不得暴露 zread 端点。生产经 `starcat-api` 聚合部署。

## 技术栈

- Go 1.25.0 · `net/http`
- `github.com/PuerkitoBio/goquery`（HTML 爬虫）
- `github.com/robfig/cron/v3` · `modernc.org/sqlite`
- `github.com/starcat-app/starcat-api-kit` v0.3.0
- `github.com/joho/godotenv`

## 关键目录

```
cmd/server/           # 入口
server/               # 可导出装配（聚合网关引用）
internal/spider/      # GitHub Trending HTML 爬虫
internal/store/       # SQLite 两表（trending_repos、trending_languages）
internal/enricher/    # GitHub API 补全 + 限流退避
internal/scheduler/   # cron 定时同步
internal/notifier/    # 可选 wiki 缓存预热
internal/tokenpool/   # GitHub PAT 池
scripts/deploy.sh
Makefile
```

## 开发与测试命令

```bash
cp .env.example .env          # API_KEYS、GITHUB_TOKENS 必填
make deps && make run         # 默认 PORT=5002
make build                    # bin/server
make check                    # fmt-check + vet + test（PR 前）
make docker-build
```

CI（`.github/workflows/go.yml`）：`gofmt -s` · `go vet ./...` · `docker build` · `go build` · `go test -race ./...`

另有 `.github/workflows/docker.yml` 推 ghcr.io 镜像（**禁止 Agent 触发**）。

环境变量见 `.env.example`：`PORT`（5002）、`STORE_FILE`、`METRICS_STORE_FILE`、`API_KEYS`、`GITHUB_TOKENS`、可选 `WIKI_API_URL`/`WIKI_API_KEY`。

## 代码与架构约束

- **三层架构**：spider → store → enricher；榜单前排优先 enrich（`enrich_priority DESC`）。
- **鉴权**：所有 `/api/v1/*` 与 `/internal/sync/*` 必须 Bearer；`/healthz` 不鉴权。
- **Token Pool**：多 PAT 冗余、quota-aware 选择、故障切换；经 `internal/tokenpool` 或 api-kit。
- **Rate Limit**：主动读 `X-RateLimit-Remaining`，低配额时减速。
- **Admin**：`/internal/sync/*` 手动触发同步。
- R-01 已删除旧端点 `/lang`、`/repo`、`/user`；勿恢复兼容层。
- 修改 `.github/workflows/` 时，对照 sharing / weekly 三仓模板是否需同步（release.yml 共用模式）。

## 安全与数据边界

- 禁止入库：`.env`、`trending.db`、`trending-metrics.db`、`bin/`、`logs/`、`coverage.out`。
- `GITHUB_TOKENS` 仅服务端；日志脱敏 `ghp_xxx****abcd`。
- Fly 生产 secrets 不用 `.env` 文件。

## 部署与发布禁令

未经 dong4j 明确授权，禁止：`make release`、`scripts/deploy.sh`、`fly deploy`、`git push`/`git tag`、docker push 到 ghcr.io。生产 Fly 仅经 `starcat-api`。
