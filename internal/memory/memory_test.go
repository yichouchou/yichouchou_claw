package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// === 原有测试保留 ===

func TestTimeBucket(t *testing.T) {
	bucket := BucketFromTime(time.Date(2026, 7, 23, 9, 0, 0, 0, localTimeZone))
	if bucket.DateDir() != "2026-07-23" {
		t.Errorf("DateDir = %q, want 2026-07-23", bucket.DateDir())
	}
	if bucket.HourFile() != "09h.md" {
		t.Errorf("HourFile = %q, want 09h.md", bucket.HourFile())
	}
	got := bucket.BucketPath("/tmp/foo")
	want := filepath.Join("/tmp/foo", "2026-07-23", "09h.md")
	if got != want {
		t.Errorf("BucketPath = %q, want %q", got, want)
	}

	bucket2 := TimeBucket{Year: 2026, Month: 1, Day: 5, Hour: 4}
	if bucket2.HourFile() != "04h.md" {
		t.Errorf("zero-padded hour: got %q, want 04h.md", bucket2.HourFile())
	}
}

// === 新接口核心测试 ===

// TestNewRecorder_4Quadrants 验证四象限布局:
// sessions/(user_request / user_response)
// inputs/(llm_input)
// outputs/(llm_output)
// errors/(error)
func TestNewRecorder_4Quadrants(t *testing.T) {
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Flush()
		_ = r.Close()
	})

	sid := "sess-001"
	traceID := NewLLMTraceID()
	groupID := NewRequestGroupID()
	agent := "ChatAgent"

	if err := r.RecordUserRequest(context.Background(), groupID, sid, agent, []byte("hello")); err != nil {
		t.Fatalf("RecordUserRequest: %v", err)
	}
	if err := r.RecordLLMInput(context.Background(), sid, traceID, agent, []byte("system: hi\nuser: hello")); err != nil {
		t.Fatalf("RecordLLMInput: %v", err)
	}
	if err := r.RecordLLMOutput(context.Background(), sid, traceID, agent, []byte("assistant: hi back")); err != nil {
		t.Fatalf("RecordLLMOutput: %v", err)
	}
	if err := r.RecordError(context.Background(), sid, traceID, agent, errors.New("boom"), "tool=local_command"); err != nil {
		t.Fatalf("RecordError: %v", err)
	}
	if err := r.RecordUserResponse(context.Background(), groupID, sid, agent, []byte("assistant: hi back")); err != nil {
		t.Fatalf("RecordUserResponse: %v", err)
	}

	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	bucket := BucketFromNow()

	tests := []struct {
		subdir string
		want   []string
	}{
		{"inputs", []string{"kind: \"llm_input\"", "system: hi", "llm_trace_id"}},
		{"outputs", []string{"kind: \"llm_output\"", "assistant: hi back", "llm_trace_id"}},
		{"errors", []string{"kind: \"error\"", "boom", "local_command"}},
		{"sessions", []string{"kind: \"user_request\"", "kind: \"user_response\""}},
	}

	for _, tt := range tests {
		path := filepath.Join(root, "memory", tt.subdir, bucket.DateDir(), bucket.HourFile())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(data)
		if len(data) == 0 {
			t.Errorf("%s is empty", path)
		}
		for _, want := range tt.want {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q\n---\n%s", path, want, body)
			}
		}
	}
}

// TestLLMTracePairing 验证 input/output 用同一 llm_trace_id,errors 也带 trace。
func TestLLMTracePairing(t *testing.T) {
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Flush()
		_ = r.Close()
	})

	sid := "sess-pair"
	traceID := NewLLMTraceID()
	agent := "ChatAgent"

	// input
	_ = r.RecordLLMInput(context.Background(), sid, traceID, agent, []byte("req"))
	// output 同一 trace
	_ = r.RecordLLMOutput(context.Background(), sid, traceID, agent, []byte("resp"))
	// error 同一 trace
	_ = r.RecordError(context.Background(), sid, traceID, agent, errors.New("oops"), "ctx")
	_ = r.Flush()

	bucket := BucketFromNow()
	expectedTrace := string(traceID)

	for _, sub := range []string{"inputs", "outputs", "errors"} {
		path := filepath.Join(root, "memory", sub, bucket.DateDir(), bucket.HourFile())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(data)
		// llm_trace_id 应在 front-matter 里
		if !strings.Contains(body, `llm_trace_id: "`+expectedTrace+`"`) {
			t.Errorf("%s missing llm_trace_id=%q\n---\n%s", path, expectedTrace, body)
		}
	}
}

