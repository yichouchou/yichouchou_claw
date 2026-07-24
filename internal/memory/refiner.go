package memory

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"
)

// chatModel 是 eino components/model 包里 ChatModel 接口的本地抽象。
// 调用方通过 ChatModelAdapter 把 eino ChatModel 适配成本接口,
// 这样 internal/memory 包不直接 import eino components/model。
type chatModel interface {
	Generate(ctx context.Context, msgs []*schema.Message) (*schema.Message, error)
}

// ChatModel 是 eino components/model 包里 ChatModel 接口的本地抽象别名。
// 调用方通过 cmAdapter 把 eino ChatModel 适配成本接口,
// 这样 internal/memory 包不直接 import eino components/model。
type ChatModel = chatModel

// AdapterFunc 把任意实现了"eino-style Generate(ctx, msgs)"的 ChatModel
// 适配成本包的 ChatModel 接口。提供给 main.go / subagents 包使用:
//
//	cm := memory.AdapterFunc(func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
//	    return einoCM.Generate(ctx, msgs)
//	})
type AdapterFunc func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error)

// Generate 满足 chatModel 接口。
func (f AdapterFunc) Generate(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
	return f(ctx, msgs)
}

const refinerLogPrefix = "[memory.refiner]"

// RefineTask 表示一次"用 LLM 重新生成摘要"任务。
//
// 来源:AsyncMarkdownRecorder 在每条 entry 落盘后,投递本任务给 refiner。
//
// 字段对应 file_updater.findAndReplaceSummary 用到的全部输入:
//   - FilePath:      文件绝对路径
//   - SessionID:     浏览器用户会话 ID
//   - LLMTraceID:    配对标识(inputs/outputs 共用)
//   - AgentName:     agent 名称(诊断用)
//   - OriginalSummary: 规则版摘要(refine 失败时仍可见)
//   - ContentSnippet: 内容前 N 字节(送 LLM 用,限制大小)
//   - Kind:          entry 类型(用于 LLM 提示)
//   - EnqueueAt:     入队时间(refine 延迟诊断)
type RefineTask struct {
	FilePath        string
	SessionID       string
	LLMTraceID      LLMTraceID
	AgentName       string
	OriginalSummary string
	ContentSnippet  string
	Kind            string
	EnqueueAt       time.Time

	// RequestGroupID 用于 sessions 类 entry(user_request / user_response)的精修定位。
	// 不是 sessions 类时为空。
	RequestGroupID RequestGroupID

	// Side 标识精修的是 request 还是 response。仅 sessions 类有值("request" / "response")。
	Side string
}

// RefinerConfig 是 refiner 的可调参数。
type RefinerConfig struct {
	// Workers 是并行 LLM 调用 worker 数量。LLM 调用是 IO 密集型,
	// 默认 2 既能保证吞吐,又能避开限流。
	Workers int

	// QueueSize 是 refine 任务 channel 缓冲。满后 enqueue 会被丢弃
	//(refine 不影响主流程,丢弃合理)。
	QueueSize int

	// PerCallTimeout 每次 LLM 调用的超时。默认 30s。
	PerCallTimeout time.Duration

	// ContentSnippetBytes 喂给 LLM 的内容长度。截断防 LLM token 超限。
	ContentSnippetBytes int

	// ModelTemerature refine 用低温度,聚焦"精确摘要"。
	ModelTemperature float32

	// MaxSummaryLength 在 Refine 阶段也强制不超这个字数。
	// 默认 100。
	MaxSummaryLength int

	// Enabled 全局开关。可由环境变量 YICHOUCHOU_LLM_REFINE=off 关闭。
	Enabled bool
}

// DefaultRefinerConfig 返回保守默认值。
func DefaultRefinerConfig() RefinerConfig {
	return RefinerConfig{
		Workers:             2,
		QueueSize:           256,
		PerCallTimeout:      30 * time.Second,
		ContentSnippetBytes: 1500,
		ModelTemperature:    0.2,
		MaxSummaryLength:    MaxSummaryLength,
		Enabled:             true,
	}
}

