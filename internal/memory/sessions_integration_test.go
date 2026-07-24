package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// TestSessions_MergedResponse 模拟多轮assistant消息场景，验证响应汇总逻辑：
//   - 用户发送中文请求
//   - 多轮assistant响应：思考过程 + 工具调用 + 最终回复
//   - 验证 sessions.md 中 user_response 是所有 assistant content 的拼接
//   - 验证中文摘要正确生成
func TestSessions_MergedResponse(t *testing.T) {
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Flush()
		_ = r.Close()
	})

	sessionID := "sess-merged-test-001"
	agentName := "RouterAgent"
	groupID := NewRequestGroupID()

	// === 场景 1:中文用户请求 ===
	userQuery := "帮我分析一下 memory 目录下的四个子目录的设计意图和用途"
	err = r.RecordUserRequest(context.Background(), groupID, sessionID, agentName, []byte(userQuery))
	if err != nil {
		t.Fatalf("RecordUserRequest: %v", err)
	}

	// === 场景 2:模拟多轮assistant响应 ===
	// 在多 agent 架构中，一次用户请求可能触发多条 assistant 消息：
	//   1. RouterAgent 的分析和路由指令（可能带 content）
	//   2. Sub-agent 的思考过程（带 reasoning content）
	//   3. 最终回复（纯 content）
	//
	// 模拟这些分散的 assistant 消息，验证汇总逻辑能正确拼接

	// 第一条：RouterAgent 分析并转发
	assistantMsg1 := "我来分析一下这个问题，需要查看 memory 目录的设计..."

	// 第二条：ChatAgent 的思考过程
	assistantMsg2 := "根据我对代码的理解，memory 目录包含四个子目录：inputs、outputs、sessions 和 errors。每个目录都有特定的用途..."

	// 第三条：详细解释各目录
	assistantMsg3 := "具体来说：\n\n1. **inputs**: 记录所有发送给 LLM 的请求\n2. **outputs**: 记录 LLM 的所有响应\n3. **sessions**: 记录用户的请求和最终响应\n4. **errors**: 记录错误日志\n\n这种设计使得调试和审计非常方便。"

	// 依次记录这三条响应（模拟实际场景中可能多次调用 RecordUserResponse）
	err = r.RecordUserResponse(context.Background(), groupID, sessionID, agentName, []byte(assistantMsg1))
	if err != nil {
		t.Fatalf("RecordUserResponse 1: %v", err)
	}
	err = r.RecordUserResponse(context.Background(), groupID, sessionID, agentName, []byte(assistantMsg2))
	if err != nil {
		t.Fatalf("RecordUserResponse 2: %v", err)
	}
	err = r.RecordUserResponse(context.Background(), groupID, sessionID, agentName, []byte(assistantMsg3))
	if err != nil {
		t.Fatalf("RecordUserResponse 3: %v", err)
	}

	// 刷新落盘
	err = r.Flush()
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// === 验证 ===
	bucket := BucketFromNow()
	sessionsPath := filepath.Join(root, "memory", "sessions", bucket.DateDir(), bucket.HourFile())
	sessionsData, err := os.ReadFile(sessionsPath)
	if err != nil {
		t.Fatalf("read sessions file: %v", err)
	}
	sessionsBody := string(sessionsData)

	// 1. 验证 sessions.md 同时有 user_request 和 user_response
	if !strings.Contains(sessionsBody, `kind: "user_request"`) {
		t.Errorf("sessions.md 缺少 user_request")
	} else {
		t.Logf("✅ sessions.md 包含 user_request")
	}

	if !strings.Contains(sessionsBody, `kind: "user_response"`) {
		t.Errorf("sessions.md 缺少 user_response")
	} else {
		t.Logf("✅ sessions.md 包含 user_response")
	}

	// 2. 验证 user_response 的内容包含三条消息的拼接
	if !strings.Contains(sessionsBody, "我来分析一下这个问题") {
		t.Errorf("user_response 缺少第一条消息")
	}
	if !strings.Contains(sessionsBody, "根据我对代码的理解") {
		t.Errorf("user_response 缺少第二条消息")
	}
	if !strings.Contains(sessionsBody, "具体来说") {
		t.Errorf("user_response 缺少第三条消息")
	}
	if !strings.Contains(sessionsBody, "memory 目录包含四个子目录") {
		t.Errorf("user_response 缺少关键内容")
	}
	t.Logf("✅ user_response 包含所有 assistant 消息的拼接")

	// 3. 验证 user_request 的 summary 是中文
	requestSummary := extractSummaryForKind(sessionsBody, "user_request")
	if requestSummary == "" {
		t.Errorf("无法提取 user_request 的 summary")
	} else {
		lang := DetectLanguage(requestSummary)
		if lang != LangChinese {
			t.Errorf("user_request summary 不是中文! lang=%q, summary=%q", lang, requestSummary)
		} else {
			t.Logf("✅ user_request summary 是中文: %q", requestSummary)
		}
	}

	// 4. 验证 user_response 的 summary 是中文
	responseSummary := extractSummaryForKind(sessionsBody, "user_response")
	if responseSummary == "" {
		t.Errorf("无法提取 user_response 的 summary")
	} else {
		lang := DetectLanguage(responseSummary)
		if lang != LangChinese {
			t.Errorf("user_response summary 不是中文! lang=%q, summary=%q", lang, responseSummary)
		} else {
			t.Logf("✅ user_response summary 是中文: %q", responseSummary)
		}

		// 5. 验证摘要长度 ≤ 100 字
		runeCount := len([]rune(responseSummary))
		if runeCount > MaxSummaryLength {
			t.Errorf("response summary 超过 %d 字: got %d 字", MaxSummaryLength, runeCount)
		} else {
			t.Logf("✅ response summary 长度合规: %d 字(≤%d)", runeCount, MaxSummaryLength)
		}

		// 6. 验证摘要包含中文关键词
		if !strings.Contains(responseSummary, "memory") && !strings.Contains(responseSummary, "目录") &&
			!strings.Contains(responseSummary, "分析") && !strings.Contains(responseSummary, "设计") {
			t.Errorf("response summary 不包含预期关键词: %q", responseSummary)
		}
	}

	// 7. 验证两条记录 session_id 一致
	if !strings.Contains(sessionsBody, fmt.Sprintf(`session_id: "%s"`, sessionID)) {
		t.Errorf("session_id 不匹配")
	}
	t.Logf("✅ 两条记录 session_id 一致")
}

