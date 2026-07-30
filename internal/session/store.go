package session

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"
)

const storeLogPrefix = "[session]"

// KeySessionID 是 SessionValues 中用来传递会话 id 的 key。
// 通过 adk.WithSessionValues 注入到 ctx，middleware 可从 ctx 中读取。
const KeySessionID = "session_id"

// KeyRequestGID 是 SessionValues 中用来传递"本次浏览器请求组 ID"的 key。
//
// 一个浏览器请求(用户视角:一次 request + 一次 response)可能触发 N 次
// LLM 调用(Router → ChatAgent → 工具 → 再调用)。用 request_group_id
// 把这 N 次 LLM 调用聚合起来,方便 sessions 目录的回溯与精修定位。
//
// 由 main.go handleChat 在请求开始时生成,通过 adk.WithSessionValues
// 注入 ctx,middleware 从 ctx 中读取。
const KeyRequestGID = "request_group_id"

// Store 是一个简单的内存版多轮对话会话存储。
// 同一 sessionID 共享一段对话历史，最多保留最近 MaxRounds 轮上下文。
//
// 多模态支持(2026-07-29):
//   - PersistPath 不为空时,Replace 后会把 messages 序列化到
//     <PersistPath>/<sessionID>.json,重启时自动 Load 恢复。
//   - schema.Message 本身支持 JSON 序列化(MultiContent/UserInputMultiContent
//     等多模态字段都包含在内),不需要自定义编解码。
//   - 单文件体积可能因为 base64 图片较大;Replace 时如果单条 message >8MB,
//     仅持久化 text+tool_calls,跳过 base64 内容。
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*Conversation
	// MaxRounds 限制保存的最大轮数（一问一答算 1 轮）。
	MaxRounds int
	// PersistPath 可选:磁盘持久化目录。空 → 不持久化。
	// 多模态场景强烈建议设置,否则 base64 图片只活在内存里。
	PersistPath string
}

// Conversation 保存单个会话的消息列表，索引按顺序递增。
type Conversation struct {
	Messages []*schema.Message
}

// NewStore 构造一个 Store，maxRounds <= 0 时使用默认值 12。
func NewStore(maxRounds int) *Store {
	if maxRounds <= 0 {
		maxRounds = 12
	}
	return &Store{
		sessions:  make(map[string]*Conversation),
		MaxRounds: maxRounds,
	}
}

// NewStoreWithPersist 构造带磁盘持久化的 Store。
//
// persistPath 会自动创建(若不存在)。重启时自动从该目录加载已存在的会话。
func NewStoreWithPersist(maxRounds int, persistPath string) *Store {
	s := NewStore(maxRounds)
	s.PersistPath = persistPath
	if persistPath != "" {
		if err := os.MkdirAll(persistPath, 0o755); err != nil {
			log.Printf("%s NewStoreWithPersist mkdir failed path=%s err=%v", storeLogPrefix, persistPath, err)
		}
	}
	return s
}

// Get 返回指定会话的当前消息副本。
func (s *Store) Get(sessionID string) []*schema.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	conv, ok := s.sessions[sessionID]
	if !ok {
		return nil
	}
	out := make([]*schema.Message, len(conv.Messages))
	copy(out, conv.Messages)
	return out
}

