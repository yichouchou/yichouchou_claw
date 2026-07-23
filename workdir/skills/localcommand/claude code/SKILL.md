---
name: claude-code
description: 当用户要求"调 Claude Code 跑开发任务"、"用 Claude 做代码评审 / 架构 review / 编写测试 / 修 bug"、"非交互跑 claude -p ..."等开发相关场景时,加载本 skill。LocalCommandAgent 通过 local_command 工具调用主机上已安装的 claude CLI(Anthropic 官方 Claude Code),完成非交互式编码任务。本 skill 只在 eino 沙箱里"调用主机 CLI",不在 eino 内部实现 Claude Code。
context: inline
---

# Claude Code 集成(Eino + LocalCommandAgent 适配版)

本 skill 让 LocalCommandAgent 通过 `local_command` 工具调用**主机已安装的 Anthropic Claude Code CLI**(命令名:`claude`),完成非交互式编码任务。

## 前置条件

1. **主机已安装 Claude Code CLI**:
   - macOS / Linux / WSL:`curl -fsSL https://claude.ai/install.sh | bash`
   - Windows PowerShell:`irm https://claude.ai/install.ps1 | iex`
   - macOS(Homebrew):`brew install --cask claude-code`
   - Windows(WinGet):`winget install Anthropic.ClaudeCode`
   - 或 npm:`npm install -g @anthropic-ai/claude-code`(Node.js ≥ 18)
2. **`claude` 命令在 localcommand 白名单内**:见 `workdir/config/exec-approvals.json`(已放开)
3. **已配置 ANTHROPIC_API_KEY 或登录账号**:`claude auth login`(订阅)/ `claude auth login --console`(API 付费)
4. **沙箱 env 已净化**:localcommand 沙箱会从父进程 env 中剥离 `ARK_*` / `OPENAI_*` / `VOLCENGINE_*` / `COZE_*` / `MINIMAX_*` 等"其他 LLM 提供方"前缀,只透传 `ANTHROPIC_*` / `CLAUDE_*`
5. **沙箱工作目录可写**:默认 `/tmp`(Linux/macOS)或当前 cwd(Windows)

## CLI 命令清单(官方完整)

### 启动 & 会话

| 命令 | 说明 | 沙箱里用法 |
|---|---|---|
| `claude` | 启动交互式会话(本地终端用) | ❌ 沙箱非 TTY,会卡住 |
| `claude "query"` | 启动交互式会话并带上初始 prompt | ❌ 同上 |
| `claude -p "query"` | 非交互模式(SDK 模式),执行后立即退出 | ✅ **最常用** |
| `cat file \| claude -p "query"` | 管道输入 | ✅ |
| `claude -c` | 继续当前目录最近一次会话(交互) | ❌ |
| `claude -c -p "query"` | 继续最近会话(非交互) | ✅ |
| `claude -r "<session>" "query"` | 按 ID 或名字恢复历史会话 | ✅ |
| `claude --from-pr <num\|url>` | 恢复与某个 GitHub PR 关联的会话 | ✅ |
| `claude --fork-session` | 启动新 session ID(不复用旧的) | ✅ |
| `claude --session-id <uuid>` | 用指定 session ID | ✅ |

### 安装 / 更新 / 认证

| 命令 | 说明 |
|---|---|
| `claude update` | 升级到最新版本 |
| `claude install [version]` | 安装/重装原生二进制,`stable` / `latest` / `2.1.118` 等具体版本号 |
| `claude auth login` | 登录 Anthropic 账号(`--email` / `--sso` / `--console`) |
| `claude auth logout` | 登出 |
| `claude auth status` | 打印当前认证状态(加 `--text` 看人读格式) |

### 后台任务 & Agent 视图

