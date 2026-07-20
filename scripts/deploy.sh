#!/usr/bin/env bash
# =============================================================================
# scripts/deploy.sh — starcat-xxx-api 发版脚本
# =============================================================================
#
# 用法:
#   ./scripts/deploy.sh v1.1.0           # 真实部署
#   ./scripts/deploy.sh --dry-run v1.1.0 # 只 echo, 不实际执行 (用于验证)
#   DRY_RUN=1 ./scripts/deploy.sh v1.1.0 # 同上, 环境变量形式
#
# 前置依赖:
#   - git (>= 2.x)
#   - gh (GitHub CLI, 已认证: `gh auth status`)
#   - 当前所在分支不能是 main / master
#
# 完整流程 (按顺序):
#   1. 校验参数 (semver: vX.Y.Z)
#   2. 校验: 在 git 仓库, 当前分支不是 main / master
#   3. 校验: 工作区干净 (无 uncommitted / untracked)
#   4. 校验: 当前分支没有未推送的 commit
#   5. 校验: 目标 tag 在 local + origin 都不存在
#   6. 校验: 目标 tag 不低于最新已有 tag (semver compare)
#   7. 校验: gh CLI 已认证
#   8. 推送当前分支到 origin
#   9. 创建 PR (dev → main) 用 PULL_REQUEST_TEMPLATE
#  10. 合并 PR (--merge, 保留 dev 历史, 不删 dev 分支)
#  11. checkout main, pull
#  12. 打 annotated tag v1.1.0 (指向 merge commit)
#  13. 推送 tag → 触发 .github/workflows/{go,fly-deploy,release}.yml
#     (go.yml 必跑在前, 成功后 fly-deploy + release 并行跑)
#
# 关键约束 (踩过的坑):
#   - tag 必须在 PR merge 之后打, 确保 tag 指向 main 的 merge commit,
#     而不是 dev 的 tip (否则 tag 跟 main HEAD 指向不同 commit,
#     fly-deploy 会部署到错的代码)
#   - 不能用 --squash merge, 否则会丢失 dev 上的多个 commit 信息
#   - PR merge --delete-branch 删远端 dev, step 11.5 立即从 main 重建
#     (下次发版: git checkout dev → 改 → push → PR, 循环)
#     若 step 11.5 失败: dev 不存在, 下次发版前手动 git branch dev main
#   - main / master 上禁止运行此脚本 (会 PR 自己到自己)
#   - 推 tag 后必须等 go.yml 跑完才会触发 fly-deploy/release
#     (workflow_run 监听, 失败时不会部署, 必须先修代码重打 tag)
#
# 失败处理: set -e + 任意一步 exit 1 都会停止, 不会留下半成品状态
# (已创建的 PR 不会自动关, 需要手动去 GitHub 处理或 gh pr close)
# =============================================================================

set -euo pipefail

# =============================================================================
# --dry-run 参数解析 (必须在 VERSION 之前处理)
# =============================================================================
DRY_RUN="${DRY_RUN:-}"
if [[ "${1:-}" == "--dry-run" ]]; then
    DRY_RUN=1
    shift
fi