// Append 在会话末尾追加消息，并裁剪到最近 maxRounds 轮。
func (s *Store) Append(sessionID string, msgs ...*schema.Message) {
	if len(msgs) == 0 {
		return
	}
	for _, m := range msgs {
		//log.Printf("%s Append session=%s role=%s tool_call_id=%s tool_name=%s content_len=%d tool_calls=%d",
		//	storeLogPrefix, sessionID, m.Role, safeIDForLog(m.ToolCallID), m.ToolName, len(m.Content), len(m.ToolCalls))
		if len(m.ToolCalls) > 0 {
			logToolCalls("Append.tool_calls", m.ToolCalls)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conv, ok := s.sessions[sessionID]
	if !ok {
		conv = &Conversation{}
		s.sessions[sessionID] = conv
	}
	conv.Messages = append(conv.Messages, msgs...)
	s.trimLocked(conv)
	//log.Printf("%s Append done session=%s total_msgs=%d rounds=%d",
	//	storeLogPrefix, sessionID, len(conv.Messages), len(conv.Messages)/2)
	// 异步持久化(2026-07-29 新增): Append 也需要落盘,
	// 否则 PersistMiddleware 用 Append 后,Replace 那条路径不会被触发。
	if s.PersistPath != "" {
		go s.persistConvLocked(sessionID, conv)
	}
}

// Replace 直接以当前整段 messages 覆盖会话历史，并按 maxRounds 窗口裁剪。
// 用于 "AfterAgent" 钩子内 ChatModelAgent 维护的 state 已经包含了完整多轮上下文。
func (s *Store) Replace(sessionID string, msgs []*schema.Message) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conv, ok := s.sessions[sessionID]
	if !ok {
		conv = &Conversation{}
		s.sessions[sessionID] = conv
	}
	conv.Messages = append(conv.Messages[:0:0], msgs...)
	s.trimLocked(conv)
	// 统计多模态字段
	totalMulti := 0
	for _, m := range conv.Messages {
		totalMulti += len(m.UserInputMultiContent) + len(m.AssistantGenMultiContent)
	}
	log.Printf("%s Replace session=%s new_total_msgs=%d rounds=%d multi_parts=%d",
		storeLogPrefix, sessionID, len(conv.Messages), len(conv.Messages)/2, totalMulti)
	for i, m := range conv.Messages {
		multi := len(m.UserInputMultiContent) + len(m.AssistantGenMultiContent)
		log.Printf("%s   [%02d] role=%s tool_call_id=%s tool_name=%s tool_calls=%d text_len=%d multi_parts=%d",
			storeLogPrefix, i, m.Role, safeIDForLog(m.ToolCallID), m.ToolName, len(m.ToolCalls), len(m.Content), multi)
	}
	// 异步持久化（不阻塞 Replace 路径）
	if s.PersistPath != "" {
		go s.persistConvLocked(sessionID, conv)
	}
}

// persistConvLocked 把会话序列化到磁盘。
//
// 体积优化:对每条 message 做"瘦身"——
//   - 如果 Base64Data 长度 > 1MB,只保留前 64 字符 + "<truncated N bytes>" 标记
//   - 限制单条 message 总大小 < 8MB
//
// 不瘦身会出现问题:一张 5MB 图片 → 6.67MB base64 → 50 条历史就是 333MB,
// 重启时一次 Load 把内存打爆。
func (s *Store) persistConvLocked(sessionID string, conv *Conversation) {
	if s.PersistPath == "" {
		return
	}
	shrunk := shrinkMessagesForPersist(conv.Messages)
	data, err := json.Marshal(shrunk)
	if err != nil {
		log.Printf("%s persist marshal failed session=%s err=%v", storeLogPrefix, sessionID, err)
		return
	}
	if len(data) > 32*1024*1024 {
		log.Printf("%s persist too large session=%s bytes=%d, skipping", storeLogPrefix, sessionID, len(data))
		return
	}
	target := filepath.Join(s.PersistPath, sanitizeFileName(sessionID)+".json")
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("%s persist write failed session=%s err=%v", storeLogPrefix, sessionID, err)
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		log.Printf("%s persist rename failed session=%s err=%v", storeLogPrefix, sessionID, err)
		return
	}
	log.Printf("%s persist ok session=%s bytes=%d path=%s",
		storeLogPrefix, sessionID, len(data), target)
}

// LoadFromDisk 从 PersistPath 加载所有已持久化的会话到内存。
//
// 启动时调一次即可;会话会被追加到 sessions map(已有同名会被覆盖)。
// 加载失败的文件会被跳过并打日志,不影响其他会话。
func (s *Store) LoadFromDisk() error {
	if s.PersistPath == "" {
		return nil
	}
	if _, err := os.Stat(s.PersistPath); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	entries, err := os.ReadDir(s.PersistPath)
	if err != nil {
		return err
	}
	loaded := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.PersistPath, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("%s load read failed path=%s err=%v", storeLogPrefix, path, err)
			continue
		}
		var msgs []*schema.Message
		if err := json.Unmarshal(data, &msgs); err != nil {
			log.Printf("%s load unmarshal failed path=%s err=%v", storeLogPrefix, path, err)
			continue
		}
		sessionID := strings.TrimSuffix(e.Name(), ".json")
		s.mu.Lock()
		s.sessions[sessionID] = &Conversation{Messages: msgs}
		s.mu.Unlock()
		loaded++
	}
	log.Printf("%s LoadFromDisk loaded=%d from=%s", storeLogPrefix, loaded, s.PersistPath)
	return nil
}

// shrinkMessagesForPersist 在持久化前对多模态大数据做瘦身。
//
// 规则:
//   - Base64Data > 1MB → 截断为 64 字节 + "<truncated N bytes>" 标记
//   - 单条 message JSON > 8MB → 整个消息的 MultiContent/UserInputMultiContent
//     全置空(只保留 Content + ToolCalls)
func shrinkMessagesForPersist(msgs []*schema.Message) []*schema.Message {
	const maxBase64Inline = 1 * 1024 * 1024
	const maxMessageJSON = 8 * 1024 * 1024
	out := make([]*schema.Message, 0, len(msgs))
	for _, m := range msgs {
		copyM := *m
		// 修剪 UserInputMultiContent
		if len(copyM.UserInputMultiContent) > 0 {
			parts := copyM.UserInputMultiContent
			for i := range parts {
				truncateInputPartBase64(&parts[i], maxBase64Inline)
			}
			copyM.UserInputMultiContent = parts
		}
		// 修剪 AssistantGenMultiContent
		if len(copyM.AssistantGenMultiContent) > 0 {
			parts := copyM.AssistantGenMultiContent
			for i := range parts {
				truncateOutputPartBase64(&parts[i], maxBase64Inline)
			}
			copyM.AssistantGenMultiContent = parts
		}
		// 单条 message 太大 → 直接抹掉多模态
		if buf, err := json.Marshal(copyM); err == nil && len(buf) > maxMessageJSON {
			copyM.UserInputMultiContent = nil
			copyM.AssistantGenMultiContent = nil
		}
		out = append(out, &copyM)
	}
	return out
}