// LLMRefiner 是"用 LLM 重新生成摘要"的契约。
//
// 接口设计目的:让 refiner 与具体 ChatModel 实现解耦(测试用 fake,
// 生产用 eino ChatModel)。
type LLMRefiner interface {
	// Refine 同步生成一个 ≤MaxSummaryLength 字的精准摘要。
	// 失败返回 error,refiner worker 会 log warning 但不向上抛。
	Refine(ctx context.Context, task RefineTask) (string, error)
}

// refiner 包级单例(可选)。nil 表示禁用。
var (
	refinerMu sync.RWMutex
	refinerR  LLMRefiner
)

// SetRefiner 安装全局 refiner。
// nil 表示禁用(markdown_writer 投递前会查,无则跳过)。
func SetRefiner(r LLMRefiner) {
	refinerMu.Lock()
	defer refinerMu.Unlock()
	refinerR = r
}

// GetRefiner 取出当前 refiner(无则 nil)。
func GetRefiner() LLMRefiner {
	refinerMu.RLock()
	defer refinerMu.RUnlock()
	return refinerR
}

// Reset 移除全局 refiner(测试用)。
func resetRefiner() {
	refinerMu.Lock()
	defer refinerMu.Unlock()
	refinerR = nil
}

// EinoLLMRefiner 是基于 eino ChatModel 的 LLMRefiner 实现。
type EinoLLMRefiner struct {
	cm       chatModel
	cfg      RefinerConfig
	systemFn func() *schema.Message
}

// NewEinoLLMRefiner 构造一个 refiner。
//   - cm: eino ChatModel 实例(model.NewChatModel())
//   - cfg: 配置(Workers/Timeout/... 多数用 DefaultRefinerConfig)
//   - cm 为 nil 返回 nil,让 main.go 能判空后禁用
//
// 注意:cm 实际类型是 eino ToolCallingChatModel,但因为 internal/memory 包
// 不引 eino components/model,这里用本地 interface 适配。
func NewEinoLLMRefiner(cm chatModel, cfg RefinerConfig) *EinoLLMRefiner {
	if cm == nil {
		return nil
	}
	// 注意:零值 cfg.Enabled = false 不应当 disable,只有显式 cfg.Enabled = false 才 disable。
	// cfg.Workers <= 0 / PerCallTimeout <= 0 等"零值"按默认值补。
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.PerCallTimeout <= 0 {
		cfg.PerCallTimeout = 30 * time.Second
	}
	if cfg.ContentSnippetBytes <= 0 {
		cfg.ContentSnippetBytes = 1500
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 256
	}
	if cfg.MaxSummaryLength <= 0 {
		cfg.MaxSummaryLength = MaxSummaryLength
	}
	return &EinoLLMRefiner{cm: cm, cfg: cfg}
}