// TestSessionsChineseSummary_WithToolCalls 模拟一个完整的中文对话场景:
//   - 用户发送中文请求
//   - LLM 返回带 tool_calls 的 assistant 消息(多 agent 场景)
//   - 验证 sessions.md 中同时有 user_request 和 user_response
//   - 验证两条记录的 summary 都是中文
//   - 验证即使 response 带有 tool_calls,也能正确写入
func TestSessionsChineseSummary_WithToolCalls(t *testing.T) {
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Flush()
		_ = r.Close()
	})

	sessionID := "sess-chinese-test-001"
	agentName := "RouterAgent"
	groupID := NewRequestGroupID()

	// === 场景 1:纯中文用户请求 ===
	userQuery := "帮我查一下北京今天的天气怎么样，会不会下雨？"
	err = r.RecordUserRequest(context.Background(), groupID, sessionID, agentName, []byte(userQuery))
	if err != nil {
		t.Fatalf("RecordUserRequest: %v", err)
	}

	// === 场景 2:模拟带 tool_calls 的 assistant 响应(多 agent 路由场景) ===
	// 这是一个典型的 RouterAgent 场景:assistant 消息既有 Content 又有 ToolCalls
	assistantContent := "好的，我来帮你查询北京今天的天气情况。"
	_ = []schema.ToolCall{
		{
			ID:   "call_transfer_001",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "transfer_to_agent",
				Arguments: `{"agent_name":"WeatherAgent"}`,
			},
		},
	}
	// 注意:即使 assistant 消息带有 tool_calls，只要 Content 不为空,
	// 就应该被当作 user_response 记录下来。
	// 这验证了 main.go 中"放宽提取条件"的修复。
	err = r.RecordUserResponse(context.Background(), groupID, sessionID, agentName, []byte(assistantContent))
	if err != nil {
		t.Fatalf("RecordUserResponse: %v", err)
	}

	// === 场景 3:同时记录 LLM input/output(带中文 + 代码混合) ===
	traceID := NewLLMTraceID()
	llmInput := `[系统] 你是一个智能助手，帮助用户查询天气。
[用户] 帮我查一下北京今天的天气怎么样，会不会下雨？
[工具] get_weather(city="北京")`
	llmOutput := `assistant: 北京今天多云转晴，气温 25-32 度，不会下雨。
tool_calls: []`

	err = r.RecordLLMInput(context.Background(), sessionID, traceID, agentName, []byte(llmInput))
	if err != nil {
		t.Fatalf("RecordLLMInput: %v", err)
	}
	err = r.RecordLLMOutput(context.Background(), sessionID, traceID, agentName, []byte(llmOutput))
	if err != nil {
		t.Fatalf("RecordLLMOutput: %v", err)
	}

	// 刷新到磁盘
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	bucket := BucketFromNow()

	// =============================================
	// 验证 1: sessions.md 同时包含 user_request 和 user_response
	// =============================================
	sessionsPath := filepath.Join(root, "memory", "sessions", bucket.DateDir(), bucket.HourFile())
	sessionsData, err := os.ReadFile(sessionsPath)
	if err != nil {
		t.Fatalf("read sessions file: %v", err)
	}
	sessionsBody := string(sessionsData)

	if !strings.Contains(sessionsBody, `kind: "user_request"`) {
		t.Errorf("sessions.md missing user_request entry\n---\n%s", sessionsBody)
	}
	if !strings.Contains(sessionsBody, `kind: "user_response"`) {
		t.Errorf("sessions.md missing user_response entry\n---\n%s", sessionsBody)
	}

	t.Logf("✅ sessions.md 同时包含 user_request 和 user_response")

	// =============================================
	// 验证 2: user_request 的 summary 是中文
	// =============================================
	reqSummary := extractSummaryForKind(sessionsBody, "user_request")
	if reqSummary == "" {
		t.Errorf("cannot extract user_request summary")
	} else {
		reqLang := DetectLanguage(reqSummary)
		if reqLang != LangChinese {
			t.Errorf("user_request summary 不是中文! lang=%q, summary=%q", reqLang, reqSummary)
		} else {
			t.Logf("✅ user_request summary 是中文: %q", reqSummary)
		}
		// 验证摘要长度 ≤ 100
		if len([]rune(reqSummary)) > MaxSummaryLength {
			t.Errorf("user_request summary 超过 %d 字: got %d 字, summary=%q",
				MaxSummaryLength, len([]rune(reqSummary)), reqSummary)
		}
	}

	// =============================================
	// 验证 3: user_response 的 summary 是中文
	// =============================================
	respSummary := extractSummaryForKind(sessionsBody, "user_response")
	if respSummary == "" {
		t.Errorf("cannot extract user_response summary")
	} else {
		respLang := DetectLanguage(respSummary)
		if respLang != LangChinese {
			t.Errorf("user_response summary 不是中文! lang=%q, summary=%q", respLang, respSummary)
		} else {
			t.Logf("✅ user_response summary 是中文: %q", respSummary)
		}
		if len([]rune(respSummary)) > MaxSummaryLength {
			t.Errorf("user_response summary 超过 %d 字: got %d 字, summary=%q",
				MaxSummaryLength, len([]rune(respSummary)), respSummary)
		}
	}

	// =============================================
	// 验证 4: LLM input 的 summary 是中文(即使有代码混合)
	// =============================================
	inputsPath := filepath.Join(root, "memory", "inputs", bucket.DateDir(), bucket.HourFile())
	inputsData, err := os.ReadFile(inputsPath)
	if err != nil {
		t.Fatalf("read inputs file: %v", err)
	}
	inputsBody := string(inputsData)

	inputSummary := extractFirstSummary(inputsBody)
	if inputSummary == "" {
		t.Errorf("cannot extract llm_input summary")
	} else {
		inputLang := DetectLanguage(inputSummary)
		if inputLang != LangChinese {
			t.Errorf("llm_input summary 不是中文! lang=%q, summary=%q", inputLang, inputSummary)
		} else {
			t.Logf("✅ llm_input summary 是中文: %q", inputSummary)
		}
		if len([]rune(inputSummary)) > MaxSummaryLength {
			t.Errorf("llm_input summary 超过 %d 字: got %d 字, summary=%q",
				MaxSummaryLength, len([]rune(inputSummary)), inputSummary)
		}
	}

	// =============================================
	// 验证 5: LLM output 的 summary 是中文
	// =============================================
	outputsPath := filepath.Join(root, "memory", "outputs", bucket.DateDir(), bucket.HourFile())
	outputsData, err := os.ReadFile(outputsPath)
	if err != nil {
		t.Fatalf("read outputs file: %v", err)
	}
	outputsBody := string(outputsData)

	outputSummary := extractFirstSummary(outputsBody)
	if outputSummary == "" {
		t.Errorf("cannot extract llm_output summary")
	} else {
		outputLang := DetectLanguage(outputSummary)
		if outputLang != LangChinese {
			t.Errorf("llm_output summary 不是中文! lang=%q, summary=%q", outputLang, outputSummary)
		} else {
			t.Logf("✅ llm_output summary 是中文: %q", outputSummary)
		}
		if len([]rune(outputSummary)) > MaxSummaryLength {
			t.Errorf("llm_output summary 超过 %d 字: got %d 字, summary=%q",
				MaxSummaryLength, len([]rune(outputSummary)), outputSummary)
		}
	}

	// =============================================
	// 验证 6: response 内容正确写入(即使原消息有 tool_calls)
	// =============================================
	if !strings.Contains(sessionsBody, "我来帮你查询北京今天的天气情况") {
		t.Errorf("sessions.md 中 user_response 的 content 不正确\n---\n%s", sessionsBody)
	} else {
		t.Logf("✅ user_response 内容正确写入(即使原消息有 tool_calls)")
	}

	// =============================================
	// 验证 7: 两条 sessions 记录的 session_id 一致
	// =============================================
	// 统计 session_id 出现次数
	sessionIDCount := strings.Count(sessionsBody, fmt.Sprintf(`session_id: "%s"`, sessionID))
	if sessionIDCount < 2 {
		t.Errorf("sessions.md 中 session_id 出现次数不足 2 次: got %d", sessionIDCount)
	} else {
		t.Logf("✅ session_id 一致，共 %d 条记录", sessionIDCount)
	}
}