| 命令 | 说明 |
|---|---|
| `claude agents` | 打开 agent 视图(监控/调度并行后台会话);加 `--json` 脚本化输出;加 `--cwd <path>` 限定;加 `--json --all` 含已终止会话 |
| `claude attach <id>` | 接入某个后台会话 |
| `claude gateway --config gateway.yaml` | 启动自托管 Claude Apps Gateway(SSO/策略下发;需 v2.1.195+) |
| `claude auto-mode defaults` | 打印 auto-mode 内置分类器规则(JSON);加 `--label 'Git Destructive'` 过滤 |
| `claude auto-mode config` | 看你当前生效的 auto-mode 配置 |
| `claude daemon status` | 后台 supervisor 状态(版本 / socket 目录 / worker 数) |
| `claude daemon stop --any` | 停掉 supervisor 与其管理的后台会话;加 `--keep-workers` 保留 worker |

## 常用 CLI Flags

### 输出控制

| Flag | 用途 |
|---|---|
| `-p`, `--print <prompt>` | 非交互模式,执行后退出 |
| `--output-format <fmt>` | `text` / `json` / `stream-json`,默认 `text` |
| `--input-format <fmt>` | `text` / `stream-json`,默认 `text` |
| `--verbose` | 详细日志(完整 turn-by-turn) |
| `--debug [categories]` | 调试模式;支持 `api,mcp` 多选,`!statsig,!file` 排除;不加值 = 全开 |

### 会话

| Flag | 用途 |
|---|---|
| `-c`, `--continue` | 继续当前目录最近一次会话 |
| `-r`, `--resume <session>` | 恢复历史会话(ID 或 name) |
| `--from-pr <num\|url>` | 恢复与 PR 关联的会话 |
| `--fork-session` | 创建新 session ID(不复用) |
| `--session-id <uuid>` | 指定 UUID 作为 session ID |
| `--remote "task"` | 在 claude.ai 创建 web 会话(订阅用户) |
| `--teleport` | 把 web 会话"传送"到本地终端继续 |

### 模型 & 行为

| Flag | 用途 |
|---|---|
| `--model <name>` | 指定模型(`sonnet` / `opus` / `haiku` / 完整名) |
| `--fallback-model <name>` | 默认模型过载时降级 |
| `--effort <level>` | 控制思考强度(`low` / `medium` / `high` / `max`) |
| `--permission-mode <mode>` | `default` / `plan` / `acceptEdits` / `auto` / `bypassPermissions` |
| `--dangerously-skip-permissions` | 跳过所有权限检查(慎用) |
| `--worktree` | 在隔离 git worktree 中跑,不污染主分支 |
| `--bare` | 最小化 UI / 隐藏 banner |
| `--name <name>`, `-n` | 显示用 session 名,出现在 `/resume` 列表 |
| `-h`, `--help` | 帮助 |

### 工具 & 上下文

| Flag | 用途 |
|---|---|
| `--add-dir <path>` | 把额外目录加进上下文(可多次传) |
| `--allowedTools <list>` | 允许的工具,逗号分隔(如 `Bash,Read,Edit`) |
| `--disallowedTools <list>` | 拒绝的工具,逗号分隔(如 `WebSearch,WebFetch`) |
| `--append-system-prompt <text>` | 在系统 prompt 末尾追加内容 |
| `--system-prompt <text>` | 替换系统 prompt |
| `--plugin-dir <path>` | 加载插件目录 |
| `--mcp-config <file>` | 指定 MCP 配置 |
| `--settings <file>` | 指定 settings.json |

### 系统 prompt flags

| Flag | 用途 |
|---|---|
| `--append-system-prompt <text>` | 追加到默认 system prompt 后 |
| `--system-prompt <text>` | 整体替换 system prompt |

## Slash Commands(在交互会话内用,非交互模式不支持)

> ⚠️ **沙箱里这些不可用**:交互模式下才生效;LLM 调 `claude -p` 时这些不会执行。LLM 在沙箱中**不要**试图调 `/compact` / `/review` 等。

