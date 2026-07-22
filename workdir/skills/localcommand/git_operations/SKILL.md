---
name: git_operations
description: 当用户要求"git fetch / pull / merge / 同步代码 / 更新代码 / 推送 / 看提交 / 看 diff / 处理冲突"时，加载本 skill 按推荐流程执行。
context: inline
---

# Git 操作标准流程

按用户意图选择对应子流程，**不要每次都从 git status 开始**——如果用户明确说"fetch 并 merge origin/main"，直接执行。

## 流程 A：仅查看状态

```bash
git status
echo "---"
git log --oneline -10
echo "---"
git branch -vv | head -20
```

## 流程 B：fetch + merge（用户主动指定 merge）

```bash
# 先 fetch（如果之前发现网络/DNS 问题，按 network_diagnosis skill 诊断）
git fetch origin

# 看一眼有什么新提交
git log --oneline HEAD..origin/$(git symbolic-ref --short HEAD)

# merge
git merge origin/$(git symbolic-ref --short HEAD) --no-edit
```

合并冲突：
- 报 `[CONFLICT]` → 列出冲突文件，**不要**自动 `-X theirs` 或 `-X ours`
- 让用户决定怎么解

## 流程 C：pull --rebase（用户在 master 上更喜欢干净历史）

```bash
git pull --rebase origin $(git symbolic-ref --short HEAD)
```

## 流程 D：查看 diff

```bash
# 工作区 vs HEAD
git diff --stat
git diff

# vs 上次提交
git diff HEAD~1
```

## 流程 E：提交

```bash
git status  # 确认要提交的内容
git diff --staged --stat
git commit -m "<message>"
```

**绝不能**自动 `git add -A` 或 `git add .`（可能误加敏感文件 .env / 凭据）。只 add 用户明确提到的文件。

## 流程 F：推送

```bash
git push origin $(git symbolic-ref --short HEAD)
```

推送前**必须**确认：
- 当前分支是用户预期的分支（避免推到错误分支）
- remote 有写权限
- 没有 `--force` 标记（除非用户明确说"强推"）

## 注意事项

- **不要**在 main / master 上直接 rebase（重写公共历史）
- **不要**自动 stash / unstash（用户可能正在编辑）
- **不要**自动修改 commit message（除非用户明确说"重写 commit"）
- 网络/DNS 报错 → 加载 network_diagnosis skill 诊断