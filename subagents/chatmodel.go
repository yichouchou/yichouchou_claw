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
	"encoding/base64"
	"fmt"
	"log"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/skill"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	memorytool "github.com/yichouchou/yichouchou_claw/adk/common/memory"
	"github.com/yichouchou/yichouchou_claw/adk/common/model"
	messagehandler "github.com/yichouchou/yichouchou_claw/adk/middlewares/messageHandler"
	"github.com/yichouchou/yichouchou_claw/internal/attachment"
	"github.com/yichouchou/yichouchou_claw/internal/config"
	"github.com/yichouchou/yichouchou_claw/internal/ipc"
	"github.com/yichouchou/yichouchou_claw/internal/localcommand"
	"github.com/yichouchou/yichouchou_claw/internal/session"
)

// buildSkillMiddleware 用本地文件系统 backend 创建 skill 中间件，
// skillsDir 是包含若干 <skill-name>/SKILL.md 的根目录。
//
// 若 skillsDir 为空字符串，返回 (nil, nil)，调用方应跳过。
// 若目录不存在或读 SKILL.md 失败，返回 error 由调用方决定是否 fatal。
func buildSkillMiddleware(ctx context.Context, skillsDir string) (adk.ChatModelAgentMiddleware, error) {
	if skillsDir == "" {
		return nil, nil
	}
	be, err := local.NewBackend(ctx, &local.Config{})
	if err != nil {
		return nil, fmt.Errorf("local backend: %w", err)
	}
	backend, err := skill.NewBackendFromFilesystem(ctx, &skill.BackendFromFilesystemConfig{
		Backend: be,
		BaseDir: skillsDir,
	})
	if err != nil {
		return nil, fmt.Errorf("skill backend (%s): %w", skillsDir, err)
	}
	return skill.NewMiddleware(ctx, &skill.Config{
		Backend: backend,
	})
}

// NewChatModelForRefiner 返回一个独立的 ChatModel 实例,专供 memory LLM
// refine worker 使用。与 ChatModelAgent 共享同一个 model.NewChatModel()
// 工厂(同一 provider/配置),但**不挂在 agent 链上**,refine worker 直接
// 调 Generate 不依赖 adk 框架。
//
// 注意:返回 nil 表示环境未配置 ChatModel(API key 缺失等),
// 调用方需判空后禁用 refine。
func NewChatModelForRefiner() einomodel.ToolCallingChatModel {
	// model.NewChatModel 在出错时 log.Fatalf — 不能在 refine 启动时让主进程挂。
	// 用 defer recover 兜底。
	var cm einomodel.ToolCallingChatModel
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[subagents] NewChatModelForRefiner panic: %v (memory refine disabled)", r)
				cm = nil
			}
		}()
		cm = model.NewChatModel()
	}()
	if cm == nil {
		return nil
	}
	// 包装:ToolCallingChatModel.WithTools 返回新实例(并发安全),让 refine
	// 走的 ChatModel 是"空 tools"副本,不会被外部 tool 配置影响。
	wrapped, err := cm.WithTools([]*schema.ToolInfo{})
	if err != nil {
		log.Printf("[subagents] WithTools wrap failed: %v (refine uses raw ChatModel)", err)
		return cm
	}
	return wrapped
}

type GetWeatherInput struct {
	City string `json:"city"`
}

// WebSearchInput 是 web_search 服务的占位输入结构体。
//
// web_search 是 Anthropic/Minimaxi 的服务端工具，实际搜索由 API 服务端完成，
// 客户端不会真正解析这里的参数。这里定义结构体只是为了满足 utils.InferTool
// 的签名要求，让 eino 能识别这个工具的定义。
type WebSearchInput struct {
	Query string `json:"query"`
}

// sharedWebSearchTool 是 ChatAgent / LocalCommandAgent / RouterAgent 等多个 agent 共享的
// web_search 占位工具。
//
// 设计要点 (2026-08-03 修复):
//   - 实际执行由 Anthropic/Minimaxi 服务端完成（API 层在请求时已开启 web_search 工具）
//   - 这里只注册一个"无害"的占位工具,让 eino ToolNode 找到对应执行入口
//   - 不返回真实结果 — 服务端工具的搜索结果已经包含在 message content 里
//   - Multiple agent 共享同一个 tool instance,避免每次 NewAgent 都重新构造
//
// 为什么 LocalCommandAgent 也需要 web_search (2026-08-03 修复):
//   - 复合任务(例如"画个流程图 + 基于公开资料"): 文字总结需要联网搜索
//   - 之前 LocalCommandAgent 没注册 web_search,LLM 看到 prompt 说"读网络"但 tools 列表
//     没这个工具,导致 LLM 误以为"只能跑本地命令",放弃总结,直接 fallback 到画图
//   - 现在 LocalCommandAgent 也持有 web_search, 能完成"先搜 → 再总结 → 再画图"流程
func sharedWebSearchTool() tool.BaseTool {
	t, err := utils.InferTool(
		"web_search",
		"联网搜索工具。由 Anthropic/Minimaxi 服务端直接执行,无需客户端处理。返回结果已包含在模型响应中。",
		func(ctx context.Context, input *WebSearchInput) (string, error) {
			return "[web_search 由服务端处理,结果已包含在模型响应中]", nil
		},
	)
	if err != nil {
		log.Fatalf("sharedWebSearchTool: %v", err)
	}
	return t
}

// sharedMemorySearchTool 是 ChatAgent / LocalCommandAgent / RouterAgent 共用的 memory_search 工具。
//
// LocalCommandAgent 持有 memory_search 的原因 (2026-08-03 修复):
//   - 复合任务 context: 当用户问"上次画的图改个样式",LocalCommandAgent 需要读历史决策
//   - 之前 LocalCommandAgent 没注册,出现"上下文断层"需要每次都 round-trip 回 RouterAgent
//   - 现在 LocalCommandAgent 自己也能查 memory,减少 transfer 次数
func sharedMemorySearchTool() tool.BaseTool {
	t, err := memorytool.NewSearchTool()
	if err != nil {
		log.Fatalf("sharedMemorySearchTool: %v", err)
	}
	return t
}

func NewWeatherAgent(store *session.Store, extraHandlers ...adk.ChatModelAgentMiddleware) adk.Agent {
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

	handlers := append([]adk.ChatModelAgentMiddleware{
		messagehandler.NewLanguageConstraintMiddleware(),
		newDynamicRecentMemoryMiddleware(config.GetRecentMemoryConfig()),
	}, extraHandlers...)
	if store != nil {
		handlers = append([]adk.ChatModelAgentMiddleware{session.NewPersistMiddleware(store)}, handlers...)
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
		Handlers: handlers,
	})
	if err != nil {
		log.Fatal(err)
	}
	return a
}

// localCommandToolDesc 是 local_command 工具对外暴露的描述（与
// internal/localcommand 中的 HardForbiddenPatterns / SoftForbiddenPatterns /
// AllowedCommands 保持同步）。
//
// 这个字符串会直接喂给 LocalCommandAgent 的 LLM，让模型在调用前就能"看到"沙箱边界。
// 修改时请同时确认 DangerousPatterns / AllowedCommands / platformHint / AuthorizationMiddleware。
const localCommandToolDesc = `在受限沙箱中执行**一条**本地 bash 命令并返回 stdout / stderr / 退出码 / 耗时。

========================================
【一、沙箱边界 — 单一真相源是运行时校验, 不是 prompt 记忆】
========================================
- 命令可用范围、硬禁止规则、软禁止规则、授权机制等**全部由** workdir/config/exec-approvals.json + 沙箱内部 DangerousPatterns 决定。
  本 prompt **不列具体可用的命令** — 列出来就会和配置漂移 (新增/删除命令后 prompt 还在说老规则), 反而误导你。
- 当你不确定某命令是否可用时: 直接调用本工具 — 沙箱会告诉你能不能跑、需不需要授权、为什么被禁。
  拿到错误信息后, 把其中的 [授权提示] / [拒绝原因] / [等价命令提示] **原样**转给用户, 不要自己脑补。
- 命令在隔离的子进程 + 单独进程组里跑; 超时会被自动 kill; 工作目录被限制在沙箱临时目录。

========================================
【二、调用约束】
========================================
- 一次只调一次 local_command (不要用 ; / && 串多条; 沙箱内部会对每段独立校验, 但一次只发一条更稳)。
- **绝对禁止**在同一次 ChatModel 输出里塞超过 5 个 local_command / skill 调用:
  eino 框架 MaxIterations=20; 一次塞满会在循环时立即触发
  "exceeds max iterations", 整个任务崩掉。
  需要"收集一批信息"时, 分 2-3 个 round:
  先 ls / cat 关键文件 → 再综合判断 → 再下一步。
- 当遇到软禁止时, **优先**"提示 + 等用户授权", 而不是反复换其他命令绕过。
- 永远不要尝试硬禁止命令; 用户硬要求时, 把错误信息直接转告并解释为何拒绝。
- 不要在命令中夹带任何凭据 (密码、Token、API Key、Cookie、私钥)。
  如需调用需要凭据的 API, 凭据必须由用户注入; 你可以向用户询问授权流程。
- 凭据相关文件 (~/.ssh、~/.bash_history 等) 禁止读取。

========================================
【三、专用工具优先 (不要用 bash 替代)】
========================================
如果你有"专用工具"可用 (Read / Edit / Grep / Glob 等), **优先用专用工具**, 不要为了
"看起来更专业"把所有事都用 bash 处理 — 例如:
  - 读文件 → Read
  - 改文件 → Edit / Write
  - 搜索代码内容 → Grep
  - 搜索文件名 → Glob
  - 问问题 → 不需要工具直接回

========================================
【四、输出解读】
========================================
返回格式:
  命令执行完成:
  退出码: <int>   ← 0=成功, >0=命令自身报错, -1=沙箱拦截
  耗时: <duration>
  标准输出: <stdout>
  标准错误: <stderr>

stderr 关键字速查 (这些都是沙箱真实返回的标识, 看到后按规则处理):
- "[授权提示]"        → 用户授权不足 / 未授权: 把提示原样转告用户, 等用户授权
- "[等价命令提示]"    → 当前命令不可用, 沙箱给出了白名单内等价命令, 按提示替换
- "[拒绝原因]"        → 硬禁止 / 软禁止 / 不在白名单, 沙箱说明具体原因
- "[平台提示]"        → 平台不兼容 (Windows 跑 Linux 命令): 改用平台等价命令
- exit_code = -1      → 沙箱拦截 (上面对应 stderr 段会说明)

========================================
【五、平台提示】
========================================
- 当前平台由沙箱在 host 侧编译时锁定, 工作在什么 OS 用什么命令。
- 不要假设路径是 /etc/... 或 windows-style; 按平台语义选:
    Linux / macOS: ls / /tmp/ /etc /usr/local/bin
    Windows:      dir / %TEMP%\ / $env:...
- 反弹 shell / 远控 / 关机重启等命令**硬禁止** — 别尝试, 沙箱会直接拒绝。
`

