package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// TestTimeBucket verifies the bucket formatting rules: zero-padded hour,
// ISO-style date, and the bucket path joining into <base>/YYYY-MM-DD/HHh.md.
func TestTimeBucket(t *testing.T) {
	// 2026-07-23 09:00 in UTC+8.
	bucket := BucketFromTime(time.Date(2026, 7, 23, 9, 0, 0, 0, localTimeZone))
	if bucket.DateDir() != "2026-07-23" {
		t.Errorf("DateDir = %q, want 2026-07-23", bucket.DateDir())
	}
	if bucket.HourFile() != "09h.md" {
		t.Errorf("HourFile = %q, want 09h.md", bucket.HourFile())
	}
	got := bucket.BucketPath("/tmp/foo")
	want := filepath.Join("/tmp/foo", "2026-07-23", "09h.md")
	if got != want {
		t.Errorf("BucketPath = %q, want %q", got, want)
	}

	// Single-digit hour should still be zero-padded.
	bucket2 := TimeBucket{Year: 2026, Month: 1, Day: 5, Hour: 4}
	if bucket2.HourFile() != "04h.md" {
		t.Errorf("zero-padded hour: got %q, want 04h.md", bucket2.HourFile())
	}
}

// TestMarkdownRecorder verifies that the four directories are created
// up front and that Record* methods write to the correct sibling dir.
func TestMarkdownRecorder(t *testing.T) {
	root := t.TempDir()

	r, err := NewMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewMarkdownRecorder: %v", err)
	}
	if r.Root() == "" {
		t.Fatalf("Root() should return non-empty when not disabled")
	}

	sid := "sess-001"
	agent := "ChatAgent"

	// session start — must land in sessions/<date>/<hour>.md
	r.RecordSessionStart(sid, agent)

	if err := r.RecordInput(sid, agent, []byte("system: hi\nuser: hello")); err != nil {
		t.Fatalf("RecordInput: %v", err)
	}
	if err := r.RecordOutput(sid, agent, []byte("assistant: hi back")); err != nil {
		t.Fatalf("RecordOutput: %v", err)
	}
	if err := r.RecordError(sid, agent, errors.New("boom"), "tool=local_command call_id=c1"); err != nil {
		t.Fatalf("RecordError: %v", err)
	}
	if err := r.RecordSessionEnd(sid, agent); err != nil {
		t.Fatalf("RecordSessionEnd: %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	bucket := BucketFromNow()
	dateDir := bucket.DateDir()
	hourFile := bucket.HourFile()

	// Each of the four sibling dirs must contain exactly one file with
	// the expected hour bucket.
	for _, sub := range []string{"sessions", "inputs", "outputs", "errors"} {
		path := filepath.Join(root, "memory", sub, dateDir, hourFile)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(data) == 0 {
			t.Errorf("%s is empty", path)
		}
		if !strings.Contains(string(data), "session_id:") {
			t.Errorf("%s missing front-matter session_id", path)
		}
	}

	// sessions/ should have all five markers; inputs/ only the input
	// payload; outputs/ only the output payload; errors/ only the error.
	sessBytes, _ := os.ReadFile(filepath.Join(root, "memory", "sessions", dateDir, hourFile))
	sess := string(sessBytes)
	for _, marker := range []string{"session_start", "input", "output", "error", "session_end"} {
		if !strings.Contains(sess, marker) {
			t.Errorf("sessions/%s missing %q marker", hourFile, marker)
		}
	}
	inputBytes, _ := os.ReadFile(filepath.Join(root, "memory", "inputs", dateDir, hourFile))
	if !strings.Contains(string(inputBytes), "system: hi") {
		t.Errorf("inputs/ missing payload body")
	}
	if strings.Contains(string(inputBytes), "session_start") {
		t.Errorf("inputs/ should not contain session_start")
	}

	outputBytes, _ := os.ReadFile(filepath.Join(root, "memory", "outputs", dateDir, hourFile))
	if !strings.Contains(string(outputBytes), "assistant: hi back") {
		t.Errorf("outputs/ missing payload body")
	}

	errorBytes, _ := os.ReadFile(filepath.Join(root, "memory", "errors", dateDir, hourFile))
	if !strings.Contains(string(errorBytes), "boom") {
		t.Errorf("errors/ missing error text")
	}
	if !strings.Contains(string(errorBytes), "local_command") {
		t.Errorf("errors/ missing ctx label")
	}
}

// TestDisabled verifies that YICHOUCHOU_MEMORY=off silently no-ops.
func TestDisabled(t *testing.T) {
	t.Setenv("YICHOUCHOU_MEMORY", "off")

	root := t.TempDir()
	r, err := NewMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewMarkdownRecorder: %v", err)
	}
	if !r.disabled {
		t.Fatalf("recorder should be disabled")
	}
	if r.Root() != "" {
		t.Errorf("Root() should be empty when disabled, got %q", r.Root())
	}

	// All calls must succeed (no error) and create no files.
	if err := r.RecordInput("s", "a", []byte("x")); err != nil {
		t.Errorf("RecordInput: %v", err)
	}
	if err := r.RecordOutput("s", "a", []byte("x")); err != nil {
		t.Errorf("RecordOutput: %v", err)
	}
	if err := r.RecordError("s", "a", errors.New("x"), "x"); err != nil {
		t.Errorf("RecordError: %v", err)
	}
	if err := r.RecordSessionEnd("s", "a"); err != nil {
		t.Errorf("RecordSessionEnd: %v", err)
	}
	r.RecordSessionStart("s", "a")

	if _, err := os.Stat(filepath.Join(root, "memory")); !os.IsNotExist(err) {
		t.Errorf("memory/ should not exist when disabled, stat err=%v", err)
	}
}

