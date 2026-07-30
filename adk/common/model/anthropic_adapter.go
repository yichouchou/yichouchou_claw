package model

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

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
		// 优先用新版字段 UserInputMultiContent;向后兼容旧字段 MultiContent。
		blocks := make([]anthropic.ContentBlockParamUnion, 0)
		if msg.Content != "" {
			blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
		}
		for _, p := range append([]schema.MessageInputPart{}, msg.UserInputMultiContent...) {
			if b, ok := a.convertInputPartToBlock(p); ok {
				blocks = append(blocks, b)
			}
		}
		return anthropic.NewUserMessage(blocks...)

	case schema.Assistant:
		// 助手消息可能包含 tool_calls + 文本 + 多模态输出
		content := make([]anthropic.ContentBlockParamUnion, 0)

		// 文本内容
		if msg.Content != "" {
			content = append(content, anthropic.NewTextBlock(msg.Content))
		}

		// 多模态输出（新版字段 AssistantGenMultiContent）
		// 注：Anthropic assistant 端多模态一般只在 tool_use 中出现；这里把图片/text 一并塞回去,
		// 大多数场景下只有 text 在这一侧,图片多用于 user 端。
		for _, p := range msg.AssistantGenMultiContent {
			if b, ok := a.convertOutputPartToBlock(p); ok {
				content = append(content, b)
			}
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
		//
		// 多模态支持(2026-07-29):
		//   EnhancedInvokableTool 返回的 *schema.ToolResult 会被 eino ToolsNode
		//   转成 ToolMessage,内容拆到 UserInputMultiContent(参见 eino compose
		//   tool_node.go:1128)。所以这里要遍历 UserInputMultiContent,把
		//   image/file parts 转成 Anthropic image block,跟 text tool_result 并列。
		//
		// Anthropic 协议下,tool_result block 必须是 single content,不能含 image。
		// 但"image + tool_result"可以按"先 tool_result,再 image block"的方式
		// 并列在同一 user message 里。Anthropic SDK 是按 user message 拆的,
		// 这里我们用 user message content 列表包含 tool_result + images。
		blocks := []anthropic.ContentBlockParamUnion{
			anthropic.NewToolResultBlock(
				msg.ToolCallID,
				msg.Content,
				false, // isError: 工具执行成功
			),
		}
		for _, p := range msg.UserInputMultiContent {
			if b, ok := a.convertInputPartToBlock(p); ok {
				blocks = append(blocks, b)
			}
		}
		return anthropic.MessageParam{
			Role:    anthropic.MessageParamRoleUser,
			Content: blocks,
		}
	}

	return anthropic.MessageParam{}
}

// convertInputPartToBlock 把 schema.MessageInputPart 转成 Anthropic content block。
//
// 当前支持的类型(2026-07-30 多模态补全)：
//   - Text         → TextBlock
//   - ImageURL     → ImageBlock (URL 或 base64 都走 NewImageBlock;Anthropic SDK 自动分发)
//   - FileURL      → DocumentBlock (PDF + 纯文本,2026-07-30 新增)
//   - MIMEType=application/pdf         → Base64PDFSourceParam 或 URLPDFSourceParam
//   - MIMEType=text/plain|markdown     → PlainTextSourceParam(Anthropic 内部解析文档结构)
//   - 其他 MIME                         → PlainTextSourceParam(纯文本 fallback)
//   - AudioURL     → 暂不支持
//   - VideoURL     → 暂不支持
//
// 返回 ok=false 表示该类型暂不支持,调用方应当跳过该 part。
func (a *AnthropicAdapter) convertInputPartToBlock(p schema.MessageInputPart) (anthropic.ContentBlockParamUnion, bool) {
	switch p.Type {
	case schema.ChatMessagePartTypeText:
		if p.Text == "" {
			return anthropic.ContentBlockParamUnion{}, false
		}
		return anthropic.NewTextBlock(p.Text), true

	case schema.ChatMessagePartTypeImageURL:
		if p.Image == nil {
			return anthropic.ContentBlockParamUnion{}, false
		}
		return buildAnthropicImageBlock(p.Image), true

	case schema.ChatMessagePartTypeFileURL:
		if p.File == nil {
			return anthropic.ContentBlockParamUnion{}, false
		}
		return buildAnthropicDocumentBlock(p.File), true

	default:
		log.Printf("[AnthropicAdapter] unsupported input part type=%q, skipping", p.Type)
		return anthropic.ContentBlockParamUnion{}, false
	}
}

