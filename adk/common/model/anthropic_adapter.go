package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// AnthropicAdapter 将 Anthropic SDK 适配到 eino 的 BaseModel 接口。
//
// 用于 ChatAgent，支持 Minimaxi 的服务端工具（web_search）。
type AnthropicAdapter struct {
	client       anthropic.Client
	model        string
	maxTokens    int64
	systemPrompt string
	serverTools  []anthropic.ToolUnionParam // 服务端工具（如 web_search）
	clientTools  []anthropic.ToolUnionParam // 客户端工具（如 skill），从 WithTools 转换而来
}

// NewAnthropicAdapter 创建一个 Anthropic 适配器。
//
// 配置说明：
//   - apiKey: 从 ANTHROPIC_API_KEY 或 OPENAI_API_KEY 环境变量读取
//   - baseURL: 默认 https://api.minimaxi.com/anthropic（Minimaxi 兼容端点）
//   - model: 模型名，如 "MiniMax-M3"
//   - tools: 服务端工具列表，如 web_search
func NewAnthropicAdapter(opts ...AnthropicAdapterOption) *AnthropicAdapter {
	cfg := &anthropicAdapterConfig{
		apiKey:       "", // 从环境变量读取
		baseURL:      "https://api.minimaxi.com/anthropic",
		model:        "MiniMax-M3",
		maxTokens:    4096,
		systemPrompt: "",
		serverTools:  nil,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	// 如果没有设置 apiKey，从环境变量读取（兜底）
	apiKey := cfg.apiKey
	if apiKey == "" {
		apiKey = osGetenv("ANTHROPIC_API_KEY", "OPENAI_API_KEY")
	}

	// 构建客户端选项
	clientOpts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithBaseURL(cfg.baseURL),
	}

	client := anthropic.NewClient(clientOpts...)

	return &AnthropicAdapter{
		client:       client,
		model:        cfg.model,
		maxTokens:    cfg.maxTokens,
		systemPrompt: cfg.systemPrompt,
		serverTools:  cfg.serverTools,
		clientTools:  nil, // 从 WithTools 动态添加
	}
}

type anthropicAdapterConfig struct {
	apiKey       string
	baseURL      string
	model        string
	maxTokens    int64
	systemPrompt string
	serverTools  []anthropic.ToolUnionParam
}

type AnthropicAdapterOption func(*anthropicAdapterConfig)

func WithAPIKey(apiKey string) AnthropicAdapterOption {
	return func(c *anthropicAdapterConfig) {
		c.apiKey = apiKey
	}
}

func WithBaseURL(baseURL string) AnthropicAdapterOption {
	return func(c *anthropicAdapterConfig) {
		c.baseURL = baseURL
	}
}

func WithModel(model string) AnthropicAdapterOption {
	return func(c *anthropicAdapterConfig) {
		c.model = model
	}
}

func WithMaxTokens(maxTokens int64) AnthropicAdapterOption {
	return func(c *anthropicAdapterConfig) {
		c.maxTokens = maxTokens
	}
}

func WithSystemPrompt(prompt string) AnthropicAdapterOption {
	return func(c *anthropicAdapterConfig) {
		c.systemPrompt = prompt
	}
}

func WithServerTools(tools []anthropic.ToolUnionParam) AnthropicAdapterOption {
	return func(c *anthropicAdapterConfig) {
		c.serverTools = tools
	}
}

// Generate 实现 model.BaseModel 接口。
func (a *AnthropicAdapter) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	params := a.buildParams(input, opts...)

	message, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic generate failed: %w", err)
	}

	return a.convertResponse(message)
}