// NewLocalCommandAgent 创建专门执行主机 bash 命令的 agent。
//
// 这是本仓库"执行类"操作的唯一入口。它持有 local_command 工具，
// 注册了 AuthorizationMiddleware 用于软禁止授权；
// 不持有聊天工具——一旦完成命令执行就直接转回 RouterAgent / 用户。
//
// 2026-08-03 修复: 工具清单扩充
//   - 之前只有 local_command + skill,导致复合任务(如"画个流程图 + 基于公开资料")
//     缺失"联网搜索"环节
//   - 现在持有 web_search + memory_search + local_command + skill
//   - 让 LocalCommandAgent 能完整处理"先搜公开资料 → 再总结 → 再画图"流程
//
// extraHandlers 追加到默认的中间件（language / authorization / retry / skill）
// 之后；用于在不改这个函数的前提下注入额外的 ChatModelAgentMiddleware
// （例如 internal/memory.MemoryMiddleware）。
// routerAgentUnknownToolHandler 在 RouterAgent 的 ToolsNode 找不到目标工具时被调用。
//
// 触发场景:LLM 偶尔会"幻觉"地调用 transfer_to_agent / memory_search 之外的工具名
// (local_command / web_search / skill / get_weather / read_file 等), 这些工具属于
// 子 agent, RouterAgent 没注册。不拦截会让 ToolsNode 直接返回
// "[NodeRunError] tool X not found in toolsNode indexes, node path: [node_1, ToolNode]",
// 整个 run 失败, 用户看不到任何修复路径。
//
// 处理策略:
//   - 检测到名字属于"子 agent 工具"时, 返回一段明确的转交提示, 引导 LLM 在下一轮
//     调用 transfer_to_agent(agent_name=LocalCommandAgent) 来"完成"原本想做的事;
//   - 其它未知工具名 (完全不属于任何子 agent): 返回"未注册"提示, 让 LLM 改用 transfer。
//
// 注意:这个 handler 只在 RouterAgent 上挂——子 agent (LocalCommandAgent / ChatAgent /
// WeatherAgent) 自己有完整的工具集, 不需要这种回退。
//
// 2026-08-04 修复: 之前只有 [NodeRunError] 硬错, LLM 看到 tool_call 失败后陷入死循环;
// 现在 LLM 拿到明确文本, 会主动 transfer 到正确的子 agent, 任务继续。
func routerAgentUnknownToolHandler(ctx context.Context, name, input string) (string, error) {
	// 子 agent 工具白名单 (按 agent 归类, 错误提示更精准)
	childAgentTools := map[string]string{
		// LocalCommandAgent
		"local_command": "LocalCommandAgent",
		"bash":          "LocalCommandAgent",
		"shell":         "LocalCommandAgent",
		"execute":       "LocalCommandAgent",
		// ChatAgent (general_chat skill 等)
		"read_file":  "ChatAgent",
		"write_file": "ChatAgent",
		"edit_file":  "ChatAgent",
		"file_read":  "ChatAgent",
		"glob":       "ChatAgent",
		"grep":       "ChatAgent",
		// WeatherAgent
		"get_weather": "WeatherAgent",
		"weather":     "WeatherAgent",
		// web_search / skill 是兜底共享, 任意 agent 可用, 但 RouterAgent 没注册
		"web_search": "LocalCommandAgent", // 复合任务通常 LocalCommandAgent 用
		"skill":      "LocalCommandAgent",
		"drawio":     "LocalCommandAgent",
	}

	if dest, ok := childAgentTools[name]; ok {
		// 不让 handler 静默成功 (LLM 会误以为调用成功了);
		// 返回明确"请改用 transfer_to_agent"的文本, LLM 下一轮自然会修。
		return fmt.Sprintf(
				"工具 %q 不在 RouterAgent 的注册列表中（它属于子 agent %s）。"+
					"RouterAgent 不能直接调用 %q；请改用 `transfer_to_agent(agent_name=%q)` "+
					"把任务委派给 %s，由它持有并执行 %q。",
				name, dest, name, dest, dest, name),
			nil
	}

	// 完全未知的工具名 — 返回"未注册"通用提示
	return fmt.Sprintf(
			"工具 %q 在 RouterAgent 中未注册。"+
				"RouterAgent 只能调用 transfer_to_agent 与 memory_search；"+
				"其它工具属于子 agent（ChatAgent / WeatherAgent / LocalCommandAgent）。"+
				"请用 transfer_to_agent(agent_name=...) 委派任务，或改用已注册工具。",
			name),
		nil
}

// localCommandUnknownToolHandler 在 LocalCommandAgent 的 ToolsNode 找不到目标工具时被调用。
//
// 触发场景:LLM 偶尔幻觉地调用 transfer_to_agent (被 WithDisallowTransferToParent 隐藏了)
// 或者 read_file / write_file (其实用 local_command 跑 cat / heredoc 即可)。
//
// 处理策略:返回明确提示, 让 LLM 在下一轮改用 local_command 内部等价命令。
//
// 2026-08-04 修复: 之前会触发 [NodeRunError] tool X not found, 整个 run 崩溃;
// 现在 LLM 拿到明确文本, 任务继续。
func localCommandUnknownToolHandler(ctx context.Context, name, input string) (string, error) {
	switch name {
	case "transfer_to_agent", "transfer_to_parent":
		return "LocalCommandAgent 没有 transfer_to_agent 工具（被 WithDisallowTransferToParent 禁用）。" +
			"LocalCommandAgent 不应把任务转移出去——它必须自己在沙箱内完成命令执行。" +
			"如果当前步骤确实需要 ChatAgent / WeatherAgent 的能力，请先完成当前命令、然后在 RouterAgent 一侧发起 transfer。", nil
	case "read_file":
		return "LocalCommandAgent 没有专用 read_file 工具；请改用 local_command 执行 `cat <path>` 或 `cat > <path> <<'EOF'` 等 shell 命令读文件。" +
			"heredoc 写文件必须用 `cat > <file> << 'EOF'` (单引号 EOF, 防止 $ 展开)。", nil
	case "write_file", "edit_file":
		return "LocalCommandAgent 没有专用 write_file 工具；请改用 local_command 执行 `cat > <file> <<'EOF' ... EOF` (heredoc) 写文件。" +
			"heredoc body 内部可包含任意字符 (xml / json / 图表源码), 单引号 EOF 防止 $VAR 展开, 是当前唯一稳定的写文件模式。", nil
	case "glob", "grep":
		return fmt.Sprintf("LocalCommandAgent 没有专用 %s 工具；请改用 local_command 执行 `find` / `ls` (glob 等价) 或 `grep` / `rg` (内容搜索等价)。", name), nil
	case "web_search":
		return "LocalCommandAgent 的 web_search 已注册 (sharedWebSearchTool)；若你看到本提示说明 LLM 调用名称大小写不一致。" +
			"请确认 tool_call 的 name 严格为 `web_search`（小写 + 下划线）。", nil
	}
	// 兜底
	return fmt.Sprintf("工具 %q 在 LocalCommandAgent 中未注册。本 agent 仅持有 local_command / web_search / memory_search / skill。"+
		"请改用这些已注册工具, 或放弃这一步。", name), nil
}

