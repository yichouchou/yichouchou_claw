// Package memory persists per-session trace data into a four-quadrant
// directory layout under <workdir>/memory/:
//
//	<workdir>/memory/
//	├── sessions/YYYY-MM-DD/HHh.md   <- browser/客户端用户会话流
//	│   包含:用户请求/响应、LLM 调用 trace 列表、摘要
//	├── inputs/YYYY-MM-DD/HHh.md     <- 每次 ChatModel.Invoke 的请求
//	│   完整 payload + llm_trace_id + 100 字摘要
//	├── outputs/YYYY-MM-DD/HHh.md    <- 每次 ChatModel.Invoke 的响应
//	│   与 inputs 一一对应(同 llm_trace_id)+ 100 字摘要
//	└── errors/YYYY-MM-DD/HHh.md     <- 错误流(LLM/工具/agent 异常)
//
// 关键 ID 体系:
//   - session_id   浏览器/客户端用户会话唯一 ID(可由前端传入或服务端生成)
//   - llm_trace_id 每次 ChatModel.Invoke 唯一 ID(UUIDv4)
//     inputs/outputs 一一对应,通过这个 ID 配对
//
// 写入策略:异步
//
// 所有 Record* 方法立即返回,内部 channel + worker goroutine 写入磁盘。
// 进程退出时通过 Flush() 优雅 drain。这保证 LLM 主流程不被磁盘 I/O 阻塞。
//
// 摘要策略:混合(规则同步 + LLM 异步)
//
//   - 规则版摘要:Record* 调用时立即生成(<100 字,首末截取),保证 front-matter
//     落地后立即可读
//   - LLM refine: 后台 worker 调另一个 ChatModel 生成 ≤100 字更精准摘要,
//     fire-and-forget 覆盖原 summary 字段
package memory

import (
	"context"
	"log"
	"sync"
)

const memoryLogPrefix = "[memory]"

// MaxSummaryLength 摘要硬上限(中英文混合估算,100 字 = 100 字符)
const MaxSummaryLength = 100

// LLMTraceID 是每次 ChatModel.Invoke 的唯一 ID。
// 用 UUIDv4,避免时间戳碰撞(同一毫秒多次 invoke)。
//
// 一个 llm_trace_id 包含:
//   - 一次完整 input(系统提示 + 历史 + 用户消息 + 工具调用)
//   - 一次完整 output(助手文本 + tool_calls)
//
// 通过 llm_trace_id 把 inputs/ 和 outputs/ 的一对记录关联起来。
type LLMTraceID = string

// EntryKind 标识一条 memory 记录的类型
type EntryKind string

const (
	KindUserRequest  EntryKind = "user_request"  // 浏览器/客户端发来的请求
	KindUserResponse EntryKind = "user_response" // 响应给浏览器/客户端的内容
	KindLLMInput     EntryKind = "llm_input"     // LLM 调用的请求(payload)
	KindLLMOutput    EntryKind = "llm_output"    // LLM 调用的响应(payload)
	KindError        EntryKind = "error"         // LLM/工具/agent 异常
	KindSessionStart EntryKind = "session_start" // session 开始标记(可选)
	KindSessionEnd   EntryKind = "session_end"   // session 结束标记(可选)
)

// Recorder 是 memory 后端的契约。
//
// 异步契约:
//   - 所有 Record* 方法立即返回(nil error 或 channel-full error),不阻塞 LLM
//   - 实现内部用 channel + worker,失败仅 log,不向上抛
//   - 进程退出时由 Close() 优雅 drain
//
// 一致性约束:
//   - RecordLLMInput 和 RecordLLMOutput 用同一 llm_trace_id 配对
//   - session_id 必须存在(浏览器用户会话 ID);llm_trace_id 可空(error 类)
//   - RecordUserRequest 和 RecordUserResponse 用同一 request_group_id 配对
//   - 一次 request_group 覆盖"一次浏览器请求"触发的所有 LLM 调用 + 最终响应
//
// 摘要约束:
//   - summary 由实现方生成(规则版 + LLM refine)
//   - 长度硬上限 MaxSummaryLength = 100
type Recorder interface {
	// === 浏览器用户会话维度(per session_id × request_group_id) ===

	// RecordUserRequest 浏览器/客户端发起的请求(用户输入的 query)
	// requestGroupID 用于把"一次浏览器请求"的所有 entry(user_request + N 个
	// llm 调用 + user_response)串起来;为空时 recorder 内部自动生成/不写。
	RecordUserRequest(ctx context.Context, requestGroupID, sessionID, agentName string, content []byte) error

	// RecordUserResponse 响应给浏览器/客户端的内容(完整 assistant 消息)
	// requestGroupID 必须与本次请求对应的 RecordUserRequest 一致。
	RecordUserResponse(ctx context.Context, requestGroupID, sessionID, agentName string, content []byte) error

	// === LLM 调用维度(per llm_trace_id) ===

	// RecordLLMInput 一次 ChatModel.Invoke 的请求 payload。
	// 必须配对调用 RecordLLMOutput(同一 llm_trace_id)
	// recorder 内部应自动从 ctx 读 request_group_id,写入 front-matter
	// (如 ctx 没有 group_id,字段可空)。
	RecordLLMInput(ctx context.Context, sessionID, llmTraceID, agentName string, content []byte) error

	// RecordLLMOutput 一次 ChatModel.Invoke 的响应 payload。
	// 必须与 RecordLLMInput 同一 llmTraceID。
	// recorder 内部应自动从 ctx 读 request_group_id,写入 front-matter。
	RecordLLMOutput(ctx context.Context, sessionID, llmTraceID, agentName string, content []byte) error

	// === 错误流(可独立于 LLM trace) ===

	// RecordError 捕获 LLM 错误 / 工具错误 / agent 异常
	// llmTraceID 可空(有些错误不绑特定 LLM 调用)
	RecordError(ctx context.Context, sessionID, llmTraceID, agentName string, err error, ctxStr string) error

	// === 生命周期 ===

	// Flush 强制把 channel 里所有待写入条目落盘(同步版本)
	// Close 调用前必须调一次
	Flush() error

	// Close 停止 worker goroutine,Flush 后调,优雅退出
	Close() error

	// Root 返回 recorder 根目录(调试用)
	Root() string
}