// TestSummaryLengthHardLimit 验证 RuleSummary 输出 ≤ MaxSummaryLength 字符。
func TestSummaryLengthHardLimit(t *testing.T) {
	// 200 字符原文 → 应该截到 ≤ 100 字符
	long := strings.Repeat("x", 200)
	got := RuleSummary([]byte(long))
	if utf8.RuneCountInString(got) > MaxSummaryLength {
		t.Errorf("summary too long: %d chars, want <= %d",
			utf8.RuneCountInString(got), MaxSummaryLength)
	}

	// 短文本 → 原样返回(折叠空白后)
	short := "hello world"
	got2 := RuleSummary([]byte(short))
	if got2 != "hello world" {
		t.Errorf("short summary should equal input, got %q", got2)
	}

	// 含多空白 → 折叠
	messy := "  hello\t\n world  "
	got3 := RuleSummary([]byte(messy))
	if got3 != "hello world" {
		t.Errorf("whitespace fold failed, got %q", got3)
	}

	// 空内容
	got4 := RuleSummary(nil)
	if got4 != "" {
		t.Errorf("nil summary should be empty, got %q", got4)
	}
}

// TestDetectLanguage 验证语言检测启发式。
//
// 规则:只要内容里有任意 1 个 CJK 字符,就判为中文,确保"只要会话有一个
// 中文,就使用中文摘要"。
func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Language
	}{
		{"empty", "", LangUnknown},
		{"single_chinese", "好", LangChinese}, // 1 个 CJK 即判为中文
		{"chinese_short", "你好世界", LangChinese},
		{"chinese_with_code", "分析 memory 模块", LangChinese}, // 中英混合,有中文
		{"chinese_long", "北京今天天气怎么样，出门需要带伞吗", LangChinese},
		{"chinese_with_punct", "北京今天天气怎么样?", LangChinese},
		{"chinese_with_english_mix", "我想分析一下 memory 这个目录下的设计思路", LangChinese},
		{"japanese", "こんにちは", LangChinese}, // Hiragana 也归 cjk
		{"english_very_short", "hi", LangUnknown},
		{"english_short", "Hello world", LangUnknown}, // 不足 15 个 ASCII 字母
		{"english_long", "The quick brown fox jumps over the lazy dog", LangEnglish},
		{"numbers_only", "12345", LangUnknown},
		{"english_with_code_mix", "fix the App bug in memory module please", LangEnglish},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectLanguage(tt.in)
			if got != tt.want {
				t.Errorf("DetectLanguage(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestRuleSummary_LanguageAligned 验证 RuleSummary 强制使用中文输出。
func TestRuleSummary_LanguageAligned(t *testing.T) {
	// 中文长文本 → 摘要应该是中文,技术术语可保留英文(如 memory)
	chineseLong := strings.Repeat("用户分析 memory 目录设计 ", 20)
	got := RuleSummary([]byte(chineseLong))
	// 摘要长度限制
	if utf8.RuneCountInString(got) > MaxSummaryLength {
		t.Errorf("chinese summary too long: %d runes, want <= %d",
			utf8.RuneCountInString(got), MaxSummaryLength)
	}
	// 应保留主要 CJK
	if !strings.Contains(got, "用户") || !strings.Contains(got, "目录") {
		t.Errorf("Chinese summary should preserve CJK keywords, got %q", got)
	}
	// 技术术语应保留英文
	if !strings.Contains(got, "memory") {
		t.Errorf("Chinese summary should preserve technical terms like 'memory', got %q", got)
	}

	// 英文长文本 → 摘要应保留英文短语
	englishLong := strings.Repeat("The user wants me to summarize a log trace and analyze ", 20)
	got2 := RuleSummary([]byte(englishLong))
	if !strings.Contains(got2, "The") {
		t.Errorf("English summary should contain English words, got %q", got2)
	}
	if utf8.RuneCountInString(got2) > MaxSummaryLength {
		t.Errorf("english summary too long: %d runes, want <= %d",
			utf8.RuneCountInString(got2), MaxSummaryLength)
	}

	// 短中文 → 折叠后保留
	shortChinese := "用户分析 memory 用途"
	got3 := RuleSummary([]byte(shortChinese))
	if !strings.Contains(got3, "用户") {
		t.Errorf("short chinese summary should preserve CJK, got %q", got3)
	}
}

// TestRuleSummary_StripFormattingMeta 验证 RuleSummary 能剥离 formatMessageAsInput 输出的元信息。
//
// 真实场景(用户报告的问题):llm_input 写入磁盘时,content 是 "[00] role=system content_len=6442 tool_calls=0\n     content: 实际内容..."
// 格式,规则版会错误地把 [00] role=system... 这种元信息截进去,看起来不像摘要。
func TestRuleSummary_StripFormattingMeta(t *testing.T) {
	// 模拟 formatMessageAsInput 输出的格式
	input := `[00] role=system content_len=6442 tool_calls=0
     content: 你是一个智能任务路由器,负责把任务委派给最合适的专家 agent。
可用的专家 agent 如下:
- ChatAgent:日常闲聊、通用知识问答、技术方案讨论、澄清式追问、代码 review、文档翻译。不执行任何命令。
- WeatherAgent:查询指定城市的天气,调用 get_weather 工具。
- LocalCommandAgent:在受限沙箱内执行主机 bash 命令。
等等

【路由判定规则】
1. 复合任务拆分:如果一条消息同时包含"分析"和"执行"等多个动词,视为复合任务。
2. 简单的日常对话转 ChatAgent。
3. 查询天气转 WeatherAgent。

[01] role=user content_len=21 tool_calls=0
     content: 北京天气怎样`

	got := RuleSummary([]byte(input))
	t.Logf("RuleSummary 输出: %q", got)

	// 1. 不应包含元信息中的数字和英文 token
	if strings.Contains(got, "role=") || strings.Contains(got, "content_len=") || strings.Contains(got, "tool_calls=") {
		t.Errorf("summary 应该剥离元信息,但仍包含: %q", got)
	}
	if strings.Contains(got, "[00]") || strings.Contains(got, "[01]") {
		t.Errorf("summary 应把 [NN] 标签转换为角色短标签,但仍含 [NN]: %q", got)
	}

	// 2. 应包含实际内容的关键中文词
	if !strings.Contains(got, "智能") || !strings.Contains(got, "任务") {
		t.Errorf("summary 应保留中文关键词, got %q", got)
	}

	// 3. 应包含角色短标签(便于理解是 system 还是 user 的内容)
	if !strings.Contains(got, "[") || !strings.Contains(got, "]") {
		t.Errorf("summary 应包含角色标签, got %q", got)
	}

	// 4. 长度合规
	runeCount := utf8.RuneCountInString(got)
	if runeCount > MaxSummaryLength {
		t.Errorf("summary 超过 %d 字: got %d 字", MaxSummaryLength, runeCount)
	}

	t.Logf("✅ 长度合规: %d 字", runeCount)
}

// TestStripFormattingMeta 验证 stripFormattingMeta 函数本身的行为。
func TestStripFormattingMeta(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "元信息行+实际内容",
			in:   "[00] role=system content_len=100 tool_calls=0\n     content: 你好世界",
			want: "[system] 你好世界",
		},
		{
			name: "纯元信息行",
			in:   "[01] role=user content_len=42 tool_calls=0",
			want: "[user]",
		},
		{
			name: "多个角色消息",
			in:   "[00] role=system content_len=10 tool_calls=0\n     content: 你是助手\n[01] role=user content_len=5 tool_calls=0\n     content: 你好",
			want: "[system] 你是助手 [user] 你好",
		},
		{
			name: "缩进 content 前缀",
			in:   "     content: 这是一条消息",
			want: "这是一条消息",
		},
		{
			name: "普通文本不变",
			in:   "这是一段普通文本",
			want: "这是一段普通文本",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripFormattingMeta(tt.in)
			if got != tt.want {
				t.Errorf("stripFormattingMeta(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSummaryInFrontMatter 验证 front-matter 里 summary 字段确实是 RuleSummary 输出。
func TestSummaryInFrontMatter(t *testing.T) {
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Flush()
		_ = r.Close()
	})

	content := strings.Repeat("alpha ", 50) // 250 字符
	_ = r.RecordLLMInput(context.Background(), "sess-summary", NewLLMTraceID(), "ChatAgent", []byte(content))
	_ = r.Flush()

	bucket := BucketFromNow()
	path := filepath.Join(root, "memory", "inputs", bucket.DateDir(), bucket.HourFile())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// front-matter 的 summary 行内容应该是 RuleSummary 输出
	body := string(data)
	expected := RuleSummary([]byte(content))
	// front-matter 段(--- 到 ---之间)
	start := strings.Index(body, "---\n")
	if start < 0 {
		t.Fatalf("front-matter not found in:\n%s", body)
	}
	end := strings.Index(body[start+4:], "\n---\n")
	if end < 0 {
		t.Fatalf("front-matter end not found in:\n%s", body)
	}
	fm := body[start : start+4+end]
	if !strings.Contains(fm, fmt.Sprintf(`summary: %q`, expected)) {
		t.Errorf("front-matter summary mismatch\nwant: %q\nfm: %s", expected, fm)
	}
}

// TestAsyncDoesNotBlock 验证 Record* 立即返回,不等 worker。
func TestAsyncDoesNotBlock(t *testing.T) {
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Flush()
		_ = r.Close()
	})

	// 100 次 Record 应当在 100ms 内完成(不阻塞 worker 写盘)
	start := time.Now()
	for i := 0; i < 100; i++ {
		_ = r.RecordLLMInput(context.Background(), "sess", NewLLMTraceID(), "X", []byte("x"))
	}
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Errorf("Record* blocked: %v for 100 calls", elapsed)
	}
}

// TestFlushDrainsChannel 验证 Flush 把所有 entry 落盘。
func TestFlushDrainsChannel(t *testing.T) {
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}

	// 100 次 enqueue
	const N = 100
	for i := 0; i < N; i++ {
		_ = r.RecordLLMInput(context.Background(), "sess", NewLLMTraceID(), "X",
			[]byte(fmt.Sprintf("payload-%d", i)))
	}

	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	bucket := BucketFromNow()
	path := filepath.Join(root, "memory", "inputs", bucket.DateDir(), bucket.HourFile())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(data)
	for i := 0; i < N; i++ {
		want := fmt.Sprintf("payload-%d", i)
		if !strings.Contains(body, want) {
			t.Errorf("payload-%d missing after Flush", i)
		}
	}

	// 关闭
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 关闭后再 Record → 应当返回 error 而不是 panic
	err = r.RecordLLMInput(context.Background(), "sess", NewLLMTraceID(), "X", []byte("after-close"))
	if err == nil {
		t.Errorf("Record after Close should return error")
	}
}

