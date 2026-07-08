package message

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/hertz-contrib/sse"
)

// SSEEvent 是推送给前端的单个事件载荷。
type SSEEvent struct {
	Type       string            `json:"type"`
	AgentName  string            `json:"agent_name,omitempty"`
	RunPath    string            `json:"run_path,omitempty"`
	Content    string            `json:"content,omitempty"`
	ToolCalls  []schema.ToolCall `json:"tool_calls,omitempty"`
	ActionType string            `json:"action_type,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// ProcessAgentEvent 把 AgentEvent 转换成若干 SSE 事件写到流上。
//
// 注意：历史消息的持久化不再由本函数负责。多轮对话的 messages 由 eino SDK 内部
// ChatModelAgent 维护，调用方应当通过 ChatModelAgentMiddleware.AfterAgent 拿到
// 完整的 state.Messages 并保存（参见 internal/session.PersistMiddleware）。
// 这样能从根本上避免 "tool call result does not follow tool call (2013)" 错误。
func ProcessAgentEvent(_ context.Context, s *sse.Stream, event *adk.AgentEvent) error {
	if event.Err != nil {
		return SendSSEEvent(s, SSEEvent{
			Type:      "error",
			AgentName: event.AgentName,
			RunPath:   formatRunPath(event.RunPath),
			Error:     event.Err.Error(),
		})
	}

	if event.Output != nil && event.Output.MessageOutput != nil {
		if err := handleMessageOutput(s, event); err != nil {
			return err
		}
	}

	if event.Action != nil {
		if err := handleAction(s, event); err != nil {
			return err
		}
	}
	return nil
}

func handleMessageOutput(s *sse.Stream, event *adk.AgentEvent) error {
	msgOutput := event.Output.MessageOutput
	if msg := msgOutput.Message; msg != nil {
		return handleRegularMessage(s, event, msg)
	}
	if stream := msgOutput.MessageStream; stream != nil {
		return handleStreamingMessage(s, event, stream)
	}
	return nil
}

func handleRegularMessage(s *sse.Stream, event *adk.AgentEvent, msg *schema.Message) error {
	eventType := "message"
	if msg.Role == schema.Tool {
		eventType = "tool_result"
	}
	ev := SSEEvent{
		Type:      eventType,
		AgentName: event.AgentName,
		RunPath:   formatRunPath(event.RunPath),
		Content:   msg.Content,
	}
	if len(msg.ToolCalls) > 0 {
		ev.ToolCalls = msg.ToolCalls
	}
	return SendSSEEvent(s, ev)
}

// handleStreamingMessage 纯转发：每个 chunk 按角色原样推到 SSE。
//
// 完整 messages 的"原貌还原"由 ChatModelAgentMiddleware.AfterAgent 提供。
func handleStreamingMessage(s *sse.Stream, event *adk.AgentEvent, stream *schema.StreamReader[*schema.Message]) error {
	toolCallsMap := make(map[int][]*schema.Message)

	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return SendSSEEvent(s, SSEEvent{
				Type:      "error",
				AgentName: event.AgentName,
				RunPath:   formatRunPath(event.RunPath),
				Error:     fmt.Sprintf("stream error: %v", err),
			})
		}

		if chunk.Content != "" {
			eventType := "stream_chunk"
			if chunk.Role == schema.Tool {
				eventType = "tool_result_chunk"
			}
			if err := SendSSEEvent(s, SSEEvent{
				Type:      eventType,
				AgentName: event.AgentName,
				RunPath:   formatRunPath(event.RunPath),
				Content:   chunk.Content,
			}); err != nil {
				return err
			}
		}

		if len(chunk.ToolCalls) > 0 {
			for _, tc := range chunk.ToolCalls {
				if tc.Index != nil {
					toolCallsMap[*tc.Index] = append(toolCallsMap[*tc.Index], &schema.Message{
						Role: chunk.Role,
						ToolCalls: []schema.ToolCall{
							{
								ID:    tc.ID,
								Type:  tc.Type,
								Index: tc.Index,
								Function: schema.FunctionCall{
									Name:      tc.Function.Name,
									Arguments: tc.Function.Arguments,
								},
							},
						},
					})
				}
			}
		}
	}

	for _, msgs := range toolCallsMap {
		concatenatedMsg, err := schema.ConcatMessages(msgs)
		if err != nil {
			return err
		}
		if err := SendSSEEvent(s, SSEEvent{
			Type:      "tool_calls",
			AgentName: event.AgentName,
			RunPath:   formatRunPath(event.RunPath),
			ToolCalls: concatenatedMsg.ToolCalls,
		}); err != nil {
			return err
		}
	}
	return nil
}

func handleAction(s *sse.Stream, event *adk.AgentEvent) error {
	action := event.Action
	if action.TransferToAgent != nil {
		return SendSSEEvent(s, SSEEvent{
			Type:       "action",
			AgentName:  event.AgentName,
			RunPath:    formatRunPath(event.RunPath),
			ActionType: "transfer",
			Content:    action.TransferToAgent.DestAgentName,
		})
	}
	if action.Interrupted != nil {
		for _, ic := range action.Interrupted.InterruptContexts {
			content := fmt.Sprintf("%v", ic.Info)
			if stringer, ok := ic.Info.(fmt.Stringer); ok {
				content = stringer.String()
			}
			if err := SendSSEEvent(s, SSEEvent{
				Type:       "action",
				AgentName:  event.AgentName,
				RunPath:    formatRunPath(event.RunPath),
				ActionType: "interrupted",
				Content:    content,
			}); err != nil {
				return err
			}
		}
	}
	if action.Exit {
		return SendSSEEvent(s, SSEEvent{
			Type:       "action",
			AgentName:  event.AgentName,
			RunPath:    formatRunPath(event.RunPath),
			ActionType: "exit",
			Content:    "Agent execution completed",
		})
	}
	return nil
}

func SendSSEEvent(s *sse.Stream, event SSEEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal SSE event: %w", err)
	}
	return s.Publish(&sse.Event{Data: data})
}

func formatRunPath(runPath []adk.RunStep) string {
	return fmt.Sprintf("%v", runPath)
}