// convertOutputPartToBlock 把 schema.MessageOutputPart 转成 Anthropic content block。
//
// 当前仅支持 text/image,其他类型(Reasoning 等)跳过。
func (a *AnthropicAdapter) convertOutputPartToBlock(p schema.MessageOutputPart) (anthropic.ContentBlockParamUnion, bool) {
	switch p.Type {
	case schema.ChatMessagePartTypeText:
		if p.Text == "" {
			return anthropic.ContentBlockParamUnion{}, false
		}
		return anthropic.NewTextBlock(p.Text), true
	case schema.ChatMessagePartTypeImageURL:
		if p.Image == nil {
			return anthropic.ContentBlockParamUnion{}, false
		}
		// assistant 端的 image base 结构与 input 相同,可以复用 buildAnthropicImageBlock
		return buildAnthropicImageFromOutput(p.Image), true
	default:
		return anthropic.ContentBlockParamUnion{}, false
	}
}

// buildAnthropicImageBlock 把 MessageInputImage 转成 Anthropic image block。
//
// 行为(2026-07-29 修复):
//   - 如果 URL 是 "data:<mime>;base64,<data>" → 拆成 Base64Data + MIMEType,走
//     base64 source(避免 Anthropic 把 data URL 当 http URL 去 fetch 而被拒)
//   - 否则 URL 是 http(s) URL → 走 URL image source
//   - 否则 Base64Data + MIMEType 已有 → 直接走 base64 source
//   - 都没有 → 返回空(并打日志)
func buildAnthropicImageBlock(img *schema.MessageInputImage) anthropic.ContentBlockParamUnion {
	if img == nil {
		return anthropic.ContentBlockParamUnion{}
	}
	// === 2026-07-29 修复: data URL 拆解 ===
	// 前端 /upload?mode=inline 返回的 data:image/png;base64,XXX 会进入 img.URL。
	// Anthropic 的 URL image source 不接受 data: 协议,会报
	// "disallowed url: data:..." 错误。所以这里把 data: URL 主动拆成 base64 source。
	if img.URL != nil && *img.URL != "" {
		if strings.HasPrefix(*img.URL, "data:") {
			mime, b64 := parseDataURL(*img.URL)
			if b64 != "" && mime != "" {
				return anthropic.NewImageBlockBase64(mime, b64)
			}
			log.Printf("[AnthropicAdapter] data URL malformed, falling through to URL source")
		}
		return anthropic.NewImageBlock(anthropic.URLImageSourceParam{URL: *img.URL})
	}
	if img.Base64Data != nil && *img.Base64Data != "" && img.MIMEType != "" {
		return anthropic.NewImageBlockBase64(img.MIMEType, *img.Base64Data)
	}
	log.Printf("[AnthropicAdapter] image part missing url/base64data/mimetype, skipping")
	return anthropic.ContentBlockParamUnion{}
}

// parseDataURL 解析 "data:<mime>[;param];base64,<data>" 格式,返回 (mime, base64data)。
// 不支持 / 不规范的格式返回 ("", "")。
func parseDataURL(s string) (string, string) {
	// data:[<mediatype>][;base64],<data>
	const prefix = "data:"
	if !strings.HasPrefix(s, prefix) {
		return "", ""
	}
	rest := s[len(prefix):]
	commaIdx := strings.Index(rest, ",")
	if commaIdx < 0 {
		return "", ""
	}
	header := rest[:commaIdx]
	payload := rest[commaIdx+1:]
	// 仅识别 ;base64 形式(其它如 ;charset=utf-8 不直接支持,留扩展位)
	isBase64 := strings.HasSuffix(header, ";base64")
	if !isBase64 {
		return "", ""
	}
	mime := strings.TrimSuffix(header, ";base64")
	if mime == "" {
		mime = "application/octet-stream"
	}
	return mime, payload
}

