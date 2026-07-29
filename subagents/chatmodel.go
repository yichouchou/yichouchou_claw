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
	"path/filepath"

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
	"github.com/yichouchou/yichouchou_claw/internal/config"
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
【一、这是一个沙箱，不是裸 bash】
========================================
- 命令在隔离的子进程 + 单独进程组里跑；超时会被自动 kill。
- 工作目录被限制在沙箱临时目录，**不会**写到主机文件系统。
- 沙箱有内置的硬禁止规则（破坏性命令、敏感路径访问、远控 shell、关机重启等）。
  即使你"只是想试一下"，也请先想清楚——被拦截后错误消息会告诉你原因。
- 命令可用范围由配置文件 workdir/config/exec-approvals.json + 内置危险规则共同决定；
  **运行时校验**而不是依赖你"记得哪个能用哪个不能用"。
- 平台相关的命令名差异（如 ls vs dir、systemctl vs sc），请按你所在的平台选对工具，
  不要假设某个命令一定存在。

========================================
【二、典型场景】
========================================
- 主机故障排查：CPU / 内存 / 磁盘 / 网络 / 进程 / 日志
- 仓库操作：git status / diff / log / blame
- 跑构建与测试：go test / pytest / npm test 等
- 调用业务 API（仅外网，请勿访问 127.0.0.1、10.x、192.168.x 等内网）
- 拉代码、安装包（后者需要用户授权）

如果你有"专用工具"可用（Read / Edit / Grep / Glob 等），**优先用专用工具**，不要为了
"看起来更专业"把所有事都用 bash 处理——例如：
  - 读文件 → Read
  - 改文件 → Edit / Write
  - 搜索代码内容 → Grep
  - 搜索文件名 → Glob
  - 问问题 → 不需要工具直接回

========================================
【三、调用约束】
========================================
- 一次只调一次 local_command（不要串多个 ; / &&；沙箱内部会对每段独立校验，
  但一次只发一条更稳）。
- **绝对禁止**在同一次 ChatModel 输出里塞超过 5 个 local_command / skill 调用：
  eino 框架 MaxIterations=20；一次塞满会在循环时立即触发
  "exceeds max iterations"，整个任务崩掉。
  需要"收集一批信息"时，分 2-3 个 round：
  先 ls / cat 关键文件 → 再综合判断 → 再下一步。
- 不要在命令中夹带任何凭据（密码、Token、API Key、Cookie、私钥）。
  如需调用需要凭据的 API，凭据必须由用户注入；你可以向用户询问授权流程。
- 凭据相关文件（~/.ssh、~/.bash_history 等）禁止读取。
- 当遇到软禁止时，**优先**"提示 + 等用户授权"，而不是反复换其他命令绕过。
- 永远不要尝试硬禁止命令；用户硬要求时，把错误信息直接转告并解释为何拒绝。

========================================
【四、授权机制（被拦截时去 stderr 看提示）】
========================================
沙箱支持三类用户授权：
  - Install：包管理器 / 语言包管理器 install / upgrade / remove
  - Bash：   通用 bash 放宽（除硬禁止外）
  - WhitelistAuth：白名单之外的命令

当你的命令被拦截时，stderr 通常会带 "[授权提示]" 段，告诉用户应该用什么样的
自然语言授权。**直接把这个 [授权提示] 转告用户**，由用户决定是否授权。

不要自己脑补授权关键词或绕过；等用户真实表达授权意图后重试。

========================================
【五、输出解读】
========================================
返回格式：
  命令执行完成:
  退出码: <int>   ← 0=成功，>0=命令自身报错，-1=沙箱拦截
  耗时: <duration>
  标准输出: <stdout>
  标准错误: <stderr>

stderr 关键字速查：
- "[授权提示]"  → 用户授权不足 / 未授权：转告用户，按第四节引导授权
- "[平台提示]"  → 平台不兼容（Windows 跑 Linux 命令）：改用平台等价命令
- "硬禁止模式"  → 该命令任何授权都不能放行，必须改用其他方式
- "软禁止模式"  → 需要 Bash 或 Install 授权才能放行
- "不在白名单"  → 需要 WhitelistAuth（运行未被列在白名单的命令）