// Refine 调 LLM 生成 ≤cfg.MaxSummaryLength 字的摘要。
//
// Fallback 策略(核心原则):
//   - 仅在 LLM 调用超时或异常时 fallback 到规则摘要
//   - 其他所有情况(包括元描述、空内容等)都通过后处理清洗 LLM 输出
//   - 如果后处理后为空,使用 LLM 原始输出(经过简单截断)或占位符,绝不 fallback
//
// 后处理流程:
//  1. 剥离 think 标签及其内容
//  2. 剥离 markdown 代码块包裹
//  3. 剥离引号包裹
//  4. 从元描述中提取实际摘要内容(剥离 meta 前缀)
//  5. 折叠空白
//  6. 按语言过滤并截断(普通类) / 直接截断(sessions 类)
//
// sessions 类 (user_request / user_response) 走专用处理:
//   - 不做按语言过滤(避免洗掉英文术语)
//   - think-only 时返回 "(无可见内容)" 占位
func (r *EinoLLMRefiner) Refine(ctx context.Context, task RefineTask) (string, error) {
	if r == nil || r.cm == nil {
		return "", fmt.Errorf("nil refiner")
	}

	contentLang := DetectLanguage(task.ContentSnippet)
	system := r.systemPrompt(task.Kind)
	user := r.userPrompt(task)

	resp, err := r.cm.Generate(ctx, []*schema.Message{system, user})
	if err != nil {
		// 唯一 fallback 场景:LLM 调用异常/超时
		log.Printf("%s LLM generate failed, fallback to rule summary: %v", refinerLogPrefix, err)
		return task.OriginalSummary, nil
	}
	if resp == nil {
		// 唯一 fallback 场景:LLM 返回空响应
		log.Printf("%s LLM returned nil response, fallback to rule summary", refinerLogPrefix)
		return task.OriginalSummary, nil
	}

	// 所有其他情况:通过后处理清洗 LLM 输出
	var summary string
	if isSessionsKind(task.Kind) {
		// sessions 类:专用后处理(不做按语言过滤、think-only → 占位)
		summary = postProcessSessionsOutput(resp.Content, r.cfg.MaxSummaryLength)
	} else {
		// 普通类:通用后处理(剥离 think + 按语言过滤)
		summary = postProcessLLMOutput(resp.Content, contentLang, r.cfg.MaxSummaryLength)
	}

	// 兜底:如果 sessions 占位也不可用 → 用 truncateByRune 截断原始输出
	if summary == "" {
		log.Printf("%s postProcess returned empty, using raw LLM output: %q",
			refinerLogPrefix, resp.Content)
		summary = truncateByRune(collapseWhitespace(resp.Content), r.cfg.MaxSummaryLength)
		if summary == "" {
			// LLM 真的一字未输出 → 用占位符(不走 fallback,因 LLM 调用已成功)
			summary = SessionsThinkOnlyPlaceholder
		}
	}

	return summary, nil
}

// isSessionsKind 判断 kind 是否属于 sessions 类(走专用处理路径)。
//
// sessions 类 kind:
//   - user_request: 浏览器用户提问
//   - user_response: 响应给浏览器的助手回复
//
// 这两类内容的特征:
//   - 短小、直接(用户提问 < 200 字,助手回复 < 1000 字居多)
//   - 可能含英文术语(模块名、API 名等)
//   - 可能被 LLM 包在 think 块里污染(RouterAgent 内部推理)
func isSessionsKind(kind string) bool {
	return kind == "user_request" || kind == "user_response"
}

// postProcessLLMOutput 对 LLM 输出进行后处理,清洗并提取最终摘要。
//
// 处理流程:
//  1. 剥离 think 标签及其内容(LLM 内部推理块,用户看不到)
//  2. 剥离 markdown 代码块包裹
//  3. 剥离引号包裹
//  4. 从元描述中提取实际摘要内容
//  5. 折叠空白
//  6. 截断到 maxLen (强制中文,不做按语言过滤)
//
// sessions 类(user_request / user_response)专用行为:
//   - 如果剥离 think 后剩余为空(或仅空白),返回 "(无可见内容)" 占位
//     避免后续 truncateByRune 处理时 fallback 到垃圾
//   - 不强按语言过滤,避免把"CPU 使用率"类的英文术语洗掉
func postProcessLLMOutput(content string, lang Language, maxLen int) string {
	if content == "" {
		return ""
	}

	// 1. 剥离 think 标签及其内容
	content = stripThinkTags(content)

	// 2. 剥离 markdown 代码块包裹
	content = stripMarkdownCodeBlocks(content)

	// 3. 剥离引号包裹
	content = stripQuotes(content)

	// 4. 从元描述中提取实际摘要内容
	content = extractFromMetaDescription(content)

	// 5. 折叠空白
	content = collapseWhitespace(content)

	// 6. 截断到 maxLen (强制中文,不做按语言过滤)
	return truncateByRune(content, maxLen)
}