# =============================================================================
# 颜色 (只在 TTY 输出, pipe 时关掉避免污染日志)
# 必须先初始化, 否则 dry-run banner 引用未定义变量会报错
# =============================================================================
if [[ -t 1 ]]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[1;33m'
    BLUE='\033[0;34m'
    NC='\033[0m'
else
    RED=''; GREEN=''; YELLOW=''; BLUE=''; NC=''
fi

# dry-run banner (在颜色初始化之后)
if [[ -n "$DRY_RUN" ]]; then
    # 把 DRY_RUN 强制设为 1, 避免奇怪的 truthy 值
    DRY_RUN=1
    echo -e "${YELLOW}=========================================${NC}"
    echo -e "${YELLOW}       D R Y   R U N   M O D E${NC}"
    echo -e "${YELLOW}=========================================${NC}"
    echo -e "${YELLOW}以下 7 个 side-effect 命令将只 echo, 不会实际执行:${NC}"
    echo -e "${YELLOW}  git push / gh pr create / gh pr merge${NC}"
    echo -e "${YELLOW}  git checkout / git pull / git tag / git push tag${NC}"
    echo -e "${YELLOW}前面 1-7 步 (校验类, 只读) 仍会实际执行${NC}"
    echo -e "${YELLOW}=========================================${NC}"
    echo ""
fi

# 错误退出: 红字 + exit 1
die() {
    echo -e "${RED}✗ Error:${NC} $*" >&2
    exit 1
}

# 成功步骤: 绿勾
ok() {
    echo -e "${GREEN}✓${NC} $*"
}

# 提示信息: 蓝色
info() {
    echo -e "${BLUE}▶${NC} $*"
}

# 警告: 黄色 (不退出)
warn() {
    echo -e "${YELLOW}!${NC} $*" >&2
}

# =============================================================================
# run / run_capture — 干跑模式包装
# =============================================================================
# run CMD...: 执行命令; --dry-run 时只 echo 到 stderr, 不执行
# run_capture CMD...: 同 run, 但 dry-run 时输出一个假的 PR URL 到 stdout
#                     (给 gh pr create 用, 让脚本能继续跑到结尾)
# 注意: echo 走 stderr, 避免污染 stdout (例如 gh pr create 的 URL 捕获)
# =============================================================================
run() {
    if [[ -n "$DRY_RUN" ]]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} $*" >&2
    else
        "$@"
    fi
}

run_capture() {
    if [[ -n "$DRY_RUN" ]]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} $*" >&2
        # 假的 PR URL, PR 号 0 (明显是 fake), 让下游 PR_NUM=$(...|grep ...) 能跑通
        # PROJECT_NAME 在 step 2 之后才定义, 但 run_capture 只在 step 9 才被调用, 顺序安全
        echo "https://github.com/starcat-app/${PROJECT_NAME}/pull/0"
    else
        "$@"
    fi
}

# =============================================================================
# 1. 参数解析
# =============================================================================
VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
    echo "Usage: $0 vX.Y.Z" >&2
    echo "  Example: $0 v1.1.0" >&2
    exit 1
fi

# semver 格式: vMAJOR.MINOR.PATCH (3 段数字, 不允许 v1.0 / v1 等简写)
if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    die "version must match vX.Y.Z (semver), got: '$VERSION' (e.g. v1.1.0)"
fi
ok "version: $VERSION"

# =============================================================================
# 2. 仓库 + 分支检查
# =============================================================================
git rev-parse --is-inside-work-tree >/dev/null 2>&1 \
    || die "not in a git repository"

CURRENT_BRANCH=$(git symbolic-ref --short HEAD 2>/dev/null \
    || git rev-parse --short HEAD)

# 硬拦: main / master 上不能跑 (会 PR 自己到自己, 无意义且危险)
if [[ "$CURRENT_BRANCH" == "main" || "$CURRENT_BRANCH" == "master" ]]; then
    die "deploy.sh cannot run on '$CURRENT_BRANCH' — switch to dev or a feature branch first"
fi
ok "branch: $CURRENT_BRANCH (not main/master)"

# 当前项目名 (从 git 仓库根目录的目录名推断)
# 3 个 starcat-*-api 项目共用同一份 deploy.sh, 用 PROJECT_NAME 拼 URL
# 必须放在 git 仓库校验之后, 否则 git rev-parse 会失败触发 set -e 退出
PROJECT_NAME=$(basename "$(git rev-parse --show-toplevel)")
ok "project: $PROJECT_NAME"

# =============================================================================
# 3. 工作区干净
# =============================================================================
if ! git diff --quiet HEAD 2>/dev/null; then
    die "working tree has unstaged/staged changes, commit or stash first:
$(git status --short)"
fi

if [[ -n "$(git ls-files --others --exclude-standard)" ]]; then
    die "working tree has untracked files, commit or remove first:
$(git ls-files --others --exclude-standard)"
fi
ok "working tree clean"

# =============================================================================
# 4. 当前分支没有未推送的 commit
# =============================================================================
# 防止本地有 commit 没推, deploy 后 origin 还少这些 commit
if git rev-parse --abbrev-ref "@{u}" >/dev/null 2>&1; then
    UNPUSHED=$(git log --oneline "@{u}..HEAD" 2>/dev/null || true)
    if [[ -n "$UNPUSHED" ]]; then
        die "current branch has unpushed commits, push first:
$UNPUSHED"
    fi
    ok "no unpushed commits on $CURRENT_BRANCH"
else
    warn "current branch has no upstream tracking — assuming local is the source of truth"
fi

# =============================================================================
# 5. Tag 不存在 (local + origin 都要检查)
# =============================================================================
if git rev-parse "refs/tags/$VERSION" >/dev/null 2>&1; then
    die "tag '$VERSION' already exists locally (use a higher version)"
fi

# 检查 origin 是否有这个 tag (处理 local 删除但 origin 仍有的情况)
if git ls-remote --tags origin 2>/dev/null | grep -q "refs/tags/${VERSION}$"; then
    die "tag '$VERSION' already exists on origin (use a higher version)"
fi
ok "tag $VERSION does not exist (local + origin)"

# =============================================================================
# 6. Tag 不低于最新 tag (semver compare)
# =============================================================================
LATEST_TAG=$(git tag --list 'v*' --sort=-v:refname | head -1 || true)
if [[ -n "$LATEST_TAG" ]]; then
    # 用 sort -V (version sort) 取出两个中较大的那个
    HIGHEST=$(printf "%s\n%s\n" "$LATEST_TAG" "$VERSION" | sort -V | tail -1)
    if [[ "$HIGHEST" != "$VERSION" ]]; then
        die "$VERSION is lower than or equal to existing tag $LATEST_TAG"
    fi
    ok "version $VERSION > $LATEST_TAG (semver ok)"
else
    ok "no existing tags — $VERSION will be the first"
fi

# =============================================================================
# 7. gh CLI 已认证
# =============================================================================
if ! gh auth status >/dev/null 2>&1; then
    die "gh CLI not authenticated, run: gh auth login"
fi
ok "gh CLI authenticated"

# =============================================================================
# 8. 推送当前分支 (确保 origin 是最新)
# =============================================================================
info "pushing $CURRENT_BRANCH to origin..."
run git push origin "$CURRENT_BRANCH"
ok "pushed $CURRENT_BRANCH"

# =============================================================================
# 9. 创建 PR (dev → main)
# =============================================================================
info "creating PR $CURRENT_BRANCH → main..."