// TestDisabled 验证 YICHOUCHOU_MEMORY=off 时全部 no-op。
func TestDisabled(t *testing.T) {
	t.Setenv("YICHOUCHOU_MEMORY", "off")

	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}
	if !r.disabled {
		t.Fatalf("recorder should be disabled")
	}
	if r.Root() != "" {
		t.Errorf("Root() should be empty when disabled, got %q", r.Root())
	}

	// 所有调用必须成功,不能创建文件
	if err := r.RecordLLMInput(context.Background(), "s", NewLLMTraceID(), "a", []byte("x")); err != nil {
		t.Errorf("RecordLLMInput: %v", err)
	}
	if err := r.RecordLLMOutput(context.Background(), "s", NewLLMTraceID(), "a", []byte("x")); err != nil {
		t.Errorf("RecordLLMOutput: %v", err)
	}
	if err := r.RecordError(context.Background(), "s", "", "a", errors.New("x"), "x"); err != nil {
		t.Errorf("RecordError: %v", err)
	}
	if err := r.RecordUserRequest(context.Background(), NewRequestGroupID(), "s", "a", []byte("x")); err != nil {
		t.Errorf("RecordUserRequest: %v", err)
	}
	if err := r.RecordUserResponse(context.Background(), NewRequestGroupID(), "s", "a", []byte("x")); err != nil {
		t.Errorf("RecordUserResponse: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "memory")); !os.IsNotExist(err) {
		t.Errorf("memory/ should not exist when disabled")
	}
}

