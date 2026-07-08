package session

import (
	"log"
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"
)

const storeLogPrefix = "[session]"

// KeySessionID 是 SessionValues 中用来传递会话 id 的 key。
// 通过 adk.WithSessionValues 注入到 ctx，middleware 可从 ctx 中读取。
const KeySessionID = "session_id"

// Store 是一个简单的内存版多轮对话会话存储。
// 同一 sessionID 共享一段对话历史，最多保留最近 MaxRounds 轮上下文。
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*Conversation
	// MaxRounds 限制保存的最大轮数（一问一答算 1 轮）。
	MaxRounds int
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
	log.Printf("%s Replace session=%s new_total_msgs=%d rounds=%d",
		storeLogPrefix, sessionID, len(conv.Messages), len(conv.Messages)/2)
	for i, m := range conv.Messages {
		log.Printf("%s   [%02d] role=%s tool_call_id=%s tool_name=%s tool_calls=%d content_len=%d",
			storeLogPrefix, i, m.Role, safeIDForLog(m.ToolCallID), m.ToolName, len(m.ToolCalls), len(m.Content))
	}
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
