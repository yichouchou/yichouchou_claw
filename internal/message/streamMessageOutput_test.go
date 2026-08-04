package message

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSSEEvent_AttachmentOmitempty 验证 SSEEvent.Attachment 用指针类型后,
// nil 时不序列化 attachment 字段(omitempty 生效), 避免污染 stream_chunk/end 等无关 event。
//
// 2026-08-04 复盘测试: 之前 Attachment 是值类型, 即便全零也会序列化成
// `"attachment":{"type":"","url":"","mime_type":"","size":0,"name":""}`,
// 让前端在 stream_chunk event 上也看到 data.attachment 存在, 干扰 routing。
func TestSSEEvent_AttachmentOmitempty(t *testing.T) {
	// 1. stream_chunk 事件: Attachment 必须不出现
	ev1 := SSEEvent{
		Type:      "stream_chunk",
		AgentName: "LocalCommandAgent",
		Content:   "hello world",
	}
	data1, err := json.Marshal(ev1)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if strings.Contains(string(data1), `"attachment"`) {
		t.Errorf("stream_chunk should NOT contain 'attachment' field, got: %s", data1)
	}
	t.Logf("stream_chunk OK: %s", data1)

	// 2. attachment 事件: 真实附件, Attachment 必须出现
	ev2 := SSEEvent{
		Type: "attachment",
		Attachment: &AttachmentEvent{
			Type:     "image",
			URL:      "/api/attachment/abc/star.png",
			MIMEType: "image/png",
			Size:     131499,
			Name:     "star.png",
		},
	}
	data2, err := json.Marshal(ev2)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(data2), `"attachment":{"type":"image"`) {
		t.Errorf("attachment event should have 'attachment' field, got: %s", data2)
	}
	t.Logf("attachment OK: %s", data2)

	// 3. end 事件: 不带 attachment
	ev3 := SSEEvent{Type: "end"}
	data3, _ := json.Marshal(ev3)
	if strings.Contains(string(data3), `"attachment"`) {
		t.Errorf("end should NOT contain 'attachment' field, got: %s", data3)
	}
	t.Logf("end OK: %s", data3)
}