func NewLocalCommandAgent(ctx context.Context, skillsDir string, store *session.Store, extraHandlers ...adk.ChatModelAgentMiddleware) adk.Agent {
	// 构造 skill 中间件（skillsDir 下属的 localcommand/ 目录里的 skill 列表会被加载）。
	// 涵盖 system_diagnosis / git_operations / network_diagnosis 等命令模板。
	skillMw, err := buildSkillMiddleware(ctx, filepath.Join(skillsDir, "localcommand"))
	if err != nil {
		log.Fatalf("LocalCommandAgent skill middleware: %v", err)
	}
	// 动态 recent memory 中间件：transfer 过来的子 agent 也能看到最近记忆,
	// 防止"上下文断层"——RouterAgent 复述得再清楚,LocalCommandAgent 也得有
	// 原始 recent memory 作为兜底。
	localCmdDynamicRecentMw := newDynamicRecentMemoryMiddleware(config.GetRecentMemoryConfig())
	// 多模态升级(2026-07-29): 改用 utils.InferEnhancedTool,
	// 让 ToolResult 既包含文本 part(命令输出 + exit code + duration),
	// 也包含命令生成的"产物文件"作为附件(PDF / 图片 / 日志等)。
	localCmdTool, err := utils.InferEnhancedTool(
		"local_command",
		localCommandToolDesc,
		func(ctx context.Context, input *localcommand.CommandInput) (*schema.ToolResult, error) {
			// 把 agent 名注入到 ctx,沙箱里的 per-agent 白/黑名单缓存才能命中。
			ctx = localcommand.WithAgentName(ctx, "LocalCommandAgent")
			result, err := localcommand.Execute(ctx, input)
			if err != nil {
				return nil, err
			}
			textPart := fmt.Sprintf("命令执行完成:\n退出码: %d\n耗时: %s\n标准输出:\n%s\n标准错误:\n%s",
				result.ExitCode, result.Duration, result.Stdout, result.Stderr)
			result2 := &schema.ToolResult{
				Parts: []schema.ToolOutputPart{
					{Type: schema.ToolPartTypeText, Text: textPart},
				},
			}
			// === 扫描命令生成的产物文件(2026-07-29 新增;2026-08-03 重构为"image_url-only"模式) ===
			//
			// 关键设计变更 (2026-08-03 17:23 复盘后):
			//
			// 之前的设计 (2026-07-29 ~ 2026-08-03 16h):
			//   - 检测到 .png/.pdf 等产物 → 读 base64 → 构造 ToolPartTypeFile part
			//   - eino ToolNode 把 ToolResult 转 user message multi_content →
			//     产生 ChatMessagePartTypeFileURL → Ark adapter 100% 报错
			//
			// 之前的设计 (2026-07-30 "LLM 复述 markdown image" 路径):
			//   - 把 base64 拼成 `![name](data:image/png;base64,xxx)` 嵌进 stdout text
			//   - 期望 LLM 看到后,复述同样的语法到自己的回复里
			//   - 错误: LLM (MiniMax-M3) 是文本模型,无法处理 300KB base64 字符串;
			//     复述极易出错 (中间截断、字符错误、token 浪费)
			//
			// 当前设计 (2026-08-03 17:23 复盘后):
			//   - buildArtifactFilePart 返回 ToolPartTypeImage (而非 File)
			//   - eino ToolNode 转 ChatMessagePartTypeImageURL —— Ark adapter 支持 (line 644)
			//   - image_url part 进入 user message multi_content → LLM 上下文
			//     (MiniMax-M3 是文本 LLM 看不了图,但 base64 占 token,这是 Ark 支持下的妥协)
			//   - 同时 handleRegularMessage 扫描 UserInputMultiContent → 转 SSE multi_content
			//     → 前端 addToolResult 在 assistant 消息下追加 <img>
			//   - text/* 类不进 ToolOutputPart (LLM 看不懂 base64 文本),而是被 buildArtifactFilePart
			//     识别后返回 false;这里同时记录元数据(路径 + mime + size)给 LLM 看
			//
			// 用户核心需求 (2026-08-03 18:xx 再次确认):
			//   1. LLM 不需要看到图片字节——妥协: Ark 支持 image_url (不发报错),LLM token 浪费但能用
			//   2. 前端**必须能看到图**——通过 SSE multi_content 推 base64 → <img src="data:...">
			//   3. 推送给前端的必须是二进制图片内容 (不是文件路径)
			//
			// === 2026-08-03 重构: Codex CLI view_image 模式 ===
			//
			// 设计:
			//   1) 工具产物 /tmp/xxx.png → 复制到 attachment.Store → 生成 URL
			//      (URL 字符串进 LLM 上下文 + 浏览器 <img src=URL> 直接 fetch)
			//   2) LLM 上下文**完全不接触** base64,节省 token + 避免 Ark 报错
			//   3) 浏览器原生 fetch 真的图片,所见即所得(不是 LLM 转录的失真版本)
			//
			// 历史 (2026-08-03):
			//   - 16h: text/* 过滤(.mmd/.drawio 等被识别成 application/octet-stream,过滤不命中)
			//   - 17h: image/png 仍被识别为 image/png,过滤仍然不命中,317KB base64 进 LLM 上下文
			//   - 17h 18:xx: ToolPartTypeImage 替代 File(LLM 看到但不再报错)
			//   - 17h 19:xx: 当前 PR ——  完整 Codex CLI 模式,LLM 只看到 URL 字符串
			//
			// 兼容: 如果 ctx 没注入 attachment.Store (单测或老调用路径),
			//       退回到"元数据 + ToolPartTypeImage"模式,让 LLM 至少能看到信息。
			store := attachment.GetFromCtx(ctx)
			artifacts := detectCommandArtifacts(input.Command)
			var artifactMetas []string
			for _, p := range artifacts {
				info, err := os.Stat(p)
				if err != nil || info.Size() == 0 || info.Size() > 2*1024*1024 {
					continue
				}
				mime := guessArtifactMIME(p)

				// === 优先 // === 2026-08-03: IPC 通道模式 (LLM 上下文 **完全不接触** 图片字节) ===
				//
				// 设计:
				//   - 工具产物 /tmp/xxx.png → attachment.Store → 生成 URL
				//   - URL 通过 ctx 里的 IPC channel 推 main.go 的 SSE handler
				//   - ToolResult 文本只写元数据(size/mime/path), **不** 推 image part
				//   - LLM 完全看不到 URL/base64 → 节省 token + Ark 完全不接触 URL
				//   - 前端从 attachment SSE event 拿 URL → <img src=URL>
				//
				// 优势 vs Codex 模式(URL 进 LLM):
				//   - 不依赖"Ark 接受内网 URL"(开发环境 Ark disallow 172.x.x.x)
				//   - LLM 不接收 URL/base64 → 节省 token (200KB PNG 不再吃 270KB 上下文)
				//   - 浏览器请求走 origin,后端校验 token 即可
				//
				// 链路:
				//   - LocalCommandAgent 工具 wrapper: ctx 注入 ipc.SessionCh
				//   - 工具 wrapper: ipc.Publish(ctx, meta) → 写 channel
				//   - main.go session goroutine: 从 channel 读 → 发 SSE attachment event
				//   - 前端: appendMultiPart 收到 attachment → <img src=URL>
				if store != nil {
					_, publicURL, absoluteURL, err := store.Register(p, mime, filepath.Base(p))
					if err == nil {
						// === 关键: 通过 IPC channel 推送给前端,LLM 完全不接触 ===
						//   - ctx 里的 channel 直接写到 main.go session goroutine
						//   - 完全 eino ChatModelAgent 框架外
						//   - LLM 推理上下文没 image part,没 URL,没 base64
						ipc.PublishAttachment(ctx, ipc.AttachmentEvent{
							Type:         classifyAttachmentType(mime),
							URL:          publicURL,   // 前端 fetch 用的相对 URL
							AbsoluteURL:  absoluteURL, // 备用:publicBaseURL 配置时完整
							MIMEType:     mime,
							Size:         info.Size(),
							Name:         filepath.Base(p),
							OriginalPath: p,
							Source:       "local_command",
						})
						// 文本元数据(LLM 看到)— **不**含 URL,只含 size/mime/path
						// 让 LLM 知道"产物已生成",但不去 fetch URL
						artifactMetas = append(artifactMetas,
							fmt.Sprintf("- %s (mime=%s, size=%d bytes, name=%s)",
								p, mime, info.Size(), filepath.Base(p)))
						log.Printf("[local_command artifact] IPC mode: path=%s size=%d mime=%s (LLM will NOT see URL)",
							p, info.Size(), mime)
						continue
					}
					// 注册失败 (盘容满 / 权限) → 降级到只写文本元数据
					log.Printf("[local_command artifact] store.Register failed path=%s err=%v; pure metadata fallback",
						p, err)
				}

				// 兜底: ctx 没注入 store (单测 / 老调用路径) → 仅写文本元数据
				artifactMetas = append(artifactMetas,
					fmt.Sprintf("- %s (mime=%s, size=%d bytes)", p, mime, info.Size()))
			}
			if len(artifactMetas) > 0 {
				if store != nil {
					result2.Parts[0] = schema.ToolOutputPart{
						Type: schema.ToolPartTypeText,
						Text: textPart + "\n\n产物文件已生成 (前端已收到 attach URL, **严禁** 重复 base64/转 data URL/再次 local_command 转码):\n" +
							strings.Join(artifactMetas, "\n"),
					}
				} else {
					result2.Parts[0] = schema.ToolOutputPart{
						Type: schema.ToolPartTypeText,
						Text: textPart + "\n\n产物文件已生成 (前端已收到 attach URL, **严禁** 重复 base64/转 data URL/再次 local_command 转码):\n" +
							strings.Join(artifactMetas, "\n"),
					}
				}
			}
			return result2, nil
		},
	)
	if err != nil {
		log.Fatal(err)
	}

	a, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:        "LocalCommandAgent",
		Description: "本机命令执行 agent：唯一有权调用 local_command 工具，负责在受限沙箱中执行主机 bash 命令（系统查询、文件查看、网络诊断、项目代码 review、用户授权后的安装/写操作等）。能读项目目录代码做架构 review，能跑 gh CLI 提交 issue。**不**擅长闲聊、概念解释、天气查询等非命令执行类请求。",
		Instruction: `你是 yichouchou_claw 的"本机命令执行 agent"。RouterAgent 会把"涉及主机 bash / 操作系统命令"的请求转给你处理。

========================================
【零、你可以使用的工具清单】
========================================
(2026-08-03 修复: 之前的 prompt 误导你"能读网络"但实际 tools 列表没 web_search,
 现在你**真正**拥有以下工具,LLM 端看到的工具列表与下面清单一致)

- local_command: 在受限沙箱中执行单条 bash 命令,返回 stdout / stderr / 退出码 / 耗时。
  命令可用范围受 workdir/config/exec-approvals.json + 内置危险规则约束。
- web_search: 联网搜索工具(**由 Anthropic/Minimaxi 服务端执行**,不是你手动执行)。
  适用场景: 用户问"基于公开资料 / 找最新资料 / 查实时信息"等需要联网的子任务。
  调用方式: 直接 output tool_call web_search(query=...) — 服务端会返回结果到模型响应中。
- memory_search: 检索 workdir/memory/ 索引中的历史对话/trace 条目。
  适用场景: 用户提到"之前/上次/以前"或者需要历史决策依据时。
- skill: 加载 skill 文档(例如 drawio / network_diagnosis)按规范执行。
  适用场景: 图表类任务 / 复杂结构化诊断流程。

⚠️ 重要:
1. 工具描述你**有**的就是真的有,不要再 fallback 说"这个环境没有 web_search"。
2. 复合任务(例如"画个流程图 + 基于公开资料")的**统一处理流程**:
   a) 先 web_search 搜公开资料 → 服务端返回结果
   b) 综合形成文字总结
   c) 再 skill drawio + local_command 写源文件 + 调 drawio CLI 导出 PNG
3. 不要在 summary 之前直接画图,文字总结必须先做。

========================================
【一、能力边界】
========================================
- ✅ 通过 local_command 工具在受限沙箱内执行 bash 命令 (查询 / 调试 / 安装 / 配置等)。
- ❌ 不擅长闲聊、概念解释、文档翻译; 这类请求应转给 ChatAgent。
- ❌ 没有 transfer_to_agent 能力; 不要试图把任务转回 RouterAgent 或其他 agent。
- ❌ 不要伪造"用户已授权"的假象来绕过沙箱。

========================================
【二、沙箱规则 — 单一真相源】
========================================
**不要在 prompt 里记命令白名单 / 黑名单 / 授权关键词** — 全部由:
- workdir/config/exec-approvals.json (白名单)
- 沙箱内部 DangerousPatterns / 软硬禁止规则
- AuthorizationMiddleware (用户自然语言授权识别)
实时决定。本节**只**描述行为准则, 不描述具体规则。

行为准则:
- ❌ 不要擅自把软禁止命令改写成"看起来等价但绕过沙箱"的形式 (e.g. apt install → python -m subprocess 调 apt)。
- ❌ 不要在用户没有授权时反复重试同一命令。
- ❌ 不要伪造"用户已授权"的假象来绕过沙箱。
- ❌ 永远不要为了绕过沙箱而重新表述 / 编码 / 拆分软禁止命令。
- ✅ 当命令被拦截, 把 stderr 的 [授权提示] / [拒绝原因] / [等价命令提示] **原样**转给用户, 不要自己改写。
- ✅ 用户授权后, 下一轮直接执行命令即可, **不要**再让用户重复授权。
- ✅ 不要把任务退回 ChatAgent; 本 agent 内的命令问题**本 agent 内解决**。

========================================
【三、何时调用 local_command】
========================================
用户请求涉及"在本机上做点什么" (查状态 / 跑测试 / 改配置 / 装软件 / 删文件 / 调试网络) → 必须调用 local_command。
用户问"怎么..."(教学/概念问题) → 文字说明即可, 不要执行。

========================================
【四、写文件: heredoc 模式 (沙箱内唯一稳定的写文件路径)】
========================================
LLM 之前栽过的坑 (反复触发沙箱拦截, 9 分钟耗光): base64 编码 → python 写文件 → tee 写文件。
**唯一稳定**的写文件模式:
  cat > /tmp/<file> << 'EOF'
  <任意内容,含 XML / HTML / drawio / JSON / base64 字面量等>
  EOF

关键:
- **必须**用 << 'EOF' (单引号), 不是 << EOF。单引号告诉 shell 不展开 $VAR / 不转义反斜杠。
- 触发词 'EOF' **单写一行** (不带前导空格 / 后缀)。
- 内容里**可以**含任何字符; 沙箱对 heredoc body 不做关键字扫描。
- 一次性写 30KB+ 内容都没问题, 沙箱按行检查硬禁止。

========================================
【五、产物附件: 严禁重复处理】
========================================
local_command 跑出产物文件 (.png / .jpg / .pdf / .drawio / 任意), 后端**已经自动**:
- 把文件注册到 attachment.Store
- 通过 SSE attachment event 推 URL 给前端 (前端用 <img src=URL> 直接渲染)
- LLM 上下文**绝不**接收 base64 (节省 token)

所以你 (LLM) **绝对不要**:
- ❌ 用 base64 / xxd / od 转码产物 → 只是把图片转回字符串, 前端无法渲染
- ❌ 在文字回复里写 markdown 格式的 base64 → markdown 渲染 base64 失败
- ❌ 再次 local_command 把产物 cp 到共享目录 / push 到 OSS / curl 上传
- ❌ 在最终回复里复述 URL 字符串或 base64 内容
- ✅ 正确行为: 只输出简短文字元数据 (如 "产物: /tmp/xxx.png (image/png, 248KB)"), 让用户去产物面板看。

========================================
【六、错误处理 (行为准则, 不列具体规则)】
========================================
- 命令被拦截: 把 stderr 的 [授权提示] / [拒绝原因] / [等价命令提示] 原样转给用户。
- 命令非 0 退出: 阅读 stderr 定位真实错误, 不要盲目重试。
- 多次失败 (本 agent 内 ≥ 2 次同类错误): 主动告知用户"该路径在当前沙箱下不可行", 给出替代方案, **不要**把任务退回 ChatAgent。
- 拿到用户授权后**禁止重复检查** (which / command -v), 直接执行命令。
- 一次性给出 1-3 个安装/替代方案 + 请求授权, **不要**多轮询问。
- **网络访问失败时**: 沙箱代码层面没有任何网络限制, 不要直接说"沙箱限制网络"。按 3 步排障:
  1) 查 DNS / curl -v 看具体哪一步失败
  2) 查代理 (git config / $HTTPS_PROXY 等环境变量)
  3) 对比测试 (curl 不同域名) 区分"全部不通"还是"仅某域名不通"
  根据诊断引导用户, 不要凭空猜"网络被沙箱禁了"。

========================================
【七、输出风格】
========================================
- 中文问题用中文, 英文问题用英文 (LanguageConstraintMiddleware 强制)。
- 命令结果先给结论再贴原始输出, 不要把大量噪音直接堆给用户。
- 长输出用 markdown 代码块包裹, 标注命令类型。
- 不要重复用户问题, 不要用"当然 / 很乐意"之类的客套开头。

========================================
【强约束】
========================================
- 你**看不到** transfer_to_agent 工具 (框架已用 WithDisallowTransferToParent 移除)。
  如果训练惯性让你输出 "transfer_to_agent(...)", 框架会报 "[NodeRunError] tool not found",
  **这条错误不可恢复, 必须立即停止重试, 转为文本回答用户**。
- **不要试图转回 RouterAgent / ChatAgent** — LocalCommandAgent 没兄弟 agent,
  任何"转出"需求都改成"文本回答用户"即可。`,
		Model: model.NewChatModel(),
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				// 2026-08-03 修复: LocalCommandAgent 持有 web_search + memory_search
				//   - web_search: 复合任务(如"画图 + 基于公开资料总结")需要先联网搜索
				//   - memory_search: 跨 transfer 时 LocalCommandAgent 也能查历史决策
				//   - 之前只挂 localCmdTool,LLM 看到 tools 列表里没有 web_search 就会丢弃
				//     "联网搜索"环节,直接 fallback 到画图,缺失文字总结
				Tools: []tool.BaseTool{localCmdTool, sharedWebSearchTool(), sharedMemorySearchTool()},
				// 2026-08-04 修复: 加 UnknownToolsHandler 防 LLM 幻觉调用 transfer_to_agent / read_file
				//   等不在本 agent 注册的工具名。之前会触发 [NodeRunError] tool X not found in
				//   toolsNode indexes, 整个 run 崩溃。现在 handler 返回明确文本, LLM 下一轮
				//   会用 local_command 等价 shell 命令重做, 任务继续。
				UnknownToolsHandler: localCommandUnknownToolHandler,
			},
		},
		Handlers: append(append(append([]adk.ChatModelAgentMiddleware{
			messagehandler.NewLanguageConstraintMiddleware(),
			messagehandler.NewAuthorizationMiddleware(),
			messagehandler.NewRetryHintMiddleware(),
			localCmdDynamicRecentMw,
		}, func() adk.ChatModelAgentMiddleware {
			if store != nil {
				return session.NewPersistMiddleware(store)
			}
			return nil
		}()), skillMw), extraHandlers...),
		// MaxIterations 从 application.yml → agent.per_agent_max_iterations.local_command
		// 读取,fallback 到 agent.max_iterations,再 fallback 到 eino 默认 20。
		// 本机排障场景经常需要 7+ 步（DNS → 代理 → 测 github → 测 baidu →
		// 换镜像 → 重试），20 不够，默认 50 留足余量。
		// 调小时务必同步调整 sandbox 总超时。
		MaxIterations: config.GetMaxIterationsFor("local_command"),
	})
	if err != nil {
		log.Fatal(err)
	}
	return a
}

