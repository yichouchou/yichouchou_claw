package memory

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const markdownLogPrefix = "[memory.md]"

// SubDirs 是四象限子目录名,与 markdown_writer 强耦合。
// sessions 是浏览器用户会话;inputs/outputs 是 LLM 调用 trace;errors 是错误流。
var SubDirs = []string{"sessions", "inputs", "outputs", "errors"}

// AsyncMarkdownRecorder 是异步 + channel-based 的 Markdown 后端实现。
//
// 设计:
//   - Record* 方法立即返回,内部把 entry 投到 buffered channel
//   - 后台 worker goroutine 从 channel 取 entry,异步写入磁盘
//   - 进程退出时 Flush() 同步 drain;Close() 停 worker
//   - 写入失败仅 log,不向上抛(LLM 主流程不受影响)
//
// 并发安全:
//   - 所有 Record* 通过 buffered channel 串行化
//   - Flush() 等待 worker 把当前所有 entry 处理完
//   - Close() 后再 Record* 返回 ErrClosed
//
// 文件格式:每个 entry 是一个 YAML front-matter 块 + Markdown body:
//
//	---
//	session_id: "..."
//	llm_trace_id: "llm-..."
//	agent: "ChatAgent"
//	kind: "llm_input"
//	time: "2026-07-23T10:15:30+08:00"
//	summary: "rule-summary text"
//	---
//
//	```text
//	...content...
//	```
type AsyncMarkdownRecorder struct {
	workdir  string
	disabled bool

	queue    chan *entry
	flushCh  chan chan struct{}
	stopCh   chan struct{}
	doneCh   chan struct{}
	closedMu sync.RWMutex
	closed   bool

	// refiner 是可选的 LLM 摘要精炼队列。
	// nil = 关闭 refine(规则版摘要即是最终结果)。
	// 每个 entry 落盘成功后投递 RefineTask。
	refiner *RefineQueue
}

// entry 内部待写入条目。
type entry struct {
	subdir         string
	sessionID      string
	llmTraceID     LLMTraceID
	requestGroupID RequestGroupID // 本次浏览器请求组 ID(用于跨 entry 关联)
	agentName      string
	kind           string
	summary        string // 规则版摘要(用于 refine)
	contentSnippet string // 喂给 LLM 的内容片段
	bucket         TimeBucket
	front          string // YAML front-matter 块(已含 --- ... ---)
	body           string // Markdown body(含 fenced code block)
	fullPath       string // 完整路径(用于诊断)
}

