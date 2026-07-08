package session

import (
	"context"
	"log"

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

	// 注意：子 agent 上注册的同名 middleware 也会触发，这里靠调用方把 PersistMiddleware
	// 只挂在最外层 ChatModelAgent 上来确保只持久化一次。
	msgs := append([]*schema.Message(nil), state.Messages...)
	m.store.Replace(sessionID, msgs)

	log.Printf("%s AfterAgent persisted session=%s messages=%d",
		middlewareLogPrefix, sessionID, len(msgs))
	return ctx, nil
}