// 不再持有 local_command 工具——所有"主机命令执行"类请求由 RouterAgent
// 直接委派给 LocalCommandAgent 处理；ChatAgent 只负责闲聊、概念解释、
// 技术方案讨论、代码 review、文档整理、翻译等不需要执行命令的任务。
func NewChatAgent(ctx context.Context, skillsDir string, store *session.Store, extraHandlers ...adk.ChatModelAgentMiddleware) adk.Agent {
	// 构造 skill 中间件（skillsDir 下属的 chat/ 目录里的 skill 列表会被加载）。
	// 涵盖 general_chat（闲聊风格）等。code_review skill 已迁到
	// LocalCommandAgent 下，因为代码 review 任务通常需要读项目目录代码，
	// 必须由 LocalCommandAgent 用 local_command 工具读代码后完成。
	skillMw, err := buildSkillMiddleware(ctx, filepath.Join(skillsDir, "chat"))
	if err != nil {
		log.Fatalf("ChatAgent skill middleware: %v", err)
	}

	// web_search + memory_search 共享工具 (2026-08-03 改为共享版本,LocalCommandAgent 也复用)
	//   - 共享是包级别函数,避免每个 Agent 重复声明
	//   - 实际功能没变,只是改成共享
	webSearchTool := sharedWebSearchTool()
	memorySearchTool := sharedMemorySearchTool()

	// ==== D 方案: 自动注入最近记忆到 system prompt ====
	// 参考 Claude Code 的 CLAUDE.md 机制: 模型无需主动调 memory_search，
	// 直接在 system prompt 里看到最近 N 条历史。提升触发率的最大杠杆点。
	//
	// 配置从 config.GetRecentMemoryConfig() 读取(可由 application.yml 配置)。
	// 不再 hardcoded 默认值,完全由配置文件控制。
	recentCfg := config.GetRecentMemoryConfig()
	var recentBlock string
	if !recentCfg.Enabled {
		// 配置关闭 → 不注入任何 recent_memory 块,保持极简 system prompt
		recentBlock = ""
	} else {
		recentBlock = memorytool.RecentMemoryBlock(memorytool.RecentMemoryConfig{
			Enabled:     true,
			Limit:       recentCfg.Limit,
			MaxTokens:   recentCfg.MaxTokens,
			KindsFilter: recentCfg.KindsFilter,
			MaxAgeDays:  recentCfg.MaxAgeDays,
		})
		if recentBlock == "<recent_memory>\n\n</recent_memory>" {
			// 索引未启用 或 无最近记忆 → 给一个轻量占位说明
			recentBlock = "<recent_memory>\n（长期记忆索引未启用或暂无最近对话；如需历史上下文,请调用 memory_search 工具）\n</recent_memory>"
		}
	}
	// 动态 recent block 中间件：每次 ChatModel 调用前重新生成,保证 transfer 后
	// 子 agent 也能看到最新 recent memory(修复上下文断层)。
	chatDynamicRecentMw := newDynamicRecentMemoryMiddleware(recentCfg)

	a, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:        "ChatAgent",
		Description: "通用对话 agent：日常闲聊、通用知识问答、技术方案讨论、概念解释、代码 review、文档整理、翻译。**不**执行任何主机命令——命令执行类请求由 LocalCommandAgent 处理。",
		Instruction: `你是 yichouchou_claw 的"通用对话助手"。

========================================
【一、能力边界】
========================================
- ✅ 闲聊、概念解释、方案对比、代码 review、文档整理、翻译
- ✅ 技术讨论（不实际执行命令，只给思路/代码示例）
- ✅ 检索 workdir/memory/ 历史对话：用户提到"之前/上次/以前"或需要历史决策依据时，主动调用 memory_search 工具
- ✅ 联网搜索（web_search 工具，结果由服务端返回）
- ❌ **没有执行主机命令的能力**——所有"跑一下命令"、"查一下系统状态"、"装个软件"等请求
  都应通过 RouterAgent 转给 LocalCommandAgent
- ❌ **没有产出图片的能力**——所有"画流程图 / 出 PNG / 渲染图表"类请求必须转 LocalCommandAgent
  （详见【二】）
- ❌ 没有 transfer_to_agent 能力；不要试图调用任何形式的转出工具

========================================
【二、图表类请求必须拒绝 + 引导用户重路由】
========================================
**这是最容易踩坑的地方**——LLM 训练数据里大量 Mermaid / PlantUML 源码示例，看到"画流程图"
会本能想"我可以写 Mermaid 给你"。**但这是错的**：

- ❌ 你不能调用 drawio / mmdc / dot / plantuml 等任何渲染工具——这些都不在你工具集
- ❌ 你不能读 /uploads/xxx.png / 不能 base64 编解码图片 / 不能 push SSE multi_content
- ❌ 就算你写出完美的 Mermaid 源码，用户也收不到图——前端不会自动渲染源码
- ✅ 唯一能让用户拿到图的路径：让 RouterAgent 转 LocalCommandAgent，由它写源文件 + 调
  本机 drawio CLI 导出 PNG + 走 SSE multi_content 推前端

【用户消息含这些组合时，明确告知用户该走 LocalCommandAgent】

图表类关键词：画 / 绘制 / 流程图 / 架构图 / 时序图 / 状态图 / ERD / UML / 类图 / 数据库图 /
draw.io / drawio / mermaid / PlantUML / graphviz / 思维导图 / org chart / 拓扑 / 框图

导出/产出动词：导出 / 发给 / 发我 / 下载 / 给我 / 保存 / 输出 / PNG / SVG / PDF / 图片 / 截图

**触发时回复模板**：
"画图表/导 PNG 这类任务需要调用本机的 drawio CLI，**我（ChatAgent）没有 local_command 工具，
做不了图片产物**。请重新发消息，我会让 RouterAgent 转给 LocalCommandAgent 处理——它会写源文件
+ 调 drawio 导出 PNG + 直接把图推到你浏览器。"

【反例——这些情况你仍然可以正常回答】
- "解释一下微服务架构的流程"（纯文字描述） → 正常答
- 用户在对话里贴了图，要求"帮我看下哪里有问题" → 正常答（用视觉理解能力或请用户口述）
- "用什么工具画流程图好" → 答：推荐 drawio / mermaid-live
- 用户说"给我一份 Mermaid 源码就行，我自己渲染" → 可以给源码（**前提：用户明确接受**）

【绝对禁止】
- ❌ 主动给出"可复制粘贴的 Mermaid 源码 + 复制到 mermaid.live 渲染"这种"假完成"回复
  ——用户问的是"画图发给我"，源码不满足需求
- ❌ 假装"已生成图"，编造任何图片 base64 / data URL
- ❌ 写 .drawio XML / .mmd 文件到磁盘但声称"图已生成"——磁盘文件用户看不到

========================================
【三、何时该把请求转给 LocalCommandAgent】
========================================
如果你收到（无论是 RouterAgent 转来的，还是本不该到你这里的）这类请求，**直接用文字告知用户**
应该由 LocalCommandAgent 处理：
- "看下磁盘 / 内存 / CPU / 进程" → "请稍等，我让 LocalCommandAgent 帮你查"
- "跑一下 go test" → "这类执行类请求我会路由到 LocalCommandAgent"
- "帮我装个 nginx" → "需要执行安装，建议路由到 LocalCommandAgent 并先获取 Install 授权"
- "删除某个文件" → "涉及写操作，需要 Bash 授权，请通过 LocalCommandAgent"
- "画个 xxx 流程图" → "图表生成类任务，必须路由到 LocalCommandAgent，详见【二】"

不要假装执行，也不要给出一份"假装执行"的输出。所有真实命令执行交给 LocalCommandAgent。

========================================
【四、对话风格】
========================================
- 中文回答时用中文，英文问题用英文（由 LanguageConstraintMiddleware 强制）
- 技术回答尽量给可运行的代码片段，并标注语言/框架
- 不要重复用户问题，不要用"当然 / 很乐意"之类的客套开头
- 概念解释要简洁，必要时给类比

========================================
【五、长期记忆检索】
========================================
"之前 / 上次 / 以前 / 为什么用 X / 项目规则 / 我们做过什么 / 之前怎么解决的"
等提问, 必须先调 memory_search 工具, **严禁凭训练数据猜测**。
- query 用核心名词 2-4 词最有效; "我们聊过什么" 时 query 留空(自动注入 7 天窗口)
- 拿到命中列表后**再**用 Read 读原始 markdown 获取完整上下文
- 0 命中不代表"没聊过", 试更短或换关键词
详见 memory_search 工具描述的反模式章节, 不在这里重复。
` + recentBlock + `

【强约束】
- 你没有可以转出的 sub-agent，**严禁**调用 transfer_to_agent 或任何形式的转出工具
- 不要尝试委派任务给其他 agent（RouterAgent 会负责路由，你不需要再转移）
- 不要伪造"已执行"的输出（包括画图——见【二】）
- 不要在 ChatAgent 里假装做了命令执行；如需执行，明确告诉用户会路由到 LocalCommandAgent
- 当用户询问实时信息（新闻、天气、股价、最新事件）时，可以使用 web_search 工具联网搜索
- 用户要求"画图 + 发给我"：**永远不**给出"假完成"回复，永远引导重路由到 LocalCommandAgent`,
		Model: model.NewChatModelForChatAgent(),
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				// 注册 web_search 占位工具，让 eino ToolNode 能找到对应的执行入口。
				// 实际执行由 Anthropic/Minimaxi 服务端完成。
				Tools: []tool.BaseTool{webSearchTool, memorySearchTool},
			},
		},
		Handlers: append(append(append([]adk.ChatModelAgentMiddleware{
			messagehandler.NewLanguageConstraintMiddleware(),
			chatDynamicRecentMw,
		}, func() adk.ChatModelAgentMiddleware {
			if store != nil {
				return session.NewPersistMiddleware(store)
			}
			return nil
		}()), skillMw), extraHandlers...),
	})
	if err != nil {
		log.Fatal(err)
	}
	return a
}