// postProcessSessionsOutput 是 sessions 类 kind 的专用后处理:
//
// user_request / user_response 的内容可能:
//  1. 完整 - 直接折叠截断
//  2. think-only(剥离后无可见内容,如 RouterAgent 内部推理块)
//  3. 含英文术语(如 "memory 模块", "CPU 使用率") - 不要按语言过滤
//
// 与 postProcessLLMOutput 的关键差异:
//   - 不走 truncateByLanguage(避免中文 ASCII 过滤洗掉英文术语)
//   - think-only 时返回明确占位,而不是空字符串
func postProcessSessionsOutput(content string, maxLen int) string {
	if content == "" {
		return ""
	}

	// 1. 剥离 think 块(LLM 内部推理)
	content = stripThinkTags(content)

	// 2. 剥离 markdown 代码块 + 引号(通用清洗)
	content = stripMarkdownCodeBlocks(content)
	content = stripQuotes(content)

	// 3. 折叠空白
	content = collapseWhitespace(content)
	content = strings.TrimSpace(content)

	// 4. think-only / 全部噪声 → 占位符(明确语义,绝不返回空)
	if content == "" {
		return SessionsThinkOnlyPlaceholder
	}

	// 5. 截断到 maxLen(不做按语言过滤)
	return truncateByRune(content, maxLen)
}

// SessionsThinkOnlyPlaceholder 是 sessions 后处理在剥离 think 块后
// 没有可见内容时的占位摘要。
//
// 用明确占位而非空串:
//   - 让 front-matter summary 至少有可读字符串
//   - 防止 LLM 调用失败时 fallback 到充满空格的 OriginalSummary
//   - 标识这种 entry 是"助手/用户无可见内容"(可能是纯工具调用等场景)
const SessionsThinkOnlyPlaceholder = "(无可见内容)"

// stripThinkTags 剥离 think 及其内容。
func stripThinkTags(s string) string {
	// 移除所有 think 块
	re := regexp.MustCompile(`(?is)<think.*?<\/think>`)
	return re.ReplaceAllString(s, "")
}

// stripMarkdownCodeBlocks 剥离 markdown 代码块包裹。
// 例如:
//
//	```
//	摘要内容
//	```
//
// 或
//
//	```text
//	摘要内容
//	```
func stripMarkdownCodeBlocks(s string) string {
	trimmed := strings.TrimSpace(s)

	// 检查是否以 ``` 开头
	if !strings.HasPrefix(trimmed, "```") {
		return s
	}

	// 找到第一个换行
	firstNewline := strings.Index(trimmed, "\n")
	if firstNewline == -1 {
		return s
	}

	// 找到最后一个 ```
	lastCodeBlock := strings.LastIndex(trimmed, "```")
	if lastCodeBlock <= firstNewline {
		return s
	}

	// 提取中间内容
	content := trimmed[firstNewline+1 : lastCodeBlock]
	return strings.TrimSpace(content)
}

// stripQuotes 剥离引号包裹。
// 例如:
//   - "摘要内容" → 摘要内容
//   - "摘要内容" → 摘要内容
//   - '摘要内容' → 摘要内容
func stripQuotes(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return s
	}

	// 检查是否被引号包裹
	if (strings.HasPrefix(trimmed, `"`) && strings.HasSuffix(trimmed, `"`)) ||
		(strings.HasPrefix(trimmed, "“") && strings.HasSuffix(trimmed, "”")) ||
		(strings.HasPrefix(trimmed, `'`) && strings.HasSuffix(trimmed, `'`)) {
		return trimmed[1 : len(trimmed)-1]
	}

	return trimmed
}

