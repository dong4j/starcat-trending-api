# Changelog

本项目的所有重要变更都会记录在此文件中。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### Fixed
- **Trending 侧栏全「未分类」+ repo 卡片无 language**（2026-06-18 生产 fly.dev 排查）。
  - 根因 ①：GitHub trending 页 DOM 改版，旧 spider 用「第 3 个 div + 三层嵌套 span」解析 language，
    现页使用 `span[itemprop="programmingLanguage"]`，导致 spider 入库 `language=""`。
  - 根因 ②：`UpsertRepo` ON CONFLICT 无条件 `language = excluded.language`，每小时 cron 重爬
    把 enricher 从 GitHub API 补全的语言盖回空串。
  - 修复：`internal/spider/repo.go` 改用 microdata + `div.f6` 元数据行解析 stars/forks/change；
    `internal/store/sqlite.go` 冲突更新改为 `COALESCE(NULLIF(excluded.language,''), …)`；
    `internal/scheduler/cron.go` spider 空 language 写 NULL 而非 `""`。
  - 单测：`spider/repo_test.go` fixture 回归 + `store_test.go::TestUpsertRepo_EmptyLanguageDoesNotOverwrite`。

### Changed
- **`GET /api/v1/languages` 改造为基于 `trending_repos` 实际数据聚合**（2026-06-11 dong4j 反馈）。
  - 历史实现：返回 `trending_languages` 表（GitHub trending 页面爬虫快照，700+ 全量语言菜单），
    与实际是否有 repo 完全无关，客户端展示这个列表 90%+ 的语言下都没数据。
  - 新实现：基于 `trending_repos` 表 `WHERE is_available = 1 AND enriched_at IS NOT NULL`
    实时聚合，`GROUP BY language` 输出实际有 repo 的语言 + 每语言 `count`。
  - **新增 `__uncategorized__` 哨兵项**：`language IS NULL OR language = ''` 的 repo 归到一桶，
    label 为 `Uncategorized`（客户端可走 i18n 覆盖）。客户端可通过
    `GET /api/v1/repos?lang=__uncategorized__` 拉这部分 repo。
  - 排序：未分类**永远排在最后**，其它按 `count DESC, key ASC`。
  - 响应字段兼容：`key` / `label` 保持 v1 语义；新增 `count` 字段，旧客户端忽略即可。
  - 实现：
    - `internal/model/trending.go`：新增 `LanguageAggregate{key,label,count}` + 哨兵常量
    - `internal/store/store.go` + `sqlite.go`：新增 `GetAggregatedLanguages()`
    - `internal/store/sqlite.go::GetRepos`：识别 `lang=__uncategorized__` 哨兵
    - `internal/handler/languages.go`：handler 改用 `store.Store` 而非 `*scheduler.Scheduler`
    - `cmd/server/main.go`：路由改注 `sqliteStore`
  - 单测：`store_test.go` +4 case（聚合基本分组 / 空表 / 全是未分类 / GetRepos 哨兵）；
    新增 `handler/languages_test.go` 3 case（fresh / cold / store 错误）。

### Added
- **R-03 (2026-06-11)**：新增 `GET /api/v1/ping` 端点，专给 Starcat 客户端「测试连接」按钮用。
  - 走 BearerAuth 中间件，鉴权通过返回 200 + envelope `{data: {service: "trending", ok: true}}`；
    无效 / 缺失 Key → 401；服务故障 → 5xx。
  - 实现：`internal/handler/ping.go` + `internal/handler/ping_test.go`（7 case）。
  - 设计意图：替代「`/healthz`（无鉴权，无法验 Key）+ 业务 endpoint 充当 auth probe（副作用大）」
    的旧两阶段探测，把「服务可达 + Key 正确」收敛到一个语义明确的端点。
  - 跨项目约定：本 `ping.go` 与 weekly / sharing / wiki 三个项目「除 import path 外 byte-level 一致」。

## [0.1.1] - 2026-06-10

补齐 v0.1.0 全新服务单测基线（0 项 → 75 项 case 全绿）。

### Added
- **`internal/store/store_test.go`**：11 case
  - 4 索引存在性 + UpsertRepo 幂等（同 (full_name, since) 二次写入更新）
  - GetRepos 多条件过滤（since / lang / is_available / enriched_at）+ limit 默认 100
  - GetUnenrichedRepos 倒序按 priority
  - UpdateEnriched 字段映射 + stars 单调递增保护（CASE WHEN）
  - MarkUnavailable 翻转 is_available
  - RecomputePriorities 三档：top30=100, next70=50, 其余=10
  - UpsertLanguages 事务覆写 + GetLanguages 按 label 排序
  - queryRepos Scan 全 24 字段端到端往返（17 enricher 字段 + 7 基础字段）
  - Close 后再操作报错
- **`internal/handler/handler_test.go`**：11 case
  - writeJSON / writeJSONWithMeta envelope JSON 字段对齐 + omitempty
  - writeError 6 种 HTTP status code 透传 + ErrorEnvelope 序列化
  - details=nil 不输出 details 字段
- **`internal/handler/repos_test.go`**：15 case
  - 7 个 query 场景（默认 since / 3 个合法 since / since 非法 / source 拒绝 ×3 / lang 透传 / limit clamp / limit 正常值）
  - cacheStatus fresh/cold
  - 内部 store 错误 → 500
  - TrendingRepo → StarcatRepoCardDTO 字段映射（含 contributors 解析 + `/` 前缀去除）
  - description 空时回退到 desc_text
  - 用 fakeStore 实现 store.Store interface（10 个方法,只走 GetRepos 路径）