// buildAnthropicDocumentBlock 把 MessageInputFile 转成 Anthropic DocumentBlock。
//
// 2026-07-30 新增: 把 FileURL part 真的传给 Anthropic(之前会被 default 丢)。
//
// 分支策略(根据 MIMEType):
//   - application/pdf:
//     1) Base64Data → Base64PDFSourceParam
//     2) http(s) URL → URLPDFSourceParam(Anthropic 自己 fetch,本机 URL 不行——
//     与 image 路径一样需要外部可达)
//     3) 都没有 → log + skip
//   - text/plain / text/markdown / text/html:
//     1) Base64Data → 解 base64 得字符串 → PlainTextSourceParam
//     2) URL       → 不直接支持(PlainTextSource 没 URL 字段);走 base64 路径或 skip
//     3) 都没有 → skip
//   - 其他 MIME:
//     走 PlainText fallback 或 skip(避免 Anthropic 拒绝未知 MIME)
//
// 注意: 跟 image 路径相同——如果用户传 "data:application/pdf;base64,..." 进 URL 字段,
// 我们也走 parseDataURL 拆出来;保证 LLM 看到的都是 base64 source,不被本地 URL 卡住。
func buildAnthropicDocumentBlock(file *schema.MessageInputFile) anthropic.ContentBlockParamUnion {
	if file == nil {
		return anthropic.ContentBlockParamUnion{}
	}
	mime := file.MIMEType
	base64Data := ""
	if file.Base64Data != nil {
		base64Data = *file.Base64Data
	}
	urlVal := ""
	if file.URL != nil {
		urlVal = *file.URL
	}

	// === 兼容性: 把 data: URL 拆解,统一走 base64 路径 ===
	if strings.HasPrefix(urlVal, "data:") {
		parsedMime, parsedB64 := parseDataURL(urlVal)
		if parsedB64 != "" {
			if mime == "" {
				mime = parsedMime
			}
			base64Data = parsedB64
			urlVal = ""
		}
	}

	// === 按 MIME 分发 ===
	switch {
	case strings.HasPrefix(mime, "application/pdf"):
		// PDF 路径: 优先 base64 → 兜底 http(s) URL
		if base64Data != "" {
			log.Printf("[AnthropicAdapter] document pdf base64 size=%d mime=%s", len(base64Data), mime)
			return anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
				Data: base64Data, // Base64PDFSourceParam.Data 是 string,直接传 base64 字符串
			})
		}
		if strings.HasPrefix(urlVal, "http://") || strings.HasPrefix(urlVal, "https://") {
			log.Printf("[AnthropicAdapter] document pdf URL=%s", urlVal)
			return anthropic.NewDocumentBlock(anthropic.URLPDFSourceParam{
				URL: urlVal,
			})
		}
		log.Printf("[AnthropicAdapter] PDF part missing base64data/url, skipping")
		return anthropic.ContentBlockParamUnion{}

	case strings.HasPrefix(mime, "text/"):
		// 文本路径: base64 → 字符串;URL 不直接支持
		if base64Data != "" {
			decoded, derr := decodeBase64String(base64Data)
			if derr != nil {
				log.Printf("[AnthropicAdapter] text document base64 decode failed: %v", derr)
				return anthropic.ContentBlockParamUnion{}
			}
			log.Printf("[AnthropicAdapter] document text mime=%s size=%d", mime, len(decoded))

			// === 2026-07-30: 让 LLM 知道文本格式语义 ===
			//
			// Anthropic PlainTextSourceParam 只接受 MediaType=text/plain,
			// 这意味着我们无法把 mime=text/markdown / text/html 精确传给 LLM。
			// 但模型是 markdown / html 双语料训练的,我们给内容加个 "格式提示前缀",
			// 让 LLM 明确把内容当 markdown/html 解析,而不是 raw text。
			//
			// 例如用户问 "总结这份 md 的大纲",模型就知道按 markdown 结构去读。
			prefix := textFormatHint(mime)

			return anthropic.NewDocumentBlock(anthropic.PlainTextSourceParam{
				Data: prefix + decoded,
				// MediaType 默认 text/plain,这里就算 SDK 不让也没办法。
			})
		}
		log.Printf("[AnthropicAdapter] text document missing base64data, skipping (URL not supported)")
		return anthropic.ContentBlockParamUnion{}

	default:
		// 未知 MIME: 兜底作为纯文本(如果能 base64 decode 出来)
		if base64Data != "" {
			decoded, derr := decodeBase64String(base64Data)
			if derr != nil {
				log.Printf("[AnthropicAdapter] unknown mime=%s base64 invalid, skipping", mime)
				return anthropic.ContentBlockParamUnion{}
			}
			log.Printf("[AnthropicAdapter] document fallback plain mime=%s size=%d", mime, len(decoded))
			return anthropic.NewDocumentBlock(anthropic.PlainTextSourceParam{
				Data: decoded,
			})
		}
		log.Printf("[AnthropicAdapter] unknown mime=%s missing base64, skipping", mime)
		return anthropic.ContentBlockParamUnion{}
	}
}

// textFormatHint 根据 mime 给出提示前缀,让 LLM 知道文本格式。
//
// 返回 "" 表示无需提示(纯文本 / log / 未知 mime)。
// 返回 "<hint>\n" 表示在前缀后追加内容。
//
// 用法示例:
//
//	mime=text/markdown  → 返回 "以下内容是 Markdown 格式,请按 Markdown 语法识别:\n"
//	mime=text/html      → 返回 "以下内容是 HTML 源码,请按 HTML 结构识别:\n"
//	mime=text/plain     → 返回 ""
//	mime=log/text-log   → 返回 ""
func textFormatHint(mime string) string {
	switch mime {
	case "text/markdown":
		return "以下内容是 Markdown 格式,请按 Markdown 语法(标题、列表、代码块、链接等)识别:\n\n"
	case "text/html":
		return "以下内容是 HTML 源代码,请按 HTML 结构(标签、属性、文本节点)识别,而不是渲染后的可视化样式:\n\n"
	case "text/plain":
		return ""
	default:
		return ""
	}
}