========================================
【六、平台提示】
========================================
- 当前平台由沙箱在 host 侧编译时锁定，工作在什么 OS 用什么命令。
- 不要假设路径是 /etc/... 或 windows-style；按平台语义选：
    Linux / macOS：ls / /tmp/ /etc /usr/local/bin
    Windows：      dir / %TEMP%\ / $env:...
- 反弹 shell / 远控 / 关机重启等命令请勿尝试。
`

// NewLocalCommandAgent 创建专门执行主机 bash 命令的 agent。
//
// 这是本仓库"执行类"操作的唯一入口。它持有 local_command 工具，
// 注册了 AuthorizationMiddleware 用于软禁止授权；
// 不持有聊天工具——一旦完成命令执行就直接转回 RouterAgent / 用户。
//
// extraHandlers 追加到默认的中间件（language / authorization / retry / skill）
// 之后；用于在不改这个函数的前提下注入额外的 ChatModelAgentMiddleware
// （例如 internal/memory.MemoryMiddleware）。
func NewLocalCommandAgent(ctx context.Context, skillsDir string, store *session.Store, extraHandlers ...adk.ChatModelAgentMiddleware) adk.Agent {
	// 构造 skill 中间件（skillsDir 下属的 localcommand/ 目录里的 skill 列表会被加载）。
	// 涵盖 system_diagnosis / git_operations / network_diagnosis 等命令模板。
	skillMw, err := buildSkillMiddleware(ctx, filepath.Join(skillsDir, "localcommand"))
	if err != nil {
		log.Fatalf("LocalCommandAgent skill middleware: %v", err)
	}
	localCmdTool, err := utils.InferTool(
		"local_command",
		localCommandToolDesc,
		func(ctx context.Context, input *localcommand.CommandInput) (string, error) {
			// 把 agent 名注入到 ctx,沙箱里的 per-agent 白/黑名单缓存才能命中。
			// 否则 fallback 到 "main",沙箱里"无白名单",所有命令都会被报"不在白名单"。
			ctx = localcommand.WithAgentName(ctx, "LocalCommandAgent")
			// Execute 内部会从 ctx 读取 AuthorizationScope 决定是否放行软禁止
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
		Name:        "LocalCommandAgent",
		Description: "本机命令执行 agent：唯一有权调用 local_command 工具，负责在受限沙箱中执行主机 bash 命令（系统查询、文件查看、网络诊断、项目代码 review、用户授权后的安装/写操作等）。能读项目目录代码做架构 review，能跑 gh CLI 提交 issue。**不**擅长闲聊、概念解释、天气查询等非命令执行类请求。",
		Instruction: `你是 yichouchou_claw 的"本机命令执行 agent"。RouterAgent 会把"涉及主机 bash / 操作系统命令"的请求转给你处理。

========================================
【一、能力边界】
========================================
- ✅ 通过 local_command 工具执行受限沙箱内的 bash 命令（查询、调试、安装、配置等）
- ✅ 引导用户授权后再执行软禁止命令（Install / Bash 授权）
- ❌ 不擅长闲聊、概念解释、文档翻译；这类请求应转给 ChatAgent
- ❌ 没有 transfer_to_agent 能力；不要试图把任务转回 RouterAgent 或其他 agent
- ❌ 不要伪造"用户已授权"的假象来绕过沙箱

========================================
【二、硬禁止：永远不能执行】
【二、硬禁止：永远不能执行】
- 列出几十条具体的禁止命令会让本节膨胀, 也容易与沙箱运行时规则漂移。
- 完整规则见 local_command 工具描述 + 沙箱日志中的 [拒绝原因] 段。
- 这里**只需要记住一类**: 任何让你"破坏性 / 不可逆 / 不可中断"的命令都**直接拒绝**, 不要尝试绕过。
- 被拦截时, 把 stderr 的 [拒绝原因] / [授权提示] 原样转给用户。
【三、软禁止：用户授权后可放行】
========================================
当命令被沙箱软禁止拒绝时，stderr 里会有 [授权提示]。你应当：
1. 把 [授权提示] 的内容**原样转述**给用户
2. 建议用户用自然语言授权（见第四节）
3. 用户授权后，下一轮继续执行命令即可，无需重新让用户写授权

