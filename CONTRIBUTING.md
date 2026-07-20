# 贡献指南

感谢你考虑为本项目做出贡献！🎉

## 📋 目录

- [行为准则](#行为准则)
- [我能做什么贡献？](#我能做什么贡献)
- [开发流程](#开发流程)
- [代码规范](#代码规范)
- [提交规范](#提交规范)
- [Pull Request 流程](#pull-request-流程)

## 行为准则

本项目遵循 [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md)。
参与本项目即表示你同意遵守其条款。

## 我能做什么贡献？

- 🐛 报告 Bug
- 💡 提出新功能建议
- 📝 改进文档
- 🔧 提交代码修复或新功能
- ✅ 评审他人的 PR
- 🌐 翻译文档

## 开发流程

### 1. Fork 仓库

点击 GitHub 页面右上角的 Fork 按钮。

### 2. 克隆到本地

```bash
git clone https://github.com/starcat-app/starcat-trending-api.git
cd starcat-trending-api
```

### 3. 创建分支

```bash
git checkout -b feat/your-feature-name
# 或
git checkout -b fix/issue-number-description
```

### 4. 安装依赖

```bash
go mod download
```

### 5. 开发与测试

```bash
# 编译
go build -o bin/server ./cmd/server/

# 运行
./bin/server

# 运行测试
go test ./...

# 代码检查
go vet ./...
gofmt -s -l .
```

### 6. 提交并推送

```bash
git add .
git commit -m "feat: add new feature"
git push origin feat/your-feature-name
```

### 7. 创建 Pull Request

在 GitHub 上创建 PR，并填写 PR 模板。

## 代码规范

### Go 风格

- 遵循 [Effective Go](https://go.dev/doc/effective_go)
- 使用 `gofmt` 格式化（`gofmt -s -w .`）
- 导出符号必须有注释
- 复杂逻辑必须有"为什么这样做"的注释

### 文件组织

```
cmd/server/        # 程序入口
internal/          # 内部包（不对外暴露）
  models/          # 数据模型
  spider/          # 爬虫逻辑
pkg/               # 可对外暴露的包
  utils/           # 工具函数
```

### 错误处理

```go
// ✅ 正确：包装错误并提供上下文
if err != nil {
    return fmt.Errorf("fetch repo list: %w", err)
}

// ❌ 错误：吞掉错误
if err != nil {
    return nil
}
```

### 命名

- 包名：小写、简短、有意义（如 `spider` 而非 `trending_spider`）
- 导出函数/类型：PascalCase
- 私有函数/变量：camelCase
- 缩写词保持一致大小写（`URL` 而非 `Url`）

## 提交规范

本项目使用 [Conventional Commits](https://www.conventionalcommits.org/) 规范。

### 格式

```
<type>(<scope>): <subject>

<body>

<footer>
```

### Type 类型

| Type     | 用途           |
|----------|----------------|
| feat     | 新功能         |
| fix      | Bug 修复       |
| docs     | 文档变更       |
| style    | 代码格式（不影响功能） |
| refactor | 重构           |
| perf     | 性能优化       |
| test     | 测试相关       |
| chore    | 构建/CI/工具   |

### 示例

```bash
feat(repo): add language filter to /repo endpoint
fix(spider): handle empty response from GitHub
docs(readme): update deployment instructions
```

## Pull Request 流程

1. **确保 CI 通过**：GitHub Actions 会自动运行 `go vet` / `gofmt` / 编译 / 测试
2. **更新文档**：如修改了 API，请同步更新 README
3. **关联 Issue**：使用 `Closes #123` 或 `Fixes #123` 关联相关 Issue
4. **描述清晰**：填写 PR 模板，说明改动的动机和影响
5. **响应评审**：及时回复评审意见，必要时推送新的提交
6. **Squash 合并**：合并时使用 squash 合并，保持主分支历史干净

## 第一次贡献？

可以从 [good first issue](https://github.com/starcat-app/starcat-trending-api/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22) 标签的 Issue 入手。

## 联系方式

- 提交 [Issue](https://github.com/starcat-app/starcat-trending-api/issues)
- 加入 [Discussions](https://github.com/starcat-app/starcat-trending-api/discussions)
- 邮件: [dong4j@gmail.com](mailto:dong4j@gmail.com)

感谢你的贡献！💖
