---
name: claude-code
description: 当用户要求"调 Claude Code 查文档"、"用 Claude Code 跑编码任务"、"查询 Claude Code 最佳实践 / subagent / MCP 配置"、"用 claude-code 子 agent 写代码"等场景时,加载本 skill。LocalCommandAgent 会通过 local_command 工具调用主机上已安装的 claude-code CLI(query / task / docs / info 四个子命令)完成文档查询、编码任务分发与工作流编排。本 skill 不在 eino 内部实现 claude-code,而是把宿主机的 claude-code 程序作为外部命令通过白名单+沙箱调用。
context: inline
---

# Claude Code 集成（Eino + LocalCommandAgent 适配版）

本 skill 的目的是让 LocalCommandAgent 通过 `local_command` 工具调用**主机上已安装**的 `claude-code` CLI，完成 Claude Code 官方文档查询、subagent 编码任务分发与开发工作流编排。

## ⚠️ 前置条件（按顺序确认）

1. **主机已安装 Claude Code CLI**：见 <https://claude.com/code>
2. **`claude-code` 命令在 localcommand 白名单内**：见 `internal/localcommand/allowed_unix.go` / `allowed_windows.go`（已放开）
3. **用户已登录 Claude 账号**：`claude` CLI 首次运行需要登录
4. **沙箱工作目录可写**：默认 `/tmp`（Linux/macOS）或当前 cwd（Windows）

## 通过 local_command 调用主机 CLI

LocalCommandAgent 提供 `local_command` 工具，LLM 直接调 `claude-code` 即可。**绝对不要**在 eino 内部重新实现 `claude-code` 的子命令。

### 四个核心子命令

```bash
# 1) query —— 查询官方文档某主题
claude-code query "subagents"
claude-code query "agent-teams"
claude-code query "best practices"
claude-code query "common workflows"
claude-code query "settings"
claude-code query "troubleshooting"
claude-code query "mcp"
claude-code query "plugins"

# 2) task —— 创建编码子任务
claude-code task --description "实现用户认证模块" --priority high
claude-code task --description "重构数据库查询层" --priority medium
claude-code task --description "编写 API 单元测试" --model claude-3-5-sonnet

# 3) docs —— 查看文档章节概览
claude-code docs
claude-code docs quickstart
claude-code docs best-practices
claude-code docs common-workflows
claude-code docs settings
claude-code docs troubleshooting

# 4) info —— 查看 Claude Code 配置与状态
claude-code info
```

### 选项说明

| 选项 | 简写 | 必填 | 说明 |
|---|---|---|---|
| `--description` | `-d` | ✅（task 命令） | 任务描述 |
| `--priority` | `-p` | ❌ | low / medium / high，默认 medium |
| `--model` | `-m` | ❌ | 指定模型名（如 `claude-3-5-sonnet`） |

## ⚠️ 沙箱与白名单约束（LLM 必须遵守）

1. **命令白名单**：本 skill 依赖 `claude-code` 已在白名单内；若运行报"命令不在白名单中"，引导用户授权（`whitelist auth for claude-code`）。
2. **凭据安全**：禁止在参数中夹带 Authorization / Cookie / API Key 等敏感信息。
3. **路径处理**：调用 `claude-code` 前如需传文件路径，统一用绝对路径。
4. **不要走 Python 脚本**：`scripts/claude-code.py` 是 OpenClaw 旧封装，本 skill 下**不要调用**它。直接走 CLI。
5. **eino 内部概念隔离**：本 skill 只关心"调主机 claude-code CLI"，不依赖也不应使用 eino 的 `transfer_to_agent` / `AgentAsTool` 与 Claude Code 的 subagent 互通——两套体系各自独立。

## 与 eino 框架的关系澄清

| 概念 | eino 体系 | Claude Code 体系（主机 CLI） |
|---|---|---|
| 派生子任务 | `adk.SetSubAgents` / `transfer_to_agent` | `claude-code task --description "..."`（CLI 自己管理 subagent） |
| 文档查询 | 本 skill 的内容 | `claude-code query <topic>` |
| 会话管理 | `session.Store` / `adk.WithSessionValues` | Claude Code 自己的会话 |
| 工具调用 | `local_command` / filesystem / skill 等中间件 | Claude Code 的 MCP / 内置工具 |

**关键**：`claude-code task` 内部的 subagent 调度是 Claude Code CLI **自己**的事，eino 这边只是 host 进程。LLM 不要尝试用 eino 的 subagent 机制去"干预" Claude Code CLI 内部的 subagent。

## 工作流示例

### 复杂 Bug 修复

```bash
# 1. 查询排障最佳实践
claude-code query "debugging best practices"

# 2. 创建高优先级子任务定位问题
claude-code task --description "定位并修复 userService.js 中的空指针异常" --priority high

# 3. 查询 review 最佳实践
claude-code query "code review best practices"
```

### 新功能开发

```bash
# 1. 查询 API 设计最佳实践
claude-code query "API design best practices"

# 2. 创建开发任务
claude-code task --description "实现用户管理 REST API" --priority medium

# 3. 查询代码风格配置
claude-code query "code style settings"
```

### 自动化代码评审

```bash
claude-code query "PR review workflows"
claude-code task --description "评审过去一周的所有 PR" --priority low
```

## 反模式

- ❌ 期望 `claude-code` 是 eino 内置命令——它是主机外部程序，走 local_command
- ❌ 调 `scripts/claude-code.py` 脚本——OpenClaw 旧封装，本 skill 不需要
- ❌ 用 eino 的 `transfer_to_agent` 调度 Claude Code 的 subagent——两套体系不互通
- ❌ 在参数里塞凭据 / 走 `--data` / `-H Authorization`——沙箱会拦截
- ❌ 不读文档就直接编任务描述——先 `query` 再 `task` 是推荐顺序
- ❌ 期望 `claude-code` 在 Windows 自动安装——本 skill 不负责安装，仅调用

## 配置

- **环境变量**：基础使用无需配置；Claude Code CLI 自己的环境变量（`ANTHROPIC_API_KEY` 等）由 Claude Code 自行读取
- **模型**：使用 Claude Code CLI 默认模型；可通过 `--model` 在 task 命令覆盖
- **subagent 数量**：由 Claude Code CLI 自身管理（与 eino 无关）

## 注意事项

- 本 skill 是 Claude Code CLI 的 **薄封装层**，所有 LLM 调用由 Claude Code CLI 自行处理
- 任务执行走主机 Claude Code 沙箱，**不**经过 eino 的 sub-agent 系统
- Claude Code CLI 的完整能力需在主机上单独安装（见 <https://claude.com/code>）
- 所有命令执行受 localcommand 的硬禁止 / 软禁止 / 白名单机制约束

## 参考

- Claude Code 官方文档：<https://code.claude.com/docs>
- eino skill middleware：仅扫描 `<baseDir>/<一级子目录>/SKILL.md`
- 安装脚本：`scripts/install.sh`（Linux）
- 辅助脚本：`scripts/claude-code.py`（OpenClaw 旧封装，**本 skill 不使用**）