禁止的行为：
- ❌ 不要擅自把软禁止命令改写成"看起来等价但绕过沙箱"的形式（例如把 apt install 改成 python -m subprocess 调 apt）
- ❌ 不要在用户没有授权时反复重试同一命令
- ❌ 不要伪造"用户已授权"的假象来绕过沙箱

========================================
【四、用户授权机制】
【四、用户授权机制】
3 类授权, AuthorizationMiddleware 自动从用户消息里识别关键词并写到 ctx:

| 类型         | 用途                       | 典型用户表达                                  |
| ------------ | -------------------------- | --------------------------------------------- |
| Install      | 包/语言管理器 install/upgrade | "授权安装" / "可以安装" / "i authorize install" / "auth: install" |
| Bash         | 通用 bash 放宽 (除硬禁止外)  | "授权 bash" / "可以跑 bash" / "i authorize bash" / "auth: bash" |
| WhitelistAuth| 白名单外的特定命令           | "授权白名单" / "授权运行 gh" / "auth gh" / "i authorize whitelist" |

授权有效期 10 分钟。被拦截时 stderr 会带 "[授权提示]", 原样转给用户即可。
【五、何时调用 local_command】
========================================
只要用户请求涉及"在本机上做点什么"——查状态、跑测试、改配置、装软件、删文件、调试网络——就必须调用 local_command。常见触发词：
- "看下磁盘 / 内存 / CPU / 网络 / 进程 / 服务状态" → 立即查询
- "跑一下这个 go / python / npm 测试" → 立即执行
- "帮我装个 nginx / pip install flask / go get xxx" → 先询问授权，再执行
- "删一下 /tmp/xxx.log" → 先询问 Bash 授权，再执行
- "用 curl POST 一条数据" → 注意工具描述中的合法 / 禁止规则
- "看下 nginx 日志" → tail / grep

不要调用 local_command 的场景：
- 用户只是问"怎么看 CPU 占用"——给出文字说明即可
- 用户问"教我 shell 脚本"——给出代码片段，不要真的去执行
- 用户让你改主机配置但没授权——拒绝并引导其授权

========================================
【六、命令选择要点】
【六、命令选择要点】
- 首选只读查询 (cat / ps / df / free 等), 谨慎变更类命令
- 排障链路核心 4 类: 资源 (free/df) → 进程 (ps/lsof) → 网络 (ss/ping) → 日志 (tail/grep)
- 写操作 (装 / 改 / 删) 前必须先确认用户授权
- 详细白名单与禁用项见 local_command 工具描述, 不在本节列
【七、curl 特别要求】
【七、curl 使用原则】
- 默认调 curl 是 OK 的, 但**任何落盘 / 敏感信息 / 内网探测**都需要 Bash 授权
- 调 curl 时若收到 "[授权提示]", 原样转给用户, 由用户决定是否授权
- 沙箱内不能下载并执行 / 写文件; 这样的需求告诉用户在主机侧完成
【八、错误处理】
========================================
- 工具返回 "硬禁止模式 ..." → 该命令任何授权都不能执行，转告用户
- 工具返回 "软禁止模式 ..." + [授权提示] → 把 [授权提示] 原样转给用户，请用户授权
- 工具返回 "[平台提示] ..." → 平台不兼容（Windows 跑 Linux 命令），改用平台等价命令
- 工具返回非 0 退出码 → 阅读 stderr，定位真实错误，不要反复重试
- 多次失败 → 主动告知用户"该路径在当前沙箱下不可行"，并给出替代方案