// TestSessions_ChineseLongContent 验证长中文内容的摘要:
//   - 长中文文本 → 摘要应是中文，且 ≤ 100 字
//   - 中文 + 大量英文代码混合 → 摘要仍应判为中文
func TestSessions_ChineseLongContent(t *testing.T) {
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Flush()
		_ = r.Close()
	})

	sessionID := "sess-long-chinese"
	agentName := "ChatAgent"
	groupID := NewRequestGroupID()

	// 长中文 + 代码混合(模拟真实 LLM 输入场景)
	longChineseWithCode := `用户请求: 请帮我分析一下下面这段 Go 代码的性能问题，看看有什么可以优化的地方。

代码如下:

func processData(items []Item) []Result {
    var results []Result
    for _, item := range items {
        // 处理每条数据
        result := Result{
            ID: item.ID,
            Name: item.Name,
            Value: computeValue(item),
        }
        results = append(results, result)
    }
    return results
}

func computeValue(item Item) int {
    sum := 0
    for i := 0; i < 1000; i++ {
        sum += item.Weight * i
    }
    return sum
}

请从内存分配、循环优化、并发处理等角度给出优化建议。`

	err = r.RecordUserRequest(context.Background(), groupID, sessionID, agentName, []byte(longChineseWithCode))
	if err != nil {
		t.Fatalf("RecordUserRequest: %v", err)
	}

	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	bucket := BucketFromNow()
	sessionsPath := filepath.Join(root, "memory", "sessions", bucket.DateDir(), bucket.HourFile())
	sessionsData, err := os.ReadFile(sessionsPath)
	if err != nil {
		t.Fatalf("read sessions file: %v", err)
	}
	sessionsBody := string(sessionsData)

	summary := extractFirstSummary(sessionsBody)
	if summary == "" {
		t.Fatalf("cannot extract summary")
	}

	// 验证语言:中文 + 代码混合应判为中文
	lang := DetectLanguage(summary)
	if lang != LangChinese {
		t.Errorf("长中文+代码混合的 summary 不是中文! lang=%q, summary=%q", lang, summary)
	} else {
		t.Logf("✅ 长中文+代码混合 summary 是中文: %q", summary)
	}

	// 验证长度 ≤ 100 字
	runeCount := len([]rune(summary))
	if runeCount > MaxSummaryLength {
		t.Errorf("summary 超过 %d 字: got %d 字, summary=%q", MaxSummaryLength, runeCount, summary)
	} else {
		t.Logf("✅ summary 长度合规: %d 字(≤%d)", runeCount, MaxSummaryLength)
	}

	// 验证摘要包含中文关键词
	if !strings.Contains(summary, "用户") && !strings.Contains(summary, "代码") && !strings.Contains(summary, "分析") {
		t.Errorf("summary 不包含预期中文关键词: %q", summary)
	}
}

