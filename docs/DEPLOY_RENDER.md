# Render 部署指南

> 适用场景：完全零配置、GitHub 集成最强、适合 MVP
> ⚠️ **注意**：免费层 15 分钟无请求后会休眠，首次访问需 30-50 秒冷启动

## 📋 前置条件

1. 拥有 GitHub 账号
2. 项目已推送到 GitHub

## 🚀 部署步骤

### 方式 1：通过 Blueprint 自动部署（推荐）

1. 登录 [Render Dashboard](https://dashboard.render.com/)
2. 点击 **New** → **Blueprint**
3. 连接你的 GitHub 仓库 `starcat-app/starcat-trending-api`
4. Render 会自动检测 `render.yaml` 并创建服务
5. 点击 **Apply** 开始部署

### 方式 2：手动创建 Web Service

1. 登录 [Render Dashboard](https://dashboard.render.com/)
2. 点击 **New** → **Web Service**
3. 连接 GitHub 仓库
4. 配置：
   - **Environment**: `Docker`
   - **Region**: `Singapore` (离中国近)
   - **Branch**: `main`
   - **Dockerfile Path**: `./Dockerfile`
   - **Plan**: `Free`
5. 点击 **Create Web Service**

## 🔧 环境变量

在 Render Dashboard → 你的服务 → **Environment** 标签页添加：

| 变量 | 值 | 说明 |
|------|----|------|
| `PORT` | `5002` | 监听端口 |
| `GOMAXPROCS` | `1` | 限制 CPU 核心数 |
| `GOGC` | `100` | GC 触发阈值 |
| `GO_ENV` | `production` | 运行环境标识 |

## 🧪 验证部署

部署成功后访问：

```
https://starcat-trending-api-xxxx.onrender.com/
```

测试 API：

```bash
curl https://starcat-trending-api-xxxx.onrender.com/
curl https://starcat-trending-api-xxxx.onrender.com/lang | head -c 200
curl https://starcat-trending-api-xxxx.onrender.com/repo?lang=go | head -c 200
```

## ⚠️ 免费层休眠问题

**问题**：
- 15 分钟无请求后，Render 会将服务置为休眠
- 下次请求需要 30-50 秒冷启动

**解决方案**：

1. **使用 UptimeRobot 等监控**（推荐）：
   ```
   URL: https://your-app.onrender.com/
   Interval: 14 minutes
   ```
   - 这是 Render 官方推荐做法，不会封号

2. **升级到 Starter Plan**（$7/月）：
   - 不休眠
   - 适合生产环境

3. **接受冷启动**（开发/演示用）：
   - README 中注明"首次访问可能需要等待"

## 💰 费用

| 资源 | 免费层 | Starter | Standard |
|------|--------|---------|----------|
| 价格 | $0 | $7/月 | $25/月 |
| 休眠 | 15 分钟无请求 | 不休眠 | 不休眠 |
| 冷启动 | 30-50s | 无 | 无 |
| 内存 | 512MB | 512MB | 2GB |
| CPU | 0.1 CPU | 0.5 CPU | 1 CPU |
| 带宽 | 100GB/月 | 100GB/月 | 400GB/月 |

**对于本项目**：
- 免费层已足够（无状态 API）
- 休眠问题可用 UptimeRobot 缓解

## 🌍 修改区域

| Region | 代码 | 适合用户 |
|--------|------|---------|
| Oregon | `oregon` | 美国西海岸 |
| Ohio | `ohio` | 美国东海岸 |
| Frankfurt | `frankfurt` | 欧洲 |
| **Singapore** | `singapore` | **亚洲 (推荐)** |
| Virginia | `virginia` | 美国东海岸 |

在 `render.yaml` 中修改 `region` 字段。

## 🐛 故障排查

### 部署失败

1. 查看 **Logs** 标签页的错误信息
2. 常见问题：
   - **Dockerfile 路径错误**：检查 `dockerfilePath`
   - **构建超时**：免费层构建限制 15 分钟，本项目 < 1 分钟
   - **内存不足**：升级到 Starter ($7/月)

### 运行时错误

```bash
# 在 Dashboard → Logs 标签页查看
# 或启用实时日志
```

## 📚 参考

- [Render 官方文档](https://render.com/docs)
- [Blueprint Spec](https://render.com/docs/blueprint-spec)
- [Web Service 快速开始](https://render.com/docs/web-services)