// TestNilSafety 验证空参数不 panic。
func TestNilSafety(t *testing.T) {
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Flush()
		_ = r.Close()
	})

	if err := r.RecordLLMInput(context.Background(), "s", NewLLMTraceID(), "a", nil); err != nil {
		t.Errorf("RecordLLMInput(nil): %v", err)
	}
	if err := r.RecordLLMInput(context.Background(), "s", NewLLMTraceID(), "a", []byte{}); err != nil {
		t.Errorf("RecordLLMInput(empty): %v", err)
	}
	if err := r.RecordError(context.Background(), "s", "", "a", nil, "ctx"); err != nil {
		t.Errorf("RecordError(nil err): %v", err)
	}
	// sessionID 为空 → 跳过
	if err := r.RecordLLMInput(context.Background(), "", NewLLMTraceID(), "a", []byte("x")); err != nil {
		t.Errorf("RecordLLMInput(empty sid): %v", err)
	}
}

// TestConcurrentWrites 验证并发 Record* 不丢字节。
func TestConcurrentWrites(t *testing.T) {
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Flush()
		_ = r.Close()
	})

	const goroutines = 8
	const perGoroutine = 25
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				sid := fmt.Sprintf("sess-%d-%d", id, i)
				_ = r.RecordUserRequest(context.Background(), NewRequestGroupID(), sid, "AgentX", []byte("req"))
				_ = r.RecordLLMInput(context.Background(), sid, NewLLMTraceID(), "AgentX", []byte("hello"))
				_ = r.RecordLLMOutput(context.Background(), sid, NewLLMTraceID(), "AgentX", []byte("world"))
				_ = r.RecordUserResponse(context.Background(), NewRequestGroupID(), sid, "AgentX", []byte("resp"))
			}
		}(g)
	}
	wg.Wait()
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	bucket := BucketFromNow()
	for _, sub := range []string{"sessions", "inputs", "outputs"} {
		path := filepath.Join(root, "memory", sub, bucket.DateDir(), bucket.HourFile())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(data)
		for g := 0; g < goroutines; g++ {
			for i := 0; i < perGoroutine; i++ {
				marker := fmt.Sprintf("sess-%d-%d", g, i)
				if !strings.Contains(body, marker) {
					t.Errorf("%s missing marker %q (concurrent write lost?)", sub, marker)
					return
				}
			}
		}
	}
}