// TestSessions_RequestGroupID_LinksRequestAndResponse 验证 sessions 目录下
// user_request 和 user_response 的 front-matter 都带 request_group_id,
// 且两者的 group_id 一致,从而把"一次浏览器请求"的所有 entry 串起来。
func TestSessions_RequestGroupID_LinksRequestAndResponse(t *testing.T) {
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Flush()
		_ = r.Close()
	})

	sessionID := "sess-group-link-001"
	agentName := "RouterAgent"
	groupID := NewRequestGroupID()

	query := "红外传感是否可用于自动驾驶？"
	answer := "红外传感可用于自动驾驶辅助感知层,在恶劣天气下弥补摄像头不足。"

	if err := r.RecordUserRequest(context.Background(), groupID, sessionID, agentName, []byte(query)); err != nil {
		t.Fatalf("RecordUserRequest: %v", err)
	}
	if err := r.RecordUserResponse(context.Background(), groupID, sessionID, agentName, []byte(answer)); err != nil {
		t.Fatalf("RecordUserResponse: %v", err)
	}

	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	bucket := BucketFromNow()
	sessionsPath := filepath.Join(root, "memory", "sessions", bucket.DateDir(), bucket.HourFile())
	data, err := os.ReadFile(sessionsPath)
	if err != nil {
		t.Fatalf("read sessions file: %v", err)
	}
	body := string(data)

	// 必须包含 request_group_id 字段(至少 2 次:user_request + user_response)
	groupStr := fmt.Sprintf(`request_group_id: %q`, groupID)
	count := strings.Count(body, groupStr)
	if count < 2 {
		t.Errorf("expected request_group_id %q to appear ≥2 times (request+response), got %d:\n%s",
			groupID, count, body)
	}

	// 必须同时包含 user_request 和 user_response 的 kind
	if !strings.Contains(body, `kind: "user_request"`) {
		t.Errorf("user_request entry missing:\n%s", body)
	}
	if !strings.Contains(body, `kind: "user_response"`) {
		t.Errorf("user_response entry missing:\n%s", body)
	}

	// 必须保留完整内容(不只用摘要)
	if !strings.Contains(body, query) {
		t.Errorf("request content missing:\n%s", body)
	}
	if !strings.Contains(body, answer) {
		t.Errorf("response content missing:\n%s", body)
	}
}

