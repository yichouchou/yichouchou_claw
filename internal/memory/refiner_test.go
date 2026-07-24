package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// fakeCM 模拟 eino ChatModel,返回一个固定摘要。
type fakeCM struct {
	summary string
	err     error
	calls   int
}

func (f *fakeCM) Generate(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return schema.AssistantMessage(f.summary, nil), nil
}

// TestEinoLLMRefiner_Refine 验证 Refine 拿到 LLM 响应 → 截断到 MaxSummaryLength。
func TestEinoLLMRefiner_Refine(t *testing.T) {
	cm := &fakeCM{summary: "用户分析 memory 4 个子目录的用途与设计"}
	r := NewEinoLLMRefiner(cm, DefaultRefinerConfig())
	if r == nil {
		t.Fatalf("refiner should not be nil")
	}

	got, err := r.Refine(context.Background(), RefineTask{
		FilePath:        "/tmp/test.md",
		SessionID:       "s1",
		LLMTraceID:      "llm-1",
		AgentName:       "ChatAgent",
		OriginalSummary: "原规则摘要",
		ContentSnippet:  "long content...",
		Kind:            "llm_input",
	})
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}
	if got != "用户分析 memory 4 个子目录的用途与设计" {
		t.Errorf("unexpected refined summary: %q", got)
	}
	if cm.calls != 1 {
		t.Errorf("cm.calls = %d, want 1", cm.calls)
	}
}

// TestEinoLLMRefiner_TruncatesLongResponse 验证 LLM 返回超长时仍截到 ≤100 字符。
func TestEinoLLMRefiner_TruncatesLongResponse(t *testing.T) {
	long := strings.Repeat("x", 500)
	cm := &fakeCM{summary: long}
	r := NewEinoLLMRefiner(cm, DefaultRefinerConfig())
	got, err := r.Refine(context.Background(), RefineTask{Kind: "llm_input"})
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}
	// 默认 MaxSummaryLength=100,字段里的字符应 ≤100。
	count := 0
	for range got {
		count++
	}
	if count > MaxSummaryLength {
		t.Errorf("refined too long: %d runes, want ≤ %d", count, MaxSummaryLength)
	}
}

// TestEinoLLMRefiner_LLMError 验证 LLM 错误时 fallback 到规则版摘要(不丢弃任务)。
// 这是唯一允许 fallback 的场景。
func TestEinoLLMRefiner_LLMError(t *testing.T) {
	cm := &fakeCM{err: errors.New("rate limited")}
	r := NewEinoLLMRefiner(cm, DefaultRefinerConfig())
	originalSummary := "用户分析 memory 模块"
	got, err := r.Refine(context.Background(), RefineTask{
		OriginalSummary: originalSummary,
		ContentSnippet:  "用户分析 memory 模块的设计",
	})
	if err != nil {
		t.Fatalf("Refine should not error when LLM fails, got: %v", err)
	}
	// 关键:LLM 失败时,返回 OriginalSummary(规则版摘要),不丢弃任务
	if got != originalSummary {
		t.Errorf("LLM failure should fallback to OriginalSummary, got %q want %q", got, originalSummary)
	}
}

// TestEinoLLMRefiner_LLMNilResponse 验证 LLM 返回 nil 响应时 fallback 到规则版摘要。
func TestEinoLLMRefiner_LLMNilResponse(t *testing.T) {
	// 包装一下,让 Generate 返回 nil message
	nilCM := &nilFakeCM{}
	r := NewEinoLLMRefiner(nilCM, DefaultRefinerConfig())
	originalSummary := "用户分析 memory 模块"
	got, err := r.Refine(context.Background(), RefineTask{
		OriginalSummary: originalSummary,
		ContentSnippet:  "用户分析 memory 模块的设计",
	})
	if err != nil {
		t.Fatalf("Refine should not error, got: %v", err)
	}
	if got != originalSummary {
		t.Errorf("nil response should fallback to OriginalSummary, got %q want %q", got, originalSummary)
	}
}

// nilFakeCM 返回 nil message 的 fake。
type nilFakeCM struct{}

func (f *nilFakeCM) Generate(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
	return nil, nil
}

