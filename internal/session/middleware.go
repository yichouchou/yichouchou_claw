package session

import (
	"context"
	"log"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

const middlewareLogPrefix = "[middleware]"

// PersistMiddleware 是一个 eino 原生的 ChatModelAgentMiddleware。
//
// 它通过实现 ChatModelAgentMiddleware.AfterAgent 直接拿到 eino 内部维护的
// ChatModelAgentState.Messages —— 这些消息由 SDK 自己产线化维护，已经包含了
// 完整的 assistant(tool_calls) ↔ tool(result) 配对、tool_call_id 正确、toolName
// 正确，因此可以原样回填给下一轮 LLM，从而彻底解决 "tool call result does not
// follow tool call (2013)" 这类校验失败问题。
//
// 调用方应当只把此 middleware 注册到 **最外层** ChatModelAgent（在 SetSubAgents
// 之后作为 router 的那个），避免子 agent 的 state 覆盖 store。
//
// session_id 通过 adk.WithSessionValues 注入到 ctx（key 为 KeySessionID）。
type PersistMiddleware struct {
	*adk.BaseChatModelAgentMiddleware

	store *Store
}

// NewPersistMiddleware 创建一个 AfterAgent 钩子，会把 ChatModelAgent 内部的 state.Messages
// 以 sessionID 为 key 写入 store。
func NewPersistMiddleware(store *Store) *PersistMiddleware {
	return &PersistMiddleware{store: store}
}

// AfterAgent 是 SDK 暴露的官方钩子：每次 ChatModelAgent 达到成功终态时被调用。
// state.Messages 是该次 run 的完整 messages，**已经满足 OpenAI/Ark 风格模型对
// assistant(tool_calls) → tool(result) 顺序与 id 对齐的要求**。
//
// 修改(2026-07-29 多模态改造):
//  1. 每个 agent 都触发 AfterAgent;但只有"非空 store 前缀"才 Append,避免
//     RouterAgent → 子 agent 的 transfer 链导致重复追加。
//  2. 去重策略:如果 store 里已包含新追加的首条消息(tool_call_id 或内容指纹),
//     则视为重复,跳过。
//  3. 持久化由 Store.Append 内部触发(2026-07-29 新增),不再单独调 persist。
func (m *PersistMiddleware) AfterAgent(ctx context.Context, state *adk.ChatModelAgentState) (context.Context, error) {
	// 通过 SDK 官方 API 从 sessionValues 读取 session_id。
	raw, _ := adk.GetSessionValue(ctx, KeySessionID)
	sessionID, _ := raw.(string)
	if sessionID == "" {
		log.Printf("%s AfterAgent skip: no session_id in ctx", middlewareLogPrefix)
		return ctx, nil
	}

	if state == nil || len(state.Messages) == 0 {
		log.Printf("%s AfterAgent skip: empty state, session=%s", middlewareLogPrefix, sessionID)
		return ctx, nil
	}

	// 找到本次 agent 新增的消息（最后一条 assistant）
	var newMsgs []*schema.Message
	for i := len(state.Messages) - 1; i >= 0; i-- {
		msg := state.Messages[i]
		if msg.Role == schema.Assistant {
			newMsgs = state.Messages[i:]
			break
		}
	}
	if len(newMsgs) == 0 {
		log.Printf("%s AfterAgent skip: no assistant message, session=%s", middlewareLogPrefix, sessionID)
		return ctx, nil
	}

	// === 多模态修复: 去重检测 ===
	// RouterAgent → 子 agent 的 transfer 链里,每个 agent 的 AfterAgent 都会触发。
	// 但子 agent 的 state.Messages 包含完整历史(从 transfer 时继承的 user msg
	// + 自己新生成的 assistant msg),如果直接 Append 会重复添加整段。
	// 检测方式:比对 newMsgs[0] 与 store 末尾消息的关键指纹。
	if m.isDuplicate(sessionID, newMsgs[0]) {
		log.Printf("%s AfterAgent skip: duplicate detected session=%s msg_count=%d",
			middlewareLogPrefix, sessionID, len(newMsgs))
		return ctx, nil
	}

	m.store.Append(sessionID, newMsgs...)
	log.Printf("%s AfterAgent appended session=%s messages=%d",
		middlewareLogPrefix, sessionID, len(newMsgs))
	return ctx, nil
}

// isDuplicate 检查 newMsg 是否已在 store 末尾出现过。
//
// 指纹策略:
//   - tool message: 用 ToolCallID + ToolName 配对(同 call id 只应出现一次)
//   - assistant message: 取 Content 前 64 字节 + ToolCallIDs 集合
func (m *PersistMiddleware) isDuplicate(sessionID string, newMsg *schema.Message) bool {
	existing := m.store.Get(sessionID)
	if len(existing) == 0 {
		return false
	}
	// 只跟 store 末尾 N 条比较(N=5),不扫全表(多模态 store 可能很大)
	const tailN = 5
	start := len(existing) - tailN
	if start < 0 {
		start = 0
	}
	tail := existing[start:]

	switch newMsg.Role {
	case schema.Tool:
		// tool message: 比对 ToolCallID
		for _, em := range tail {
			if em.Role == schema.Tool && em.ToolCallID == newMsg.ToolCallID && em.ToolCallID != "" {
				return true
			}
		}
	case schema.Assistant:
		// assistant message: 比对 Content 前 64 字节 + ToolCallIDs
		newSig := assistantSignature(newMsg)
		for _, em := range tail {
			if em.Role != schema.Assistant {
				continue
			}
			if assistantSignature(em) == newSig && newSig != "" {
				return true
			}
		}
	}
	return false
}

// assistantSignature 计算 assistant 消息的指纹。
//
// 返回: Content[:64] + "|" + tool_call_ids 拼接;同 assistant 文本+同 tool_calls 视为重复。
// 多模态 part 不参与指纹(避免 base64 长尾干扰);若 Content 为空(纯多模态),用
// AssistantGenMultiContent 数量作为补充指纹。
func assistantSignature(msg *schema.Message) string {
	prefix := msg.Content
	if len(prefix) > 64 {
		prefix = prefix[:64]
	}
	ids := make([]string, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		ids = append(ids, tc.ID)
	}
	sig := prefix + "|" + strings.Join(ids, ",")
	if sig == "|" {
		// Content 为空 + 无 tool_calls(罕见;可能是纯多模态输出)
		sig = "multi:" + strconvI(len(msg.AssistantGenMultiContent))
	}
	return sig
}

// strconvI 是 strconv.Itoa 的零依赖替代,避免循环 import。
func strconvI(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