// NewRouterAgent 创建最外层 ChatModelAgent，并通过 Handlers 字段挂载 session.PersistMiddleware。
// 这是一个 eino 原生方案：AfterAgent 钩子会自动拿到 SDK 内部维护的完整 messages
// （包含 assistant(tool_calls) ↔ tool(result) 完整 id 对齐），无需手动从 SSE 推断。
//
// RouterAgent 可转移的子 agent：
//   - ChatAgent：闲聊、通用问答、技术讨论
//   - WeatherAgent：查天气
//   - LocalCommandAgent：执行主机 bash 命令（含沙箱授权）
//
// 注意：RouterAgent 同时持有 memory_search 工具，并自动注入"最近记忆"到
// system prompt。原因：路由前需要先理解上下文（用户经常问"刚才那个 XX 是什么"、
// "上一次装的包是啥"），必须先调 memory 检索历史再判断委派。
func NewRouterAgent(store *session.Store, extraHandlers ...adk.ChatModelAgentMiddleware) adk.Agent {
	// memory_search 工具：路由前先读历史,避免在没有上下文时反问用户。
	memorySearchTool, err := memorytool.NewSearchTool()
	if err != nil {
		log.Fatalf("RouterAgent memory_search tool: %v", err)
	}

	// ==== 自动注入最近记忆到 RouterAgent 的 system prompt ====
	// 关键修复(2026-07-29)：之前的版本在 NewRouterAgent() 构造时把 recentBlock
	// 写死到 Instruction 里,导致:
	//   1. 服务重启后,新会话第一条 user_message 之前的 recent memory 还是
	//      "启动那一刻"的内容(若重启前最后一条对话还没被 refine/落盘就漏掉)
	//   2. 同一会话连问多轮,recentBlock 不会更新
	// 改进:让 recentBlock 仍作为 Instruction 的静态部分(提供基础上下文),
	// 再**额外**注册一个 BeforeModelRewriteState middleware,在每次
	// ChatModel 调用前把"最新"recent memory 拼到当前 user message 前面,
	// 保证 RouterAgent 看到的"最近记忆"始终是最新视角。
	recentCfg := config.GetRecentMemoryConfig()
	var recentBlock string
	if !recentCfg.Enabled {
		recentBlock = ""
	} else {
		recentBlock = memorytool.RecentMemoryBlock(memorytool.RecentMemoryConfig{
			Enabled:     true,
			Limit:       recentCfg.Limit,
			MaxTokens:   recentCfg.MaxTokens,
			KindsFilter: recentCfg.KindsFilter,
			MaxAgeDays:  recentCfg.MaxAgeDays,
		})
		if recentBlock == "<recent_memory>\n\n</recent_memory>" {
			recentBlock = "<recent_memory>\n（长期记忆索引未启用或暂无最近对话；如需历史上下文,请调用 memory_search 工具）\n</recent_memory>"
		}
	}
	// 动态 recent block 中间件:每次调用前重新生成。
	dynamicRecentMw := newDynamicRecentMemoryMiddleware(recentCfg)

	a, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:        "RouterAgent",
		Description: "一个智能任务路由器，负责将任务分配给其他专家 agent。",
		Instruction: `你是 RouterAgent，**唯一的职责**是把任务分给最合适的专家 agent。

========================================
【⚠️ HARD RULE #0：你能且只能使用这两个工具】
========================================
✅ transfer_to_agent(agent_name=...)   ← 路由
✅ memory_search(query=..., limit=...)   ← 查历史

❌ 其它任何工具——local_command / web_search / skill / get_weather /
   read_file / write_file 等都属于子 agent，RouterAgent 没注册，调用就
   触发 "tool XXX not found in toolsNode indexes"。

如果你不小心调了这种工具，**不要重试**，立刻在文字回复里说明"该工具属于子 agent Y，
已转 Y 处理"，然后发起 transfer_to_agent。

可用的专家 agent：
- ChatAgent：日常闲聊、通用知识问答、技术方案讨论、澄清式追问、代码 review、文档翻译。
  **不**执行任何命令。技能：general_chat。
- WeatherAgent：查询指定城市天气，调 get_weather 工具。
- LocalCommandAgent：在受限沙箱内执行主机 bash 命令（系统查询、日志查看、网络诊断、
  安装/写操作、**图表渲染**等）；需要用户授权的写操作由它负责交互。
  技能：system_diagnosis / git_operations / network_diagnosis / **drawio**（2026-08-03 移入）。

========================================
【#1 路由前先理解上下文】
========================================
在判断"这条消息转给谁"之前，先理解用户问什么。两种手段：

(a) **下方 <recent_memory> 块**：系统已自动注入最近 7 天的对话摘要。
    这是事实来源，不要凭训练数据猜测。

(b) **memory_search 工具**：仅当 recent_memory 窗口太短/太旧，或用户明确要求
    "翻 memory / 之前怎么做的" 时主动调。

【绝对禁止】
- 不查 memory 就对模糊短问反问用户"请提供具体命令"。
- 仅凭当前 query 路由，忽略上文。

========================================
【#2 transfer 必须显式复述上下文（修复上下文断层）】
========================================
eino 的 transfer_to_agent 工具签名是固定的（一个 agent_name 字段），**无法带自定义 payload**。
被 transfer 过去的子 agent 看不到 recent memory，只看到你整理后的"任务陈述"。

因此**你在 transfer 之前必须在文字回复里复述上下文**，否则子 agent 收到
"系统初始化，没有用户问题"，会反问澄清。

【标准工作流】
1) 先读 recent_memory：能推出上下文就够用。
2) 不够就调 memory_search：query 用核心名词定位（如 "pwd 替代命令"）。
3) 在工具调用前的文字回复里复述清楚："用户问的'它'指代的是 X" + 简述用户原话与上文的关联。
4) 然后才发起 transfer_to_agent。

【反模式】
- 直接 transfer_to_agent 不交代上下文。
- 调 memory_search 但不整合结果就 transfer。
- 反问用户"你指的是哪个"——永远先自己查。

========================================
【#3 路由判定规则（按优先级）】
========================================

### 3.0 图表/图表生成类任务（强专项，最优先）

⚠️ **本规则 = 最高优先级**，**先于 3.1 强语义判定**——
一旦命中下方"图表类关键词 + 导出/产出动词"组合，**直接转 LocalCommandAgent**，
不要再去匹配 3.1 的关键词。

【反例（**不**走本规则）】
- 纯文字描述图表："解释一下微服务架构的流程" → ChatAgent
- 已经在对话里贴过图片，要求"帮我看下哪里有问题" → ChatAgent
- 只问"用什么工具画流程图好" → ChatAgent

【命中条件】
- 用户消息含**任一**图表类关键词：画 / 绘制 / 流程图 / 架构图 / 时序图 / 状态图 /
  ERD / UML / 类图 / 数据库图 / draw.io / drawio / mermaid / PlantUML / graphviz /
  架构 / 拓扑 / 框图 / 思维导图 / org chart
- 且消息同时含**任一**导出/产出动词：导出 / 发给 / 发我 / 下载 / 给我 / 保存 / 输出 /
  PNG / SVG / PDF / 图片 / 截图

→ 命中后一律转 LocalCommandAgent。

【理由】图表生成本质是"写源文件 + 调渲染工具（drawio / mmdc / dot / plantuml 等）
+ 产物推给用户"。全链路需要 shell 命令，**ChatAgent 没有 local_command 工具，做不出
图片产物**。LocalCommandAgent 已加载 drawio skill（2026-08-03 移入），能直接处理。

【路由前必须显式复述上下文】——
⚠️ **只描述任务与用户原话**，**绝对不要在文字里写具体命令字符串**（如 which drawio、
drawio -x -f png 等）。原因：RouterAgent 自己没有 local_command 工具，如果
写了命令示例，LLM 会模仿输出 local_command 调用，触发 eino 报错。
正确写法：用"任务陈述 + 用户原话 + 期望产物格式"自然语言描述。

### 3.1 复合任务拆分（次优先）

消息同时含"分析 / review / 解释" + "提交 / 跑命令 / 创建 / 执行"等多动词时：

→ **统一转 LocalCommandAgent**——
  LocalCommandAgent 能用 local_command 工具读项目代码 + 加载 code_review skill 做架构
  review + 跑 gh issue create 等命令自动完成。
  ChatAgent 没有 local_command，转 ChatAgent 只能让它问"请把代码贴进来"——卡住。

【反例】
- 纯 review 提问："帮我看下 main.go 的设计思路" → ChatAgent（纯讨论）。
- 用户已贴代码/项目结构到对话里 → ChatAgent 做 review。

【MaxIterations 触顶防护】LocalCommandAgent 单次 ChatModel 输出禁止超过 5 个工具调用。
任务需看 10+ 文件时，拆"先 ls → 再 cat 关键文件 → 再综合 review"几轮，不要一次塞 20 个 cat。

### 3.2 强语义优先

按核心动词/名词判断归属：

- WeatherAgent: 天气 / 温度 / 下雨 / 湿度 / 下周天气
- LocalCommandAgent: 跑 / 装 / 删 / 查系统 / 看日志 / 跑测试 / git push / 提交 / debug /
  **画 / 绘制 / 渲染 / 导出图片 / 调 drawio / 调 mmdc / 跑 mermaid**
  （注：与 3.0 重复的图表类词——若同时含"导出动词"，归 3.0；否则按本条转）
- ChatAgent: 解释 / 方案 / review / 翻译 / 为什么 / 怎么理解

### 3.3 短问追问

用户消息 ≤ 8 汉字，或 follow-up 形式（"北京的呢？"、"那上海呢"、"然后呢"、"继续"）：

- 绝对不要直接反问。先看 recent_memory 能否推出上下文。
- 若上文在聊某命令 → 转 LocalCommandAgent 继续。
- 若上文聊某 topic → 转对应 ChatAgent / WeatherAgent 继续。
- recent_memory + memory_search 都查不到 → 走 ChatAgent 反问澄清。

严格禁止对无上文且措辞模糊的短问直接转 WeatherAgent 或 LocalCommandAgent。

### 3.4 地理孤词例外

用户只写一个地名（"西藏"、"新疆"、"上海"）且上文无法推出天气话题时：
→ 用 ChatAgent 给出简短反问澄清，不要直接跳到查天气。

### 3.5 无匹配

如果没有任何 agent 能处理，直接让 ChatAgent 回复"我无法处理这个请求"。

========================================
【#4 强约束】
========================================
- 不要重复发起 transfer_to_agent；一次请求最多一次路由。
  上一轮已经成功转给 LocalCommandAgent（tool result 含 "successfully transferred to agent"），
  当前轮你已经在 LocalCommandAgent 内执行后续动作，不需要再 transfer。
  再次调 transfer_to_agent，框架会因工具不可见报
  "[NodeRunError] tool transfer_to_agent not found"——整个 run 失败，必须避免。
- 不要在 instruction 中复述任何工具调用细节给用户听。
- 你自己不要回答业务问题；永远先把任务委派给最合适的 agent。
- LocalCommandAgent 处理完后用户继续追问命令执行相关内容 → 转回 LocalCommandAgent。
- ⚠️ 格式说明：本 Instruction 涉及示例时一律用代码风格（反引号标注工具名）描述，
  不要写裸 JSON，避免被 eino FString 模板解析。` + recentBlock,
		Model: model.NewChatModel(),
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				// 注册 memory_search 工具,让 RouterAgent 在路由前能主动检索历史。
				// 最近记忆通常已通过 recentBlock 自动注入,只在窗口不够时调。
				Tools: []tool.BaseTool{memorySearchTool},
				// 2026-08-04 修复: 加 UnknownToolsHandler 防 LLM 幻觉调 local_command / web_search 等
				//   "子 agent 工具"。RouterAgent 的工具集只有 memory_search (+ transfer_to_agent
				//   由 eino 隐式注册), LLM 偶尔会在试图"直接做事"时调出 local_command 等名字,
				//   触发 [NodeRunError] tool X not found in toolsNode indexes 硬错导致整个 run
				//   崩溃。现在 handler 返回明确"请改用 transfer_to_agent(agent_name=LocalCommandAgent)"
				//   提示, LLM 下一轮会主动 transfer, 任务继续。
				UnknownToolsHandler: routerAgentUnknownToolHandler,
			},
		},
		// 只在 RouterAgent 上注册 PersistMiddleware，让最外层 agent 在每次成功结束后
		// 把完整 messages 写入 store；子 agent（ChatAgent / WeatherAgent / LocalCommandAgent）不会触发。
		Handlers: append([]adk.ChatModelAgentMiddleware{
			session.NewPersistMiddleware(store),
			messagehandler.NewLanguageConstraintMiddleware(),
			messagehandler.NewAuthorizationMiddleware(),
			dynamicRecentMw,
		}, extraHandlers...),
	})
	if err != nil {
		log.Fatal(err)
	}
	return a
}

