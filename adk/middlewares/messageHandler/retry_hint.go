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

package messagehandler

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// RetryHintMiddleware 在每次调用模型前，扫描 messages 中的 tool result 消息，
// 如果发现上一次 local_command 工具失败时附加的 [命令建议] 段，就把"该重试"的
// 语义化提示注入到 system message，让 LLM 自动换工具再试一次。
//
// 这个 middleware 的核心定位：
//   - local_command 工具抛出的 [命令建议] 已经携带了"为什么失败 + 建议方向"；
//   - 本 middleware 负责把这个信息"翻译"成给 LLM 的指令，避免 LLM 误解
//     错误信号、放弃执行任务、或错误地路由到 ChatAgent。
//
// 与 AuthorizationMiddleware 的区别：
//   - AuthorizationMiddleware 处理用户输入（user message），识别授权意图；
//   - RetryHintMiddleware 处理工具输出（tool message），识别失败信号。
//
// 注意：本 middleware 不强制重试、不改写命令，只是"提示 LLM 该怎么重试"。
type RetryHintMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
}

// NewRetryHintMiddleware 创建 RetryHintMiddleware。
func NewRetryHintMiddleware() *RetryHintMiddleware {
	return &RetryHintMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
	}
}

// toolResultMarkers 用于识别"上一次 local_command 失败"的标志。
// 这些标记由 internal/localcommand/tool.go 的 commandNotFoundHint / authorizationHint / platformHintFor 注入。
var toolResultMarkers = []string{
	"[命令建议]",
	"[授权提示]",
	"[平台提示]",
	"安全拦截:",
	"硬禁止模式",
	"软禁止模式",
	"executable file not found",
	"is not recognized as",
}

// retryHintNotice 在 system message 末尾追加的提示。
// 注意：保持简洁，避免干扰 LLM 主流程。
const retryHintNotice = `

【重试提示】你刚才调用 local_command 工具失败，stderr 中包含了 [命令建议] / [授权提示] / [平台提示] 等结构化段。请按以下步骤处理：
1) 先读 stderr 中的结构化段，理解失败原因；
2) 如果是 [命令建议]：原命令是 POSIX shell builtin 或沙箱不支持，**请改用白名单内的等价命令重试**（参考工具描述的"白名单分组速查"，如 command -v X → which X）；
3) 如果是 [授权提示]：把提示原样转给用户，请用户授权后再重试；
4) 如果是 [平台提示]：换用平台等价的命令重试；
5) **不要**因为工具失败就把任务"退回"给 ChatAgent 去解释；失败也要留在本 agent 内解决；
6) 最多连续重试 2 次，仍失败就给用户清晰错误 + 替代方案。`

// BeforeModelRewriteState 在模型调用前扫描 tool message，注入重试提示。
func (m *RetryHintMiddleware) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	mc *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || len(state.Messages) == 0 {
		return ctx, state, nil
	}

	// 找最近的 tool message（失败的工具结果）
	latestToolResult := findLatestToolResult(state.Messages)
	if latestToolResult == "" {
		return ctx, state, nil
	}

	// 检查是否包含失败标记
	if !containsAnyMarker(latestToolResult, toolResultMarkers) {
		return ctx, state, nil
	}

	// 把重试提示注入到 system message
	for _, msg := range state.Messages {
		if msg.Role == schema.System && !strings.Contains(msg.Content, retryHintNotice) {
			msg.Content += retryHintNotice
			break
		}
	}
	return ctx, state, nil
}

// findLatestToolResult 从 messages 末尾向前扫描，返回最近一条 tool message 的内容。
// 如果没有 tool message，返回空字符串。
func findLatestToolResult(messages []*schema.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg == nil {
			continue
		}
		if msg.Role == schema.Tool {
			return msg.Content
		}
	}
	return ""
}

// containsAnyMarker 判断文本是否包含任一标记。
func containsAnyMarker(text string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}
