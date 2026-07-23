package memory

import (
	"bytes"
	"context"
	"fmt"
	"log"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// hookLogPrefix is the log tag used by the MemoryMiddleware.
const hookLogPrefix = "[memory.hook]"

// MaxContentPreview caps the size of LLM input / output payloads dumped to
// the sessions/ summary file. The dedicated inputs/ and outputs/ files
// receive the full payload. 8 KiB is enough to debug a prompt without
// making the review log unreadable.
const MaxContentPreview = 8 * 1024

// sessionCtxKey is the key under which main.go stores the session id in
// ctx via adk.WithSessionValues. Centralised here so the middleware and
// main.go don't drift.
const sessionCtxKey = "session_id"

// MemoryMiddleware implements adk.ChatModelAgentMiddleware. It captures
// five kinds of events:
//
//  1. session start  (BeforeAgent)
//  2. LLM input      (BeforeModelRewriteState — model-bound messages)
//  3. LLM output     (AfterModelRewriteState — last assistant message)
//  4. tool error     (WrapInvokableToolCall — catches non-nil error returns)
//  5. session end    (AfterAgent)
//
// All events go through the package-wide Recorder. If no recorder is set
// (SetRecorder never called, or YICHOUCHOU_MEMORY=off), the middleware is
// a silent no-op: no allocations, no logging, no overhead beyond one map
// lookup per method invocation.
//
// Per-agent wiring: each ChatModelAgent gets its own MemoryMiddleware
// instance with the agent name baked in, so the markdown files distinguish
// RouterAgent from ChatAgent / LocalCommandAgent / WeatherAgent.
type MemoryMiddleware struct {
	*adk.BaseChatModelAgentMiddleware

	// AgentName is written verbatim into each markdown entry. Required.
	AgentName string
}

// NewMemoryMiddleware constructs a middleware bound to AgentName. Pass
// the same agent name you used in ChatModelAgentConfig.Name so log lines
// can be traced back to the agent that produced them.
func NewMemoryMiddleware(agentName string) *MemoryMiddleware {
	return &MemoryMiddleware{AgentName: agentName}
}

// agentName returns the configured agent name, falling back to "unknown"
// if the caller forgot to set it. We never panic: a misconfigured agent
// is far less damaging than a panic in the request path.
func (m *MemoryMiddleware) agentName() string {
	if m == nil || m.AgentName == "" {
		return "unknown"
	}
	return m.AgentName
}

// sessionID extracts the session id injected by main.go via
// adk.WithSessionValues. Empty string means "not in a request scope".
func (m *MemoryMiddleware) sessionID(ctx context.Context) string {
	if v, ok := adk.GetSessionValue(ctx, sessionCtxKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// BeforeAgent records a session_start marker. We don't dump messages
// here because BeforeModelRewriteState will fire before the first model
// call and will dump the full conversation as "input".
func (m *MemoryMiddleware) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	if GetRecorder() == nil {
		return ctx, runCtx, nil
	}
	sid := m.sessionID(ctx)
	GetRecorder().RecordSessionStart(sid, m.agentName())
	return ctx, runCtx, nil
}

// BeforeModelRewriteState dumps the messages that are about to be sent
// to the model. State is the SDK's authoritative view (already includes
// any prior tool results), so we don't have to re-walk the agent.
func (m *MemoryMiddleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if GetRecorder() == nil {
		return ctx, state, nil
	}
	sid := m.sessionID(ctx)
	if sid == "" {
		return ctx, state, nil
	}
	body := formatMessagesAsInput(state.Messages)
	safeInput(sid, m.agentName(), []byte(body))
	return ctx, state, nil
}

// AfterModelRewriteState dumps the assistant message that the model just
// produced (the last entry in state.Messages). If the model issued
// tool_calls, we render the call list as well so the markdown review log
// shows *what the model asked for* without needing to scroll to outputs/.
func (m *MemoryMiddleware) AfterModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if GetRecorder() == nil {
		return ctx, state, nil
	}
	sid := m.sessionID(ctx)
	if sid == "" || len(state.Messages) == 0 {
		return ctx, state, nil
	}
	last := state.Messages[len(state.Messages)-1]
	body := formatMessageAsOutput(last)
	safeOutput(sid, m.agentName(), []byte(body))
	return ctx, state, nil
}

// AfterAgent records a session_end marker. We do NOT dump messages here
// because the same data already appears in inputs/ and outputs/.
func (m *MemoryMiddleware) AfterAgent(ctx context.Context, _ *adk.ChatModelAgentState) (context.Context, error) {
	r := GetRecorder()
	if r == nil {
		return ctx, nil
	}
	sid := m.sessionID(ctx)
	if sid == "" {
		return ctx, nil
	}
	if err := r.RecordSessionEnd(sid, m.agentName()); err != nil {
		log.Printf("%s RecordSessionEnd err=%v", hookLogPrefix, err)
	}
	return ctx, nil
}

// WrapInvokableToolCall intercepts tool errors. Streamed tool calls
// (WrapStreamableToolCall) are left alone: a stream error is logged by
// the framework, and the invokable path covers local_command / get_weather
// which is where most actionable errors surface.
//
// The wrapper is transparent on success: same return value, same error.
func (m *MemoryMiddleware) WrapInvokableToolCall(ctx context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	wrapped := func(c context.Context, args string, opts ...tool.Option) (string, error) {
		out, err := endpoint(c, args, opts...)
		if err != nil {
			sid := m.sessionID(c)
			ctxStr := fmt.Sprintf("tool=%s call_id=%s args=%s",
				tCtx.Name, tCtx.CallID, truncate(args, 200))
			safeError(sid, m.agentName(), err, ctxStr)
		}
		return out, err
	}
	return wrapped, nil
}

// formatMessagesAsInput renders a multi-line text view of all messages
// that are about to be sent to the model. The format is intentionally
// human-readable (not JSON) so a reviewer can grep through a day's
// traces without parsing tooling.
func formatMessagesAsInput(msgs []*schema.Message) string {
	if len(msgs) == 0 {
		return "(no messages)"
	}
	var buf bytes.Buffer
	for i, msg := range msgs {
		fmt.Fprintf(&buf, "[%02d] role=%s content_len=%d tool_calls=%d\n",
			i, msg.Role, len(msg.Content), len(msg.ToolCalls))
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
		if msg.Content != "" {
			fmt.Fprintf(&buf, "     content: %s\n", truncate(msg.Content, MaxContentPreview))
		}
	}
	return buf.String()
}

// formatMessageAsOutput renders a single assistant message. If the
// message carries tool_calls, each call is dumped on its own line.
func formatMessageAsOutput(msg *schema.Message) string {
	if msg == nil {
		return "(nil message)"
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "role=%s content_len=%d tool_calls=%d\n",
		msg.Role, len(msg.Content), len(msg.ToolCalls))
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
	if msg.Content != "" {
		fmt.Fprintf(&buf, "content: %s\n", msg.Content)
	}
	return buf.String()
}

// truncate keeps the first n bytes of s and appends an ellipsis marker.
// Used by input / output dumps to keep markdown viewable.
func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}