// extractFromMetaDescription 从元描述中提取实际摘要内容。
//
// 算法:剥离 meta 句子
//  1. 检测是否包含 meta 前缀模式(如 "The user wants me to...", "我需要...")
//  2. 如果包含,找到 meta 前缀所在**整个句子的结束位置**
//     (遇到 . ! ? 或 \n 等句末标点 / 换行)
//  3. 剥离该 meta 句子,返回剩余内容作为实际摘要
//  4. 如果 meta 句子后没有内容,返回原文(让后续截断逻辑处理)
//
// 示例:
//   - "The user wants me to summarize this. User asks about weather."
//     → 剥离 "The user wants me to summarize this." → "User asks about weather."
//   - "我需要先理解内容。用户询问天气情况。"
//     → 剥离 "我需要先理解内容。" → "用户询问天气情况。"
//   - "用户查询北京天气" → "用户查询北京天气"(不包含 meta 前缀)
func extractFromMetaDescription(s string) string {
	if s == "" {
		return s
	}

	trimmed := strings.TrimSpace(s)

	// 英文 meta 前缀模式(按长度降序排列,优先匹配更长的模式)
	englishPrefixes := []string{
		"the user wants me to",
		"the user is asking",
		"the user has asked",
		"the user asked me",
		"i need to",
		"i should",
		"i will",
		"i'll ",
		"let me think",
		"let me analyze",
		"let me consider",
		"let me review",
		"let me look",
		"let me check",
		"first, i",
		"first, let me",
		"to summarize",
		"in summary",
		"the task is",
		"the request is",
		"based on the content",
		"based on the input",
		"based on the above",
		"the goal is",
		"my task is",
		"my role is",
		"as an ai",
		"as a language model",
		"i am an ai",
		"i'm an ai",
		"i'm a ",
		"sure! here",
		"certainly! here",
		"of course! here",
		"here is a summary",
		"here's a summary",
		"here is the summary",
		"here's the summary",
		"this summary",
		"summary:",
	}

	// 中文 meta 前缀模式(按长度降序排列,优先匹配更长的模式)
	chinesePrefixes := []string{
		"我需要",
		"我应该",
		"我作为",
		"作为一个",
		"作为ai",
		"作为人工智能",
		"让我想想",
		"让我分析",
		"让我考虑",
		"让我看一下",
		"让我检查",
		"首先,我应该",
		"首先,我需要",
		"首先,我将",
		"根据内容",
		"根据上述",
		"根据以上",
		"根据输入",
		"用户想要",
		"用户希望",
		"用户的请求",
		"用户问了",
		"用户问的是",
		"任务是",
		"我的任务是",
		"以下是对",
		"以下是",
		"总结一下",
		"总结为",
		"总而言之",
		"好的,以下",
		"好的,根据",
		"好的,我来",
		"好的!以下",
		"摘要如下",
		"摘要:",
		"好的,这里是",
	}

	lower := strings.ToLower(trimmed)

	// 找到 meta 前缀的位置
	prefixIdx := -1
	prefixLen := 0

	for _, prefix := range englishPrefixes {
		if idx := strings.Index(lower, prefix); idx != -1 {
			prefixIdx = idx
			prefixLen = len(prefix)
			break
		}
	}

	if prefixIdx == -1 {
		for _, prefix := range chinesePrefixes {
			if idx := strings.Index(trimmed, prefix); idx != -1 {
				prefixIdx = idx
				prefixLen = len(prefix)
				break
			}
		}
	}

	if prefixIdx == -1 {
		return s
	}

	// 从前缀结束位置开始,找到句末(第一个 . ! ? 或 \n 等标点)
	// 按 rune 迭代以正确处理 UTF-8 多字节字符(中文标点)
	startScan := prefixIdx + prefixLen
	endOfMeta := -1
	byteIdx := 0
	for _, r := range trimmed {
		if byteIdx >= startScan {
			if r == '.' || r == '!' || r == '?' || r == '。' || r == '！' || r == '？' || r == '\n' || r == '\r' {
				// 包含这个标点本身:endOfMeta = 这个标点的下一个字节位置
				endOfMeta = byteIdx + len(string(r)) // 单 rune 的 UTF-8 字节长度
				break
			}
		}
		byteIdx += len(string(r))
	}

	// 如果没找到句末标点 → 说明整个内容都是 meta 前缀
	if endOfMeta == -1 {
		return "" // 让外层处理空字符串
	}

	remaining := strings.TrimSpace(trimmed[endOfMeta:])
	if remaining == "" {
		return "" // 没有 meta 之外的实质内容
	}

	return remaining
}

