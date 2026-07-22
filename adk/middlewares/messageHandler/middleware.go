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

	lang := detectLanguageFromHistory(state.Messages)
	if lang == "unknown" {
		return ctx, state, nil
	}

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

// detectLanguageFromHistory 优先看"最早一条 user message"——它反映了用户最初的语种偏好；
// 若该 message 含英文字母命令（如 gh auth login），不会影响中文判定（因为中文比例远高于命令）。
// 仅当整条 history 全英文时才返回 "en"。
func detectLanguageFromHistory(messages []*schema.Message) string {
	for _, msg := range messages {
		if msg.Role != schema.User {
			continue
		}
		return detectLanguage(msg.Content)
	}
	return "unknown"
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

	if chineseCount == 0 && englishCount == 0 {
		return "unknown"
	}

	// 策略：消息含任意中文字符即判 zh。
	// 理由：用户消息常中英混排（如 "在我的主机上执行 github登录，gh auth login"），
	// 此时"中文 = 用户意图说明"，英文 = 命令片段或专有名词；中文比例被英文命令稀释，
	// 仅靠比例阈值会误判。英文用户全用英文时不带中文字符，仍能正确判 en。
	if chineseCount > 0 {
		return "zh"
	}
	return "en"
}