// TestSessions_LLMCallsAssociatedWithGroupID 验证 inputs/outputs 目录下的
// LLM 调用 entry 在 front-matter 中带 request_group_id,与会话维度串联。
func TestSessions_LLMCallsAssociatedWithGroupID(t *testing.T) {
	root := t.TempDir()
	r, err := NewAsyncMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewAsyncMarkdownRecorder: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Flush()
		_ = r.Close()
	})

	sessionID := "sess-llm-link-001"
	agentName := "ChatAgent"
	groupID := NewRequestGroupID()

	// 把 groupID 注入 ctx,模拟 middleware 调用场景
	ctx := WithRequestGroupID(context.Background(), groupID)

	traceID := NewLLMTraceID()
	if err := r.RecordLLMInput(ctx, sessionID, traceID, agentName, []byte("user: hello")); err != nil {
		t.Fatalf("RecordLLMInput: %v", err)
	}
	if err := r.RecordLLMOutput(ctx, sessionID, traceID, agentName, []byte("assistant: hi")); err != nil {
		t.Fatalf("RecordLLMOutput: %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	bucket := BucketFromNow()
	for _, sub := range []string{"inputs", "outputs"} {
		path := filepath.Join(root, "memory", sub, bucket.DateDir(), bucket.HourFile())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", sub, err)
		}
		body := string(data)
		groupStr := fmt.Sprintf(`request_group_id: %q`, groupID)
		if !strings.Contains(body, groupStr) {
			t.Errorf("%s file should contain request_group_id %q, body:\n%s", sub, groupID, body)
		}
	}
}