// TestEmptyWorkdir verifies the second disable path: empty workdir.
func TestEmptyWorkdir(t *testing.T) {
	t.Setenv("YICHOUCHOU_MEMORY", "")
	r, err := NewMarkdownRecorder("   ")
	if err != nil {
		t.Fatalf("NewMarkdownRecorder: %v", err)
	}
	if !r.disabled {
		t.Fatalf("recorder should be disabled on empty workdir")
	}
}

// TestNilSafety ensures none of the Record* methods panic on empty args.
func TestNilSafety(t *testing.T) {
	root := t.TempDir()
	r, err := NewMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewMarkdownRecorder: %v", err)
	}
	// Empty content is a no-op rather than an error.
	if err := r.RecordInput("s", "a", nil); err != nil {
		t.Errorf("RecordInput(nil): %v", err)
	}
	if err := r.RecordInput("s", "a", []byte{}); err != nil {
		t.Errorf("RecordInput(empty): %v", err)
	}
	if err := r.RecordError("s", "a", nil, "ctx"); err != nil {
		t.Errorf("RecordError(nil err): %v", err)
	}
}

// TestConcurrentWrites verifies that concurrent Record* calls don't
// interleave or corrupt the output file. We hammer the recorder from
// N goroutines and assert that every line we expect appears at least once.
func TestConcurrentWrites(t *testing.T) {
	root := t.TempDir()
	r, err := NewMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewMarkdownRecorder: %v", err)
	}

	const goroutines = 8
	const perGoroutine = 25
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				sid := fmt.Sprintf("sess-%d-%d", id, i)
				r.RecordSessionStart(sid, "AgentX")
				if err := r.RecordInput(sid, "AgentX", []byte("hello")); err != nil {
					t.Errorf("RecordInput: %v", err)
					return
				}
				if err := r.RecordOutput(sid, "AgentX", []byte("world")); err != nil {
					t.Errorf("RecordOutput: %v", err)
					return
				}
				if err := r.RecordError(sid, "AgentX", errors.New("e"), "ctx"); err != nil {
					t.Errorf("RecordError: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	// sessions/ file should contain every (id, i) we wrote — at least one
	// occurrence each. Concurrency-safe append must not drop bytes.
	bucket := BucketFromNow()
	path := filepath.Join(root, "memory", "sessions", bucket.DateDir(), bucket.HourFile())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sessions file: %v", err)
	}
	for g := 0; g < goroutines; g++ {
		for i := 0; i < perGoroutine; i++ {
			marker := fmt.Sprintf("sess-%d-%d", g, i)
			if !strings.Contains(string(data), marker) {
				t.Errorf("sessions file missing marker %q (concurrent write lost?)", marker)
				return
			}
		}
	}
}