// TestEinoLLMRefiner_LanguageAlignment 验证语言对齐逻辑:
// 当内容是中文但 LLM 返回英文摘要时,保留 LLM 输出(不强过滤,避免清空)。
func TestEinoLLMRefiner_LanguageAlignment(t *testing.T) {
	// 模拟 LLM 返回英文摘要(可能因为上下文混杂代码而用英文)
	cm := &fakeCM{summary: "User asks to analyze memory module design"}
	r := NewEinoLLMRefiner(cm, DefaultRefinerConfig())
	got, err := r.Refine(context.Background(), RefineTask{
		ContentSnippet: "用户分析 memory 模块设计意图",
	})
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}
	// 内容是中文,LLM 返回英文 → 保留 LLM 输出(不强行过滤成空白)
	if got == "" {
		t.Errorf("LLM output should be preserved, got empty")
	}
	t.Logf("LLM 输出保留: %q", got)
}

// TestEinoLLMRefiner_EnglishContent 验证英文内容 → 英文摘要(LLM正确时)。
func TestEinoLLMRefiner_EnglishContent(t *testing.T) {
	cm := &fakeCM{summary: "User queries weather in Beijing"}
	r := NewEinoLLMRefiner(cm, DefaultRefinerConfig())
	got, err := r.Refine(context.Background(), RefineTask{
		ContentSnippet: "What is the weather in Beijing today?",
	})
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}
	if got != "User queries weather in Beijing" {
		t.Errorf("unexpected: %q", got)
	}
}

// TestEinoLLMRefiner_ChineseContentChineseSummary 验证中文内容+中文LLM摘要直接通过。
func TestEinoLLMRefiner_ChineseContentChineseSummary(t *testing.T) {
	cm := &fakeCM{summary: "用户查询北京天气"}
	r := NewEinoLLMRefiner(cm, DefaultRefinerConfig())
	got, err := r.Refine(context.Background(), RefineTask{
		ContentSnippet: "帮我查一下北京今天的天气",
	})
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}
	if got != "用户查询北京天气" {
		t.Errorf("unexpected: %q", got)
	}
	// 验证最终是中文
	if DetectLanguage(got) != LangChinese {
		t.Errorf("expected Chinese, got lang=%q summary=%q", DetectLanguage(got), got)
	}
}

// TestEinoLLMRefiner_MetaDescriptionPostProcess 验证 LLM 返回"思考过程元描述"时
// 通过后处理清洗提取实际摘要,而不是 fallback 到规则摘要。
//
// 核心原则:只有 LLM 调用异常/超时才 fallback,其他情况都通过后处理清洗。
func TestEinoLLMRefiner_MetaDescriptionPostProcess(t *testing.T) {
	tests := []struct {
		name           string
		llmOutput      string
		contentSnippet string
		wantContains   string // 期望结果包含的子串
		wantNotOrg     bool   // 期望结果不等于 OriginalSummary
	}{
		{
			name:           "think标签被剥离后提取内容",
			llmOutput:      "<think>The user wants me to summarize</think>用户查询北京天气",
			contentSnippet: "今天北京天气",
			wantContains:   "用户查询北京天气",
			wantNotOrg:     true,
		},
		{
			name:           "I need to开头-提取实际摘要",
			llmOutput:      "I need to summarize this. User asks about weather.",
			contentSnippet: "今天北京天气",
			wantContains:   "User asks about weather",
			wantNotOrg:     true,
		},
		{
			name:           "The user wants me to开头-提取实际内容",
			llmOutput:      "The user wants me to provide a summary. User asks about weather in Beijing.",
			contentSnippet: "今天北京天气",
			wantContains:   "User asks about weather",
			wantNotOrg:     true,
		},
		{
			name:           "中文我需要-提取实际摘要",
			llmOutput:      "我需要先理解这个对话。用户询问天气情况。",
			contentSnippet: "今天北京天气",
			wantContains:   "用户询问天气情况",
			wantNotOrg:     true,
		},
		{
			name:           "中文让我想想-提取实际摘要",
			llmOutput:      "让我想想,用户问的是天气相关的问题",
			contentSnippet: "今天北京天气",
			wantContains:   "用户问的是天气相关的问题",
			wantNotOrg:     true,
		},
		{
			name:           "markdown代码块包裹-提取内容",
			llmOutput:      "```\n用户查询北京天气\n```",
			contentSnippet: "今天北京天气",
			wantContains:   "用户查询北京天气",
			wantNotOrg:     true,
		},
		{
			name:           "引号包裹-提取内容",
			llmOutput:      `"用户查询北京天气"`,
			contentSnippet: "今天北京天气",
			wantContains:   "用户查询北京天气",
			wantNotOrg:     true,
		},
		{
			name:           "真正的中文摘要-直接通过",
			llmOutput:      "用户查询北京今日天气,气温25度",
			contentSnippet: "今天北京天气",
			wantContains:   "用户查询北京今日天气",
			wantNotOrg:     true,
		},
		{
			name:           "真正的英文摘要-直接通过",
			llmOutput:      "User queries weather in Beijing, 25 degrees",
			contentSnippet: "What is the weather in Beijing?",
			wantContains:   "User queries weather in Beijing",
			wantNotOrg:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := &fakeCM{summary: tt.llmOutput}
			r := NewEinoLLMRefiner(cm, DefaultRefinerConfig())
			originalSummary := "原始规则摘要"
			got, err := r.Refine(context.Background(), RefineTask{
				OriginalSummary: originalSummary,
				ContentSnippet:  tt.contentSnippet,
			})
			if err != nil {
				t.Fatalf("Refine: %v", err)
			}
			// 关键:不应该 fallback 到 OriginalSummary(除非 LLM 调用失败)
			if tt.wantNotOrg && got == originalSummary {
				t.Errorf("should NOT fallback to OriginalSummary, got %q", got)
			}
			// 期望结果包含特定子串
			if tt.wantContains != "" && !strings.Contains(got, tt.wantContains) {
				t.Errorf("expected to contain %q, got %q", tt.wantContains, got)
			}
			// 结果不应为空
			if got == "" {
				t.Errorf("refined summary should not be empty")
			}
		})
	}
}

