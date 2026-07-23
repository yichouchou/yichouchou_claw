---
name: claude-code-scripts
description: claude-code skill 的脚本资源说明。展示如何在 eino 框架下通过 local_command 工具调用主机上的 claude CLI(Anthropic Claude Code 主入口)。覆盖 -p / --add-dir / --output-format / --model 等真实 CLI 选项。适用场景:编码助手、代码评审、自动化重构、架构 review、AI 开发工作流编排。
context: inline
---

# Claude Code Skill(eino 适配版)脚本说明

本 skill 把 Anthropic Claude Code CLI 的能力集成进 eino 框架:非交互式编码任务(`-p`)、工作目录注入(`--add-dir`)、输出格式控制(`--output-format`)、模型覆盖(`--model`)等。

> **职责说明**:本文件是"脚本资源 + 工作流引导"。所有底层命令执行依赖 eino 内置的 `local_command` 工具;本 skill 不在 eino 内部实现 Claude Code 子 agent。

## 适用场景

### 📚 代码评审与理解

- PR diff 评审(给路径或文件给 `--add-dir`)
- 全仓架构 review(指定 `--add-dir .`)
- 解释陌生代码库(`claude -p "explain this repo"`)

### 🤖 自动化编码

- 实现新功能(`claude -p "实现 X" --add-dir ./src --allowedTools Read,Edit`)
- 重构(`claude -p "refactor X to Y" --model claude-sonnet-4-5`)
- 测试编写(`claude -p "为 X 写单元测试"`)

### 🛠️ 排障与调试

- 复杂 Bug 修复工作流
- 性能瓶颈定位
- 配置文件语义分析

## 真实 CLI 接口(Anthropic Claude Code)

```bash
# 非交互模式 —— sandbox 里 LLM 最常用
claude -p "explain the auth flow"
claude -p "find all SQL injection risks" --add-dir ./src

# 输出格式
claude -p "..." --output-format text          # 默认
claude -p "..." --output-format json         # 结构化
claude -p "..." --output-format stream-json  # 流式

# 模型覆盖
claude -p "..." --model claude-sonnet-4-5
claude -p "..." --model claude-opus-4-1

# 工具白/黑名单
claude --allowedTools "Bash,Read,Edit" -p "..."
claude --disallowedTools "WebSearch,WebFetch" -p "..."

# 会话继续
claude -c -p "再列出 3 个待办"
```

**详细选项**:见主 SKILL.md 的"选项速查"表。

## ⚠️ 命令名澄清

| 名称 | 状态 | 备注 |
|---|---|---|
| `claude` | ✅ 真实存在 | Anthropic Claude Code CLI 主入口 |
| `claude-code` | ❌ 不存在 | OpenClaw 旧命令名,调了会 "command not found" |
| `claude-code query/task/docs/info` | ❌ 不存在 | OpenClaw 旧 CLI 假语法,LLM 不要调 |

## 与 eino 框架的集成

- **命令执行**:通过 eino 的 `local_command` 工具在受限沙箱中执行 `claude` CLI,保留硬禁止 / 软禁止授权机制
- **白名单**:`workdir/config/exec-approvals.json` 中 `claude` 已在 allowlist
- **env 净化**:localcommand 沙箱默认剥离 `ARK_*` / `OPENAI_*` / `VOLCENGINE_*` / `COZE_*` / `MINIMAX_*`,只透传 `ANTHROPIC_*` / `CLAUDE_*`,确保 `claude` 走真实 Anthropic API
- **路径隔离**:SensitivePaths 与 denylist path `/mnt` 双重拦截,WSL 内禁止访问 Windows 主机
- **会话管理**:Claude Code 自己的会话机制(`claude -c`),与 eino session.Store 互不干涉

## 工作流示例

### 复杂 Bug 修复

```bash
# 1. 先让 Claude 列出可能根因
claude -p "分析 userService.js 中 NPE 的可能根因,输出 3 个最可能的" \
  --add-dir ./src --output-format text

# 2. 再让 Claude 写修复 patch
claude -p "基于上述根因,输出修复后的 userService.js 完整代码" \
  --add-dir ./src --allowedTools "Read,Edit"
```

### 新功能开发

```bash
# 1. 设计评审
claude -p "评审我打算给用户管理加 REST API 的方案,给出 3 个改进点" \
  --add-dir ./docs/api-design.md

# 2. 实现
claude -p "按评审意见实现 user API" \
  --add-dir ./src --output-format text
```

### 自动化代码评审

```bash
claude -p "评审以下 diff,按严重度(高/中/低)列出问题" \
  --add-dir ./pr-1234.diff --output-format json | \
  jq '.[] | select(.severity=="high")'
```

## ⚠️ scripts/ 目录说明

| 文件 | 状态 | 用途 |
|---|---|---|
| `install.sh` | ✅ 有用 | Linux 安装脚本,把 skill 拷到另一个 eino 项目的 skills 目录(可选) |
| `README.md` | ✅ 当前文件 | 本文档,解释脚本与用法 |
| `claude-code.py` | ❌ 不被本 skill 调用 | OpenClaw 旧封装,保留仅为历史兼容;**LLM 不要调它** |

## 配置

### 环境变量

- `ANTHROPIC_API_KEY` —— 必填其一,或在首次使用时 OAuth 登录
- `CLAUDE_*` —— 透传到沙箱子进程
- `ARK_*` / `OPENAI_*` / `VOLCENGINE_*` / `COZE_*` / `MINIMAX_*` —— 会被沙箱剥离,不会污染 `claude` CLI 的 base_url

### 模型

- 默认使用 Claude Code CLI 默认模型
- 通过 `--model` 在每次调用覆盖(如 `--model claude-sonnet-4-5`)

## 与原 Claude Code CLI 共存

```bash
claude -p "implement X"     # Claude Code CLI(Anthropic 官方)
```

无 OpenClaw "claude-code" 二进制共存的问题 —— `claude-code` 命令在本机不存在。

## 注意事项

- 本 skill 是 Claude Code CLI 的薄封装层,所有 LLM 调用由 Claude Code CLI 自行处理
- 任务执行走主机 Claude Code,**不**经过 eino 的 sub-agent 系统
- Claude Code CLI 的完整能力需要在主机上单独安装(见 <https://claude.com/code>)
- 所有命令执行受 localcommand 的五段链路约束(硬禁止 / 软禁止 / 白名单 / 敏感路径 / denylist path)
- `scripts/claude-code.py` 是历史遗留,不要调用

## 参考资料

- Claude Code 官方文档:<https://code.claude.com/docs>
- Claude Code 安装:<https://claude.com/code>
- eino 框架:<https://github.com/cloudwego/eino>