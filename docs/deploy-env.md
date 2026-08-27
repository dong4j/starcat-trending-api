# Fly.io 部署环境变量配置

> 适用: `starcat-sharing-api` 通过 Fly.io 部署 + GitHub Actions 自动触发

## 当前生效规则

`cmd/server/main.go` 启动时:
```go
baseURL := os.Getenv("BASE_URL")
if baseURL == "" {
    baseURL = "https://starcat.ink"   // 代码里的兜底默认值
}
```

优先级: **`fly secrets` 里的值** > `fly.toml [env]` > `main.go` 里的硬编码默认值

## 配置 BASE_URL (推荐: fly secrets)

### 第一次设置
```bash
cd supports/starcat-sharing-api
fly secrets set BASE_URL=https://starcat.ink
```
一行做两件事: 加密存值 + 自动重启容器让新值生效。

### 后期换域名
```bash
fly secrets set BASE_URL=https://www.starcat.ink
```
不需要 push 代码, 不需要等 CI。

### 查看 / 调试
```bash
fly secrets list                          # 列出所有 (值脱敏)
fly ssh console -C "printenv BASE_URL"    # 进容器看真实值
fly logs                                  # 看启动日志
```

### 临时回退到代码默认值
```bash
fly secrets unset BASE_URL
```
容器重启后会用 `main.go` 里的兜底值。

## 跟 GitHub Actions 的关系

业务仓库的 GitHub Actions 只负责 CI、镜像和 GitHub Release，**不再部署独立 Fly App**。
Starcat 官方生产环境由 `starcat-api` 聚合仓从六个业务仓库构建并统一部署；这里的
`fly secrets` / `fly deploy` 命令仅供第三方自托管独立实例时使用。

```
业务仓库 tag → CI + GitHub Release
starcat-api 聚合发布 → 单一 Fly App
```

## 别踩的坑

| 错误做法 | 为什么 |
|---------|--------|
| 把 URL 写进 `fly.toml` 的 `[env]` | 那个文件进 git 仓库, 即使是公开 URL 也不该当环境配置用 |
| 改完 `main.go` 默认值就以为生效 | 跑过 `fly secrets set` 之后, 默认值就被永久覆盖了, 改代码没用 |
| 改了 secret 几秒后没看到效果 | 等滚动重启 (5-10s), 还不对就 `fly logs` |
| 把 `FLY_API_TOKEN` 存到应用 secrets | 自托管时它属于部署端凭据，不应进入应用运行环境 |

## 其他常用 env 对照

| 变量 | 当前值 | 存哪 | 改法 |
|------|--------|------|------|
| `PORT` | `5001` | `fly.toml [env]` | 改 `fly.toml` 提交 (公开值, 不敏感) |
| `STORE_FILE` | `/data/data.json` | `fly.toml [env]` | 改 `fly.toml` 提交 (公开值) |
| `GOMAXPROCS` | `1` | `fly.toml [env]` | 改 `fly.toml` 提交 |
| `GOGC` | `100` | `fly.toml [env]` | 改 `fly.toml` 提交 |
| `BASE_URL` | (代码兜底 `https://starcat.ink`) | **fly secrets** | `fly secrets set BASE_URL=...` |
| `FLY_API_TOKEN` | (无) | **GitHub repo secrets** | GitHub Settings → Secrets → Actions |
