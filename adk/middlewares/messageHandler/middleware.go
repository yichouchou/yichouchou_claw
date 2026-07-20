package messagehandler

import (
	"context"
	"strings"
	"unicode"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type LanguageConstraintMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
}

func NewLanguageConstraintMiddleware() *LanguageConstraintMiddleware {
	return &LanguageConstraintMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
	}
}

func (m *LanguageConstraintMiddleware) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	mc *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || len(state.Messages) == 0 {
		return ctx, state, nil
	}

	var latestUserContent string
	for i := len(state.Messages) - 1; i >= 0; i-- {
		msg := state.Messages[i]
		if msg.Role == schema.User {
			latestUserContent = msg.Content
			break
		}
	}

	if latestUserContent == "" {
		return ctx, state, nil
	}

	lang := detectLanguage(latestUserContent)

	var constraint string
	switch lang {
	case "zh":
		constraint = "\n\n【语言约束】你必须用中文回复用户，包括所有文本内容、代码注释、工具调用描述等。"
	case "en":
		constraint = "\n\n【Language Constraint】You must respond in English, including all text content, code comments, tool call descriptions, etc."
	default:
		return ctx, state, nil
	}

	for _, msg := range state.Messages {
		if msg.Role == schema.System && !strings.Contains(msg.Content, constraint) {
			msg.Content += constraint
			break
		}
	}

	return ctx, state, nil
}

func detectLanguage(text string) string {
	chineseCount := 0
	englishCount := 0

	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			chineseCount++
		} else if unicode.IsLetter(r) {
			englishCount++
		}
	}

	total := chineseCount + englishCount
	if total == 0 {
		return "unknown"
	}

	chineseRatio := float64(chineseCount) / float64(total)

	if chineseRatio >= 0.5 {
		return "zh"
	}
	return "en"
}