// TestFormatMessages 验证 schema.Message 渲染函数。
func TestFormatMessages(t *testing.T) {
	msgs := []*schema.Message{
		schema.SystemMessage("you are a router"),
		schema.UserMessage("hi"),
		schema.AssistantMessage("hello back", nil),
	}
	got := formatMessagesAsInput(msgs)
	for _, want := range []string{"role=system", "role=user", "role=assistant", "content_len="} {
		if !strings.Contains(got, want) {
			t.Errorf("formatMessagesAsInput missing %q", want)
		}
	}

	out := formatMessageAsOutput(schema.AssistantMessage("hi", []schema.ToolCall{
		{ID: "tc1", Function: schema.FunctionCall{Name: "local_command", Arguments: `{"cmd":"ls"}`}},
	}))
	for _, want := range []string{"role=assistant", "tool_call id=tc1", "name=local_command"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatMessageAsOutput missing %q", want)
		}
	}
}

// TestMiddleware_NoRecorder 验证 middleware 在 SetRecorder 未调时是 no-op。
func TestMiddleware_NoRecorder(t *testing.T) {
	Reset()
	mw := NewMemoryMiddleware("TestAgent")

	ctx := context.Background()
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("hi")}}
	if _, _, err := mw.BeforeModelRewriteState(ctx, state, nil); err != nil {
		t.Fatalf("BeforeModelRewriteState: %v", err)
	}
	if _, _, err := mw.AfterModelRewriteState(ctx, state, nil); err != nil {
		t.Fatalf("AfterModelRewriteState: %v", err)
	}
}