// Stream 实现 model.BaseModel 接口。
func (a *AnthropicAdapter) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	params := a.buildParams(input, opts...)

	stream := a.client.Messages.NewStreaming(ctx, params)

	// 创建 eino 的 StreamReader
	reader, writer := schema.Pipe[*schema.Message](100)

	go func() {
		defer writer.Close()

		accumulated := &anthropic.Message{}
		for stream.Next() {
			event := stream.Current()
			if err := accumulated.Accumulate(event); err != nil {
				log.Printf("[AnthropicAdapter] accumulate event failed: %v", err)
				continue
			}

			// 将增量事件转换为 Message chunk
			chunk := a.convertStreamEvent(event, accumulated)
			if chunk != nil {
				if closed := writer.Send(chunk, nil); closed {
					// channel 已关闭，停止发送
					return
				}
			}
		}

		if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
			log.Printf("[AnthropicAdapter] stream error: %v", err)
		}
	}()

	return reader, nil
}

// buildParams 构建 Anthropic API 参数
func (a *AnthropicAdapter) buildParams(input []*schema.Message, opts ...model.Option) anthropic.MessageNewParams {
	// 提取通用选项
	options := model.GetCommonOptions(nil, opts...)

	// 转换消息
	messages := make([]anthropic.MessageParam, 0, len(input))
	for _, msg := range input {
		anthropicMsg := a.convertMessage(msg)
		if anthropicMsg.Role != "" { // 有效的消息
			messages = append(messages, anthropicMsg)
		}
	}

	// 构建参数
	params := anthropic.MessageNewParams{
		Model:     a.model, // Model 是 string 类型，直接赋值
		MaxTokens: a.maxTokens,
		Messages:  messages,
	}

	// 系统提示（优先使用 option 中的，其次使用配置的）
	systemPrompt := a.systemPrompt
	if options.Model != nil && *options.Model != "" {
		// 如果通过 option 传入了 model，使用它（虽然 system prompt 不在这里）
		// 注意：eino 可能通过其他方式传递 system prompt，这里需要检查
	}
	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt},
		}
	}

	// 合并工具：服务端工具 + 客户端工具
	tools := make([]anthropic.ToolUnionParam, 0, len(a.serverTools)+len(a.clientTools))
	tools = append(tools, a.serverTools...)
	tools = append(tools, a.clientTools...)
	if len(tools) > 0 {
		params.Tools = tools
		log.Printf("[AnthropicAdapter] total tools: %d (server: %d, client: %d)",
			len(tools), len(a.serverTools), len(a.clientTools))
	}

	return params
}

// convertMessage 将 eino Message 转换为 Anthropic MessageParam
func (a *AnthropicAdapter) convertMessage(msg *schema.Message) anthropic.MessageParam {
	if msg == nil {
		return anthropic.MessageParam{}
	}

	switch msg.Role {
	case schema.System:
		// 系统消息在 params.System 中处理，这里返回空
		return anthropic.MessageParam{}

	case schema.User:
		return anthropic.NewUserMessage(anthropic.NewTextBlock(msg.Content))

	case schema.Assistant:
		// 助手消息可能包含 tool_calls
		content := make([]anthropic.ContentBlockParamUnion, 0)

		// 文本内容
		if msg.Content != "" {
			content = append(content, anthropic.NewTextBlock(msg.Content))
		}

		// Tool calls
		for _, tc := range msg.ToolCalls {
			content = append(content, anthropic.NewToolUseBlock(
				tc.ID,
				tc.Function.Name,      // 使用 Function.Name
				tc.Function.Arguments, // 使用 Function.Arguments
			))
		}

		return anthropic.NewAssistantMessage(content...)

	case schema.Tool:
		// Tool result - 手动构造 MessageParam
		return anthropic.MessageParam{
			Role: anthropic.MessageParamRoleUser, // Tool 结果作为 user 消息
			Content: []anthropic.ContentBlockParamUnion{
				anthropic.NewToolResultBlock(
					msg.ToolCallID,
					msg.Content,
					false, // isError: 工具执行成功
				),
			},
		}
	}

	return anthropic.MessageParam{}
}

