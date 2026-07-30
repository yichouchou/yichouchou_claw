package memory

import (
	"bytes"
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// MaxContentPreview caps the size of LLM input / output payloads dumped to
// the summary file. 8 KiB is enough to debug a prompt without making the
// review log unreadable. The dedicated inputs/ and outputs/ files receive
// the full payload.
const MaxContentPreview = 8 * 1024

// sessionCtxKey is the key under which main.go stores the session id in
// ctx via adk.WithSessionValues. Centralised here so the middleware and
// main.go don't drift.
const sessionCtxKey = "session_id"

// requestGroupIDCtxKey 会话中传递 request_group_id 的 ctx key。
//
// main.go 在 adk.WithSessionValues 里同时注入 session_id 和 request_group_id,
// memory middleware 从 ctx 读 request_group_id 后注入到 ctx values(memory.WithRequestGroupID),
// 让下游 RecordLLMInput/RecordLLMOutput 写 front-matter 时拿到这个 ID。
//
// 必须与 session.KeyRequestGID 字面值一致,以便同一 request_group_id 在 SDK
// chain 与 memory 包之间无障碍传递。
const hookRequestGroupIDKey = "request_group_id"

// MemoryMiddleware 实现 adk.ChatModelAgentMiddleware。
//
// 三个核心职责:
//  1. 注入 llm_trace_id:每次 BeforeModelRewriteState 前生成新 UUID,让后续
//     RecordLLMInput / RecordLLMOutput 配对
//  2. 把模型调用的 input / output 落盘到 inputs/ outputs/,完整 payload
//  3. 捕获工具错误,落到 errors/
//
// 异步契约:每个 hook 调 SafeRecord* fire-and-forget,worker 异步写盘。
//
// Per-agent wiring:每个 ChatModelAgent 自己的 MemoryMiddleware 实例,
// agent name 注入到每条 entry 的 front-matter。
type MemoryMiddleware struct {
	*adk.BaseChatModelAgentMiddleware

	// AgentName 写到每条 entry 的 front-matter。必填。
	AgentName string
}

// NewMemoryMiddleware 构造绑了 AgentName 的 middleware。
// 用法和 ChatModelAgentConfig.Name 一致。
func NewMemoryMiddleware(agentName string) *MemoryMiddleware {
	return &MemoryMiddleware{AgentName: agentName}
}

func (m *MemoryMiddleware) agentName() string {
	if m == nil || m.AgentName == "" {
		return "unknown"
	}
	return m.AgentName
}

// sessionID 提取 main.go 通过 adk.WithSessionValues 注入的 session id。
func (m *MemoryMiddleware) sessionID(ctx context.Context) string {
	if v, ok := adk.GetSessionValue(ctx, sessionCtxKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// requestGroupID 提取 main.go 通过 adk.WithSessionValues 注入的 request_group_id。
// 用于把本次浏览器请求触发的所有 LLM 调用 entry(user_request + N 个 llm +
// user_response)在 front-matter 串联起来。
func (m *MemoryMiddleware) requestGroupID(ctx context.Context) string {
	if v, ok := adk.GetSessionValue(ctx, hookRequestGroupIDKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// BeforeModelRewriteState 在 eino 把 state 发给 ChatModel 之前:
//  1. 生成新 llm_trace_id 注入 ctx(后续 RecordLLMOutput 自动配对)
//  2. 把 messages 完整 payload 落盘到 inputs/
//
// state.Messages 是 SDK 权威视图(已含之前工具结果),无需重走 agent。
func (m *MemoryMiddleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if GetRecorder() == nil {
		return ctx, state, nil
	}

	// 每次 ChatModel 调用一个新 llm_trace_id 注入 ctx
	// 即使 sid 为空也注入,让下游(AfterModelRewriteState / WrapInvokableToolCall)
	// 能拿到配对 ID。Recording 跳过即可。
	traceID := NewLLMTraceID()
	ctx = WithLLMTraceID(ctx, traceID)
	ctx = WithAgentName(ctx, m.agentName())

	sid := m.sessionID(ctx)
	if sid == "" {
		return ctx, state, nil
	}

	// 把 adk.WithSessionValues 注入的 request_group_id 桥接到 memory.WithRequestGroupID,
	// 让 AsyncMarkdownRecorder.RecordLLMInput/Output 自动写到 front-matter。
	if gid := m.requestGroupID(ctx); gid != "" {
		ctx = WithRequestGroupID(ctx, RequestGroupID(gid))
	}

	body := formatMessagesAsInput(state.Messages)
	SafeRecordLLMInput(ctx, sid, traceID, m.agentName(), []byte(body))
	return ctx, state, nil
}

// AfterModelRewriteState 把 ChatModel 刚生成的助手消息落盘到 outputs/。
// 配对规则:从 ctx 取 LLMTraceID,与 BeforeModelRewriteState 注入的同一 ID。
func (m *MemoryMiddleware) AfterModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if GetRecorder() == nil {
		return ctx, state, nil
	}
	sid := m.sessionID(ctx)
	if sid == "" || len(state.Messages) == 0 {
		return ctx, state, nil
	}

	traceID := LLMTraceIDFromContext(ctx) // BeforeModelRewriteState 注入
	if traceID == "" {
		traceID = NewLLMTraceID() // fallback(理论上不会发生)
	}

	last := state.Messages[len(state.Messages)-1]
	body := formatMessageAsOutput(last)
	SafeRecordLLMOutput(ctx, sid, traceID, m.agentName(), []byte(body))
	return ctx, state, nil
}

// WrapInvokableToolCall 拦截工具错误(同步路径)。
// 流式路径(WrapStreamableToolCall)留空:框架已 log,invokable 路径覆盖
// 大部分可操作错误(local_command / get_weather 等)。
//
// 透明包装:成功时同样返回,只捕获 error。
func (m *MemoryMiddleware) WrapInvokableToolCall(ctx context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	wrapped := func(c context.Context, args string, opts ...tool.Option) (string, error) {
		out, err := endpoint(c, args, opts...)
		if err != nil {
			sid := m.sessionID(c)
			traceID := LLMTraceIDFromContext(c)
			ctxStr := fmt.Sprintf("tool=%s call_id=%s args=%s",
				tCtx.Name, tCtx.CallID, TruncateBytes(args, 200))
			SafeRecordError(c, sid, traceID, m.agentName(), err, ctxStr)
		}
		return out, err
	}
	return wrapped, nil
}

// formatMessagesAsInput 把即将发给模型的多轮 messages 渲染成可读文本。
//
// 多模态支持(2026-07-29):
//   - 单独统计 text_len / multi_parts 两个指标,避免单数字 metric 误判
//   - 对每条 multi_part 输出一行: type / mime / url(摘要)
//   - 大幅 base64 数据不展开,只统计长度(避免 trace 文件被 base64 撑爆)
func formatMessagesAsInput(msgs []*schema.Message) string {
	if len(msgs) == 0 {
		return "(no messages)"
	}
	var buf bytes.Buffer
	for i, msg := range msgs {
		multiPartCount := len(msg.UserInputMultiContent) + len(msg.AssistantGenMultiContent)
		fmt.Fprintf(&buf, "[%02d] role=%s text_len=%d multi_parts=%d tool_calls=%d\n",
			i, msg.Role, len(msg.Content), multiPartCount, len(msg.ToolCalls))
		if msg.Role == schema.Tool {
			fmt.Fprintf(&buf, "     tool_call_id=%s tool_name=%s\n",
				msg.ToolCallID, msg.ToolName)
		}
		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				fmt.Fprintf(&buf, "     - tool_call id=%s name=%s args=%s\n",
					tc.ID, tc.Function.Name, truncate(tc.Function.Arguments, 500))
			}
		}
		// 用户多模态输入
		for j, p := range msg.UserInputMultiContent {
			fmt.Fprintf(&buf, "     - input_part[%d] type=%s mime=%s url=%s\n",
				j, p.Type, getInputPartMIME(p), getInputPartURLOrSummary(p))
		}
		// 模型多模态输出
		for j, p := range msg.AssistantGenMultiContent {
			fmt.Fprintf(&buf, "     - output_part[%d] type=%s mime=%s url=%s\n",
				j, p.Type, getOutputPartMIME(p), getOutputPartURLOrSummary(p))
		}
		if msg.Content != "" {
			fmt.Fprintf(&buf, "     content: %s\n", truncate(msg.Content, MaxContentPreview))
		}
	}
	return buf.String()
}

// getInputPartMIME 从 MessageInputPart 拿 MIME(可能为空)。
func getInputPartMIME(p schema.MessageInputPart) string {
	switch {
	case p.Image != nil:
		return p.Image.MIMEType
	case p.Audio != nil:
		return p.Audio.MIMEType
	case p.Video != nil:
		return p.Video.MIMEType
	case p.File != nil:
		return p.File.MIMEType
	}
	return ""
}

// getInputPartURLOrSummary 拿 URL/base64 长度摘要。
//
// 大幅 base64 (>=256 字节) 不展开,只输出长度,避免 trace 文件被撑爆。
func getInputPartURLOrSummary(p schema.MessageInputPart) string {
	switch {
	case p.Image != nil:
		return inputDataURLOrSummary(p.Image.URL, p.Image.Base64Data)
	case p.Audio != nil:
		return inputDataURLOrSummary(p.Audio.URL, p.Audio.Base64Data)
	case p.Video != nil:
		return inputDataURLOrSummary(p.Video.URL, p.Video.Base64Data)
	case p.File != nil:
		return inputDataURLOrSummary(p.File.URL, p.File.Base64Data)
	}
	return p.Text
}

func inputDataURLOrSummary(url, b64 *string) string {
	if url != nil && *url != "" {
		return *url
	}
	if b64 != nil && *b64 != "" {
		if len(*b64) > 256 {
			return fmt.Sprintf("<base64 %d bytes>", len(*b64))
		}
		return *b64
	}
	return ""
}

// getOutputPartMIME 从 MessageOutputPart 拿 MIME(可能为空)。
func getOutputPartMIME(p schema.MessageOutputPart) string {
	switch {
	case p.Image != nil:
		return p.Image.MIMEType
	case p.Audio != nil:
		return p.Audio.MIMEType
	case p.Video != nil:
		return p.Video.MIMEType
	}
	return ""
}

// getOutputPartURLOrSummary 与 getInputPartURLOrSummary 同形,只接收 output 类型。
func getOutputPartURLOrSummary(p schema.MessageOutputPart) string {
	switch {
	case p.Image != nil:
		return inputDataURLOrSummary(p.Image.URL, p.Image.Base64Data)
	case p.Audio != nil:
		return inputDataURLOrSummary(p.Audio.URL, p.Audio.Base64Data)
	case p.Video != nil:
		return inputDataURLOrSummary(p.Video.URL, p.Video.Base64Data)
	}
	if p.Reasoning != nil {
		return fmt.Sprintf("<reasoning %d bytes>", len(p.Reasoning.Text))
	}
	return ""
}

// formatMessageAsOutput 渲染一条助手消息(含 tool_calls)。
func formatMessageAsOutput(msg *schema.Message) string {
	if msg == nil {
		return "(nil message)"
	}
	var buf bytes.Buffer
	multiPartCount := len(msg.UserInputMultiContent) + len(msg.AssistantGenMultiContent)
	fmt.Fprintf(&buf, "role=%s text_len=%d multi_parts=%d tool_calls=%d\n",
		msg.Role, len(msg.Content), multiPartCount, len(msg.ToolCalls))
	if msg.Role == schema.Tool {
		fmt.Fprintf(&buf, "tool_call_id=%s tool_name=%s\n",
			msg.ToolCallID, msg.ToolName)
	}
	if len(msg.ToolCalls) > 0 {
		for _, tc := range msg.ToolCalls {
			fmt.Fprintf(&buf, "tool_call id=%s name=%s args=%s\n",
				tc.ID, tc.Function.Name, truncate(tc.Function.Arguments, 500))
		}
	}
	// 多模态输出 part 摘要(2026-07-29 新增)
	for j, p := range msg.AssistantGenMultiContent {
		fmt.Fprintf(&buf, "output_part[%d] type=%s mime=%s url=%s\n",
			j, p.Type, getOutputPartMIME(p), getOutputPartURLOrSummary(p))
	}
	if msg.Content != "" {
		fmt.Fprintf(&buf, "content: %s\n", msg.Content)
	}
	return buf.String()
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