// TestMiddleware_LLMTraceID 验证 BeforeModelRewriteState 注入 trace_id,
// AfterModelRewriteState 正确读出 + 与 RecordLLMInput 配对。
//
// 注意:eino session 注入走 AgentRunOption,框架内部会从 option 转 ctx。
// 本测试通过 SafeRecordLLMInput 模拟框架行为,绕过 session 注入。
func TestMiddleware_LLMTraceID(t *testing.T) {
	Reset()
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Flush()
		_ = r.Close()
	})
	SetRecorder(r)
	t.Cleanup(Reset)

	mw := NewMemoryMiddleware("ChatAgent")
	ctx := context.Background()

	// Before 应注入 trace_id 到 ctx(sid 为空时 middleware 跳过 Record,
	// 但 ctx 仍会被改写,这是 by-design)
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("hi")}}
	newCtx, _, err := mw.BeforeModelRewriteState(ctx, state, nil)
	if err != nil {
		t.Fatalf("BeforeModelRewriteState: %v", err)
	}
	traceID := LLMTraceIDFromContext(newCtx)
	if traceID == "" {
		t.Fatalf("BeforeModelRewriteState should inject llm_trace_id")
	}
	if !strings.HasPrefix(traceID, "llm-") {
		t.Errorf("trace_id should have 'llm-' prefix, got %q", traceID)
	}

	// AfterModelRewriteState 从 ctx 读 trace_id
	state.Messages = append(state.Messages, schema.AssistantMessage("hello back", nil))
	if _, _, err := mw.AfterModelRewriteState(newCtx, state, nil); err != nil {
		t.Fatalf("AfterModelRewriteState: %v", err)
	}

	// session_id 空 → middleware 跳过 Record,我们手工调用一次 Record
	// 模拟 eino 框架把 session_id 注入到 ctx 后的行为
	sid := "test-trace-sid"
	SafeRecordLLMInput(newCtx, sid, traceID, "ChatAgent", []byte("input"))
	SafeRecordLLMOutput(newCtx, sid, traceID, "ChatAgent", []byte("output"))

	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// 验证 inputs/ 和 outputs/ 都带同一 trace_id
	bucket := BucketFromNow()
	for _, sub := range []string{"inputs", "outputs"} {
		path := filepath.Join(root, "memory", sub, bucket.DateDir(), bucket.HourFile())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(data)
		if !strings.Contains(body, `llm_trace_id: "`+traceID+`"`) {
			t.Errorf("%s missing matching llm_trace_id=%q\n%s", sub, traceID, body)
		}
	}
}

