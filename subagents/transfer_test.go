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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/yichouchou/yichouchou_claw/internal/session"
)

// fakeChatModel 是一个确定性的 chat model 替身：
// 它把「本次调用交给模型的工具列表」记录下来，并按 reply 函数返回固定回复。
// 整个测试不依赖任何 LLM 行为，也不需要网络与 API key。
type fakeChatModel struct {
	mu        sync.Mutex
	toolNames []string
	calls     int
	reply     func(call int) *schema.Message
}

func (f *fakeChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	common := einomodel.GetCommonOptions(nil, opts...)

	names := make([]string, 0, len(common.Tools))
	for _, ti := range common.Tools {
		names = append(names, ti.Name)
	}

	f.mu.Lock()
	f.toolNames = names
	f.calls++
	call := f.calls
	reply := f.reply
	f.mu.Unlock()

	if reply != nil {
		return reply(call), nil
	}
	return schema.AssistantMessage("ok", nil), nil
}

func (f *fakeChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := f.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (f *fakeChatModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return f, nil
}

// lastToolNames 返回模型最近一次调用时看到的工具名列表。
func (f *fakeChatModel) lastToolNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.toolNames))
	copy(out, f.toolNames)
	return out
}

func (f *fakeChatModel) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func contains(names []string, target string) bool {
	for _, n := range names {
		if n == target {
			return true
		}
	}
	return false
}

// drain 消费掉整个 agent 事件流，返回所有事件和第一个遇到的错误。
func drain(t *testing.T, iter *adk.AsyncIterator[*adk.AgentEvent]) ([]*adk.AgentEvent, error) {
	t.Helper()

	var (
		events []*adk.AgentEvent
		runErr error
	)
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, ev)
		if ev.Err != nil && runErr == nil {
			runErr = ev.Err
		}
	}
	return events, runErr
}

func transferCall(id, target string) []schema.ToolCall {
	return []schema.ToolCall{{
		ID: id,
		Function: schema.FunctionCall{
			Name:      adk.TransferToAgentToolName,
			Arguments: `{"agent_name":"` + target + `"}`,
		},
	}}
}

// newTestSkillsDir 造一个空的 skills 目录（子目录也要建），
// 让 buildSkillMiddleware 能正常初始化而不报致命错误。
func newTestSkillsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"chat", "localcommand"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	return dir
}

// TestSubAgentsToolListHasNoTransferToAgent 是本缺陷的核心回归测试：
//
// eino 在「agent 有 parent」时会自动注册 transfer_to_agent 工具，并追加一段
// 「鼓励转出」的 instruction（拼在该 agent 自己的 Instruction 之后）。
// 所以 Instruction 里写「严禁调用 transfer_to_agent」是压不住的 —— 工具本身就在
// 交给模型的工具列表里，模型会照抄历史中 router 那次 transfer 的 tool_result，
// 对自己发起 transfer_to_agent，形成无限循环，最终报
// 「transfer failed: agent 'ChatAgent' not found when transferring from 'ChatAgent'」。
//
// 因此这里从「工具列表」层面断言：子 agent 交给模型的工具里不含 transfer_to_agent，
// 而 RouterAgent 含。
//
// 关键点：必须先经 adk.SetSubAgents 建立父子关系，transfer_to_agent 才「本该」出现；
// 否则这个断言会空洞通过（没有 parent 就不会注册该工具），测不出任何东西。
func TestSubAgentsToolListHasNoTransferToAgent(t *testing.T) {
	ctx := context.Background()
	skillsDir := newTestSkillsDir(t)
	store := session.NewStore(4)

	fake := &fakeChatModel{}
	testModelOverride = fake
	t.Cleanup(func() { testModelOverride = nil })

	router := NewRouterAgent(store)
	chat := NewChatAgent(ctx, skillsDir, store)
	weather := NewWeatherAgent(store)
	localCmd := NewLocalCommandAgent(ctx, skillsDir, store)

	if _, err := adk.SetSubAgents(ctx, router, []adk.Agent{chat, weather, localCmd}); err != nil {
		t.Fatalf("SetSubAgents failed: %v", err)
	}

	// --- RouterAgent：合法持有 transfer_to_agent ---
	if _, err := drain(t, router.Run(ctx, &adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage("你好")},
	})); err != nil {
		t.Fatalf("router run failed: %v", err)
	}
	routerTools := fake.lastToolNames()
	t.Logf("RouterAgent tools = %v", routerTools)
	if !contains(routerTools, adk.TransferToAgentToolName) {
		t.Fatalf("RouterAgent 应当持有 %s，实际工具列表 = %v", adk.TransferToAgentToolName, routerTools)
	}

	// --- ChatAgent：不得持有 transfer_to_agent ---
	if _, err := drain(t, chat.Run(ctx, &adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage("你好")},
	})); err != nil {
		t.Fatalf("chat run failed: %v", err)
	}
	chatTools := fake.lastToolNames()
	t.Logf("ChatAgent tools = %v", chatTools)
	if contains(chatTools, adk.TransferToAgentToolName) {
		t.Fatalf("ChatAgent 的工具列表里不应出现 %s，实际 = %v", adk.TransferToAgentToolName, chatTools)
	}
	if !contains(chatTools, "web_search") {
		t.Fatalf("ChatAgent 应当保留 web_search 工具，实际 = %v", chatTools)
	}

	// --- WeatherAgent：不得持有 transfer_to_agent ---
	if _, err := drain(t, weather.Run(ctx, &adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage("北京天气")},
	})); err != nil {
		t.Fatalf("weather run failed: %v", err)
	}
	weatherTools := fake.lastToolNames()
	t.Logf("WeatherAgent tools = %v", weatherTools)
	if contains(weatherTools, adk.TransferToAgentToolName) {
		t.Fatalf("WeatherAgent 的工具列表里不应出现 %s，实际 = %v", adk.TransferToAgentToolName, weatherTools)
	}
	if !contains(weatherTools, "get_weather") {
		t.Fatalf("WeatherAgent 应当保留 get_weather 工具，实际 = %v", weatherTools)
	}

	// --- LocalCommandAgent：不得持有 transfer_to_agent ---
	if _, err := drain(t, localCmd.Run(ctx, &adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage("看看目录")},
	})); err != nil {
		t.Fatalf("localcmd run failed: %v", err)
	}
	localCmdTools := fake.lastToolNames()
	t.Logf("LocalCommandAgent tools = %v", localCmdTools)
	if contains(localCmdTools, adk.TransferToAgentToolName) {
		t.Fatalf("LocalCommandAgent 的工具列表里不应出现 %s，实际 = %v", adk.TransferToAgentToolName, localCmdTools)
	}
	if !contains(localCmdTools, "local_command") {
		t.Fatalf("LocalCommandAgent 应当保留 local_command 工具，实际 = %v", localCmdTools)
	}
}