- **`internal/middleware/auth_test.go`**：10 case
  - NewBearerAuth 跳过空 key / 全空白 key
  - 缺 Authorization / 错前缀（Basic/Token/bearer 小写） / 错 key / 通过
  - 多个 key 注册,任一合法都通过
  - token 前后空白被 trim
  - maskKey 短/16/长字符串三种长度
- **`internal/enricher/ratelimit_test.go`**：7 case
  - Wait 第一次不 sleep / 第二次 sleep 补齐 / 间隔够远不 sleep
  - Pause 期间 sleep / Pause 已过期立即返回 / Reset 清空 pausedUntil
  - 5 worker 并发 Wait 总时间 ≈ (N-1) × minInterval（漏桶语义）
- **`internal/enricher/queue_test.go`**：7 case
  - NewEnrichQueue workerCnt <= 0 走默认 2
  - Start 启动 worker + Stop 关闭
  - 重复 Start 幂等（不启两轮 worker）
  - Stop 在没 Start 时不 panic
  - Stats 初始 0
  - Start + Stop 并发不竞态
  - 用临时 SQLite store 构造最小 EnrichQueue
- **`internal/tokenpool/tokenpool_test.go`**：15 case
  - New 跳过空 / 全空白
  - PickBest 5 场景（全 dead / 一活多死 / unknown 优先 / 选 max / exhausted 跳过 / reset 已过重新可用）
  - UpdateFromResponse 解析 X-RateLimit-Remaining/Reset / 401 翻 Dead / 5xx 累计 5 次翻 Dead / 2xx 复位 / 4xx 不累计
  - EarliestReset 选 alive 中最早 / 全部 dead 或 zero reset 返 zero
  - Stats alive/dead/totalRemaining 计数
  - maskToken 三长度 / trimSpace 五种空白

### Notes
- 5 包 build/vet/test 全绿（store / handler / middleware / enricher / tokenpool）；cmd / model / scheduler / spider / version / pkg/utils 暂未加测试,理由：
  - **cmd / version**：纯 main 入口或常量包,无可测逻辑
  - **model**：纯数据结构,字段往返由 store 测覆盖
  - **scheduler**：依赖 spider + store + enricher 完整链,e2e curl 已验证
  - **spider**：依赖真实 GitHub HTML,需 mock HTTP server + HTML fixture,优先级低
- 用 `t.TempDir()` + `NewSQLiteStore` 起临时 SQLite db,测试结束自动清理,互不污染
- 部分子测试用 `t.Run` 共享 setup,共 **75 项**子测试 + **5 包**全绿

## [0.1.0] - 2026-06-10

starcat-trending-api 首版（Go 重写 + GitHub Trending 单源）。

### Added
- **三层架构**：spider（GitHub Trending 爬虫）→ store（SQLite）→ enricher（GitHub API 补全）
- **SQLite 持久化**：`trending_repos` + `trending_languages` 表，WAL 模式
- **GitHub API Enricher**：补全 `gh_repo_id`、topics、license、owner_avatar 等 18 个字段
- **Token Pool**：多 GitHub PAT 冗余 + Quota-aware `PickBest()` + 故障切换
- **Rate Limit 退避**：主动读 `X-RateLimit-Remaining` 头，低配额自动减速
- **Cron Scheduler**：daily/weekly/monthly 定时爬取 + 长尾 enrich + 过期清理
- **Enrich Queue**：优先级队列 + worker pool（`enrich_priority DESC`）
- **Bearer Token 鉴权**：所有 `/api/v1/*` 和 `/internal/*` 端点强制鉴权
- **Admin Endpoints**：`/internal/sync/{repos,languages,users}` 手动触发同步
- **Envelope 统一响应**：`schema_version` + `data` / `error` + `meta`
- **`.env` 配置**：godotenv 加载，敏感值走 fly secrets

### API
- `GET /healthz` - 健康检查（无需鉴权）
- `GET /api/v1/repos?since=daily|weekly|monthly&lang=&limit=` - 仓库卡片列表
- `GET /api/v1/languages` - 语言列表
- `GET /api/v1/users?sponsorable=true` - 趋势贡献者
- `POST /internal/sync/{repos,languages,users}` - 手动触发同步

### 数据来源
- **trending-api 只走 GitHub 单源**。zread 数据由 `starcat-weekly-api` 的
  `GET /api/v1/trending/zread` 提供，不在 trending-api 暴露。

### Notes
- 当前 `GET /api/v1/repos` 显式拒绝 `source=*` 参数（400），引导客户端去 weekly-api 取 zread。
- 跨项目 envelope 共享约定已解除：sharing / weekly / wiki / trending 各 API 的 `Meta`
  字段由各 API 自治，不再做跨项目 byte-level 一致性检查。

---

## 历史背景（不再追溯）

### v0.2.0 之前（2026-06-09 之前）
- Python/FastAPI 原版（[doforce/github-trending](https://github.com/doforce/github-trending)）
- Go 1.x 重写：标准库 `net/http` + `goquery` HTML 解析
- 旧端点 `GET /`、`GET /lang`、`GET /repo`、`GET /user`（已删除）

[Unreleased]: https://github.com/dong4j/starcat-trending-api/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/dong4j/starcat-trending-api/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/dong4j/starcat-trending-api/releases/tag/v0.1.0