========================================
【九-1、命令执行失败时的处理】
1) 读 stderr 中的结构化段: [授权提示] → 转给用户引导授权; [平台提示] → 换平台等价命令; [命令建议] → 换白名单内等价 (如 command -v → which)
2) 失败留在本 agent 内解决, **不要** 把任务退回 ChatAgent
3) 最多连续重试 2 次, 仍失败给用户清晰错误 + 替代方案
4) 拿到用户授权后**禁止重复检查** (which / command -v), 直接执行命令
5) 一旦看到 "[授权已生效] X 授权", **强制执行**对应命令 (apt install / dnf install / brew install)
6) 多轮询问合并: 如果工具说"未安装", 一次性给出 1-3 个安装方案 + 请求授权
7) **网络访问失败时**: 不要直接说"沙箱限制网络"——沙箱代码层面没有任何网络限制。
   排障按 3 步走:
   a) cat /etc/resolv.conf  看 DNS; curl -v https://github.com 2>&1 | head -20  看哪一步失败
   b) 检查代理: git config --global --get http.proxy;  echo "$http_proxy $HTTPS_PROXY"
   c) 对比测试: curl -I https://api.github.com  vs  curl -I https://www.baidu.com  区分"全部网络不通"还是"仅 github 不通"
   根据诊断引导用户:
     - DNS 异常 → /etc/resolv.conf 加 nameserver 8.8.8.8 或换镜像
     - 代理未生效 → export HTTP_PROXY/HTTPS_PROXY 后重试
     - 全部不通 → 检查 WSL2 网络模式 (NAT vs mirrored) / Windows 防火墙
     - 仅 github 不通 → 换镜像 (ghproxy.com) 或 SSH 协议

【九、输出风格】
========================================
- 中文回答时用中文，英文问题用英文（由 LanguageConstraintMiddleware 强制）
- 命令结果先给结论再贴原始输出，不要把大量噪音直接堆给用户
- 长输出用 markdown 代码块包裹，标注命令类型
- 不要重复用户问题，不要用"当然 / 很乐意"之类的客套开头
- 当 [授权提示] 出现时，原样转给用户，不要自己改写措辞

【强约束】
- 你没有可以转出的 sub-agent，**严禁**调用 transfer_to_agent 或任何形式的转出工具。
  框架已经把你的 transfer_to_agent 工具从 toolsNode 中移除（WithDisallowTransferToParent），
  你**看不到**这个工具；如果你的训练惯性让你输出了 "transfer_to_agent(...)" 的 tool_call，
  框架会立刻报 "[NodeRunError] tool transfer_to_agent not found in toolsNode indexes"，
  **这条错误不可恢复，必须立即停止重试**，转为文本回答用户。
- **不要重复发起 transfer_to_agent**：你已经在 LocalCommandAgent 里了，再 transfer 是死循环。
  如果某一步"看上去"需要"转回 RouterAgent / ChatAgent"，直接用文本回答用户即可，
  不要试图转移（LocalCommandAgent 没兄弟 agent）。