// TestEinoLLMRefiner_NilSafe 验证 nil 安全。
func TestEinoLLMRefiner_NilSafe(t *testing.T) {
	r := NewEinoLLMRefiner(nil, DefaultRefinerConfig())
	if r != nil {
		t.Errorf("nil cm should return nil refiner")
	}
	r = &EinoLLMRefiner{}
	_, err := r.Refine(context.Background(), RefineTask{})
	if err == nil {
		t.Errorf("nil refiner should error")
	}
}

// TestPostProcessLLMOutput 验证后处理流程。
func TestPostProcessLLMOutput(t *testing.T) {
	tests := []struct {
		name    string
		content string
		lang    Language
		maxLen  int
		want    string
	}{
		{
			name:    "剥离think标签",
			content: "<think>some thinking</think>用户查询天气",
			lang:    LangChinese,
			maxLen:  100,
			want:    "用户查询天气",
		},
		{
			name:    "剥离markdown代码块",
			content: "```\n用户查询天气\n```",
			lang:    LangChinese,
			maxLen:  100,
			want:    "用户查询天气",
		},
		{
			name:    "剥离引号",
			content: `"用户查询天气"`,
			lang:    LangChinese,
			maxLen:  100,
			want:    "用户查询天气",
		},
		{
			name:    "提取meta描述后的实际内容",
			content: "The user wants me to summarize. User asks about weather.",
			lang:    LangEnglish,
			maxLen:  100,
			want:    "User asks about weather.",
		},
		{
			name:    "中文meta描述提取",
			content: "我需要先分析。用户询问天气。",
			lang:    LangChinese,
			maxLen:  100,
			want:    "用户询问天气。",
		},
		{
			name:    "空内容返回空",
			content: "",
			lang:    LangChinese,
			maxLen:  100,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := postProcessLLMOutput(tt.content, tt.lang, tt.maxLen)
			if got != tt.want {
				t.Errorf("postProcessLLMOutput(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

// TestExtractFromMetaDescription 验证从元描述中提取实际摘要。
func TestExtractFromMetaDescription(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "英文meta前缀-提取后续内容",
			input: "The user wants me to summarize this. User asks about weather.",
			want:  "User asks about weather.",
		},
		{
			name:  "中文meta前缀-提取后续内容",
			input: "我需要先理解。用户询问天气。",
			want:  "用户询问天气。",
		},
		{
			name:  "无meta前缀-返回原文",
			input: "用户查询北京天气",
			want:  "用户查询北京天气",
		},
		{
			name:  "空字符串",
			input: "",
			want:  "",
		},
		{
			name:  "I need to开头",
			input: "I need to analyze this. The weather is sunny.",
			want:  "The weather is sunny.",
		},
		{
			name:  "Let me think开头",
			input: "Let me think about this. The user wants weather info.",
			want:  "The user wants weather info.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFromMetaDescription(tt.input)
			if got != tt.want {
				t.Errorf("extractFromMetaDescription(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestFileUpdater_ReplaceSummary 验证 FileUpdater 找到匹配的 front-matter 并替换。
func TestFileUpdater_ReplaceSummary(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "test.md")
	original := `---
session_id: "s1"
llm_trace_id: "llm-1"
agent: "ChatAgent"
kind: "llm_input"
time: "2026-07-24T10:00:00Z"
summary: "原规则摘要 前98字.."
---

` + "```text\nfoo\n```\n\n" + `---
session_id: "s1"
llm_trace_id: "llm-2"
agent: "ChatAgent"
kind: "llm_input"
time: "2026-07-24T10:01:00Z"
summary: "另一条"
---

` + "```text\nbar\n```\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	u := NewFileUpdater()
	if err := u.FindAndReplaceSummary(path, "llm-1", "LLM 精炼: 用户问了 X"); err != nil {
		t.Fatalf("FindAndReplaceSummary: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(got)

	// llm-1 块 summary 应被替换
	if !strings.Contains(body, `summary: "LLM 精炼: 用户问了 X"`) {
		t.Errorf("llm-1 summary not replaced:\n%s", body)
	}
	// llm-2 块 summary 不应被影响
	if !strings.Contains(body, `summary: "另一条"`) {
		t.Errorf("llm-2 summary was incorrectly modified:\n%s", body)
	}
	// 原内容应保留
	if !strings.Contains(body, "```text\nfoo\n```") {
		t.Errorf("body content lost")
	}
}

// TestFileUpdater_NotFound 验证 traceID 不存在时静默成功。
func TestFileUpdater_NotFound(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "test.md")
	original := `---
llm_trace_id: "llm-1"
summary: "foo"
---

content
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	u := NewFileUpdater()
	// 找一个不存在的 trace → 静默
	if err := u.FindAndReplaceSummary(path, "llm-does-not-exist", "new"); err != nil {
		t.Errorf("not-found should be silent: %v", err)
	}

	// 文件未变
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `summary: "foo"`) {
		t.Errorf("original file should be unchanged:\n%s", got)
	}
}

// TestFileUpdater_FileMissing 验证文件不存在时静默。
func TestFileUpdater_FileMissing(t *testing.T) {
	u := NewFileUpdater()
	if err := u.FindAndReplaceSummary("/tmp/does-not-exist-xxx.md", "llm-1", "new"); err != nil {
		t.Errorf("missing file should be silent: %v", err)
	}
}

// TestFileUpdater_NilSafe 验证 nil updater 安全。
func TestFileUpdater_NilSafe(t *testing.T) {
	var u *FileUpdater
	if err := u.FindAndReplaceSummary("/tmp/foo", "llm-1", "x"); err == nil {
		t.Errorf("nil updater should error")
	}
}

// TestRefineQueue_EndToEnd 验证 refine queue:异步接收任务 + 调 refiner + 写回文件。
func TestRefineQueue_EndToEnd(t *testing.T) {
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Flush()
		_ = r.Close()
	})

	updater := NewFileUpdater()
	cm := &fakeCM{summary: "LLM 精炼: 用户分析 memory 用途"}
	refiner := NewEinoLLMRefiner(cm, DefaultRefinerConfig())

	cfg := DefaultRefinerConfig()
	cfg.Workers = 1
	cfg.PerCallTimeout = 5 * time.Second
	q := NewRefineQueue(refiner, cfg, updater)
	if q == nil {
		t.Fatalf("refine queue nil")
	}
	r.SetRefiner(q)
	t.Cleanup(func() {
		q.Flush()
		q.Close()
	})

	// 通过 recorder 写入一条 entry → writeOne 会投递 refine 任务
	sid := "sess-e2e"
	traceID := NewLLMTraceID()
	if err := r.RecordLLMInput(context.Background(), sid, traceID, "ChatAgent", []byte("content for refine")); err != nil {
		t.Fatalf("RecordLLMInput: %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	q.Flush()

	// 验证 LLM 被调过
	if cm.calls == 0 {
		t.Errorf("expected LLM call, got 0")
	}

	// 验证文件里 summary 被替换
	bucket := BucketFromNow()
	path := filepath.Join(root, "memory", "inputs", bucket.DateDir(), bucket.HourFile())
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "LLM 精炼") {
		t.Errorf("file should contain refined summary:\n%s", got)
	}
}

// TestRefineQueue_NilSafe 验证 nil queue 不会 panic。
func TestRefineQueue_NilSafe(t *testing.T) {
	var q *RefineQueue
	q.Enqueue(RefineTask{}) // 应该 no-op
	q.Flush()
	q.Close()
}

// TestNewRefineQueue_NilRefiner 验证 nil refiner → nil queue。
func TestNewRefineQueue_NilRefiner(t *testing.T) {
	cfg := DefaultRefinerConfig()
	q := NewRefineQueue(nil, cfg, NewFileUpdater())
	if q != nil {
		t.Errorf("nil refiner should give nil queue")
	}
}

// TestNewRefineQueue_Disabled 验证 cfg.Enabled=false → nil queue。
func TestNewRefineQueue_Disabled(t *testing.T) {
	cfg := DefaultRefinerConfig()
	cfg.Enabled = false
	cm := &fakeCM{summary: "x"}
	r := NewEinoLLMRefiner(cm, cfg)
	if r == nil {
		t.Fatalf("refiner should not be nil when only cfg disabled")
	}
	q := NewRefineQueue(r, cfg, NewFileUpdater())
	// queue 创建时仍按 cfg.Enabled 拒绝
	if q != nil {
		t.Errorf("disabled queue should be nil")
	}
}

// TestNewEinoLLMRefiner_DefaultCfg 验证 DefaultRefinerConfig 应用。
func TestNewEinoLLMRefiner_DefaultCfg(t *testing.T) {
	cm := &fakeCM{summary: "x"}
	r := NewEinoLLMRefiner(cm, RefinerConfig{}) // 全 0 → 用 defaults
	if r == nil {
		t.Fatalf("refiner nil")
	}
	if r.cfg.Workers != 2 {
		t.Errorf("default Workers = %d, want 2", r.cfg.Workers)
	}
	if r.cfg.PerCallTimeout != 30*time.Second {
		t.Errorf("default timeout wrong: %v", r.cfg.PerCallTimeout)
	}
	if r.cfg.ContentSnippetBytes != 1500 {
		t.Errorf("default snippet bytes wrong: %d", r.cfg.ContentSnippetBytes)
	}
}

// TestAdapterFunc 验证 AdapterFunc 适配任意 ChatModel 接口。
func TestAdapterFunc(t *testing.T) {
	cm := &fakeCM{summary: "abc"}
	called := 0
	af := AdapterFunc(func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
		called++
		return cm.Generate(ctx, msgs)
	})

	resp, err := af.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("hi"),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Content != "abc" {
		t.Errorf("unexpected: %q", resp.Content)
	}
	if called != 1 {
		t.Errorf("adapter called = %d", called)
	}
}

// TestRecordAutoEnqueuesRefine 验证 recorder 落盘成功后,refine 自动投递。
func TestRecordAutoEnqueuesRefine(t *testing.T) {
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}

	cm := &fakeCM{summary: "LLM summary"}
	updater := NewFileUpdater()
	cfg := DefaultRefinerConfig()
	cfg.Workers = 1
	cfg.PerCallTimeout = 2 * time.Second
	q := NewRefineQueue(NewEinoLLMRefiner(cm, cfg), cfg, updater)
	r.SetRefiner(q)

	t.Cleanup(func() {
		q.Flush()
		q.Close()
		_ = r.Flush()
		_ = r.Close()
	})

	// 触发 5 条 entry
	for i := 0; i < 5; i++ {
		_ = r.RecordUserRequest(context.Background(), NewRequestGroupID(), "sess", "X", []byte(fmt.Sprintf("user input %d", i)))
	}
	_ = r.Flush()
	q.Flush()

	if cm.calls == 0 {
		t.Errorf("expected LLM calls, got 0")
	}
}