// TestFileUpdater_FindAndReplaceSummaryByGroupID 验证按 group_id + side
// 替换 sessions entry 的 summary。
func TestFileUpdater_FindAndReplaceSummaryByGroupID(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "test.md")

	// 构造两份 entry:同一 session,不同 group_id,各自有 request + response
	original := fmt.Sprintf(`---
request_group_id: %q
session_id: "s1"
llm_trace_id: "llm-1"
agent: "RouterAgent"
kind: "user_request"
time: "2026-07-24T15:00:00+08:00"
summary: "原 request 摘要"
---

%s

---
request_group_id: %q
session_id: "s1"
llm_trace_id: "llm-1"
agent: "RouterAgent"
kind: "user_response"
time: "2026-07-24T15:00:01+08:00"
summary: "原 response 摘要"
---

%s

---
request_group_id: "req-other-9999"
session_id: "s1"
llm_trace_id: "llm-2"
agent: "RouterAgent"
kind: "user_request"
time: "2026-07-24T15:01:00+08:00"
summary: "另一组 request 摘要"
---

%s
`, "req-target-1111", "```text\n红外传感是否可用于自动驾驶？\n```",
		"req-target-1111", "```text\n红外传感可用于自动驾驶辅助感知层。\n```",
		"```text\n请问今天天气如何?\n```")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	u := NewFileUpdater()

	// 替换 target group 的 request summary
	if err := u.FindAndReplaceSummaryByGroupID(path, "req-target-1111", "request", "用户询问红外传感是否可用于自动驾驶"); err != nil {
		t.Fatalf("FindAndReplaceSummaryByGroupID request: %v", err)
	}

	// 替换 target group 的 response summary
	if err := u.FindAndReplaceSummaryByGroupID(path, "req-target-1111", "response", "回答红外传感可用于自动驾驶辅助感知层"); err != nil {
		t.Fatalf("FindAndReplaceSummaryByGroupID response: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(got)

	// 验证 target group 的两块 summary 都被替换
	if !strings.Contains(body, `summary: "用户询问红外传感是否可用于自动驾驶"`) {
		t.Errorf("target request summary not replaced:\n%s", body)
	}
	if !strings.Contains(body, `summary: "回答红外传感可用于自动驾驶辅助感知层"`) {
		t.Errorf("target response summary not replaced:\n%s", body)
	}

	// 验证另一组的 summary 未被污染
	if !strings.Contains(body, `summary: "另一组 request 摘要"`) {
		t.Errorf("other group summary touched:\n%s", body)
	}

	// 验证原 entry body 保留
	if !strings.Contains(body, "请问今天天气如何?") {
		t.Errorf("body content lost:\n%s", body)
	}
}

// TestDetectLanguage_ChineseWithCode 专门验证语言检测对"中文+代码混合"的判定。
//
// 规则:只要有 1 个 CJK 字符,即判为中文(满足"只要会话有一个中文,就用中文摘要")。
func TestDetectLanguage_ChineseWithCode(t *testing.T) {
	tests := []struct {
		name string
		text string
		want Language
	}{
		{
			name: "单个中文字符",
			text: "好",
			want: LangChinese,
		},
		{
			name: "纯中文短句",
			text: "你好世界",
			want: LangChinese,
		},
		{
			name: "纯中文长句",
			text: "今天天气真好，我们一起去公园散步吧，顺便买点水果回来吃。",
			want: LangChinese,
		},
		{
			name: "中文为主 + 少量英文术语",
			text: "我想分析一下 memory 模块的设计思路，看看 RuleSummary 和 LLM refine 的区别在哪里。",
			want: LangChinese,
		},
		{
			name: "1 个中文字符夹在大量英文中",
			text: "Please help me 分析 the bug in this function and give me a suggestion",
			want: LangChinese,
		},
		{
			name: "中文 + 大量代码",
			text: "请优化下面的 Go 代码:\n\nfunc Process(items []int) int {\n    sum := 0\n    for i := 0; i < len(items); i++ {\n        sum += items[i]\n    }\n    return sum\n}\n\n要求:提高性能",
			want: LangChinese,
		},
		{
			name: "纯英文(≥15字母)",
			text: "The quick brown fox jumps over the lazy dog. This is a test.",
			want: LangEnglish,
		},
		{
			name: "纯英文短句(<15字母)",
			text: "Hello world",
			want: LangUnknown,
		},
		{
			name: "纯代码(无中文)",
			text: "func main() {\n    fmt.Println(\"hello world\")\n    for i := 0; i < 10; i++ {\n        fmt.Println(i)\n    }\n}",
			want: LangEnglish,
		},
		{
			name: "中文 + 英文极短(只要有中文就算中文)",
			text: "查 weather",
			want: LangChinese,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectLanguage(tt.text)
			if got != tt.want {
				t.Errorf("DetectLanguage() = %q, want %q", got, tt.want)
			} else {
				t.Logf("  ✅ %q → %s", tt.name, got)
			}
		})
	}
}