// dynamicRecentMemoryMiddleware 在每次 ChatModel 调用前重新生成 recent memory
// 块,并拼到当前 messages 末尾(user role)的开头。
//
// 为什么需要：NewRouterAgent 构造时算的 recentBlock 是 agent 启动时刻的快照,
// 对于长会话或服务刚重启的场景,会错过最新的 user_request。动态中间件保证
// RouterAgent 每次看到的"最近记忆"都是 Run 触发那一刻的实时视图。
type dynamicRecentMemoryMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	cfg config.RecentMemoryConfig
}

func newDynamicRecentMemoryMiddleware(cfg config.RecentMemoryConfig) *dynamicRecentMemoryMiddleware {
	return &dynamicRecentMemoryMiddleware{cfg: cfg}
}

func (m *dynamicRecentMemoryMiddleware) BeforeModelRewriteState(
	ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if !m.cfg.Enabled {
		return ctx, state, nil
	}
	if len(state.Messages) == 0 {
		return ctx, state, nil
	}
	// 取最后一条 user message,把 recent memory 块前置。
	// 原因：recent memory 是给"当前这一次"看的,不应该污染历史消息。
	for i := len(state.Messages) - 1; i >= 0; i-- {
		msg := state.Messages[i]
		if msg.Role != schema.User {
			continue
		}
		// 跳过已经注入过的(避免 iteration 重复加)
		if strings.HasPrefix(msg.Content, "<recent_memory_dynamic>") {
			return ctx, state, nil
		}
		block := memorytool.RecentMemoryBlock(memorytool.RecentMemoryConfig{
			Enabled:     true,
			Limit:       m.cfg.Limit,
			MaxTokens:   m.cfg.MaxTokens,
			KindsFilter: m.cfg.KindsFilter,
			MaxAgeDays:  m.cfg.MaxAgeDays,
		})
		if block == "<recent_memory>\n\n</recent_memory>" {
			block = "<recent_memory>\n（暂无最近对话）\n</recent_memory>"
		}
		state.Messages[i].Content = "<recent_memory_dynamic>\n" + block + "\n</recent_memory_dynamic>\n\n" + msg.Content
		break
	}
	return ctx, state, nil
}

