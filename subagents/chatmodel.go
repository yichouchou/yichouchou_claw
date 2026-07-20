/*
 * Copyright 2025 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package subagents

import (
	"context"
	"fmt"
	"log"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"

	"github.com/yichouchou/yichouchou_claw/adk/common/model"
	messagehandler "github.com/yichouchou/yichouchou_claw/adk/middlewares/messageHandler"
	"github.com/yichouchou/yichouchou_claw/internal/localcommand"
	"github.com/yichouchou/yichouchou_claw/internal/session"
)

type GetWeatherInput struct {
	City string `json:"city"`
}

func NewWeatherAgent() adk.Agent {
	weatherTool, err := utils.InferTool(
		"get_weather",
		"获取指定城市的当前天气。",
		func(ctx context.Context, input *GetWeatherInput) (string, error) {
			return fmt.Sprintf(`%s 的温度是 25°C`, input.City), nil
		},
	)
	if err != nil {
		log.Fatal(err)
	}

	a, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:        "WeatherAgent",
		Description: "这个 agent 可以获取指定城市的当前天气。",
		Instruction: `你的唯一职责是使用 'get_weather' 工具获取指定城市的天气。
调用工具后，直接向用户报告结果。

【强约束】
- 你没有可以转出的 sub-agent，**严禁**调用 transfer_to_agent 或任何形式的转出工具。
- 不要尝试委派任务给其他 agent，不要建议用户联系其他 agent。
- 如果用户的请求超出你"查天气"的能力范围（例如闲聊、其他专业问题），直接回答"我无法处理这个请求"，不要做任何重试或转移动作。
- 只在你确定需要城市天气时才调用 get_weather；其他场景直接文本回复。`,
		Model: model.NewChatModel(),
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{weatherTool},
			},
		},
		Handlers: []adk.ChatModelAgentMiddleware{
			messagehandler.NewLanguageConstraintMiddleware(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	return a
}

func NewChatAgent() adk.Agent {
	// 创建本地命令工具
	localCmdTool, err := utils.InferTool(
		"local_command",
		"执行本地 Linux 命令的沙箱工具。\n\n【安全特性】\n- 白名单机制：仅允许预定义的安全命令列表\n- 危险拦截：自动阻止高危操作 (rm -rf, dd, fdisk, reboot 等)\n- 沙箱隔离：在受限环境 (/tmp) 中执行，限制 CPU/内存/时间\n\n【使用场景】\n- 查看系统状态 (温度、内存、磁盘、进程)\n- 读取配置文件内容\n- 搜索日志文件\n- 查看网络连接\n- 执行简单的文本处理命令\n\n【禁止使用】\n- 任何修改系统状态的操作\n- 远程下载执行脚本\n- 访问敏感文件",
		func(ctx context.Context, input *localcommand.CommandInput) (string, error) {
			result, err := localcommand.Execute(ctx, input)
			if err != nil {
				return "", err
			}
			// 格式化输出
			output := fmt.Sprintf("命令执行完成:\n退出码: %d\n耗时: %s\n标准输出:\n%s\n标准错误:\n%s",
				result.ExitCode, result.Duration, result.Stdout, result.Stderr)
			return output, nil
		},
	)
	if err != nil {
		log.Fatal(err)
	}

	a, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:        "ChatAgent",
		Description: "一个通用的对话 agent，用于处理日常闲聊和本地命令执行。",
		Instruction: `你是一个友好的对话助手。
你的职责是处理日常闲聊，并回答与特定工具任务无关的问题。

【本地命令执行】
当用户需要了解主机状态、执行系统命令、查看日志或配置文件时，你应该使用 local_command 工具。
可用的系统信息查询命令包括：
- sensors: 查看硬件温度
- free/top/ps: 查看系统资源
- df/du: 查看磁盘使用
- cat/grep: 读取文件或搜索内容
- ps/netstat: 查看进程和网络

【强约束】
- 你没有可以转出的 sub-agent，**严禁**调用 transfer_to_agent 或任何形式的转出工具。
- 不要尝试委派任务给其他 agent（RouterAgent 会负责路由，你不需要再转移）。
- 如果用户的请求明显超出闲聊范围（例如需要实时天气、订单、计算器等），直接告诉用户"我无法处理，请稍后再试"，不要做任何转移动作。
- 只在闲聊/通用知识范围内作答，不要捏造事实、不要编造数据。`,
		Model: model.NewChatModel(),
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{localCmdTool},
			},
		},
		Handlers: []adk.ChatModelAgentMiddleware{
			messagehandler.NewLanguageConstraintMiddleware(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	return a
}

// NewRouterAgent 创建最外层 ChatModelAgent，并通过 Handlers 字段挂载 session.PersistMiddleware。
// 这是一个 eino 原生方案：AfterAgent 钩子会自动拿到 SDK 内部维护的完整 messages
// （包含 assistant(tool_calls) ↔ tool(result) 完整 id 对齐），无需手动从 SSE 推断。
func NewRouterAgent(store *session.Store) adk.Agent {
	a, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:        "RouterAgent",
		Description: "一个智能任务路由器，负责将任务分配给其他专家 agent。",
		Instruction: `你是一个智能任务路由器，负责把任务委派给最合适的专家 agent。
可用的专家 agent 如下：
- ChatAgent：日常闲聊、通用知识问答、技术方案讨论、澄清式追问、查看主机状态和执行系统命令。
- WeatherAgent：查询指定城市的天气，调用 get_weather 工具。
等等

【路由判定规则（按顺序）】
1. **强语义优先**：消息里包含明确关键词
   - 包含 "天气"、"温度"、"下雨"、"湿度"、"风速"、"穿什么" 等 → 转 WeatherAgent。
   - 包含 "CPU"、"内存"、"温度"、"进程"、"系统状态"、"查看日志"、"配置文件" 等系统查询 → 转 ChatAgent。
   - 包含纯闲聊、技术讨论、方案对比、概念解释、"你觉得"、"你怎么看" → 转 ChatAgent。

2. **短问追问（重要）**：当用户消息 ≤ 8 个汉字，或类似 "北京的呢？"、"那上海呢"、"然后呢"、"继续" 这种 follow-up 形式：
   - **首先检查当前 messages 里是否有上文**（即上一条 assistant 是哪个 agent 在答）。
     - 若上文是 WeatherAgent，可以推断这是天气连续追问 → 转 WeatherAgent。
     - 若上文是 ChatAgent 或没有上文 → **不要猜测意图**，用 ChatAgent 反问一句澄清，例如"你说的 XX 是什么意思？是天气吗，还是想讨论其他话题？"，然后停止本次 run。
   - 严格禁止对无上文且措辞模糊的短问直接转 WeatherAgent。

3. **地理孤词例外**：用户只写一个地名（"西藏"、"新疆"、"上海"）且上文无法推出天气话题时：
   - 用 ChatAgent 给出简短反问澄清，不要直接跳到查天气。

4. **无匹配**：如果没有任何 agent 能处理，直接让 ChatAgent 回复"我无法处理这个请求"。

【强约束】
- 不要在 instruction 中复述任何工具调用细节给用户听。
- 不要重复发起 transfer_to_agent；一次请求最多一次路由。
- 你自己不要回答业务问题；永远先把任务委派给最合适的 agent。`,
		Model: model.NewChatModel(),
		// 只在 RouterAgent 上注册 PersistMiddleware，让最外层 agent 在每次成功结束后
		// 把完整 messages 写入 store；子 agent（ChatAgent / WeatherAgent）不会触发。
		Handlers: []adk.ChatModelAgentMiddleware{
			session.NewPersistMiddleware(store),
			messagehandler.NewLanguageConstraintMiddleware(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	return a
}
