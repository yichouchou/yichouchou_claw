package memory

import (
	"context"

	"github.com/google/uuid"
)

// LLMTraceIDCtxKey 是 ctx 里 llm_trace_id 的 key。
// 用 struct 类型作为 key,避免与第三方库的 string key 冲突。
type llmTraceIDCtxKey struct{}

// llmAgentCtxKey 是 ctx 里 agent name 的 key(在 chatmodel middleware 中注入,
// 让 markdown_writer 等下游组件知道当前是哪条 agent 链)。
type llmAgentCtxKey struct{}

// NewLLMTraceID 生成新的全局唯一 llm_trace_id(UUIDv4)。
//
// 设计:用 UUIDv4 而不是时间戳 + 进程 ID,避免:
//   - 同一毫秒多次 ChatModel.Invoke 的时间戳碰撞
//   - 跨进程复制(测试 / 多 worker 场景)
//   - 可枚举(攻击者枚举出 trace 序列)
func NewLLMTraceID() LLMTraceID {
	return LLMTraceID("llm-" + uuid.NewString())
}

// WithLLMTraceID 把 llm_trace_id 注入 ctx。
//
// 用法:ChatModel middleware 在 BeforeModelRewriteState 之前调,
// 后续 RecordLLMInput / RecordLLMOutput 从 ctx 读取。
func WithLLMTraceID(ctx context.Context, traceID LLMTraceID) context.Context {
	if traceID == "" {
		return ctx
	}
	return context.WithValue(ctx, llmTraceIDCtxKey{}, traceID)
}

// LLMTraceIDFromContext 从 ctx 取出 llm_trace_id。没注入则返回空串。
func LLMTraceIDFromContext(ctx context.Context) LLMTraceID {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(llmTraceIDCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// WithAgentName 把当前 agent name 注入 ctx(用于跨子 agent 调用时,
// 下游组件知道这次调用的发起 agent)。
func WithAgentName(ctx context.Context, agentName string) context.Context {
	if agentName == "" {
		return ctx
	}
	return context.WithValue(ctx, llmAgentCtxKey{}, agentName)
}

// AgentNameFromContext 从 ctx 取出 agent name。
func AgentNameFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(llmAgentCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// RequestGroupID 是"一次浏览器用户请求"的组 ID(覆盖该请求触发的所有 LLM 调用 + 最终响应)。
//
// 设计动机:
//   - 一次浏览器请求可能触发 N 次 LLM 调用(RouterAgent → ChatAgent → 工具 → 再调用)
//   - 但用户视角只有"一次 request + 一次 response"
//   - "一次会话" 由 sessionID 标识,粒度太大(一个会话含多轮)
//   - "一次 LLM 调用" 由 llm_trace_id 标识,粒度太细(N 个 trace)
//   - 需要一个中间粒度:"一次浏览器请求组" = request_group_id
//
// 格式:"req-" + UUIDv4,与 llm_trace_id 的 "llm-" 前缀区分。
//
// 流转路径:
//   - 由 main.go handleChat 在请求开始时生成
//   - 通过 adk.WithSessionValues 注入 ctx(key 见 session.KeyRequestGID)
//   - 所有 LLM middleware 读 ctx 拿到这个 group_id
//   - RecordUserRequest / RecordUserResponse 落盘时写入 front-matter
//   - RecordLLMInput / RecordLLMOutput 也写入 front-matter(可选关联)
//   - LLM refine worker 精修 sessions 时按 group_id + side (request/response) 定位
type RequestGroupID = string

// requestGroupIDCtxKey 是 ctx 里 request_group_id 的 key(私有,避免与第三方 key 冲突)。
type requestGroupIDCtxKey struct{}

// NewRequestGroupID 生成新的 request_group_id(UUIDv4,"req-" 前缀)。
func NewRequestGroupID() RequestGroupID {
	return RequestGroupID("req-" + uuid.NewString())
}

// WithRequestGroupID 把 request_group_id 注入 ctx。
//
// 用法:main.go 在请求开始时生成 group_id,调用本函数注入 ctx。
// 后续 LLM middleware / RecordUserRequest / RecordUserResponse 从 ctx 读取。
func WithRequestGroupID(ctx context.Context, gid RequestGroupID) context.Context {
	if gid == "" {
		return ctx
	}
	return context.WithValue(ctx, requestGroupIDCtxKey{}, gid)
}

// RequestGroupIDFromContext 从 ctx 取出 request_group_id。没注入则返回空串。
func RequestGroupIDFromContext(ctx context.Context) RequestGroupID {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(requestGroupIDCtxKey{}).(string); ok {
		return v
	}
	return ""
}