# 用 heredoc 写 PR body (按 .github/PULL_REQUEST_TEMPLATE.md 规范填)
PR_BODY=$(cat <<EOF
## 变更说明

将 \`$CURRENT_BRANCH\` 合并到 \`main\`, 发布版本 **$VERSION**。

## 关联 Issue

<!-- 如有关联 Issue, 请使用 \`Closes #123\` 或 \`Fixes #123\` -->

- Fixes #

## 变更类型

请勾选适用的选项:

- [x] 新功能 (非破坏性,新增功能)
- [ ] Bug 修复
- [ ] 文档更新
- [x] 重构 / 性能优化
- [ ] 测试相关

## 变更内容

- 发版 $VERSION
- 详见 CHANGELOG.md [$VERSION] 段

## 测试

- \`go build ./...\` 通过
- \`go vet ./...\` 通过
- \`gofmt -s -l .\` 无输出
- \`go test ./...\` 通过
EOF
)

# gh pr create 失败会触发 set -e 退出
PR_URL=$(run_capture gh pr create \
    --base main \
    --head "$CURRENT_BRANCH" \
    --title "chore(release): $VERSION 发布" \
    --body "$PR_BODY")

PR_NUM=$(echo "$PR_URL" | grep -oE '/pull/[0-9]+$' | grep -oE '[0-9]+')
ok "PR created: $PR_URL (PR #$PR_NUM)"

# =============================================================================
# 10. 合并 PR (--merge 保留 dev 历史, --delete-branch 删远端 dev)
# =============================================================================
info "merging PR #$PR_NUM..."
run gh pr merge "$PR_NUM" --merge --delete-branch
ok "PR #$PR_NUM merged (remote dev deleted)"

# =============================================================================
# 11. 切 main, pull
# =============================================================================
info "switching to main and pulling..."
run git checkout main
run git pull origin main --ff-only
ok "on main, up-to-date with origin"

# =============================================================================
# 11.5 重建 dev 分支 (PR merge --delete-branch 后, 从 main 重建推 origin)
# =============================================================================
# 远端 dev 已被 step 10 删, 重建 dev 指向 main HEAD 保证下次发版有 dev 可用
# 本地 dev ref 仍存在 (git fetch --prune 才会清), -D 强删 (step 4 已校验无未推送)
# 若 step 11.5 整体失败: dev 不存在, 下次发版前手动 git branch dev main 恢复
if git show-ref --verify --quiet refs/heads/dev; then
    info "removing stale local dev ref..."
    run git branch -D dev
fi
info "rebuilding dev branch from main..."
run git branch dev main
run git push origin dev
ok "dev branch rebuilt on origin (https://github.com/starcat-app/${PROJECT_NAME}/tree/dev)"

# =============================================================================
# 12. 打 annotated tag (指向 merge commit)
# =============================================================================
info "tagging $VERSION..."
run git tag -a "$VERSION" -m "Release $VERSION

首个使用 scripts/deploy.sh 自动发布的版本。
- 合并自 $CURRENT_BRANCH (PR #$PR_NUM)
- 详见 CHANGELOG.md [$VERSION] 段"
ok "tagged $VERSION"

# =============================================================================
# 13. 推送 tag → 触发 .github/workflows/{go,fly-deploy,release}.yml
# =============================================================================
# 推 tag 后:
#   - go.yml 先跑 (gofmt + vet + build + test 验证)
#   - go.yml 成功 → fly-deploy.yml 部署 + release.yml 发版 (并行)
#   - go.yml 失败 → fly-deploy + release 都不跑 (workflow_run 监听)
info "pushing tag $VERSION to origin (triggers go.yml → fly-deploy + release)..."
run git push origin "$VERSION"
ok "tag $VERSION pushed"

# =============================================================================
# 完成
# =============================================================================
echo ""
echo -e "${GREEN}=========================================${NC}"
echo -e "${GREEN}  $VERSION 部署完成 ✓${NC}"
echo -e "${GREEN}=========================================${NC}"
echo ""
echo "  - PR:      $PR_URL"
echo "  - Tag:     https://github.com/starcat-app/${PROJECT_NAME}/releases/tag/$VERSION"
echo "  - Fly:     https://fly.io/apps/${PROJECT_NAME}/healthz"
echo "  - Actions: https://github.com/starcat-app/${PROJECT_NAME}/actions"
echo ""
echo "  下一步: 等待 go.yml → fly-deploy + release 跑完 (通常 < 3 分钟)"

# =============================================================================
# 干跑模式总结
# =============================================================================
if [[ -n "$DRY_RUN" ]]; then
    echo ""
    echo -e "${YELLOW}=========================================${NC}"
    echo -e "${YELLOW}  D R Y   R U N   C O M P L E T E${NC}"
    echo -e "${YELLOW}=========================================${NC}"
    echo ""
    echo "以上 13 步中, 1-7 步 (校验类, 只读) 实际执行了"
    echo "8-13 步 (side-effect) 都只 echo, 没实际执行:"
    echo "  ✗ git push origin <branch>   没跑"
    echo "  ✗ gh pr create               没跑 (用了假 URL)"
    echo "  ✗ gh pr merge                没跑"
    echo "  ✗ git checkout main          没跑"
    echo "  ✗ git pull origin main       没跑"
    echo "  ✗ git tag -a vX.Y.Z          没跑"
    echo "  ✗ git push origin vX.Y.Z     没跑"
    echo ""
    echo "去掉 --dry-run 重跑即可真实部署"
fi

# =============================================================================
# 14. 切回 dev, 收尾 (step 13 推 tag 之后, 作为 cleanup)
# =============================================================================
# deploy 脚本的目标终态: 让用户直接进入下次发版的开发循环
# 与 step 12/13 的 side-effect 区分: 切 worktree 无副作用, 不走 run
# dry-run 时实际执行 (本地 worktree 切换不算 side-effect), 让 dry-run 用户也在 dev 上
# 若 dev 缺失 (step 11.5 失败): 守卫 + warn, 不退出 (走 set -e 会让 dry-run 总结都打不出来)
if git show-ref --verify --quiet refs/heads/dev; then
    git checkout dev
    ok "switched to dev, ready for next release"
else
    warn "local dev branch missing — run: git branch dev main && git checkout dev"
fi
