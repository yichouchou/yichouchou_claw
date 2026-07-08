# yichouchou_claw

基于 CloudWeGo eino 框架的 Agent 项目，使用 Hertz HTTP 框架提供聊天服务，支持 SSE 流式响应和多 Agent 路由。

## 技术栈

| 分类 | 技术 |
|------|------|
| 框架 | [CloudWeGo eino](https://github.com/cloudwego/eino) v0.9.12 |
| HTTP | [Hertz](https://github.com/cloudwego/hertz) v0.10.5 |
| LLM 支持 | OpenAI API / Volcano Engine Ark API |
| 流式协议 | Server-Sent Events (SSE) |
| 语言 | Go 1.25+ |

## 项目结构

```
yichouchou_claw/
├── main.go                    # 应用入口，Hertz 服务器启动，路由配置
├── adk/                       # Agent Development Kit（公共组件库）
│   └── common/
│       ├── model/
│       │   └── chat_model.go  # LLM 模型封装，支持 OpenAI/Ark 自适应切换
│       ├── store/
│       │   └── store.go       # 内存版 CheckPointStore（GraphTool 断点续存）
│       ├── trace/
│       │   └── coze_loop.go   # CozeLoop 链路追踪集成
│       ├── prints/
│       │   └── util.go        # Agent 事件日志打印工具
│       └── tool/              # 通用 Tool 封装
│           ├── approval_wrapper.go   # 工具调用审批包装器
│           ├── follow_up_tool.go     # 用户追问中断工具
│           ├── review_edit_wrapper.go# 工具调用复核编辑包装器
│           └── graphtool/
│               ├── graph_tool.go     # GraphTool 核心实现（支持断点续存）
│               └── examples/         # GraphTool 使用示例
├── internal/                  # 项目核心模块
│   ├── session/
│   │   ├── store.go           # 会话存储（内存版，支持多轮上下文窗口）
│   │   └── middleware.go      # PersistMiddleware（AfterAgent 钩子持久化 messages）
│   ├── message/
│   │   └── streamMessageOutput.go # SSE 事件转换与发送
│   ├── logs/
│   │   └── logger.go          # 带颜色的日志工具
│   └── gptr/
│       └── ptr.go             # 泛型指针辅助函数
├── subagents/
│   └── chatmodel.go           # Agent 定义：RouterAgent / ChatAgent / WeatherAgent
├── index.html                 # Web 演示页面
├── Dockerfile
├── go.mod
└── go.sum
```

### 模块说明

- **adk/common/model** — LLM 模型统一抽象，自动根据 `MODEL_TYPE` 环境变量选择 OpenAI 或 Ark。
- **adk/common/tool** — 通用工具封装：审批、追问复核、GraphTool 断点续存。
- **internal/session** — 多轮对话状态管理，通过 `AfterAgent` 钩子同步 eino SDK 内部 messages，避免 tool_call_id 不匹配问题。
- **internal/message** — 将 eino AgentEvent 转换为 SSE 事件流。
- **subagents** — 三个 Agent：
  - `RouterAgent`：智能路由（天气 → WeatherAgent，闲聊 → ChatAgent）
  - `WeatherAgent`：提供 `get_weather` 工具
  - `ChatAgent`：通用对话

## 功能特性

- **多 Agent 路由**：RouterAgent 根据语义智能分发任务
- **SSE 流式响应**：实时推送 token 流，前端无等待
- **多轮对话记忆**：会话级别消息窗口（默认 12 轮），自动管理上下文
- **工具调用**：支持 weather 工具，GraphTool 断点续存
- **多模型支持**：OpenAI API / Ark API 一键切换
- **会话持久化**：通过 `AfterAgent` 钩子同步 SDK 内部完整 messages，避免 tool_call_id 错位
- **Web 演示页面**：内置 `index.html`，打开浏览器即可体验

## 快速开始

### 前置依赖

- Go 1.25+
- Git

### 1. 克隆项目

```bash
git clone https://github.com/yichouchou/yichouchou_claw.git
cd yichouchou_claw
```

### 2. 配置环境变量

```bash
# 使用 OpenAI（默认）
export OPENAI_API_KEY="sk-..."
export OPENAI_MODEL="gpt-4o"

# 或使用火山引擎 Ark
export MODEL_TYPE="ark"
export ARK_API_KEY="..."
export ARK_MODEL="ep-xxx"
export ARK_BASE_URL="https://ark.cn-beijing.volces.com/api/v3"
```

### 3. 启动服务

```bash
go mod download
go run .
```

服务启动后访问：

- Web 界面：http://localhost:8080/
- 直接调 API：见下方 API 示例

## 配置说明

| 环境变量 | 说明 | 默认值 |
|----------|------|--------|
| `OPENAI_API_KEY` | OpenAI API Key | — |
| `OPENAI_MODEL` | OpenAI 模型名称 | — |
| `OPENAI_BASE_URL` | OpenAI API 地址 | `https://api.openai.com/v1` |
| `OPENAI_BY_AZURE` | 是否使用 Azure OpenAI | `false` |
| `MODEL_TYPE` | 模型类型，`ark` 或 `openai` | `openai` |
| `ARK_API_KEY` | Ark API Key | — |
| `ARK_MODEL` | Ark 模型 ID | — |
| `ARK_BASE_URL` | Ark API 地址 | — |
| `COZELOOP_WORKSPACE_ID` | CozeLoop 工作空间 ID（可选，链路追踪用） | — |
| `COZELOOP_API_TOKEN` | CozeLoop API Token | — |

## API 使用示例

### SSE 聊天接口

```
GET /chat?session_id=<会话ID>&query=<URL编码的提问>
```

**请求示例：**

```bash
# 基础对话
curl -N 'http://localhost:8080/chat?session_id=demo&query=%E4%BD%A0%E5%A5%BD'

# 查天气
curl -N 'http://localhost:8080/chat?session_id=demo&query=%E5%8C%97%E4%BA%AC%E5%A4%A9%E6%B0%94%E6%80%8E%E4%B9%88%E6%A0%B7'

# 多轮对话（复用 session_id）
curl -N 'http://localhost:8080/chat?session_id=demo&query=%E9%82%A3%E4%B8%8A%E6%B5%B7%E5%91%A2'
```

**SSE 事件类型：**

| 事件类型 | 说明 | 字段 |
|----------|------|------|
| `session` | 会话 ID | `content` |
| `message` | 完整消息 | `content`, `agent_name`, `tool_calls` |
| `stream_chunk` | 文本流片段 | `content`, `agent_name` |
| `tool_calls` | 工具调用 | `tool_calls`, `agent_name` |
| `tool_result` | 工具结果 | `content` |
| `tool_result_chunk` | 工具结果片段 | `content` |
| `action` | Agent 动作（transfer/exit/interrupted） | `action_type`, `content` |
| `error` | 错误信息 | `error` |
| `end` | 流结束标记 | — |

**响应示例：**

```
event: session
data: {"type":"session","content":"abc123"}

event: message
data: {"type":"message","agent_name":"RouterAgent","content":"正在为您查询天气..."}

event: end
data: {"type":"end"}
```

### Web 界面

直接打开 http://localhost:8080/ 或 http://localhost:8080/index.html，使用内置的 Web 界面进行对话。

## License

Apache License 2.0 — see LICENSE file for details.