// TestMiddleware_WrapInvokableToolCall 验证工具错误落盘到 errors/。
//
// 注意:WrapInvokableToolCall 走的是 SafeRecordError,它要求 ctx 有
// session_id (eino 框架走 AgentRunOption 注入)。本测试模拟 eino 框架
// 已经把 session_id 注入 ctx 后的情况:直接调 SafeRecordError 验证落盘。
func TestMiddleware_WrapInvokableToolCall(t *testing.T) {
	Reset()
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Flush()
		_ = r.Close()
	})
	SetRecorder(r)
	t.Cleanup(Reset)

	// 模拟 eino 框架把 session_id 注入到 ctx 后,middleware 内部会调
	// SafeRecordError(ctx, sid, traceID, agentName, err, ctxStr)
	ctx := context.Background()
	sid := "test-tool-sid"
	traceID := NewLLMTraceID()
	toolErr := errors.New("permission denied")

	SafeRecordError(ctx, sid, traceID, "LocalCommandAgent", toolErr,
		"tool=local_command call_id=call-1 args={\"cmd\":\"ls\"}")

	// WrapInvokableToolCall 包装逻辑验证(只是错误路径走 middleware,
	// 但因为 ctx 没 session_id,middleware 内部不调 SafeRecordError)
	// 所以这里只验证 SafeRecordError 路径
	mw := NewMemoryMiddleware("LocalCommandAgent")
	calls := 0
	wrapped, err := mw.WrapInvokableToolCall(context.Background(),
		func(_ context.Context, args string, _ ...tool.Option) (string, error) {
			calls++
			if args == "ok" {
				return "stdout", nil
			}
			return "", toolErr
		},
		&adk.ToolContext{Name: "local_command", CallID: "call-1"},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall: %v", err)
	}

	if _, err := wrapped(context.Background(), "ok"); err != nil {
		t.Errorf("success path should not error: %v", err)
	}
	if _, err := wrapped(context.Background(), "forbidden"); err == nil {
		t.Errorf("error path should propagate")
	}
	if calls != 2 {
		t.Errorf("endpoint invocations = %d, want 2", calls)
	}

	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	bucket := BucketFromNow()
	path := filepath.Join(root, "memory", "errors", bucket.DateDir(), bucket.HourFile())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read errors file: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "permission denied") {
		t.Errorf("errors file missing tool error text\n---\n%s", body)
	}
	if !strings.Contains(body, "local_command") {
		t.Errorf("errors file missing ctx label\n---\n%s", body)
	}
}

// TestGlobalRecorder_GetSetReset 验证全局 singleton 生命周期。
func TestGlobalRecorder_GetSetReset(t *testing.T) {
	Reset()
	if GetRecorder() != nil {
		t.Fatalf("GetRecorder should be nil after Reset")
	}
	r, err := NewAsyncMarkdownRecorder(t.TempDir())
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Close()
	})
	SetRecorder(r)
	if GetRecorder() == nil {
		t.Fatalf("GetRecorder should not be nil after SetRecorder")
	}
	Reset()
	if GetRecorder() != nil {
		t.Fatalf("GetRecorder should be nil after Reset")
	}
}

// TestNewLLMTraceID_Unique 验证 UUIDv4 唯一。
func TestNewLLMTraceID_Unique(t *testing.T) {
	seen := make(map[LLMTraceID]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := NewLLMTraceID()
		if seen[id] {
			t.Fatalf("duplicate trace id: %s", id)
		}
		seen[id] = true
	}
}

// TestTruncateBytes 验证 byte 截断不破 utf8 边界。
func TestTruncateBytes(t *testing.T) {
	// 中英文混合
	s := "hello 世界 this is a long string"
	got := TruncateBytes(s, 10)
	if utf8.RuneCountInString(got) < 5 {
		t.Errorf("truncate too aggressive: %q", got)
	}
	// 不截断
	got2 := TruncateBytes("hi", 100)
	if got2 != "hi" {
		t.Errorf("no-truncate should equal input, got %q", got2)
	}
	// 边界 ≤ 0
	got3 := TruncateBytes("hello", 0)
	if got3 != "hello" {
		t.Errorf("zero max should not truncate, got %q", got3)
	}
}

// 防止 bytes 包被 unused 检查干掉
var _ = bytes.NewBuffer