// ==== 多模态产物文件检测 (2026-07-29 新增) ====
//
// 约定: local_command 工具执行后,如果命令"应当"产生产物(比如 pdftotext 生成
// .txt、matplotlib 生成 .png、pandoc 生成 .pdf),我们尝试在当前工作目录下扫
// 描最近生成的"已知产物类型"作为附件一并返回。
//
// 严格约束:
//   - 仅识别白名单扩展名(.png/.pdf/.txt/.md/.log/.html/.svg),不挂任意文件
//   - 单文件 <=2MB,避免撑爆上下文
//   - 只扫描命令字符串里明确出现的"输出路径";避免误把整个 cwd 当产物
//     (注:无法访问沙箱 cwd,只能从命令字符串里猜,所以是"启发式")

// artifactExtWhiteList 是会被作为产物附件的文件后缀白名单。
var artifactExtWhiteList = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".pdf":  true,
	".txt":  true,
	".md":   true,
	".html": true,
	".svg":  true,
	".log":  true,
}

// detectCommandArtifacts 从命令字符串里提取可能的产物文件路径。
//
// 启发式:
//   - 拆 token,识别 ">/" 或 "-o" 或 "tee" 后面的路径
//   - 识别 .ext 后缀的 token 本身
//   - 不递归 cwd; 只返回命令里"明示"的路径
//
// 2026-08-03 修复:
//   - 拒绝目录(用 os.Stat + isDir 校验,避免 `--output /tmp` 把 `/tmp` 目录当产物)
//   - 拒绝"无扩展名"路径(除非落在重定向 / -o / tee 解析后)
func detectCommandArtifacts(cmd string) []string {
	out := []string{}
	tokens := strings.Fields(cmd)
	if len(tokens) == 0 {
		return nil
	}

	// === 2026-08-04: read-only 命令白名单 ===
	//
	// 之前 bug: 任何 token 带 .png/.pdf 扩展都被当作"产物"。
	// 后果: `ls -lh /tmp/diagrams/star_market_2026.png` 这种**纯读取**
	//       命令, 把 star_market_2026.png 重新注册一次到 Store,
	//       并通过 IPC 推一个 attachment event 给前端 — 但前端此时
	//       还没创建 assistant message 容器, 渲染被 silently 丢;
	//       或者, 即便渲染成功, 也是同一文件在 Store 里注册多次
	//       (重复 token, 浪费内存 + 误推)。
	//
	// 修复: 命令第一个 token 是已知 read-only 命令时, **跳过 token-extension
	//       检测**; 只保留重定向 / -o / tee 三个显式产物通道(它们**真的**会写文件)。
	//
	// 关联修复: main.go 的 backfillArtifactAttachments 会在 LLM 文本里
	// 提到这类路径时, 由 Store.LookupByOriginal 主动补 push attachment。
	// 即便这里漏过, 前端最终也能拿到图。
	firstCmd := strings.ToLower(filepath.Base(tokens[0]))
	readOnlyCmds := map[string]bool{
		"ls": true, "cat": true, "head": true, "tail": true,
		"file": true, "stat": true, "du": true, "df": true,
		"find": true, "grep": true, "rg": true, "ag": true,
		"which": true, "whereis": true, "type": true,
		"echo": true, "printf": true, "pwd": true,
		"env": true, "uname": true, "whoami": true, "id": true,
		"date": true, "wc": true, "sort": true, "uniq": true,
		"diff": true, "cmp": true, "md5sum": true, "sha256sum": true,
		"tree": true, "realpath": true, "readlink": true,
		"xxd": true, "od": true, "hexdump": true,
		"jq": true, "yq": true, "xmllint": true,
	}
	isReadOnly := readOnlyCmds[firstCmd]

	for i, tok := range tokens {
		// 1) > / >> 重定向目标 (read-only 命令不会有这些 token,但安全兜底)
		if (tok == ">" || tok == ">>") && i+1 < len(tokens) {
			out = append(out, tokens[i+1])
		}
		// 2) -o / --output 参数
		if (tok == "-o" || tok == "--output") && i+1 < len(tokens) {
			out = append(out, tokens[i+1])
		}
		// 3) | tee <file>
		if tok == "tee" && i+1 < len(tokens) {
			out = append(out, tokens[i+1])
		}
	}
	// 4) 任何 token 自身是 .png/.pdf 等已知扩展 — 但只对**非 read-only** 命令生效
	//
	// 排除 read-only 命令:
	//   - `ls -lh /tmp/xxx.png` 不再把 xxx.png 当产物
	//   - `cat /tmp/xxx.png | base64` 不再把 xxx.png 当产物
	//   - `head -c 2000 /tmp/xxx.drawio` 不再把 xxx.drawio 当产物
	// 重定向 / -o / tee 通道仍然生效:
	//   - `echo "..." > /tmp/xxx.png` 仍然把 xxx.png 当产物
	//   - `drawio --output /tmp/xxx.png` 仍然把 xxx.png 当产物
	//   - `data | tee /tmp/xxx.log` 仍然把 xxx.log 当产物
	if !isReadOnly {
		for _, tok := range tokens {
			ext := strings.ToLower(filepath.Ext(tok))
			if artifactExtWhiteList[ext] {
				out = append(out, tok)
			}
		}
	}
	// 去重 + 简单路径过滤(避免 ../ / 绝对路径越界)
	// 2026-08-03: 拒绝目录(防止 --output /tmp 把 /tmp 当产物)
	uniq := map[string]struct{}{}
	cleaned := make([]string, 0, len(out))
	for _, p := range out {
		if strings.Contains(p, "..") || strings.HasPrefix(p, "/etc") || strings.HasPrefix(p, "/proc") || strings.HasPrefix(p, "/sys") {
			continue
		}
		// 校验 path 不是目录(常见误判: --output /tmp 把 /tmp 目录当产物)
		//   1) 路径必须存在
		//   2) 不能是目录(必须是个 regular file)
		if info, err := os.Stat(p); err == nil {
			if info.IsDir() {
				continue
			}
		}
		// 路径没扩展名 且 path tokens 都没出现/> -o / tee → 可能是路径而非产物
		//   (重定向/-o/tee 这几个分支显式 push 的,这里再放行)
		// 只要匹配任一产品 ext, 或 上述三种"显式"channel, 都保留。
		// 兜底: 无扩展名就只在重定向/-o/tee 几个显式分支里被加入,这里已经通过了。
		if _, ok := uniq[p]; ok {
			continue
		}
		uniq[p] = struct{}{}
		cleaned = append(cleaned, p)
	}
	return cleaned
}

