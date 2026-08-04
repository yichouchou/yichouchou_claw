package message

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/hertz-contrib/sse"
)

// SSEEvent 是推送给前端的单个事件载荷。
//
// 多模态字段（2026-07-29 新增）：
//   - MultiContent: 消息的多模态 parts（图片/文件/音频/视频）。前端按 part.Type
//     分别渲染：text → 文本；image_url → <img>；file_url → <a> 下载。
//   - Files: 工具调用返回的附件（PDF/截图/日志）。前端展示为下载链接。
//
// 与 Content 字段的关系：Content 仍是所有 text parts 拼起来的扁平字符串，
// 用于保持向后兼容；MultiContent 保留完整结构让前端可以还原图文混排。
type SSEEvent struct {
	Type         string              `json:"type"`
	AgentName    string              `json:"agent_name,omitempty"`
	RunPath      string              `json:"run_path,omitempty"`
	Content      string              `json:"content,omitempty"`
	ToolCalls    []schema.ToolCall   `json:"tool_calls,omitempty"`
	ActionType   string              `json:"action_type,omitempty"`
	Error        string              `json:"error,omitempty"`
	MultiContent []MessageOutputPart `json:"multi_content,omitempty"`
	Files        []MessageOutputFile `json:"files,omitempty"`
	// Attachment 是 2026-08-03 IPC 通道新增的"工具产物附件事件"。
	// 字段是 message.MessageAttachmentEvent 的内部类型,前端按 type 渲染 (<img>/<video>/<audio>/<a>)。
	// 与 LLM 上下文零耦合:LLM 推理不会看到 URL/base64,只产生附件 event。
	//
	// 2026-08-04 复盘: 必须用 **指针** 类型, 不能用值类型 — Go json marshaller
	//   对值类型 struct 不识别 omitempty, 即便字段全零, 也会序列化成
	//   `"attachment":{"type":"","url":"","mime_type":"","size":0,"name":""}`,
	//   污染每一个 SSE event, 让前端在 stream_chunk 上也看到 `data.attachment` 存在,
	//   干扰 routing / 触发 `if (data.attachment) renderAttachmentEvent(data.attachment)`
	//   误进入 attachment 分支 (data.attachment.url 是空字符串, 触发 `if (!ev.url) return`,
	//   但**仍消耗**了 stream_chunk 的渲染机会 — 因为 case 顺序是 'attachment' 先于
	//   'stream_chunk' 都没匹配, 后续 message 也跟着丢)。
	//
	//   修复: 改为 `*AttachmentEvent`, nil 时不序列化。
	Attachment *AttachmentEvent `json:"attachment,omitempty"`
}

// AttachmentEvent 推给前端的附件事件 (2026-08-03 IPC 模式新增)。
//
// 关键:这是 SSE 事件类型,前端直接接收。前端按 type 渲染:
//   - "image" → <img src=URL>
//   - "audio" → <audio controls src=URL>
//   - "video" → <video controls src=URL>
//   - "file"  → <a href=URL> 下载链接
//
// model UUID vs 业务:URL 是 relative path(/api/attachment/<token>/<name>),
//前端直接 fetch 同源,不需要跨域配置。
type AttachmentEvent struct {
	Type         string `json:"type"`                    // image / audio / video / file
	URL          string `json:"url"`                     // /api/attachment/<token>/<name>
	AbsoluteURL  string `json:"absolute_url,omitempty"`  // 备用:http://host:port/... 完整 URL
	MIMEType     string `json:"mime_type"`
	Size         int64  `json:"size"`
	Name         string `json:"name"`
	OriginalPath string `json:"original_path,omitempty"`
	Source       string `json:"source,omitempty"`         // 哪类工具产出的("local_command")
}