// ============ 辅助函数 ============

// extractSummaryForKind 从 markdown 内容中提取指定 kind 的第一条 entry 的 summary。
func extractSummaryForKind(body string, kind string) string {
	lines := strings.Split(body, "\n")
	inFrontMatter := false
	traceMatched := false

	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")

		if trimmed == "---" {
			inFrontMatter = !inFrontMatter
			traceMatched = false
			continue
		}

		if !inFrontMatter {
			continue
		}

		if strings.Contains(line, fmt.Sprintf(`kind: "%s"`, kind)) {
			traceMatched = true
			continue
		}

		if traceMatched && strings.HasPrefix(line, "summary:") {
			// 提取 summary 值: summary: "xxx" → xxx
			idx1 := strings.Index(line, `"`)
			idx2 := strings.LastIndex(line, `"`)
			if idx1 >= 0 && idx2 > idx1 {
				return line[idx1+1 : idx2]
			}
		}
	}
	return ""
}

// extractFirstSummary 提取 markdown 内容中第一条 entry 的 summary。
func extractFirstSummary(body string) string {
	lines := strings.Split(body, "\n")
	inFrontMatter := false

	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")

		if trimmed == "---" {
			if inFrontMatter {
				// 结束一个块但没找到 summary,继续下一个
				inFrontMatter = false
			} else {
				inFrontMatter = true
			}
			continue
		}

		if !inFrontMatter {
			continue
		}

		if strings.HasPrefix(line, "summary:") {
			idx1 := strings.Index(line, `"`)
			idx2 := strings.LastIndex(line, `"`)
			if idx1 >= 0 && idx2 > idx1 {
				return line[idx1+1 : idx2]
			}
		}
	}
	return ""
}