// truncateByLanguage 按语言过滤并截断。
func truncateByLanguage(s string, lang Language, maxLen int) string {
	if s == "" {
		return ""
	}

	runeCount := utf8.RuneCountInString(s)
	if runeCount <= maxLen {
		return s
	}

	target := maxLen - 2
	if target < 0 {
		target = 0
	}

	kept := filterByLanguage(s, lang, target)
	if utf8.RuneCountInString(kept) > target {
		// fallback:简单前 target rune
		var b strings.Builder
		count := 0
		for _, r := range s {
			if count >= target {
				break
			}
			b.WriteRune(r)
			count++
		}
		kept = b.String()
	}

	return kept + ".."
}

// systemPrompt 构造 system message。
//
// 按 kind 差异化:
//   - sessions 类 (user_request / user_response):
//     内容是用户提问/助手最终响应,可能含英文术语(如 memory 模块、CPU 使用率)
//     强化"忽略 think 块 + 直接复述或微改 + 不做技术名词翻译"
//   - 普通类 (llm_input / llm_output / ...): 用基础 prompt
//
// 强制使用中文输出摘要,不做语言检测分支。
func (r *EinoLLMRefiner) systemPrompt(kind string) *schema.Message {
	langInstruction := "用中文(简体汉字)输出摘要,最多不超过 80 字。" +
		"技术名词可保留英文(如 memory、tool_calls),描述动词必须是中文。"
	example := "示例输出:用户询问北京今日天气,助手调用 get_weather 工具查询并返回 25 度。"

	// 使用 char codes 拼接含 think 标签的错误示例,避免源代码里直接含尖括号
	thinkExample := "-" + " \"\x3Cthink\x3E...\x3C/think\x3E\"\n"

	basePrompt := "你是摘要机器。把下面给的文本整理成一句精炼摘要,描述这次 trace 在做什么。\n" +
		"\n" +
		"【输出格式】\n" +
		"直接输出摘要文本本身。不要任何解释、思考过程、前缀、后缀、标点包裹、引号。\n" +
		"\n" +
		"【正确示例】\n" +
		example + "\n" +
		"\n" +
		"【错误示例(禁止输出)】\n" +
		"- \"The user wants me to summarize...\"\n" +
		"- \"Let me think about this...\"\n" +
		"- \"我需要先理解内容...\"\n" +
		"- \"```\n[摘要内容]\n```\"\n" +
		thinkExample +
		"\n" +
		"【语言】" + langInstruction

	// sessions 类追加专用规则
	if isSessionsKind(kind) {
		thinkRuleExample := "-" + " 内容可能含 \x3Cthink\x3E...\x3C/think\x3E 块,这是 LLM 内部推理,请完全忽略。\n" +
			"- 看到 \x3Cthink 块后如果没有任何可见内容,直接输出: \"(无可见内容)\"\n" +
			"- 技术名词(memory、CPU、tool_calls、agent 等)必须按原文保留,不得翻译、改写。\n" +
			"- 用户提问通常 < 200 字,直接复述或微改即可,不要扩展、不要分析意图。\n" +
			"- 助手回复通常 < 1000 字,摘取核心观点(1-2 句),不要罗列细节。"

		basePrompt += "\n\n【sessions 专用规则】\n" + thinkRuleExample
	}

	return schema.SystemMessage(basePrompt)
}

