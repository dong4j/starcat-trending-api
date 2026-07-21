# ===========================================
# Stage 1: 构建阶段
# ===========================================
# 与 starcat-sharing-api / starcat-weekly-api 保持一致的多阶段构建
# Go 版本由项目 go.mod 决定 (1.25.0)
FROM golang:1.25-alpine AS builder

ARG VERSION=0.0.0-dev

# 设置工作目录
WORKDIR /app

# 先复制依赖文件, 利用 Docker 缓存
# 当前项目仅一个外部依赖 (goquery), go.sum 仍保留以加速缓存命中
COPY go.mod go.sum* ./
RUN go mod download

# 复制源码并编译
COPY . .
# CGO_ENABLED=0 编译为静态二进制, 适用于 alpine / scratch
# VERSION 由发布 tag 注入；默认值只用于本地开发构建。
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-w -s -X github.com/starcat-app/starcat-trending-api/internal/version.Version=${VERSION}" \
    -o /app/bin/server \
    ./cmd/server/

# ===========================================
# Stage 2: 运行阶段
# ===========================================
# 使用 alpine 基础镜像保持体积小巧
# 与 sharing / weekly 保持一致: alpine 3.21
FROM alpine:3.21

# 安装 CA 证书 (HTTPS 请求 GitHub trending 页面需要)
RUN apk --no-cache add ca-certificates tzdata

# 设置时区
ENV TZ=UTC

# 创建非 root 用户运行服务 (安全最佳实践)
RUN addgroup -S app && adduser -S app -G app

# 工作目录固定在 /app
# - /app/server   : 编译产物
# 本服务无持久化数据 (实时抓取 GitHub trending), 无需挂载卷
WORKDIR /app

# 从 builder 阶段复制编译产物
COPY --from=builder /app/bin/server /app/server

# 切换到非 root 用户
USER app

# 暴露端口 (与 main.go 默认 5002 一致)
EXPOSE 5002

# 健康检查 (与 main.go /healthz 端点对齐: 30s/3s/5s grace)
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --quiet --tries=1 --spider http://localhost:5002/healthz || exit 1

# 启动服务
ENTRYPOINT ["/app/server"]