// global recorder 单例。
var (
	globalMu sync.RWMutex
	globalR  Recorder
)

// SetRecorder 安装包级 recorder。
// nil 表示禁用(middleware 短路,no-op)。
func SetRecorder(r Recorder) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalR = r
}

// GetRecorder 取出当前 recorder(none-set 则 nil)。
func GetRecorder() Recorder {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalR
}

// Reset 移除当前 recorder(仅测试用)。
func Reset() {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalR = nil
}

// === 异步 helper(middleware 用 fire-and-forget 模式) ===

// safeAsync 通用异步包装:把 r.Record* 错误 log,但不向上抛。
func safeAsync(method string, fn func() error) {
	r := GetRecorder()
	if r == nil {
		return
	}
	if err := fn(); err != nil {
		log.Printf("%s %s err=%v", memoryLogPrefix, method, err)
	}
}

// SafeRecordUserRequest RecordUserRequest 的异步封装(同步实现立即返回)
//
// requestGroupID 必填:对应"一次浏览器请求",后续所有相关 entry(user_request,
// llm_input/output × N, user_response)都用同一个 group_id 串起来。
// 若调用方未显式指定,内部回退到 NewRequestGroupID() 生成。
func SafeRecordUserRequest(ctx context.Context, requestGroupID, sessionID, agentName string, content []byte) {
	gid := requestGroupID
	if gid == "" {
		gid = NewRequestGroupID()
	}
	safeAsync("RecordUserRequest", func() error {
		return GetRecorder().RecordUserRequest(ctx, gid, sessionID, agentName, content)
	})
}

// SafeRecordUserResponse RecordUserResponse 的异步封装
//
// requestGroupID 应与同次请求对应的 SafeRecordUserRequest 一致。
// 若为空,内部尝试从 ctx 读(RequestGroupIDFromContext);读不到才生成新 id。
func SafeRecordUserResponse(ctx context.Context, requestGroupID, sessionID, agentName string, content []byte) {
	gid := requestGroupID
	if gid == "" {
		gid = RequestGroupIDFromContext(ctx)
	}
	if gid == "" {
		gid = NewRequestGroupID()
	}
	safeAsync("RecordUserResponse", func() error {
		return GetRecorder().RecordUserResponse(ctx, gid, sessionID, agentName, content)
	})
}

// SafeRecordLLMInput RecordLLMInput 的异步封装
func SafeRecordLLMInput(ctx context.Context, sessionID, llmTraceID, agentName string, content []byte) {
	safeAsync("RecordLLMInput", func() error {
		return GetRecorder().RecordLLMInput(ctx, sessionID, llmTraceID, agentName, content)
	})
}

// SafeRecordLLMOutput RecordLLMOutput 的异步封装
func SafeRecordLLMOutput(ctx context.Context, sessionID, llmTraceID, agentName string, content []byte) {
	safeAsync("RecordLLMOutput", func() error {
		return GetRecorder().RecordLLMOutput(ctx, sessionID, llmTraceID, agentName, content)
	})
}

// SafeRecordError RecordError 的异步封装
func SafeRecordError(ctx context.Context, sessionID, llmTraceID, agentName string, err error, ctxStr string) {
	safeAsync("RecordError", func() error {
		return GetRecorder().RecordError(ctx, sessionID, llmTraceID, agentName, err, ctxStr)
	})
}