// MessageOutputPart 是 SSE 事件的多模态 part（精简版 schema.MessageOutputPart）。
//
// 仅在 SSE 层使用，传输更友好；前端可以解析成 <img>/<video>/<a> 等。
// 完整字段在 schema.MessageOutputPart 内（避免循环依赖）。
type MessageOutputPart struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	URL     string `json:"url,omitempty"`
	MIME    string `json:"mime,omitempty"`
	Name    string `json:"name,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// MessageOutputFile 是工具调用返回的附件。
type MessageOutputFile struct {
	Name   string `json:"name"`
	URL    string `json:"url,omitempty"`
	Data   string `json:"data,omitempty"` // base64 内联（小文件）
	MIME   string `json:"mime,omitempty"`
	Source string `json:"source,omitempty"` // 来源 tool 名
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
		Type:         eventType,
		AgentName:    event.AgentName,
		RunPath:      formatRunPath(event.RunPath),
		Content:      msg.Content,
		MultiContent: convertOutputPartsToSSE(msg.AssistantGenMultiContent),
	}
	// === 2026-07-30: 工具返回的多模态 part(image/pdf)推给前端 ===
	//
	// 背景: local_command 工具检出 PNG / PDF 产物后,会以 schema.ToolOutputPart
	// 形式塞回 tool message。eino 框架会把这部分内容放到 msg.UserInputMultiContent
	// (因为 tool 消息作为"user input to model"被组装时,多模态 part 走的就是这个字段)。
	//
	// 之前: 这部分只在 LLM 上下文里,前端看不到。
	// 现在: 我们把它转成 SSE 的 multi_content 字段,前端 addToolResult 流程会在
	// assistant 消息下追加 <img>/<a>。
	//
	// 同时也支持 msg.MultiContent(老式 schema,留兼容)。
	if msg.Role == schema.Tool {
		ev.MultiContent = append(ev.MultiContent,
			convertInputPartsToSSE(msg.UserInputMultiContent)...)
		ev.MultiContent = append(ev.MultiContent,
			convertOldMultiContent(convertMultiContentToInputParts(msg.MultiContent))...)
	}
	if len(msg.ToolCalls) > 0 {
		ev.ToolCalls = msg.ToolCalls
	}
	return SendSSEEvent(s, ev)
}

// convertInputPartsToSSE 把 schema.MessageInputPart 转成 SSE MessageOutputPart。
//
// 主要把 image_url / file_url 这两类带 base64 或 URL 的 part 暴露给前端。
func convertInputPartsToSSE(parts []schema.MessageInputPart) []MessageOutputPart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]MessageOutputPart, 0, len(parts))
	for _, p := range parts {
		sp := MessageOutputPart{
			Type: string(p.Type),
			Text: p.Text,
		}
		if p.Image != nil {
			if p.Image.Base64Data != nil && p.Image.MIMEType != "" {
				sp.URL = "data:" + p.Image.MIMEType + ";base64," + *p.Image.Base64Data
			} else if p.Image.URL != nil {
				sp.URL = *p.Image.URL
			}
			sp.MIME = p.Image.MIMEType
		} else if p.File != nil {
			if p.File.Base64Data != nil && p.File.MIMEType != "" {
				sp.URL = "data:" + p.File.MIMEType + ";base64," + *p.File.Base64Data
			} else if p.File.URL != nil {
				sp.URL = *p.File.URL
			}
			sp.MIME = p.File.MIMEType
		}
		out = append(out, sp)
	}
	return out
}

// convertMultiContentToInputParts 把老的 ChatMessagePart 转成 MessageInputPart。
//
// schema.Message.MultiContent(已 deprecated)里也可能在 tool 消息下藏着图片 part;
// 转换是为了兼容一些早期 eino 路径。
//
// 老的 ChatMessageImageURL / ChatMessageFileURL 只含 URL / URI / MIMEType
// (URL 本身可以是 data:<mime>;base64,...) — 没有专门的 Base64Data 字段,
// 解析 base64 需要从 URL 字符串里手动抠。
func convertMultiContentToInputParts(mc []schema.ChatMessagePart) []schema.MessageInputPart {
	if len(mc) == 0 {
		return nil
	}
	out := make([]schema.MessageInputPart, 0, len(mc))
	for _, p := range mc {
		switch p.Type {
		case schema.ChatMessagePartTypeImageURL:
			if p.ImageURL == nil {
				continue
			}
			img := &schema.MessageInputImage{}
			if p.ImageURL.URL != "" {
				url := p.ImageURL.URL
				img.URL = &url
				// 剥 data:image/xxx;base64, 前缀,如有
				if b64, ok := extractBase64FromDataURL(p.ImageURL.URL); ok {
					img.Base64Data = &b64
				}
			}
			if p.ImageURL.URI != "" {
				uri := p.ImageURL.URI
				img.URL = &uri
			}
			img.MIMEType = p.ImageURL.MIMEType
			out = append(out, schema.MessageInputPart{
				Type:  p.Type,
				Image: img,
			})
		case schema.ChatMessagePartTypeFileURL:
			if p.FileURL == nil {
				continue
			}
			f := &schema.MessageInputFile{
				Name: p.FileURL.Name,
			}
			if p.FileURL.URL != "" {
				url := p.FileURL.URL
				f.URL = &url
				if b64, ok := extractBase64FromDataURL(p.FileURL.URL); ok {
					f.Base64Data = &b64
				}
			}
			if p.FileURL.URI != "" {
				uri := p.FileURL.URI
				f.URL = &uri
			}
			f.MIMEType = p.FileURL.MIMEType
			out = append(out, schema.MessageInputPart{
				Type: p.Type,
				File: f,
			})
		}
	}
	return out
}

// extractBase64FromDataURL 从 "data:image/png;base64,XXXXX" 抽出 base64 body。
func extractBase64FromDataURL(s string) (string, bool) {
	const prefix = "base64,"
	i := strings.Index(s, prefix)
	if i < 0 {
		return "", false
	}
	return s[i+len(prefix):], true
}

// convertOldMultiContent 把老版 ChatMessagePart 直接转成 SSE 输出。
func convertOldMultiContent(parts []schema.MessageInputPart) []MessageOutputPart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]MessageOutputPart, 0, len(parts))
	for _, p := range parts {
		sp := MessageOutputPart{
			Type: string(p.Type),
			Text: p.Text,
		}
		if p.Image != nil {
			if p.Image.Base64Data != nil && p.Image.MIMEType != "" {
				sp.URL = "data:" + p.Image.MIMEType + ";base64," + *p.Image.Base64Data
			} else if p.Image.URL != nil {
				sp.URL = *p.Image.URL
			}
			sp.MIME = p.Image.MIMEType
		} else if p.File != nil {
			if p.File.Base64Data != nil && p.File.MIMEType != "" {
				sp.URL = "data:" + p.File.MIMEType + ";base64," + *p.File.Base64Data
			} else if p.File.URL != nil {
				sp.URL = *p.File.URL
			}
			sp.MIME = p.File.MIMEType
		}
		out = append(out, sp)
	}
	return out
}

// convertOutputPartsToSSE 把 eino schema.MessageOutputPart 转成 SSE 层的精简结构。
//
// 只透传 URL/MIME/Text 等元数据;base64 内联数据如果超过 1KB 就只发摘要,
// 避免 SSE 事件过大阻塞 stream 流。
func convertOutputPartsToSSE(parts []schema.MessageOutputPart) []MessageOutputPart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]MessageOutputPart, 0, len(parts))
	for _, p := range parts {
		sp := MessageOutputPart{
			Type: string(p.Type),
			Text: p.Text,
		}
		if p.Image != nil {
			if p.Image.URL != nil {
				sp.URL = *p.Image.URL
			}
			sp.MIME = p.Image.MIMEType
		} else if p.Audio != nil {
			if p.Audio.URL != nil {
				sp.URL = *p.Audio.URL
			}
			sp.MIME = p.Audio.MIMEType
		} else if p.Video != nil {
			if p.Video.URL != nil {
				sp.URL = *p.Video.URL
			}
			sp.MIME = p.Video.MIMEType
		}
		out = append(out, sp)
	}
	return out
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

		if chunk.Content != "" || len(chunk.AssistantGenMultiContent) > 0 {
			eventType := "stream_chunk"
			if chunk.Role == schema.Tool {
				eventType = "tool_result_chunk"
			}
			if err := SendSSEEvent(s, SSEEvent{
				Type:         eventType,
				AgentName:    event.AgentName,
				RunPath:      formatRunPath(event.RunPath),
				Content:      chunk.Content,
				MultiContent: convertOutputPartsToSSE(chunk.AssistantGenMultiContent),
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

	// Fallback：streaming chunk 里如果完全没下发 ToolCalls（某些 LLM 后端 / 模型
	// 在 streaming 模式下只回 content 不回 tool_calls），上面的累积 map 是空的，
	// 就直接拿 event.Output.MessageOutput.Message（非 streaming 路径下 eino 会
	// 把完整 message 放在这里）兜底提取 tool_calls 并推一次 SSE。
	if len(toolCallsMap) == 0 && event.Output != nil && event.Output.MessageOutput != nil {
		if m := event.Output.MessageOutput.Message; m != nil && len(m.ToolCalls) > 0 {
			log.Printf("[streamMessageOutput] fallback tool_calls from event.Message count=%d agent=%s",
				len(m.ToolCalls), event.AgentName)
			if err := SendSSEEvent(s, SSEEvent{
				Type:      "tool_calls",
				AgentName: event.AgentName,
				RunPath:   formatRunPath(event.RunPath),
				ToolCalls: m.ToolCalls,
			}); err != nil {
				return err
			}
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