// userPrompt 构造 user message。
//
// 对不同 kind (llm_input / llm_output / user_request / user_response) 给出
// 不同的指令,让 LLM 更精准地提取关键信息。
func (r *EinoLLMRefiner) userPrompt(task RefineTask) *schema.Message {
	content := task.ContentSnippet
	if len(content) > r.cfg.ContentSnippetBytes {
		content = content[:r.cfg.ContentSnippetBytes] + "..."
	}

	var instruction string
	switch task.Kind {
	case "llm_input":
		instruction = "【任务】LLM 调用请求。提取:助手角色定位 + 用户最新问题 + 是否调用工具及工具名。"
	case "llm_output":
		instruction = "【任务】LLM 调用响应。提取:助手回复的关键信息 + 调用了哪些工具 + 是否结束对话。"
	case "user_request":
		instruction = "【任务】用户请求(浏览器用户提问)。\n" +
			"- 直接复述用户的问题或微改措辞,不要扩展、不要分析意图、不要补全背景。\n" +
			"- 技术名词(memory、CPU、agent 等)按原文保留,不翻译。\n" +
			"- 如果内容已是完整句子,直接输出原句。"
	case "user_response":
		// think 标签用 \x3C / \x3E 表达,避免源代码直含尖括号与 pipeline 冲突
		thinkTag := "\x3Cthink\x3E"
		thinkTagEnd := "\x3C/think\x3E"
		instruction = "【任务】助手响应(浏览器用户最终接收到的回复)。\n" +
			"- 内容可能含 " + thinkTag + "..." + thinkTagEnd + " 块,这是 LLM 内部推理,务必忽略。\n" +
			"- 只摘录剥离 " + thinkTag + " 块后、用户能看到的真实回复内容。\n" +
			"- 若剥离 " + thinkTag + " 块后为空,直接输出 \"(无可见内容)\"。\n" +
			"- 摘要应描述助手最终说了什么 / 做了什么(1-2 句)。"
	default:
		instruction = fmt.Sprintf("【任务】%s。提取核心意图。", task.Kind)
	}

	prompt := fmt.Sprintf("%s\nagent=%s\n\n【待摘要内容】\n%s",
		instruction, task.AgentName, content)
	return schema.UserMessage(prompt)
}

func (q *RefineQueue) process(task RefineTask, workerID int) {
	ctx, cancel := context.WithTimeout(context.Background(), q.cfg.PerCallTimeout)
	defer cancel()

	start := time.Now()
	newSummary, err := q.refiner.Refine(ctx, task)
	if err != nil {
		log.Printf("%s worker=%d refine failed session=%s trace=%s kind=%s err=%v",
			refinerLogPrefix, workerID, task.SessionID, task.LLMTraceID, task.Kind, err)
		return
	}

	// 如果后处理后为空,跳过替换,保留 OriginalSummary
	if newSummary == "" {
		log.Printf("%s worker=%d refined summary is empty, keeping original session=%s trace=%s kind=%s",
			refinerLogPrefix, workerID, task.SessionID, task.LLMTraceID, task.Kind)
		return
	}

	// 写回 front-matter。
	// 定位策略按 kind 分流:
	//   - sessions 类(user_request / user_response):按 RequestGroupID + Side 定位
	//     (因这些 entry 可能没有可定位的 llm_trace_id,且 front-matter 用 group_id 串联)
	//   - 其它:按 llm_trace_id 定位(原行为)
	if q.updater == nil {
		return
	}

	if isSessionsKind(task.Kind) && task.RequestGroupID != "" && task.Side != "" {
		if err := q.updater.FindAndReplaceSummaryByGroupID(
			task.FilePath, task.RequestGroupID, task.Side, newSummary); err != nil {
			log.Printf("%s worker=%d write back failed session=%s group=%s side=%s err=%v",
				refinerLogPrefix, workerID, task.SessionID, task.RequestGroupID, task.Side, err)
			return
		}
	} else {
		if err := q.updater.FindAndReplaceSummary(
			task.FilePath, task.LLMTraceID, newSummary); err != nil {
			log.Printf("%s worker=%d write back failed session=%s trace=%s err=%v",
				refinerLogPrefix, workerID, task.SessionID, task.LLMTraceID, err)
			return
		}
	}

	log.Printf("%s worker=%d refined session=%s trace=%s kind=%s summary=%q in %v",
		refinerLogPrefix, workerID, task.SessionID, task.LLMTraceID, task.Kind, newSummary, time.Since(start))
}