// NewAsyncMarkdownRecorder 构造异步 recorder。
//
// 三个开关:
//   - YICHOUCHOU_MEMORY=off/false/0/no/disable/disabled → disabled=true,所有 Record* no-op
//   - workdir 空 → disabled=true
//   - queueSize 由调用方决定;默认 1024,够 LLM trace 突发不丢
//
// 构造后立即启动 worker goroutine。Close() 前 worker 会一直跑。
func NewAsyncMarkdownRecorder(workdir string) (*AsyncMarkdownRecorder, error) {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("YICHOUCHOU_MEMORY"))); v != "" {
		switch v {
		case "off", "false", "0", "no", "disable", "disabled":
			log.Printf("%s disabled via YICHOUCHOU_MEMORY=%q", markdownLogPrefix, v)
			return &AsyncMarkdownRecorder{disabled: true}, nil
		}
	}
	if strings.TrimSpace(workdir) == "" {
		log.Printf("%s workdir is empty, recorder disabled", markdownLogPrefix)
		return &AsyncMarkdownRecorder{disabled: true}, nil
	}
	memDir := filepath.Join(workdir, "memory")
	for _, sub := range SubDirs {
		if err := os.MkdirAll(filepath.Join(memDir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}
	r := &AsyncMarkdownRecorder{
		workdir: memDir,
		queue:   make(chan *entry, 1024),
		flushCh: make(chan chan struct{}),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	go r.worker()
	return r, nil
}

// Root 返回 recorder 根目录。
func (r *AsyncMarkdownRecorder) Root() string {
	if r == nil || r.disabled {
		return ""
	}
	return r.workdir
}

// SetRefiner 绑定一个 RefineQueue,使每个 entry 落盘后自动投递 refine 任务。
// 传 nil 关闭 refine。
func (r *AsyncMarkdownRecorder) SetRefiner(q *RefineQueue) {
	if r == nil {
		return
	}
	r.refiner = q
}

// === Recorder 接口实现 ===

// RecordUserRequest 浏览器/客户端发起的请求。
// requestGroupID 必填:用于把"一次浏览器请求"的所有 entry(user_request +
// N 个 llm 调用 + user_response)串起来;空字符串会内部自动生成新 ID。
// 自动生成 trace id(用于 LLM refine 链路跟踪,即便不是 LLM 调用 trace)。
func (r *AsyncMarkdownRecorder) RecordUserRequest(ctx context.Context, requestGroupID, sessionID, agentName string, content []byte) error {
	if r == nil || r.disabled {
		return nil
	}
	gid := requestGroupID
	if gid == "" {
		gid = NewRequestGroupID()
	}
	traceID := NewLLMTraceID()
	return r.enqueueWithGroup("sessions", sessionID, traceID, RequestGroupID(gid), agentName, "user_request", content)
}

// RecordUserResponse 响应给浏览器/客户端的内容。
// requestGroupID 必须与本次请求对应的 RecordUserRequest 一致。
func (r *AsyncMarkdownRecorder) RecordUserResponse(ctx context.Context, requestGroupID, sessionID, agentName string, content []byte) error {
	if r == nil || r.disabled {
		return nil
	}
	gid := requestGroupID
	if gid == "" {
		gid = RequestGroupIDFromContext(ctx)
	}
	if gid == "" {
		gid = NewRequestGroupID()
	}
	traceID := NewLLMTraceID()
	return r.enqueueWithGroup("sessions", sessionID, traceID, RequestGroupID(gid), agentName, "user_response", content)
}

// RecordLLMInput 一次 ChatModel.Invoke 的请求。
// recorder 自动从 ctx 读 request_group_id(若调用方已注入),写入 front-matter。
func (r *AsyncMarkdownRecorder) RecordLLMInput(ctx context.Context, sessionID, llmTraceID, agentName string, content []byte) error {
	if r == nil || r.disabled {
		return nil
	}
	gid := RequestGroupIDFromContext(ctx)
	return r.enqueueWithGroup("inputs", sessionID, llmTraceID, gid, agentName, "llm_input", content)
}

// RecordLLMOutput 一次 ChatModel.Invoke 的响应。
// recorder 自动从 ctx 读 request_group_id(若调用方已注入),写入 front-matter。
func (r *AsyncMarkdownRecorder) RecordLLMOutput(ctx context.Context, sessionID, llmTraceID, agentName string, content []byte) error {
	if r == nil || r.disabled {
		return nil
	}
	gid := RequestGroupIDFromContext(ctx)
	return r.enqueueWithGroup("outputs", sessionID, llmTraceID, gid, agentName, "llm_output", content)
}

// RecordError 错误流
func (r *AsyncMarkdownRecorder) RecordError(ctx context.Context, sessionID, llmTraceID, agentName string, err error, ctxStr string) error {
	if r == nil || r.disabled {
		return nil
	}
	if err == nil {
		return nil
	}
	body := fmt.Sprintf("**ctx**: %s\n\n**err**: `%s`\n",
		escape(ctxStr), strings.TrimSpace(err.Error()))
	return r.enqueueRaw("errors", sessionID, llmTraceID, agentName, "error", body)
}

// Flush 同步等待 worker 把当前所有 entry 落盘。
// Close 前必须调一次。
func (r *AsyncMarkdownRecorder) Flush() error {
	if r == nil || r.disabled {
		return nil
	}
	r.closedMu.RLock()
	if r.closed {
		r.closedMu.RUnlock()
		return nil
	}
	r.closedMu.RUnlock()

	respCh := make(chan struct{})
	select {
	case r.flushCh <- respCh:
		<-respCh
	case <-time.After(30 * time.Second):
		return fmt.Errorf("flush timeout after 30s")
	}
	return nil
}

// Close 停止 worker goroutine(必须在 Flush 后调)。
// 二次 Close 安全(idempotent)。
func (r *AsyncMarkdownRecorder) Close() error {
	if r == nil || r.disabled {
		return nil
	}
	r.closedMu.Lock()
	if r.closed {
		r.closedMu.Unlock()
		return nil
	}
	r.closed = true
	r.closedMu.Unlock()

	close(r.stopCh)
	<-r.doneCh
	return nil
}

// === 内部 ===

// enqueue 入队普通 entry(自动生成 front-matter 与 body)。
//
// 摘要生成按 kind 分流:
//   - sessions 类 (user_request / user_response) → RuleSummaryForSessions
//   - 其他 (llm_input / llm_output / ...)       → RuleSummary(剥离元信息 + 按语言过滤)
//
// 见 RuleSummaryKind 与 RuleSummaryForSessions 的注释了解分流原因。
//
// 此为不带 request_group_id 的便捷封装(给 ctx 里没有 group id 的旧调用方);
// 内部自动 group="" 透传,front-matter 不写 group_id 字段。
func (r *AsyncMarkdownRecorder) enqueue(subdir, sessionID, llmTraceID, agentName, kind string, content []byte) error {
	if len(content) == 0 {
		return nil
	}
	summary := RuleSummaryKind(kind, content)
	front := renderFrontMatter("", sessionID, llmTraceID, agentName, kind, time.Now(), summary)
	body := fmt.Sprintf("\n```text\n%s\n```\n", string(content))
	return r.enqueueRawWithFront(subdir, sessionID, llmTraceID, "", agentName, kind, front, body, summary, content)
}

// enqueueWithGroup 入队带 request_group_id 的 entry。
//
// 摘要生成按 kind 分流(同 enqueue):
//   - sessions 类 → RuleSummaryForSessions
//   - 其他 → RuleSummary
//
// requestGroupID 写入 front-matter 的 `request_group_id:` 字段(空字符串时不写)。
func (r *AsyncMarkdownRecorder) enqueueWithGroup(subdir, sessionID, llmTraceID string, requestGroupID RequestGroupID, agentName, kind string, content []byte) error {
	if len(content) == 0 {
		return nil
	}
	summary := RuleSummaryKind(kind, content)
	front := renderFrontMatter(requestGroupID, sessionID, llmTraceID, agentName, kind, time.Now(), summary)
	body := fmt.Sprintf("\n```text\n%s\n```\n", string(content))
	return r.enqueueRawWithFront(subdir, sessionID, llmTraceID, requestGroupID, agentName, kind, front, body, summary, content)
}

// enqueueRaw 入队自定义 body(用于 error / sessions 事件)。
func (r *AsyncMarkdownRecorder) enqueueRaw(subdir, sessionID, llmTraceID, agentName, kind, body string) error {
	summary := RuleSummary([]byte(body))
	front := renderFrontMatter("", sessionID, llmTraceID, agentName, kind, time.Now(), summary)
	return r.enqueueRawWithFront(subdir, sessionID, llmTraceID, "", agentName, kind, front, body, summary, []byte(body))
}

func (r *AsyncMarkdownRecorder) enqueueRawWithFront(
	subdir, sessionID, llmTraceID string, requestGroupID RequestGroupID, agentName, kind, front, body string,
	summary string, contentSnippet []byte,
) error {
	if sessionID == "" {
		// sessionID 空 → 跳过(浏览器用户会话必须有 session)
		return nil
	}

	bucket := BucketFromNow()
	path := bucket.BucketPath(filepath.Join(r.workdir, subdir))

	e := &entry{
		subdir:         subdir,
		sessionID:      sessionID,
		llmTraceID:     LLMTraceID(llmTraceID),
		requestGroupID: requestGroupID,
		agentName:      agentName,
		kind:           kind,
		summary:        summary,
		contentSnippet: string(contentSnippet),
		bucket:         bucket,
		front:          front,
		body:           body,
		fullPath:       path,
	}

	r.closedMu.RLock()
	closed := r.closed
	r.closedMu.RUnlock()
	if closed {
		return fmt.Errorf("recorder closed")
	}

	select {
	case r.queue <- e:
		return nil
	case <-time.After(100 * time.Millisecond):
		// channel 满 + worker 长时间未消费,放弃该 entry
		log.Printf("%s queue full, dropping entry session=%s kind=%s",
			markdownLogPrefix, sessionID, kind)
		return fmt.Errorf("queue full")
	}
}

// worker 后台写盘 goroutine。
//
// 从 queue 取 entry,串行写入。Flush() 通过 flushCh 触发同步等待。
func (r *AsyncMarkdownRecorder) worker() {
	defer close(r.doneCh)
	for {
		select {
		case <-r.stopCh:
			// 退出前尽量把残留的 entry 也写了
			for {
				select {
				case e := <-r.queue:
					r.writeOne(e)
				default:
					return
				}
			}
		case e := <-r.queue:
			r.writeOne(e)
		case respCh := <-r.flushCh:
			// 同步 drain:把所有待写 entry 写完,然后回 ack
			drained := false
			for !drained {
				select {
				case e := <-r.queue:
					r.writeOne(e)
				default:
					drained = true
				}
			}
			respCh <- struct{}{}
		}
	}
}

func (r *AsyncMarkdownRecorder) writeOne(e *entry) {
	if e == nil {
		return
	}
	dir := filepath.Join(r.workdir, e.subdir, e.bucket.DateDir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("%s mkdir %s err=%v", markdownLogPrefix, dir, err)
		return
	}
	f, err := os.OpenFile(e.fullPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("%s open %s err=%v", markdownLogPrefix, e.fullPath, err)
		return
	}
	if _, err := f.WriteString(e.front + e.body); err != nil {
		_ = f.Close()
		log.Printf("%s write %s err=%v", markdownLogPrefix, e.fullPath, err)
		return
	}
	if err := f.Close(); err != nil {
		log.Printf("%s close %s err=%v", markdownLogPrefix, e.fullPath, err)
	}

	// 落盘成功 → 投递 refine 任务(可选)。
	// 仅当 refiner 已配置 + llm_trace_id 非空时投递。
	// user_request / user_response 没 llm_trace_id 时也投递,refine 后
	// 仍然可以改善"用户问了什么"的摘要质量。
	if r.refiner != nil && e.llmTraceID != "" {
		// sessions 类的 entry 用 RequestGroupID + Side 精修定位;其它 kind 仍走 llm_trace_id
		var (
			reqGID RequestGroupID
			side   string
		)
		if isSessionsKind(e.kind) {
			reqGID = e.requestGroupID
			switch e.kind {
			case "user_request":
				side = "request"
			case "user_response":
				side = "response"
			}
		}
		r.refiner.Enqueue(RefineTask{
			FilePath:        e.fullPath,
			SessionID:       e.sessionID,
			LLMTraceID:      e.llmTraceID,
			AgentName:       e.agentName,
			OriginalSummary: e.summary,
			ContentSnippet:  e.contentSnippet,
			Kind:            e.kind,
			RequestGroupID:  reqGID,
			Side:            side,
		})
	}
}

// renderFrontMatter 生成 YAML front-matter。
//
// 字段:
//   - request_group_id 本次浏览器请求的组 ID(可选,空时不写)。用于把同一次
//     浏览器请求的 user_request + N 个 llm 调用 + user_response
//     串成一个原子单元。
//   - session_id       浏览器/客户端用户会话 ID
//   - llm_trace_id     LLM 调用 trace ID(可能为空,如 user_request / error)
//   - agent            agent 名称
//   - kind             entry 类型(user_request / llm_input / llm_output / ...)
//   - time             RFC3339 时间戳
//   - summary          ≤100 字摘要(规则版,后续 LLM refine 会异步覆盖)
//
// v2 兼容性:request_group_id 字段缺失时,旧 entry 与新 entry 共存。LLM refine
// 在 sessions 类下按 request_group_id + side (request/response) 定位;不存在
// group_id 的旧 entry 回退到按 llm_trace_id 定位。
func renderFrontMatter(requestGroupID RequestGroupID, sessionID, llmTraceID, agentName, kind string, t time.Time, summary string) string {
	header := fmt.Sprintf(
		"---\nsession_id: %q\nllm_trace_id: %q\nagent: %q\nkind: %q\ntime: %q\nsummary: %q\n---\n",
		sessionID, string(llmTraceID), agentName, kind, t.Format(time.RFC3339), summary)
	if requestGroupID != "" {
		// 插入到 session_id 之前,作为最显眼的"组标识"
		header = fmt.Sprintf(
			"---\nrequest_group_id: %q\nsession_id: %q\nllm_trace_id: %q\nagent: %q\nkind: %q\ntime: %q\nsummary: %q\n---\n",
			string(requestGroupID), sessionID, string(llmTraceID), agentName, kind, t.Format(time.RFC3339), summary)
	}
	return header
}

func escape(s string) string {
	if s == "" {
		return "(none)"
	}
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