| 命令 | 用途 |
|---|---|
| `/help` | 显示所有可用命令 |
| `/clear` | 清空会话上下文(彻底重置) |
| `/compact [focus]` | 压缩上下文,保留关键信息;可选 `Focus on ...` 指定保留重点 |
| `/context` | 看当前 context 使用情况 |
| `/cost` | 看 token 用量 + 估算成本 |
| `/init` | 在项目根目录生成 CLAUDE.md |
| `/review` | 审查当前分支 PR |
| `/security-review` | 审查当前分支安全风险 |
| `/memory` | 查/改 CLAUDE.md 持久化记忆 |
| `/skills` | 列出可用 Skills |
| `/agents` | 管理 sub-agent |
| `/bashes` | 列后台 bash 进程 |
| `/kill <id>` | 停掉某个后台进程 |
| `/commands` | 列出所有命令 |
| `/hooks` | 看配置的 hooks |
| `/plugin` | 插件管理 |
| `/config` | 改 settings |
| `/exit` | 退出会话 |

## 沙箱里 LLM 实际能用的命令

**只有非交互模式(`/SDK 模式`)在沙箱里能跑**。完整列表:

```bash
# 单次任务(最常用)
claude -p "<task>"

# 管道输入
cat file.txt | claude -p "summarize"

# 续接会话
claude -c -p "再列 3 个待办"

# 恢复历史会话
claude -r "auth-refactor" "Finish this PR"
claude -r "abc123" "继续 review"

# 输出格式
claude -p "..." --output-format json
claude -p "..." --output-format stream-json

# 模型覆盖
claude -p "..." --model sonnet
claude -p "..." --model opus
claude -p "..." --model claude-sonnet-4-5

# 工作目录上下文
claude -p "review src/auth" --add-dir ./src
claude -p "review" --add-dir ./a --add-dir ./b

# 工具白/黑名单
claude -p "fix bug" --allowedTools "Bash,Read,Edit,Grep"
claude -p "offline review" --disallowedTools "WebSearch,WebFetch"

# 权限模式
claude -p "..." --permission-mode plan        # 只规划,不执行写操作
claude -p "..." --permission-mode acceptEdits # 自动接受编辑
claude -p "..." --dangerously-skip-permissions # 跳过所有权限(慎用)

# Worktree 隔离(避免污染主分支)
claude -p "尝试重构 X" --worktree

# 系统 prompt 注入
claude -p "..." --append-system-prompt "always respond in Chinese, be concise"
claude -p "..." --system-prompt "You are a strict code reviewer"

# 调试
claude -p "..." --debug                      # 全开
claude -p "..." --debug "api,mcp"            # 只开 api 和 mcp
claude -p "..." --debug "!statsig,!file"     # 排除 statsig 和 file
claude -p "..." --verbose

# 安装 / 认证(只读 + 不消耗 token)
claude auth status                # 查认证状态
claude auth login --console       # API 付费模式登录
claude update                     # 升级
claude install stable             # 重装稳定版

# 后台任务管理
claude agents --json              # 列所有活跃会话(JSON)
claude agents --json --all        # 含已终止的
claude attach <session-id>        # 接入后台会话(交互,沙箱里慎用)
claude daemon status              # supervisor 状态
claude daemon stop --any          # 停掉 supervisor
```

## 沙箱约束

1. **命令白名单**:`claude` 已在 allowlist;若运行报"命令不在白名单中",引导用户授权 `whitelist auth for claude`
2. **凭据安全**:禁止在参数中夹带 `Authorization` / `Cookie` / 原始 `API Key`
3. **路径处理**:用绝对路径;且**禁止 `/mnt` 前缀**(WSL 隔离)
4. **env 净化**:localcommand 沙箱已默认剥离"其他 LLM 提供方"前缀,LLM 不必再手动 unset `ARK_*` 等
5. **避免交互**:不要调 `claude`(不带 `-p`)/ `claude -c`/`/compact` 等交互模式 —— 沙箱非 TTY 会卡住
6. **避免 slash commands**:沙箱里 `/clear` / `/review` 等 slash 命令不生效,LLM 不要在 `-p` 文本里写 `/compact` 假装在调 slash

## 工作流示例

### 复杂 Bug 修复