// TestRouterTransfersToSubAgentEndToEnd 走一遍真实链路：
// RouterAgent 决定转移到 ChatAgent，ChatAgent 被真正唤起。
// 这里同时验证 ChatAgent 被唤起时，模型看到的工具列表里依然没有 transfer_to_agent。
func TestRouterTransfersToSubAgentEndToEnd(t *testing.T) {
	ctx := context.Background()
	skillsDir := newTestSkillsDir(t)
	store := session.NewStore(4)

	fake := &fakeChatModel{
		reply: func(call int) *schema.Message {
			if call == 1 {
				return schema.AssistantMessage("正在转交", transferCall("call-1", "ChatAgent"))
			}
			return schema.AssistantMessage("ok", nil)
		},
	}
	testModelOverride = fake
	t.Cleanup(func() { testModelOverride = nil })

	router := NewRouterAgent(store)
	chat := NewChatAgent(ctx, skillsDir, store)

	root, err := adk.SetSubAgents(ctx, router, []adk.Agent{chat})
	if err != nil {
		t.Fatalf("SetSubAgents failed: %v", err)
	}

	// 必须运行 SetSubAgents 返回的根 agent（flow agent）：
	// 「切换到目标子 agent」由 flow 承担（adk/flow.go:551 读
	// lastAction.TransferToAgent.DestAgentName）。直接跑 router（ChatModelAgent）
	// 不会触发转移 —— transfer 工具只是发出一个 action，真正的切换在 flow 里。
	events, err := drain(t, root.Run(ctx, &adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage("你好")},
	}))
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if fake.callCount() < 2 {
		t.Fatalf("ChatAgent 未被唤起，model 调用次数 = %d，events = %d", fake.callCount(), len(events))
	}
	chatTools := fake.lastToolNames()
	t.Logf("ChatAgent tools (after transfer) = %v", chatTools)
	if contains(chatTools, adk.TransferToAgentToolName) {
		t.Fatalf("被 router 转交后，ChatAgent 的工具列表里仍出现 %s，实际 = %v",
			adk.TransferToAgentToolName, chatTools)
	}
}

// TestRouterTransferTargetGuard 验证 RouterAgent 上的目标白名单：
// 模型若把任务转移给非法目标（例如把自己），流程不应被
// 「transfer failed: ... not found」打死，而应返回一条明确的错误提示。
func TestRouterTransferTargetGuard(t *testing.T) {
	ctx := context.Background()
	skillsDir := newTestSkillsDir(t)
	store := session.NewStore(4)

	fake := &fakeChatModel{
		reply: func(call int) *schema.Message {
			if call == 1 {
				return schema.AssistantMessage("转移给自己", transferCall("call-1", "RouterAgent"))
			}
			return schema.AssistantMessage("ok", nil)
		},
	}
	testModelOverride = fake
	t.Cleanup(func() { testModelOverride = nil })

	router := NewRouterAgent(store)
	chat := NewChatAgent(ctx, skillsDir, store)

	root, err := adk.SetSubAgents(ctx, router, []adk.Agent{chat})
	if err != nil {
		t.Fatalf("SetSubAgents failed: %v", err)
	}

	events, runErr := drain(t, root.Run(ctx, &adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage("你好")},
	}))
	if runErr != nil {
		t.Fatalf("非法转移目标不应打死流程，实际错误：%v", runErr)
	}

	var gotGuardMessage bool
	for _, ev := range events {
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		msg := ev.Output.MessageOutput.Message
		if msg != nil && strings.Contains(msg.Content, "转移失败") {
			gotGuardMessage = true
		}
	}
	if !gotGuardMessage {
		t.Fatalf("未捕获到白名单拦截产生的错误提示，events = %d", len(events))
	}
}
