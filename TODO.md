# TODO

> 全新服务,以下是从 R-01 v1.2 状态继承下来的待办。
> 不再保留 R-02 相关章节(本服务不接 zread,weekly-api 负责)。

## 阻断(编译 / 部署前必须解决)

- [ ] **go.mod 缺 3 个依赖**:`modernc.org/sqlite`、`github.com/robfig/cron/v3`、`github.com/joho/godotenv` 均未加入。
  ```bash
  cd supports/starcat-trending-api
  go get modernc.org/sqlite
  go get github.com/robfig/cron/v3
  go get github.com/joho/godotenv
  go mod tidy
  go build ./... && go vet ./...
  ```

## 高优(赶在首次部署前)

- [ ] **单元测试**:方案 §11.1 要求 8 个模块共 ~20 个测试,当前为 0。
  - `internal/store/sqlite.go`:Upsert / GetRepos / GetUnenriched / RecomputePriorities
  - `internal/enricher/github.go`:EnrichOne 成功/404/429
  - `internal/enricher/ratelimit.go`:主动退避 / Retry-After
  - `internal/tokenpool/tokenpool.go`:PickBest Quota-aware / MarkDead / AllDead
  - `internal/middleware/auth.go`:4 个 Bearer Token 验证
  - `internal/scheduler/cron.go`:SyncOnce 爬虫 + 落库
  - `internal/handler/repos.go`:envelope 形态 / InvalidSince
  - `internal/handler/admin.go`:task_id 返回
- [ ] **本地 smoke 测试**:启动 → healthz → `/api/v1/repos` → 无 key 401 → admin sync。
- [ ] **`/internal/sync/users` 逻辑空壳**:`HandleAdminSyncUsers` 目前仅返回 task_id,不触发实际爬取。需决定:接入 scheduler 做用户数据落库,或明确此为按需爬取的设计意图并标注。

## 中优

- [ ] **spider 类型双重定义风险**:`internal/spider/types.go` 的 `BuildBy`/`RepoItem`/`UserItem`/`LangItem` 与 `internal/model/trending.go` 的 `TrendingRepo`/`Developer`/`Language` 存在概念重叠。spider 类型是爬虫输出,model 类型是 DB/响应层——确保调用方不会混淆。
- [ ] **队列与 scheduler 的并发安全**:`EnrichQueue` 后台 worker 与 scheduler cron 的 `EnrichAll()` 可能同时 enrich 同一 repo(double-enrich 无害但浪费 API 配额)。考虑加分布式锁或统一走队列。

## 低优

- [ ] **部署阶段 1 周验收**
