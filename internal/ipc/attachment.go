// Package ipc 提供 process-level 附件 IPC 通道 (2026-08-03 新增)。
//
// 背景:
//
//	之前 LLM 接收图片 URL/Base64Data (Codex 模式 / data URL 模式),
//	导致 Ark 对 URL 校验失败 (私有网段/格式问题) 或 token 浪费 (大体积 base64)。
//
//	这次重构成 IPC 通道模式: LLM 上下文 **完全不接触** 图片字节,
//	工具产物附件 URL 通过 ctx 里的 channel 直接推 main.go SSE handler,
//	main.go 发 SSE attachment event 给前端,前端 <img src=URL> 直接 fetch。
//
// 设计:
//
//   - sync.Map[sessionID]chan AttachmentEvent: session 级的 publish/subscribe
//   - ctx 注入: 每个 session 启动时分配 channel, 注入 ctx
//   - 工具 wrapper: ipc.PublishAttachment(ctx, ev) → 写 channel (非阻塞)
//   - main.go session handler goroutine: 从 channel 读 → 发 SSE event
//   - session 结束: cleanup() 关闭 channel,从 sync.Map 移除
//
// 为什么这个设计:
//
//   - 框架无关: 完全 eino ChatModelAgent 框架外, 不污染 LLM 上下文
//   - 进程级: 一个 Go 进程内共享, 不需要引入 Redis/NATS
//   - 同步语义: channel 缓冲 64, 写不阻塞; 慢消费者会丢信号(drop)而非阻塞工具
//   - 跨 goroutine 安全: sync.Map 读写并发安全
package ipc

import (
	"context"
	"sync"
	"sync/atomic"
)

// AttachmentType 标识附件的媒体类型 (前端决定渲染方式)。
type AttachmentType string

const (
	AttachmentImage AttachmentType = "image"
	AttachmentAudio AttachmentType = "audio"
	AttachmentVideo AttachmentType = "video"
	AttachmentFile  AttachmentType = "file" // text/plain / application/pdf / .drawio 等
)

// AttachmentEvent 是工具产物 → 前端的 IPC 消息。
//
// 字段语义:
//
//   - Type: 媒体类型,前端按 type 渲染 (<img> / <audio> / <video> / <a>)
//   - URL: 相对 URL,前端 fetch 时优先用这个(同源,无需 CORS)
//   - AbsoluteURL: 完整 URL(http://host:port/...),备用,前端日常不用
//   - MIMEType: 完整 MIME 类型,前端精细控制渲染(如 application/pdf 内嵌预览)
//   - Size: 文件大小,前端展示+日志
//   - Name: 原始文件名,前端展示
//   - OriginalPath: 产物在主机的源路径,LLM 文本元数据里会用到
//   - Source: 哪类工具产出的("local_command"),日志/统计用
type AttachmentEvent struct {
	Type         AttachmentType `json:"type"`
	URL          string         `json:"url"`
	AbsoluteURL  string         `json:"absolute_url,omitempty"`
	MIMEType     string         `json:"mime_type"`
	Size         int64          `json:"size"`
	Name         string         `json:"name"`
	OriginalPath string         `json:"original_path,omitempty"`
	Source       string         `json:"source,omitempty"`
}

// sessionCh entry: 每个 session 一个 channel,关闭后从 Map 移除
type sessionCh struct {
	ch     chan AttachmentEvent
	closed atomic.Bool
}

// global 是 process-level session channel 注册表。
var global sync.Map // map[string]*sessionCh

// ChannelBufferSize 是每个 session channel 的缓冲大小。
//
// 选 64 的理由:
//   - 工具产物一般 1-5 个/turn,buffer 足够容下一个 turn
//   - 不至于内存爆 (channel buffer 在堆上, 每个元素 ~200 bytes)
//   - 慢消费者(网速差)只丢信号, 不阻塞工具 wrapper
const ChannelBufferSize = 64

// RegisterSession 给 sessionID 分配 channel, 返回 channel + cleanup function。
//
// caller 必须在 session 结束时调用 cleanup
//   - 关闭 channel
//   - 从 global sync.Map 删除
//
// 返回:
//   - <chan AttachmentEvent: 双向 channel (用于 WithAttachmentChannelV2/PublishAttachment,发送方向)
//   - cleanup: 函数
//
// 用法 (main.go session handler):
//
//	ch, cleanup := ipc.RegisterSession(sessionID)
//	defer cleanup()
//	// 读端: 启 goroutine 读 ch → SSE event
//	// 写端: ctx 注入后,工具 wrapper PublishAttachment 写 ch
//	ctx := ipc.WithAttachmentChannelV2(ctx, ch)
//	go func() { for ev := range ch { sse.Send(ev) } }()
func RegisterSession(sessionID string) (chan AttachmentEvent, func()) {
	sc := &sessionCh{
		ch: make(chan AttachmentEvent, ChannelBufferSize),
	}
	global.Store(sessionID, sc)

	cleanup := func() {
		if sc.closed.CompareAndSwap(false, true) {
			close(sc.ch)
		}
		global.Delete(sessionID)
	}
	return sc.ch, cleanup
}

// SessionsCount 返回当前活跃的 session 数 (用于监控 / 单测)。
func SessionsCount() int {
	count := 0
	global.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// attachmentContextKey 单独的 type,避免 string 冲突。
type attachmentContextKey struct{}

// WithAttachmentChannel 注入 attachment channel (send-only 视角) 到 ctx。
//
// 工具 wrapper 通过 ctx 拿 channel,PublishAttachment 写入。
// 没有 channel → no-op (兼容老调用路径,LLM 上下文只有文本元数据)。
func WithAttachmentChannel(ctx context.Context, ch chan<- AttachmentEvent) context.Context {
	return context.WithValue(ctx, attachmentContextKey{}, ch)
}

// PublishAttachment 把附件事件写入 ctx 里的 channel。
//
// 行为:
//   - ctx 没 channel (没注入 / 老调用) → silent drop
//   - channel buffer 满 → silent drop (不阻塞工具 = 不阻塞 LLM 推理)
//   - channel 已关闭 → silent drop (recover)
//
// 调用方: LocalCommandAgent 工具 wrapper
//
// 设计权衡:
//   - 非阻塞是必须的: 工具 wrapper 在 LLM 推理链路上,不能等 SSE handler
//   - silent drop 是可接受的: 极端情况下丢 1-2 个 attachment 事件,LLM 仍能继续推理
//   - 真实环境一般不会丢: SSE handler 通常 < 100ms 处理,远快于工具 wrapper 产速
func PublishAttachment(ctx context.Context, ev AttachmentEvent) {
	v := ctx.Value(attachmentContextKey{})
	if v == nil {
		// 没注入 channel: 跳过 (兼容测试 / 老调用路径)
		return
	}
	ch, ok := v.(chan<- AttachmentEvent)
	if !ok {
		// 类型断言失败: 注入的不是 channel
		return
	}
	// 非阻塞写入 + 关闭检查
	defer func() {
		recover() // 关闭后写入 → recover,保持主流程不挂
	}()
	select {
	case ch <- ev:
		// success
	default:
		// buffer 满,丢信号
		// log.Printf("[ipc] attachment channel full, dropping event for %s", ev.Name)
	}
}