// convertResponse 将 Anthropic 响应转换为 eino Message
func (a *AnthropicAdapter) convertResponse(msg *anthropic.Message) (*schema.Message, error) {
	content := ""
	toolCalls := make([]schema.ToolCall, 0)

	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			content += b.Text
		case anthropic.ToolUseBlock:
			toolCalls = append(toolCalls, schema.ToolCall{
				ID: b.ID,
				Function: schema.FunctionCall{
					Name:      b.Name,
					Arguments: string(b.Input),
				},
			})
		}
	}

	return &schema.Message{
		Role:      schema.Assistant,
		Content:   content,
		ToolCalls: toolCalls,
	}, nil
}

// convertStreamEvent 将流式事件转换为 Message chunk
func (a *AnthropicAdapter) convertStreamEvent(event anthropic.MessageStreamEventUnion, accumulated *anthropic.Message) *schema.Message {
	// 只在 content_block_delta 事件时返回增量消息
	switch evt := event.AsAny().(type) {
	case anthropic.ContentBlockDeltaEvent:
		switch delta := evt.Delta.AsAny().(type) {
		case anthropic.TextDelta:
			return &schema.Message{
				Role:    schema.Assistant,
				Content: delta.Text,
			}
		}
	}

	return nil
}

// WithTools 实现 model.ToolCallingChatModel 接口。
//
// 将 eino 的客户端工具（schema.ToolInfo）转换为 Anthropic 的 ToolParam，
// 并与服务端工具（web_search）合并。
func (a *AnthropicAdapter) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	if len(tools) == 0 {
		// 没有客户端工具，返回原实例
		return a, nil
	}

	// 转换客户端工具为 Anthropic ToolUnionParam
	clientTools := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, toolInfo := range tools {
		// 将 ParamsOneOf 转换为 JSONSchema
		paramsJSONSchema, err := toolInfo.ParamsOneOf.ToJSONSchema()
		if err != nil {
			log.Printf("[AnthropicAdapter] failed to convert tool %s parameters: %v", toolInfo.Name, err)
			continue
		}

		// 构建 ToolParam
		toolParam := anthropic.ToolParam{
			Name:        toolInfo.Name,
			Description: anthropic.Opt(toolInfo.Desc),
		}

		// 构建 InputSchema（从 JSONSchema）
		inputSchema := anthropic.ToolInputSchemaParam{
			Type: "object", // 默认类型
		}

		// 如果有 JSONSchema，设置 Properties 和 Required
		if paramsJSONSchema != nil {
			if paramsJSONSchema.Properties != nil && paramsJSONSchema.Properties.Len() > 0 {
				// 将 orderedmap 转换为 map
				properties := make(map[string]interface{})
				for pair := paramsJSONSchema.Properties.Oldest(); pair != nil; pair = pair.Next() {
					// 简化处理：直接使用 JSON 序列化
					propBytes, err := json.Marshal(pair.Value)
					if err == nil {
						var propMap map[string]interface{}
						if err := json.Unmarshal(propBytes, &propMap); err == nil {
							properties[pair.Key] = propMap
						}
					}
				}
				inputSchema.Properties = properties
			}
			if len(paramsJSONSchema.Required) > 0 {
				inputSchema.Required = paramsJSONSchema.Required
			}
		}

		toolParam.InputSchema = inputSchema

		clientTools = append(clientTools, anthropic.ToolUnionParam{
			OfTool: &toolParam,
		})

		log.Printf("[AnthropicAdapter] converted client tool: %s", toolInfo.Name)
	}

	// 返回新实例，包含客户端工具
	return &AnthropicAdapter{
		client:       a.client,
		model:        a.model,
		maxTokens:    a.maxTokens,
		systemPrompt: a.systemPrompt,
		serverTools:  a.serverTools,
		clientTools:  clientTools,
	}, nil
}

// osGetenv 按顺序读取多个环境变量名，返回第一个非空值。
//
// AnthropicAdapter 内部兜底用：调用方没显式 WithAPIKey 时按顺序尝试环境变量。
func osGetenv(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}