// truncateInputPartBase64 把过大的 base64 数据替换为截断标记。
//
// 字段类型都是 *string;传入指针,通过指针修改。
func truncateInputPartBase64(p *schema.MessageInputPart, maxBytes int) {
	truncateDataURL := func(url *string, b64 *string) {
		if b64 != nil && *b64 != "" && len(*b64) > maxBytes {
			marker := "<truncated " + itoa(len(*b64)) + " bytes>"
			*b64 = marker
			return
		}
		if url != nil && *url != "" && len(*url) > 32 && strings.HasPrefix(*url, "data:") && len(*url) > maxBytes {
			marker := "data:;<truncated " + itoa(len(*url)) + " bytes>"
			*url = marker
		}
	}
	if p.Image != nil {
		truncateDataURL(p.Image.URL, p.Image.Base64Data)
	}
	if p.Audio != nil {
		truncateDataURL(p.Audio.URL, p.Audio.Base64Data)
	}
	if p.Video != nil {
		truncateDataURL(p.Video.URL, p.Video.Base64Data)
	}
	if p.File != nil {
		truncateDataURL(p.File.URL, p.File.Base64Data)
	}
}

// truncateOutputPartBase64 与 truncateInputPartBase64 同形,只处理 output 类型。
func truncateOutputPartBase64(p *schema.MessageOutputPart, maxBytes int) {
	truncateDataURL := func(url *string, b64 *string) {
		if b64 != nil && *b64 != "" && len(*b64) > maxBytes {
			marker := "<truncated " + itoa(len(*b64)) + " bytes>"
			*b64 = marker
			return
		}
		if url != nil && *url != "" && len(*url) > 32 && strings.HasPrefix(*url, "data:") && len(*url) > maxBytes {
			marker := "data:;<truncated " + itoa(len(*url)) + " bytes>"
			*url = marker
		}
	}
	if p.Image != nil {
		truncateDataURL(p.Image.URL, p.Image.Base64Data)
	}
	if p.Audio != nil {
		truncateDataURL(p.Audio.URL, p.Audio.Base64Data)
	}
	if p.Video != nil {
		truncateDataURL(p.Video.URL, p.Video.Base64Data)
	}
}

// itoa 是 strconv.Itoa 的轻量替代,避免在这里 import strconv。
// 仅用于构造 "<truncated N bytes>" 标记字符串。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// sanitizeFileName 把 sessionID 里的不安全字符替换为下划线。
func sanitizeFileName(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, ":", "_")
	s = strings.ReplaceAll(s, "*", "_")
	s = strings.ReplaceAll(s, "?", "_")
	s = strings.ReplaceAll(s, "\"", "_")
	s = strings.ReplaceAll(s, "<", "_")
	s = strings.ReplaceAll(s, ">", "_")
	s = strings.ReplaceAll(s, "|", "_")
	return s
}

// trimLocked 仅保留最近 maxRounds 轮会话，每轮包含一条 user 和一条 assistant。
func (s *Store) trimLocked(conv *Conversation) {
	if s.MaxRounds <= 0 {
		conv.Messages = nil
		return
	}
	maxMessages := s.MaxRounds * 2
	if len(conv.Messages) > maxMessages {
		conv.Messages = conv.Messages[len(conv.Messages)-maxMessages:]
	}
}

// Reset 清空指定会话。
func (s *Store) Reset(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
	// 同步删除磁盘文件
	if s.PersistPath != "" {
		path := filepath.Join(s.PersistPath, sanitizeFileName(sessionID)+".json")
		_ = os.Remove(path)
	}
}

// RoundCount 返回指定会话已记录多少轮对话。
func (s *Store) RoundCount(sessionID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	conv, ok := s.sessions[sessionID]
	if !ok {
		return 0
	}
	return len(conv.Messages) / 2
}

func safeIDForLog(id string) string {
	if id == "" {
		return "<empty>"
	}
	if len(id) > 12 {
		return id[:12] + "..."
	}
	return id
}

func logToolCalls(prefix string, calls []schema.ToolCall) {
	parts := make([]string, 0, len(calls))
	for _, tc := range calls {
		parts = append(parts, "{id="+safeIDForLog(tc.ID)+",name="+tc.Function.Name+"}")
	}
	log.Printf("%s %s [%s]", storeLogPrefix, prefix, strings.Join(parts, ","))
}