// RefineQueue 是 refiner 的队列 + worker 调度器。
//
// 异步契约:
//   - Enqueue 立即返回,失败仅 log
//   - 后台 worker goroutine 跑 Refine + 写回文件
//   - Flush 同步 drain,Close 停 worker
type RefineQueue struct {
	refiner  LLMRefiner
	updater  *FileUpdater
	cfg      RefinerConfig
	queue    chan RefineTask
	stopCh   chan struct{}
	doneCh   chan struct{}
	closedMu sync.RWMutex
	closed   bool
}

// NewRefineQueue 构造异步 refine 队列并启动 worker。
func NewRefineQueue(r LLMRefiner, cfg RefinerConfig, updater *FileUpdater) *RefineQueue {
	if r == nil {
		return nil
	}
	if !cfg.Enabled {
		log.Printf("%s disabled by config", refinerLogPrefix)
		return nil
	}
	q := &RefineQueue{
		refiner: r,
		updater: updater,
		cfg:     cfg,
		queue:   make(chan RefineTask, cfg.QueueSize),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	for i := 0; i < cfg.Workers; i++ {
		go q.worker(i, cfg.Workers)
	}
	return q
}

// Enqueue 投递 refine 任务,立即返回。
func (q *RefineQueue) Enqueue(task RefineTask) {
	if q == nil {
		return
	}
	q.closedMu.RLock()
	closed := q.closed
	q.closedMu.RUnlock()
	if closed {
		return
	}
	task.EnqueueAt = time.Now()

	select {
	case q.queue <- task:
	default:
		log.Printf("%s queue full, dropping refine task session=%s trace=%s",
			refinerLogPrefix, task.SessionID, task.LLMTraceID)
	}
}

// Flush 同步等待 worker 处理完当前所有任务。
func (q *RefineQueue) Flush() {
	if q == nil {
		return
	}
	respCh := make(chan struct{})
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if len(q.queue) == 0 {
					respCh <- struct{}{}
					return
				}
			}
		}
	}()
	<-respCh
}

// Close 停止 worker(必须先 Flush)。
func (q *RefineQueue) Close() {
	if q == nil {
		return
	}
	q.closedMu.Lock()
	if q.closed {
		q.closedMu.Unlock()
		return
	}
	q.closed = true
	q.closedMu.Unlock()

	close(q.stopCh)
	<-q.doneCh
}

func (q *RefineQueue) worker(id int, total int) {
	isLast := id == total-1
	for {
		select {
		case <-q.stopCh:
			for {
				select {
				case task := <-q.queue:
					q.process(task, id)
				default:
					if isLast {
						close(q.doneCh)
					}
					return
				}
			}
		case task := <-q.queue:
			q.process(task, id)
		}
	}
}

// truncateByRune 按 rune 数截断,不破 utf8 边界。
func truncateByRune(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i]
		}
		count++
	}
	return s
}

// Pending 返回队列中待处理任务数(诊断用)
func (q *RefineQueue) Pending() int {
	if q == nil {
		return 0
	}
	return len(q.queue)
}

// 防止 strings 包被 unused 检查干掉
var _ = strings.TrimSpace