// buildArtifactFilePart 把磁盘文件转成 ToolOutputPart 推给前端。
//
// 2026-08-03 重写: 关键修复——**用 ToolPartTypeImage 而不是 ToolPartTypeFile**。
//
// 之前的 bug:
//   - 用 ToolPartTypeFile → eino ToolNode 转 user message 时变 ChatMessagePartTypeFileURL
//   - Ark adapter 不支持 file_url,报 "unsupported chat message part type in user message: file_url"
//   - 修复历史: text/* 过滤(16h) → 仍漏 image/png(17h 17:23) → 全部禁用 file part(临时方案)
//
// 当前正确设计:
//   - image/* / video/* / audio/*  → ToolPartTypeImage / Audio / Video (Ark adapter 支持)
//   - text/*                       → 解码 base64 拼到 stdout text part(LLM 看内容)
//   - 其他(application/pdf 等)     → ToolPartTypeImage (Ark 走 image_url,前端渲染为附件)
//
// 文件到达前端的机制:
//   - ToolResult.Parts → eino ToolNode 转 user message multi_content
//   - handleRegularMessage (internal/message/streamMessageOutput.go) line 113-129:
//     扫描 msg.UserInputMultiContent (ToolPart 来源) → 转 SSE multi_content
//   - 前端 addToolResult 流程 → 在 assistant 消息下追加 <img>/<a>
//
// LLM 上下文 vs 前端,两条路径都拿到文件 base64——这就是你要求的"图片给前端,
// LLM 拿不到图片字节"(MiniMax-M3 是文本模型,即使拿到也是 token 浪费 + 视觉无法解析)。
//
// 限制: 单文件 ≤ 2MB, 防止单 tool_result 撑爆上下文。
func buildArtifactFilePart(path string) (schema.ToolOutputPart, bool) {
	const maxFileSize = 2 * 1024 * 1024
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 || info.Size() > maxFileSize {
		return schema.ToolOutputPart{}, false
	}
	mime := guessArtifactMIME(path)
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[local_command artifact] read failed path=%s err=%v", path, err)
		return schema.ToolOutputPart{}, false
	}
	encoded := base64.StdEncoding.EncodeToString(data)

	// === 关键: 用 ToolPartTypeImage 而不是 ToolPartTypeFile ===
	//
	// eino ToolNode 会把 ToolPartTypeFile 转成 ChatMessagePartTypeFileURL
	// (eino schema/tool.go line 535-541),Ark 不支持。
	//
	// 而 ToolPartTypeImage 转 ChatMessagePartTypeImageURL (line 498-507),
	// Ark adapter (chat_completion_api.go line 644) **支持**。
	//
	// 这样既能让 LLM 上下文拿到 image part(Ark 不报错),又能通过 SSE multi_content
	// 推前端(convertInputPartsToSSE 同样处理 image_url / file_url 两种 part)。
	//
	// 即使 MiniMax-M3 不真用图片内容,base64 占的 token 也是浪费——但至少不再报错。
	// 未来切到多模态 LLM,直接复用,无需改 schema。
	switch {
	case strings.HasPrefix(mime, "image/"):
		return schema.ToolOutputPart{
			Type: schema.ToolPartTypeImage,
			Image: &schema.ToolOutputImage{
				MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &encoded,
					MIMEType:   mime,
				},
			},
		}, true
	case strings.HasPrefix(mime, "audio/"):
		return schema.ToolOutputPart{
			Type: schema.ToolPartTypeAudio,
			Audio: &schema.ToolOutputAudio{
				MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &encoded,
					MIMEType:   mime,
				},
			},
		}, true
	case strings.HasPrefix(mime, "video/"):
		return schema.ToolOutputPart{
			Type: schema.ToolPartTypeVideo,
			Video: &schema.ToolOutputVideo{
				MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &encoded,
					MIMEType:   mime,
				},
			},
		}, true
	case strings.HasPrefix(mime, "text/"),
		strings.HasSuffix(strings.ToLower(filepath.Ext(path)), ".json"),
		strings.HasSuffix(strings.ToLower(filepath.Ext(path)), ".xml"):
		// text/* 类不进 ToolOutputPart(LLM 看不懂 base64)——
		// 让调用方自己解码拼到 stdout text part(详见 detectCommandArtifacts 处的循环)。
		return schema.ToolOutputPart{}, false
	default:
		// application/pdf 等其他二进制: 走 ToolPartTypeImage 让前端可下载/预览
		// (Ark adapter image_url 接受任意 mime,前端根据 mime 决定如何渲染)
		return schema.ToolOutputPart{
			Type: schema.ToolPartTypeImage,
			Image: &schema.ToolOutputImage{
				MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &encoded,
					MIMEType:   mime,
				},
			},
		}, true
	}
}

// guessArtifactMIME 把文件路径转成 MIME type。
//
// 2026-08-03 修复:
//   - 之前 switch 只列了 .png/.jpg/.gif/.pdf/.txt/.log/.md/.html/.svg
//   - 但 MMD/Mermaid 文件 (.mmd) 、drawio (.drawio/.xml) 、graphviz (.dot) 等
//     **文本类图表/标记格式** 都没列出来,被识别成 application/octet-stream,
//     导致 buildArtifactFilePart 的 text/* 过滤失效——base64 file_url part
//     仍被打包返回,触发 Ark 报错:
//     "unsupported chat message part type in user message: file_url"
//   - 现在改用 mime.TypeByExtension(查 /etc/mime.types,系统级文本 mime 兜底),
//     同时显式补一些图表/markup 扩展名(系统 mime.types 不一定全)。
//
// 返回值约定:
//   - text/*     → buildArtifactFilePart 会丢弃该 part(LLM 已有 stdout 内容)
//   - image/*    → 保留 base64 file part(LLM 看图 + 前端展示图片)
//   - 其他      → 一律丢弃(避免 LLM 上下文出现不可解析的二进制)
func guessArtifactMIME(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		// 文件无扩展名时,试一下用文件名前缀兜底
		ext = path
	}

	// === 1) 显式兜底表:drawio / mermaid / graphviz / markup 等系统 mime.types 不一定有的扩展名 ===
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".pdf":
		return "application/pdf"
	case ".svg":
		return "image/svg+xml"
	case ".txt", ".log":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".js":
		return "text/javascript"
	case ".json":
		return "text/plain" // JSON 文本,走 text/* 过滤
	case ".xml", ".drawio", ".mmd", ".mermaid", ".dot", ".gv", ".yaml", ".yml":
		return "text/plain" // 这些都是文本,走 text/* 过滤(避免 Ark 报错)
	case ".csv":
		return "text/csv"
	case ".py", ".go", ".rs", ".c", ".cpp", ".h", ".java", ".sh", ".bash":
		return "text/plain" // 源代码文本
	}

	// === 2) 兜底:查系统 mime.types (Linux 上 /etc/mime.types) ===
	// mime.TypeByExtension 对 "未注册的扩展名" 返回空字符串,
	// 我们用 "application/octet-stream" 作为最终兜底。
	if mimeType := mime.TypeByExtension(ext); mimeType != "" {
		return mimeType
	}
	return "application/octet-stream"
}

// classifyAttachmentType 把 MIME 类型转成前端附件渲染类型 (2026-08-03 IPC 模式新增)。
//
// 设计:
//   - image/* → AttachmentImage (前端 <img>)
//   - audio/* → AttachmentAudio (前端 <audio>)
//   - video/* → AttachmentVideo (前端 <video>)
//   - 其他  → AttachmentFile (前端 <a href> 下载)
//
// 与 guessArtifactMIME 的关系:  MIME 决定"渲染形式", 头部分类决定"前端怎么展示"。
func classifyAttachmentType(mime string) ipc.AttachmentType {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return ipc.AttachmentImage
	case strings.HasPrefix(mime, "audio/"):
		return ipc.AttachmentAudio
	case strings.HasPrefix(mime, "video/"):
		return ipc.AttachmentVideo
	default:
		return ipc.AttachmentFile
	}
}

// isSelfTransferInput 检测 LLM 调 transfer_to_agent(agent_name="LocalCommandAgent")
// 的"自传"情况 (input 是 JSON 字符串, 解析 agent_name 字段)。
//
// 2026-08-04 P2-3: 14h session 14:28:12 出现过 LocalCommandAgent
//
//	自己调 transfer_to_agent(agent_name="LocalCommandAgent") 的情况。
//	框架因 WithDisallowTransferToParent 拒绝, 但浪费 1 次 LLM 推理 + 1 次 tool_call。
//
// 实现: 用字符串扫描 (不引入 encoding/json 因为 input 格式可能不严格)。
// 启发式: input 里同时含 "agent_name" 和 "LocalCommandAgent" → 自传。
func isSelfTransferInput(input string) bool {
	if input == "" {
		return false
	}
	// 不区分大小写
	lower := strings.ToLower(input)
	if !strings.Contains(lower, "localcommandagent") {
		return false
	}
	// 必须含 "agent_name" 字段 (避免误把 LocalCommandAgent 出现在 content 里当成自传)
	if !strings.Contains(lower, "agent_name") {
		return false
	}
	return true
}