```bash
# 1. 先让 Claude 列出可能根因(只读,plan 模式更安全)
claude -p "分析 userService.js 中 NPE 的可能根因,输出 3 个最可能的" \
  --add-dir ./src --output-format text --permission-mode plan

# 2. 再让 Claude 写修复 patch(给工具白名单)
claude -p "基于上述根因,输出修复后的 userService.js 完整代码" \
  --add-dir ./src --allowedTools "Read,Edit"
```

### 新功能开发(Worktree 隔离)

```bash
# 在独立 worktree 中尝试,避免污染主分支
claude -p "实现 user API,含路由、handler、单元测试" \
  --add-dir ./src --output-format text --worktree
```

### 自动化代码评审

```bash
# PR 评审(JSON 输出便于下游处理)
claude -p "评审以下 diff,按严重度(高/中/低)列出问题" \
  --add-dir ./pr-1234.diff --output-format json | \
  jq '.[] | select(.severity=="high")'

# 全仓扫描(屏蔽网络工具保证离线)
claude -p "扫描 ./src 下所有 .go 文件,找出不符合 gofmt 的 import 排序" \
  --disallowedTools "WebSearch,WebFetch" --model sonnet
```

### CI/CD 集成

```bash
# 自动测试 + 自动修
claude -p "运行 npm test,如果失败就修,直到通过" \
  --allowedTools "Bash,Read,Edit" \
  --permission-mode acceptEdits \
  --dangerously-skip-permissions

# 看成本
claude -p "..." --output-format json | jq '.cost.total_cost_usd'
```

### 调试 / 排障

```bash
# 看 claude CLI 自己在做什么(turn-by-turn)
claude -p "..." --verbose

# 只看 api 调用 + mcp 通信,不看 statsig
claude -p "..." --debug "api,mcp"

# 排除 file 类调试,只看 api
claude -p "..." --debug "api,!file"
```

### 后台会话管理

```bash
# 列出所有后台会话(JSON 格式便于脚本)
claude agents --json --all

# 看 supervisor 状态
claude daemon status

# 停掉 supervisor 与所有后台会话
claude daemon stop --any

# 接入某个后台会话(交互,慎用)
claude attach 7c5dcf5d
```

### 多轮续接

```bash
# 第 1 轮:分析
claude -p "评审 user API 设计,列 3 个改进点" --add-dir ./docs

# 第 2 轮:继续同一会话
claude -c -p "按评审意见实现 handler 部分"

# 第 3 轮:换个名字恢复
claude -r "auth-refactor" -p "补单元测试"
```

## 自定义 Slash Commands(用户级 `.claude/commands/`)

> ⚠️ 沙箱里无效 —— 是给交互模式用的。LLM 不要尝试调用 `/refactor` 等。

```markdown
# .claude/commands/refactor.md
---
allowed-tools: Read, Edit
description: Refactor selected code
---

Refactor the selected code to improve readability and maintainability.
Focus on clean code principles and best practices.
```

调用方法(交互):`/refactor src/auth/login.ts`

## 反模式

- ❌ 跳过 `-p` 直接调 `claude`(交互模式) —— 沙箱非 TTY,会卡住
- ❌ 用相对路径或 `/mnt/*` 路径 —— 必须绝对路径,且沙箱屏蔽 `/mnt`
- ❌ 在 `-p "..."` 文本里塞 `/compact` / `/clear` 等 slash 命令 —— 沙箱里不会执行
- ❌ 在参数里塞凭据 / 走 `-H Authorization` / `--data`(curl 语义) —— 沙箱会拦截
- ❌ 用 `--dangerously-skip-permissions` 跑危险任务 —— 配合 Bash 白名单易越权
- ❌ 期望 `claude` 在 Windows 自动安装 —— 本 skill 不负责安装,仅调用

## 参考

- Claude Code 官方 CLI 参考:<https://code.claude.com/docs/en/cli-reference>
- Claude Code 安装:<https://claude.com/code>
- Slash commands 完整列表:<https://code.claude.com/docs/en/interactive-mode>
- 社区整理(80+ slash commands / 60+ flags):[Cranot/claude-code-guide](https://github.com/Cranot/claude-code-guide)
- 配套脚本:`scripts/install.sh`(Linux,可选)