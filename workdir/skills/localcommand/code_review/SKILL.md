---
name: code_review
description: 当用户要求"review 项目代码"、"分析项目架构"、"review 我这个项目"、"分析架构"、"review 整个项目"且任务需要在主机上读代码（不是已经贴在对话里的代码片段）时，加载本 skill 按系统化模板审查项目架构。LocalCommandAgent 会在一次跑通"读代码 → review → 提交 issue"流程时使用本 skill。
context: inline
---

# 项目架构 review 系统化模板（LocalCommandAgent 专用）

这是 ChatAgent 的 code_review skill 的 LocalCommandAgent 版本。区别：
- **本 skill 的目的是让 LocalCommandAgent 能"读代码 → review → 提交 issue"全自动跑完**
- LocalCommandAgent 有 local_command 工具，能直接读项目代码
- review 过程就是 LLM 按本模板输出文字，**不是用其他工具**

## ⚠️ 重要执行约束

1. **单次 ChatModel 输出工具调用不超过 5 个**——eino 默认 MaxIterations=20；
   一次塞 20 个 cat 命令会立刻触顶报错 "exceeds max iterations"。
2. **按"看结构 → 看重点 → 综合 review" 2-3 轮跑**，不要一次性 cat 全部文件。
3. **review 报告用文字输出给用户**——这是 LLM 的输出，不是工具调用。

## 第一步：了解项目结构（1 轮，2-3 个命令）

```bash
cd <项目目录> && ls -la
echo "---"
cd <项目目录> && find . -type f -name "*.go" -not -path "./.git/*" -not -path "./vendor/*" | head -30
echo "---"
cd <项目目录> && cat go.mod
```

如果有多个语言/包管理器，按需调整。

## 第二步：看核心文件（1 轮，3-5 个命令）

按项目结构选最关键的 3-5 个文件，**最多 5 个 cat**：
- 入口文件（main.go / app.go / server.go / index.js）
- 路由 / 控制器层（router / handler / controller）
- 业务核心层（service / usecase / core）
- 数据访问层（repository / dao / model）
- 配置 / 工具（config / util）

每文件单独 cat，方便 review。

## 第三步：综合 review（1 轮，0 个命令）

按以下 5 个维度输出 review 报告，**给用户看，不用工具调用**：

### 1. 🔴 正确性（Correctness）
- 并发安全、goroutine 泄漏、defer 释放、错误处理、panic 风险

### 2. 🟡 安全性（Security）
- 凭据泄漏、路径穿越、命令注入、权限提升、信任边界校验

### 3. 🟡 性能（Performance）
- 时间/空间复杂度、I/O 热点、锁粒度、goroutine 数量

### 4. 🟢 可读性（Readability）
- 命名、函数长度、注释、错误信息

### 5. 🟢 可维护性（Maintainability）
- 重复代码、魔法数字、抽象层次、测试覆盖

每条问题必须包含：
- **位置**：文件:行号 或 函数名
- **严重度**：🔴 / 🟡 / 🟢
- **问题**：1-2 句话描述
- **建议**：具体修复方案（代码片段更好）

最后给总评：✅ / ⚠️ / ❌

## 第四步（可选）：提交 issue

如果用户说"review + 提交 issue"，review 完成后：
1. 把 review 报告整理成 issue body（markdown）
2. 跑 `gh issue create --title "..." --body-file -`（注意 `--body-file -` 从 stdin 读）
3. 如果 `gh` 沙箱跑不通（DNS / 网络），告诉用户"沙箱无法访问 github，请在本机提交"
4. 把 draft issue body 给用户，方便用户复制粘贴

## 反模式

- ❌ 一次 ChatModel 输出塞 20 个 cat 命令
- ❌ 不读代码就输出泛泛而谈的 review（"模块化做得不错"）
- ❌ 编造文件:行号
- ❌ review 后忘记按 SKILL.md 模板输出（用户已经按"位置/严重度/问题/建议"格式期待）