// TestFormatMessages verifies the human-readable dumper for both
// multi-message input and single-message output.
func TestFormatMessages(t *testing.T) {
	msgs := []*schema.Message{
		schema.SystemMessage("you are a router"),
		schema.UserMessage("hi"),
		schema.AssistantMessage("hello back", nil),
	}
	got := formatMessagesAsInput(msgs)
	for _, want := range []string{"role=system", "role=user", "role=assistant", "content_len="} {
		if !strings.Contains(got, want) {
			t.Errorf("formatMessagesAsInput missing %q\n---\n%s", want, got)
		}
	}

	out := formatMessageAsOutput(schema.AssistantMessage("hi", []schema.ToolCall{
		{ID: "tc1", Function: schema.FunctionCall{Name: "local_command", Arguments: `{"cmd":"ls"}`}},
	}))
	for _, want := range []string{"role=assistant", "tool_call id=tc1", "name=local_command"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatMessageAsOutput missing %q\n---\n%s", want, out)
		}
	}
}

// TestMiddleware_NoRecorder confirms the middleware is a no-op when
// SetRecorder has never been called.
func TestMiddleware_NoRecorder(t *testing.T) {
	Reset()
	mw := NewMemoryMiddleware("TestAgent")

	ctx := context.Background()
	runCtx := &adk.ChatModelAgentContext{}
	if _, _, err := mw.BeforeAgent(ctx, runCtx); err != nil {
		t.Fatalf("BeforeAgent: %v", err)
	}
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("hi")}}
	if _, _, err := mw.BeforeModelRewriteState(ctx, state, nil); err != nil {
		t.Fatalf("BeforeModelRewriteState: %v", err)
	}
	if _, _, err := mw.AfterModelRewriteState(ctx, state, nil); err != nil {
		t.Fatalf("AfterModelRewriteState: %v", err)
	}
	if _, err := mw.AfterAgent(ctx, state); err != nil {
		t.Fatalf("AfterAgent: %v", err)
	}
}

// TestMiddleware_WrapInvokableToolCall verifies that a non-nil tool
// error is forwarded to the recorder, and a nil error is silently
// ignored.
func TestMiddleware_WrapInvokableToolCall(t *testing.T) {
	Reset()
	root := t.TempDir()
	r, err := NewMarkdownRecorder(root)
	if err != nil {
		t.Fatalf("NewMarkdownRecorder: %v", err)
	}
	SetRecorder(r)
	t.Cleanup(Reset)

	mw := NewMemoryMiddleware("LocalCommandAgent")

	calls := 0
	wrapped, err := mw.WrapInvokableToolCall(context.Background(),
		func(_ context.Context, args string, _ ...tool.Option) (string, error) {
			calls++
			if args == "ok" {
				return "stdout", nil
			}
			return "", errors.New("permission denied")
		},
		&adk.ToolContext{Name: "local_command", CallID: "call-1"},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall: %v", err)
	}

	if _, err := wrapped(context.Background(), "ok"); err != nil {
		t.Errorf("success path should not error: %v", err)
	}
	if _, err := wrapped(context.Background(), "forbidden"); err == nil {
		t.Errorf("error path should propagate")
	}
	if calls != 2 {
		t.Errorf("endpoint invocations = %d, want 2", calls)
	}

	bucket := BucketFromNow()
	path := filepath.Join(root, "memory", "errors", bucket.DateDir(), bucket.HourFile())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read errors file: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "permission denied") {
		t.Errorf("errors file missing tool error text\n---\n%s", body)
	}
	if !strings.Contains(body, "tool=local_command") {
		t.Errorf("errors file missing ctx label\n---\n%s", body)
	}
}

// TestGlobalRecorder_GetSetReset covers the singleton lifecycle.
func TestGlobalRecorder_GetSetReset(t *testing.T) {
	Reset()
	if GetRecorder() != nil {
		t.Fatalf("GetRecorder should be nil after Reset")
	}
	r, err := NewMarkdownRecorder(t.TempDir())
	if err != nil {
		t.Fatalf("NewMarkdownRecorder: %v", err)
	}
	SetRecorder(r)
	if GetRecorder() == nil {
		t.Fatalf("GetRecorder should not be nil after SetRecorder")
	}
	Reset()
	if GetRecorder() != nil {
		t.Fatalf("GetRecorder should be nil after Reset")
	}
}