// decodeBase64String 把标准 / URL-safe base64 编码的字符串解码回原文。
//
// Anthropic 文档说: Anthropic API 接受 standard or URL-safe base64(without padding)。
// 我们这里用 std(strict)— padding 缺失会自动容错。
func decodeBase64String(s string) (string, error) {
	// 数据是 "data:..." 前缀后面的纯 base64,可能没有 padding
	// 补全 padding
	padded := s
	if mod := len(padded) % 4; mod != 0 {
		padded += strings.Repeat("=", 4-mod)
	}
	raw, err := base64.StdEncoding.DecodeString(padded)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// buildAnthropicImageFromOutput 与 buildAnthropicImageBlock 同形,只接收 MessageOutputImage。
func buildAnthropicImageFromOutput(img *schema.MessageOutputImage) anthropic.ContentBlockParamUnion {
	if img == nil {
		return anthropic.ContentBlockParamUnion{}
	}
	if img.URL != nil && *img.URL != "" {
		return anthropic.NewImageBlock(anthropic.URLImageSourceParam{URL: *img.URL})
	}
	if img.Base64Data != nil && *img.Base64Data != "" && img.MIMEType != "" {
		return anthropic.NewImageBlockBase64(img.MIMEType, *img.Base64Data)
	}
	return anthropic.ContentBlockParamUnion{}
}

// convertResponse 将 Anthropic 响应转换为 eino Message
//
// 2026-07-29 多模态版:
//   - 把每个 TextBlock 同时记录到 AssistantGenMultiContent(便于 SSE 推送)
//   - 处理 server-tool (web_search) 的结果块:把搜索结果 URL 列表转成特殊的
//     "search_citation" part,前端可以渲染为"参考来源"列表
func (a *AnthropicAdapter) convertResponse(msg *anthropic.Message) (*schema.Message, error) {
	content := ""
	toolCalls := make([]schema.ToolCall, 0)
	multiParts := make([]schema.MessageOutputPart, 0)

	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			content += b.Text
			// 同时把 text part 也记录到 AssistantGenMultiContent,确保下游 SSE 推送时
			// 看到完整的 part 列表(而不是只看到合并后的 Content 字符串)。
			multiParts = append(multiParts, schema.MessageOutputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: b.Text,
			})
		case anthropic.ToolUseBlock:
			toolCalls = append(toolCalls, schema.ToolCall{
				ID: b.ID,
				Function: schema.FunctionCall{
					Name:      b.Name,
					Arguments: string(b.Input),
				},
			})
			// 默认行为:tool_use 不会出现在 AssistantGenMultiContent 里(它是结构化
			// 工具调用,不是"多模态 part")。这里仅记录 tool_calls 数组。
		case anthropic.WebSearchToolResultBlock:
			// server-tool (web_search) 的执行结果:Content 是 union,OfWebSearchResultBlockArray
			// 含若干 search result 块(URL + title + snippet + encrypted_content)。
			// 2026-07-29 注:ContentUnion 没有 AsAny() 方法,直接访问字段。
			for _, searchResult := range b.Content.OfWebSearchResultBlockArray {
				title := searchResult.Title
				url := searchResult.URL
				if url == "" {
					continue
				}
				multiParts = append(multiParts, schema.MessageOutputPart{
					Type: schema.ChatMessagePartTypeText,
					Text: "🔗 [" + title + "](" + url + ")",
				})
			}
			// 注意: 信息已经在 assistant 文本回复中被引用,这里只是把搜索来源显式
			// 推到 SSE 前端。前端用 text part 渲染为"参考来源"区块。
		}
	}

	// 合并相邻 text parts,避免一条 message 出现 N 个连续 text part
	merged := mergeConsecutiveTextParts(multiParts)

	return &schema.Message{
		Role:                     schema.Assistant,
		Content:                  content,
		ToolCalls:                toolCalls,
		AssistantGenMultiContent: merged,
	}, nil
}

// mergeConsecutiveTextParts 把相邻的 text part 合并成一个(避免 SSE 推送时刷屏)。
func mergeConsecutiveTextParts(parts []schema.MessageOutputPart) []schema.MessageOutputPart {
	if len(parts) <= 1 {
		return parts
	}
	out := make([]schema.MessageOutputPart, 0, len(parts))
	for _, p := range parts {
		if len(out) > 0 && out[len(out)-1].Type == schema.ChatMessagePartTypeText && p.Type == schema.ChatMessagePartTypeText {
			out[len(out)-1].Text += p.Text
			continue
		}
		out = append(out, p)
	}
	return out
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
