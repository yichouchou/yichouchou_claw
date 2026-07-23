---
name: claude-code-scripts
description: claude-code skill 的脚本资源说明。展示如何在 Eino 框架下集成 Claude Code 能力：通过 local_command 工具调用 claude-code CLI；通过 AgentAsTool 机制把 claude-code sub-agent 包装为可被其他 agent 调用的工具。覆盖 query / task / docs / info 四类命令。适用场景：编码助手、文档查阅、AI 开发工作流编排。
context: inline
---

# Claude Code Skill（Eino 适配版）

将 Claude Code 的 AI 辅助开发能力整合进 Eino 框架：官方文档查询、编码任务管理、Claude Code sub-agent 调度、开发最佳实践与常见排障指南。

> **职责说明**：本 skill 是一个"操作手册 + 工作流引导"。所有底层命令执行都依赖 Eino 内置的 `local_command` 工具；所有 sub-agent 调度都基于 Eino 的 `AgentAsTool` 委派机制。

## 适用场景

### 📚 文档查询
- 查询 Claude Code 官方文档（subagents / agent-teams / best-practices / settings / mcp / plugins / troubleshooting）
- 获取编码最佳实践与典型工作流
- 排查常见问题

### 🤖 编码任务管理
- 创建编码 sub-agent 执行复杂任务
- 管理 agent 团队
- 自动化代码评审与 PR 工作流

### 🛠️ 开发工作流
- AI 辅助编码最佳实践
- 通用工作流与模式
- 自定义 settings 与配置
- 故障排查指南

## 调用方式

### 通过 local_command 直接调用 Claude Code CLI

```bash
claude-code query "subagents"
claude-code query "best-practices"
claude-code task --description "Fix the login bug" --priority high
claude-code docs
claude-code info
```

### 通过 AgentAsTool 委派给 Claude Code sub-agent

```go
claudeCodeTool := adk.NewAgentTool(ctx, claudeCodeAgent)

chatAgent, _ := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
    Name:        "ChatAgent",
    Description: "通用对话 agent，可委派 Claude Code sub-agent 处理编码任务",
    ToolsConfig: adk.ToolsConfig{
        ToolsNodeConfig: compose.ToolsNodeConfig{
            Tools: []tool.BaseTool{claudeCodeTool},
        },
    },
})
```

主 agent 通过 `claude_code(task="...")` 形式触发子 agent，子 agent 在隔离 context 中跑 claude-code CLI，把结果汇总回主 agent。

## 命令详解

### query — 文档查询

```bash
claude-code query <topic>
```

**示例**：

```bash
claude-code query "subagents"
claude-code query "agent-teams"
claude-code query "best practices"
claude-code query "common workflows"
claude-code query "settings"
claude-code query "troubleshooting"
claude-code query "mcp"
```

**支持的主题**：subagents / agent-teams / best-practices / common-workflows / settings / troubleshooting / plugins / mcp / Headless / Programmatic 用法

### task — 创建编程任务

```bash
claude-code task --description "<任务描述>" [--priority <level>] [--model <model-name>]
```

**选项**：
- `--description, -d`：任务描述（必填）
- `--priority, -p`：优先级（low / medium / high，默认 medium）
- `--model, -m`：指定模型（可选）

**示例**：

```bash
claude-code task --description "实现用户认证模块"
claude-code task --description "重构数据库查询" --priority high
claude-code task --description "编写 API 单元测试" --model claude-3-5-sonnet
```

### docs — 文档章节概览

```bash
claude-code docs [section]
```

**章节**：

- `quickstart` — 入门指南
- `best-practices` — AI 编码最佳实践
- `common-workflows` — 典型开发工作流
- `settings` — 自定义选项
- `troubleshooting` — 常见问题与解决方案
- `all` — 完整文档概览（默认）

**示例**：

```bash
claude-code docs
claude-code docs quickstart
claude-code docs best-practices
claude-code docs troubleshooting
```

### info — 显示配置状态

```bash
claude-code info
```

**输出**：版本信息 / 可用 sub-agents / 已配置模型 / MCP servers 状态

## 与 Eino 框架的集成

- **Sub-agent 委派**：Claude Code sub-agent 通过 Eino 的 AgentAsTool 机制被其他 agent 调用，主 agent 在隔离 context 中接收结果
- **命令执行**：通过 Eino 的 `local_command` 工具在受限沙箱中执行 claude-code CLI，保留硬禁止 / 软禁止授权机制
- **文件管理**：与 Eino 的 filesystem 中间件组合，实现完整代码库读写
- **会话管理**：Claude Code 任务继承 Eino 的 session / store 机制
- **流式输出**：AgentAsTool 配置 `EmitInternalEvents` 后，Claude Code sub-agent 的内部事件可实时转发给终端用户

## 工作流示例

### 复杂 Bug 修复

```bash
claude-code query "debugging best practices"
claude-code task --description "定位并修复 userService.js 中的空指针异常" --priority high
claude-code query "code review best practices"
```

### 新功能开发

```bash
claude-code query "API design best practices"
claude-code task --description "实现用户管理的 REST API" --priority medium
claude-code query "code style settings"
```

### 自动化代码评审

```bash
claude-code query "PR review workflows"
claude-code task --description "评审过去一周的所有 PR" --priority low
```

## 配置

### 环境变量

基础使用无需配置。Claude Code 集成通过 Eino 的原生能力调用 LLM，复用 Eino 已配置的 ChatModel。

### 模型

使用 Eino 的默认 ChatModel。可通过 Eino 的 ChatModel 配置（`adk/common/model/`）全局切换，或在 `claude-code task` 调用时通过 `--model` 覆盖。

### Sub-agent 数量限制

由 Eino 的 sub-agent 配置管理（`adk.AgentWithOptions(WithMaxIterations(N))`）。当前默认 MaxIterations=50，足以支撑复杂排障场景。

## 与原 Claude Code CLI 共存

如果你在主机上同时安装了 Claude Code CLI：

```bash
claude-code query "API design"   # 本 skill（通过 local_command）
claude code "implement API"      # Claude Code CLI（直接调用，需登录）
```

两套体系可以共存：

- **本 skill**：通过 Eino 沙箱走 LLM 调度，所有命令受白名单 + 授权约束
- **Claude Code CLI**：直接与 Anthropic 服务交互，需要 Anthropic 账号

## 注意事项

- 本 skill 是 Claude Code 工作流的封装层，所有 LLM 调用走 Eino 已配置的 ChatModel
- 复杂编码任务通过 Eino 的 sub-agent 系统执行，受沙箱边界保护
- claude-code CLI 的完整能力需要单独安装 Claude Code（见 https://claude.com/code）
- 任务执行均通过 Eino 的安全 agent 基础设施

## 参考资料

- Claude Code 官方文档：https://code.claude.com/docs
- Eino 框架：https://github.com/cloudwego/eino
- 最佳实践：源自 Claude Code 指南并适配 Eino 概念

## 版本

- 当前版本：1.0.0（Eino 适配版）
- 兼容性：Eino v0.x / cloudwego/eino-adk
- 最后更新：基于原 OpenClaw claude-code skill 1.0.0 转译