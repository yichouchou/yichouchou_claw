package messagehandler

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestDetectLanguageFromHistory_MixedCmd(t *testing.T) {
	// 用户场景："在我的主机上执行github登录，gh auth login"
	// 这是一条中英混排的消息：中文主体 + 英文命令片段
	// 期望判为 zh（中文占比 >= 50%）
	msg := "在我的主机上执行github登录，gh auth login"
	lang := detectLanguage(msg)
	if lang != "zh" {
		t.Fatalf("detectLanguage(%q) = %q, want zh", msg, lang)
	}
}

func TestDetectLanguageFromHistory_FirstUserMessage(t *testing.T) {
	// detectLanguageFromHistory 应该取"最早"一条 user message 的语言
	messages := []*schema.Message{
		{Role: schema.User, Content: "在我的主机上执行github登录，gh auth login"},
		{Role: schema.Assistant, Content: "Routing..."},
		{Role: schema.User, Content: "gh auth login"}, // 后续纯英文命令
	}
	lang := detectLanguageFromHistory(messages)
	if lang != "zh" {
		t.Fatalf("detectLanguageFromHistory = %q, want zh (应取最早一条 user message 的语言)", lang)
	}
}

func TestDetectLanguageFromHistory_OnlyEnglish(t *testing.T) {
	// 全英文场景
	messages := []*schema.Message{
		{Role: schema.User, Content: "Please check disk usage"},
		{Role: schema.Assistant, Content: "OK"},
		{Role: schema.User, Content: "df -h"},
	}
	lang := detectLanguageFromHistory(messages)
	if lang != "en" {
		t.Fatalf("detectLanguageFromHistory = %q, want en", lang)
	}
}

func TestDetectLanguageFromHistory_NoUserMessages(t *testing.T) {
	// 没有 user message：unknown
	messages := []*schema.Message{
		{Role: schema.System, Content: "You are a helpful assistant"},
		{Role: schema.Assistant, Content: "Hello"},
	}
	lang := detectLanguageFromHistory(messages)
	if lang != "unknown" {
		t.Fatalf("detectLanguageFromHistory = %q, want unknown", lang)
	}
}
