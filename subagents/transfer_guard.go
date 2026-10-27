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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
)

// transferTargets 是 RouterAgent 唯一允许的转移目标白名单。
// 与 main.go 里 adk.SetSubAgents(ctx, routerAgent, []adk.Agent{...}) 传入的子 agent 保持一致。
var transferTargets = []string{"ChatAgent", "WeatherAgent", "LocalCommandAgent"}

// NewTransferTargetGuard 返回一个 ToolMiddleware，用于校验 transfer_to_agent 的目标 agent。
//
// 背景：eino 内置的 transferToAgent 工具不校验目标名字，任何名字都会直接回显
// "successfully transferred to agent [X]"，真正的失败发生在 adk/flow.go：
// 目标不在可转移列表里时会报 "transfer failed: agent 'X' not found ..."，整个 run 被打死。
//
// 这里在工具执行前做白名单校验：目标不在白名单内时，不执行真正的转移动作，
// 而是直接返回一条明确的错误提示（例如模型误把 RouterAgent 转移给自己），
// 让流程以一条可读的消息正常结束，而不是抛致命错误。
func NewTransferTargetGuard(allowed []string) compose.ToolMiddleware {
	allowedSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = true
	}
	allowedText := strings.Join(allowed, ", ")

	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				if input == nil || input.Name != adk.TransferToAgentToolName {
					return next(ctx, input)
				}

				var params struct {
					AgentName string `json:"agent_name"`
				}
				if err := json.Unmarshal([]byte(input.Arguments), &params); err != nil {
					return &compose.ToolOutput{
						Result: fmt.Sprintf(
							"转移失败：无法解析 transfer_to_agent 的参数（%v）。合法的目标 agent 只有：%s。请重新选择一个目标。",
							err, allowedText),
					}, nil
				}

				if !allowedSet[params.AgentName] {
					return &compose.ToolOutput{
						Result: fmt.Sprintf(
							"转移失败：目标 agent %q 不在允许的转移列表 [%s] 中。你不能把任务转移给自己或未注册的 agent，请重新选择一个合法的目标 agent，或直接回答用户的问题。",
							params.AgentName, allowedText),
					}, nil
				}

				return next(ctx, input)
			}
		},
	}
}