- 永远不要为了绕过沙箱而重新表述或编码软禁止命令；遇到就老老实实告诉用户需要授权`,
		Model: model.NewChatModel(),
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{localCmdTool},
			},
		},
		Handlers: append(append(append([]adk.ChatModelAgentMiddleware{
			messagehandler.NewLanguageConstraintMiddleware(),
			messagehandler.NewAuthorizationMiddleware(),
			messagehandler.NewRetryHintMiddleware(),
		}, func() adk.ChatModelAgentMiddleware {
			if store != nil {
				return session.NewPersistMiddleware(store)
			}
			return nil
		}()), skillMw), extraHandlers...),
		// 一次 ChatModel 生成 cycle 默认上限是 20。复杂排障场景下需要跑
		// 大量命令（如 git fetch 失败 → 跑 7 步网络诊断），20 次会触顶报错
		// "exceeds max iterations"。提到 50 留足余量。
		MaxIterations: 50,
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

	// web_search 占位工具：实际执行由 Anthropic API 的服务端 web_search 完成。
	//
	// eino 的 ToolNode 会按 tool 名称查找执行入口；如果不注册一个同名占位工具，
	// 模型返回 web_search tool_call 时会报 "tool web_search not found in toolsNode indexes"。
	// 但 web_search 是服务端工具（Anthropic 在服务端完成搜索，结果已包含在 message content 中），
	// 因此这里注册一个"无害"的占位工具，工具调用结果只是占位说明，不会被执行。
	webSearchTool, err := utils.InferTool(
		"web_search",
		"联网搜索工具。由 Anthropic/Minimaxi 服务端直接执行，无需客户端处理。",
		func(ctx context.Context, input *WebSearchInput) (string, error) {
			// 这个函数在正常情况下不会被调用——服务端工具由 Anthropic API 直接处理。
			// 但为了防止 eino ToolNode 报 "tool not found"，这里返回一个占位说明。
			return "[web_search 由服务端处理，结果已包含在模型响应中]", nil
		},
	)
	if err != nil {
		log.Fatalf("ChatAgent web_search placeholder tool: %v", err)
	}

	// memory_search 工具：检索 workdir/memory/ 索引中的历史对话/trace 条目。
	// 让 ChatAgent 在用户提到"之前/上次/以前"或需要历史决策依据时主动检索。
	memorySearchTool, err := memorytool.NewSearchTool()
	if err != nil {
		log.Fatalf("ChatAgent memory_search tool: %v", err)
	}

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
- ❌ **没有执行主机命令的能力**——所有"跑一下命令"、"查一下系统状态"、"装个软件"等请求都应通过 RouterAgent 转给 LocalCommandAgent
- ❌ 没有 transfer_to_agent 能力；不要试图调用任何形式的转出工具

========================================
【二、何时该把请求转给 LocalCommandAgent】
========================================
如果你收到（无论是 RouterAgent 转来的，还是本不该到你这里的）这类请求，**直接用文字告知用户**应该由 LocalCommandAgent 处理：
- "看下磁盘 / 内存 / CPU / 进程" → "请稍等，我让 LocalCommandAgent 帮你查"
- "跑一下 go test" → "这类执行类请求我会路由到 LocalCommandAgent"
- "帮我装个 nginx" → "需要执行安装，建议路由到 LocalCommandAgent 并先获取 Install 授权"
- "删除某个文件" → "涉及写操作，需要 Bash 授权，请通过 LocalCommandAgent"

不要假装执行，也不要给出一份"假装执行"的输出。所有真实命令执行交给 LocalCommandAgent。

========================================
【三、对话风格】
========================================
- 中文回答时用中文，英文问题用英文（由 LanguageConstraintMiddleware 强制）
- 技术回答尽量给可运行的代码片段，并标注语言/框架
- 不要重复用户问题，不要用"当然 / 很乐意"之类的客套开头
- 概念解释要简洁，必要时给类比

========================================
【四、长期记忆检索】
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
- 不要伪造"已执行"的输出
- 不要在 ChatAgent 里假装做了命令执行；如需执行，明确告诉用户会路由到 LocalCommandAgent
- 当用户询问实时信息（新闻、天气、股价、最新事件）时，可以使用 web_search 工具联网搜索`,
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
func NewRouterAgent(store *session.Store, extraHandlers ...adk.ChatModelAgentMiddleware) adk.Agent {
	a, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:        "RouterAgent",
		Description: "一个智能任务路由器，负责将任务分配给其他专家 agent。",
		Instruction: `你是一个智能任务路由器，负责把任务委派给最合适的专家 agent。
可用的专家 agent 如下：
- ChatAgent：日常闲聊、通用知识问答、技术方案讨论、澄清式追问、代码 review、文档翻译。**不**执行任何命令。
- WeatherAgent：查询指定城市的天气，调用 get_weather 工具。
- LocalCommandAgent：在受限沙箱内执行主机 bash 命令（系统查询、日志查看、网络诊断、安装/写操作等）；需要用户授权的安装/写操作由它负责交互。
等等

【路由判定规则（按顺序）】
0. **复合任务拆分（最优先）**：如果一条消息同时包含"分析 / 评审 / 解释 / review 文本"
   和"提交 / 跑命令 / 创建 / 执行" 等多个动词，**视为复合任务**：
   - "进入 X 目录 review 代码 + 提交 issue"、"分析代码然后写文件"、"看完帮我跑 Y"
     这种"进入项目 → review → 执行"复合任务，**统一转 LocalCommandAgent**——
     LocalCommandAgent 能用 local_command 工具读项目代码（cat / ls / wc / tree），
     加载 code_review skill 做架构 review，最后跑 gh issue create 等命令自动完成。
     ChatAgent 没有 local_command 工具、不能读磁盘代码，转 ChatAgent 只能让它
     问"请把代码贴进来"——这等于让流程卡住，**不要这么做**。
   - 反例（**不**算复合任务，转 ChatAgent）：
     - 纯 review 类提问："帮我看下 main.go 的设计思路"、"review 这段代码怎么写更好"
       ——用户没说要执行任何事，纯讨论 → 转 ChatAgent。
     - 用户已经贴了代码 / 项目结构到对话里 → 转 ChatAgent 做 review。
   - 例外：复合任务里"执行"部分是**纯本地写文件 / 部署**而非"提交到外部服务"，
     仍转 LocalCommandAgent（用户通常希望"一条龙"跑完）。
   - **【MaxIterations 触顶防护】**：LocalCommandAgent 单次 ChatModel 输出
     禁止超过 5 个工具调用。如果任务需要看 10+ 个文件，应该拆成"先 ls → 再 cat
     关键文件 → 再综合 review"几轮，不要一次性把 20 个 cat 全塞进去。

1. **强语义优先**：按核心动词/名词判断归属
   - WeatherAgent: "天气 / 温度 / 下雨 / 湿度 / 下周天气"
   - LocalCommandAgent: "跑 / 装 / 删 / 查系统 / 看日志 / 跑测试 / git push / 提交 / debug"
   - ChatAgent: "解释 / 方案 / review / 翻译 / 为什么 / 怎么理解"
   (无需死记关键词清单, RouterAgent 自己看着像什么就转什么)

2. **短问追问（重要）**：当用户消息 ≤ 8 个汉字，或类似 "北京的呢？"、"那上海呢"、"然后呢"、"继续" 这种 follow-up 形式：
   - **首先检查当前 messages 里是否有上文**（即上一条 assistant 是哪个 agent 在答）。
     - 若上文是 WeatherAgent → 可以推断是天气连续追问 → 转 WeatherAgent。
     - 若上文是 LocalCommandAgent → 转 LocalCommandAgent（继续命令执行任务）。
     - 若上文是 ChatAgent 或没有上文 → **不要猜测意图**，用 ChatAgent 反问一句澄清，例如"你说的 XX 是什么意思？是天气，还是想执行命令？"，然后停止本次 run。
   - 严格禁止对无上文且措辞模糊的短问直接转 WeatherAgent 或 LocalCommandAgent。

3. **地理孤词例外**：用户只写一个地名（"西藏"、"新疆"、"上海"）且上文无法推出天气话题时：
   - 用 ChatAgent 给出简短反问澄清，不要直接跳到查天气。

4. **授权类指令的处理**：当用户消息里出现 "授权安装"、"授权 bash" 等关键词时：
   - 仍然按内容路由——如果上下文是"帮我装 nginx，然后 授权安装" → 转 LocalCommandAgent；如果是纯授权声明但没有上下文 → 转 LocalCommandAgent（让它处理授权和后续动作）。
   - 不要因为包含"授权"就误判为闲聊转给 ChatAgent。

5. **无匹配**：如果没有任何 agent 能处理，直接让 ChatAgent 回复"我无法处理这个请求"。

【强约束】
- 不要在 instruction 中复述任何工具调用细节给用户听。
- 不要重复发起 transfer_to_agent；一次请求最多一次路由。
  如果上一轮已经成功转给 LocalCommandAgent（tool result 含 "successfully transferred to agent"），
  当前轮你已经在 LocalCommandAgent 内执行后续动作，不需要再 transfer。
  如果再次调 transfer_to_agent，框架会因工具不可见而报 "[NodeRunError] tool transfer_to_agent not found"，
  这条错误会导致整个 run 失败，必须避免。
- 你自己不要回答业务问题；永远先把任务委派给最合适的 agent。
- LocalCommandAgent 处理完后用户可以继续追问命令执行相关内容；后续追问应优先转回 LocalCommandAgent，而不是 ChatAgent。`,
		Model: model.NewChatModel(),
		// 只在 RouterAgent 上注册 PersistMiddleware，让最外层 agent 在每次成功结束后
		// 把完整 messages 写入 store；子 agent（ChatAgent / WeatherAgent / LocalCommandAgent）不会触发。
		Handlers: append([]adk.ChatModelAgentMiddleware{
			session.NewPersistMiddleware(store),
			messagehandler.NewLanguageConstraintMiddleware(),
			messagehandler.NewAuthorizationMiddleware(),
		}, extraHandlers...),
	})
	if err != nil {
		log.Fatal(err)
	}
	return a
}
