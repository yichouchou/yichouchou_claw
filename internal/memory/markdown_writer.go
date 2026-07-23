package memory

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// markdownLogPrefix is the log tag used by the MarkdownRecorder.
const markdownLogPrefix = "[memory.md]"

// MarkdownRecorder persists trace data to local Markdown files under
// <workdir>/memory/. Layout:
//
//	<workdir>/memory/
//	├── sessions/2026-07-23/09h.md   <- merged human-readable review log
//	├── inputs/2026-07-23/09h.md     <- raw LLM input payloads
//	├── outputs/2026-07-23/09h.md    <- raw LLM output payloads
//	└── errors/2026-07-23/09h.md     <- tool / agent / stream errors
//
// All four sibling directories are created on construction. Each entry is
// prepended with a YAML front-matter block delimited by "---" so scripts
// can grep / parse metadata without regex-ing the body.
//
// Concurrency: writes are serialized via mu. The file is opened, appended,
// and closed on every call so the kernel's fs flushes happen at close time;
// no buffering across calls. The cost (one syscall per record) is acceptable
// given memory writes are infrequent.
type MarkdownRecorder struct {
	mu       sync.Mutex
	workdir  string // <workdir>/memory
	disabled bool   // true → all Record* calls are silent no-ops
}

// NewMarkdownRecorder constructs a recorder rooted at <workdir>/memory.
// The four subdirectories (sessions / inputs / outputs / errors) are
// created eagerly so the first write never has to handle ENOENT.
//
// Two ways the recorder is silenced:
//
//  1. Env var YICHOUCHOU_MEMORY is one of "off", "false", "0" (case-insensitive).
//  2. workdir is empty after trimming whitespace.
//
// In both cases the returned recorder is a no-op (disabled=true) and
// Record* methods succeed with a nil error.
func NewMarkdownRecorder(workdir string) (*MarkdownRecorder, error) {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("YICHOUCHOU_MEMORY"))); v != "" {
		switch v {
		case "off", "false", "0", "no", "disable", "disabled":
			log.Printf("%s disabled via YICHOUCHOU_MEMORY=%q", markdownLogPrefix, v)
			return &MarkdownRecorder{disabled: true}, nil
		}
	}
	if strings.TrimSpace(workdir) == "" {
		log.Printf("%s workdir is empty, recorder disabled", markdownLogPrefix)
		return &MarkdownRecorder{disabled: true}, nil
	}
	memDir := filepath.Join(workdir, "memory")
	for _, sub := range []string{"sessions", "inputs", "outputs", "errors"} {
		if err := os.MkdirAll(filepath.Join(memDir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}
	return &MarkdownRecorder{workdir: memDir}, nil
}

// Root returns the absolute (or relative, depending on workdir) path of
// the recorder's root directory. Useful for diagnostics.
func (r *MarkdownRecorder) Root() string {
	if r == nil || r.disabled {
		return ""
	}
	return r.workdir
}

// writeAppend opens (or creates) the hour-bucket file under subdir and
// appends body. All errors are wrapped with the full path so the warning
// log points to a single line of evidence.
func (r *MarkdownRecorder) writeAppend(subdir string, bucket TimeBucket, body string) error {
	if r == nil || r.disabled {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	dir := filepath.Join(r.workdir, subdir, bucket.DateDir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := bucket.BucketPath(filepath.Join(r.workdir, subdir))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

// RecordSessionStart writes a session-start header into sessions/.
// Errors are swallowed on purpose: this is a fire-and-forget marker.
func (r *MarkdownRecorder) RecordSessionStart(sessionID, agentName string) {
	if r == nil || r.disabled {
		return
	}
	bucket := BucketFromNow()
	body := renderSessionEvent("session_start", sessionID, agentName, time.Now(), "")
	if err := r.writeAppend("sessions", bucket, body); err != nil {
		log.Printf("%s RecordSessionStart err=%v", markdownLogPrefix, err)
	}
}

// RecordInput dumps the full LLM input payload into inputs/ and a concise
// summary into sessions/. The full payload is wrapped in a fenced text
// block to keep long lines from being re-flowed by markdown viewers.
func (r *MarkdownRecorder) RecordInput(sessionID, agentName string, content []byte) error {
	if r == nil || r.disabled {
		return nil
	}
	if len(content) == 0 {
		return nil
	}
	bucket := BucketFromNow()

	full := renderFrontMatter(sessionID, agentName, "input", time.Now()) +
		"\n```text\n" + string(content) + "\n```\n"
	if err := r.writeAppend("inputs", bucket, full); err != nil {
		return err
	}

	preview := string(content)
	if len(preview) > 2000 {
		preview = preview[:2000] + "\n... (truncated; see inputs/ for full payload)\n"
	}
	summary := renderSessionEvent("input", sessionID, agentName, time.Now(),
		fmt.Sprintf("**size**: %d bytes\n\n**preview**:\n\n```text\n%s\n```\n", len(content), preview))
	return r.writeAppend("sessions", bucket, summary)
}

// RecordOutput dumps the full LLM output payload into outputs/ and a
// concise summary into sessions/.
func (r *MarkdownRecorder) RecordOutput(sessionID, agentName string, content []byte) error {
	if r == nil || r.disabled {
		return nil
	}
	if len(content) == 0 {
		return nil
	}
	bucket := BucketFromNow()

	full := renderFrontMatter(sessionID, agentName, "output", time.Now()) +
		"\n```text\n" + string(content) + "\n```\n"
	if err := r.writeAppend("outputs", bucket, full); err != nil {
		return err
	}

	preview := string(content)
	if len(preview) > 2000 {
		preview = preview[:2000] + "\n... (truncated; see outputs/ for full payload)\n"
	}
	summary := renderSessionEvent("output", sessionID, agentName, time.Now(),
		fmt.Sprintf("**size**: %d bytes\n\n**preview**:\n\n```text\n%s\n```\n", len(content), preview))
	return r.writeAppend("sessions", bucket, summary)
}

// RecordError writes a structured error record into errors/ and a one-line
// summary into sessions/. nil errors are ignored.
func (r *MarkdownRecorder) RecordError(sessionID, agentName string, err error, ctx string) error {
	if r == nil || r.disabled {
		return nil
	}
	if err == nil {
		return nil
	}
	bucket := BucketFromNow()

	body := renderFrontMatter(sessionID, agentName, "error", time.Now()) +
		fmt.Sprintf("\n**context**: %s\n\n**error**: `%s`\n",
			escape(ctx), strings.TrimSpace(err.Error()))
	if err := r.writeAppend("errors", bucket, body); err != nil {
		return err
	}

	summary := renderSessionEvent("error", sessionID, agentName, time.Now(),
		fmt.Sprintf("**ctx**: %s\n\n**err**: `%s`\n", escape(ctx), strings.TrimSpace(err.Error())))
	return r.writeAppend("sessions", bucket, summary)
}

// RecordSessionEnd writes a session-end marker into sessions/.
func (r *MarkdownRecorder) RecordSessionEnd(sessionID, agentName string) error {
	if r == nil || r.disabled {
		return nil
	}
	bucket := BucketFromNow()
	body := renderSessionEvent("session_end", sessionID, agentName, time.Now(), "")
	return r.writeAppend("sessions", bucket, body)
}

// Flush is a no-op (every write closes its file immediately).
func (r *MarkdownRecorder) Flush() error { return nil }

// renderFrontMatter emits a YAML front-matter block delimited by "---".
// Downstream parsers can split on the first "---" / last "---" pair to
// extract session_id, agent, kind, and time without regex.
func renderFrontMatter(sessionID, agentName, kind string, t time.Time) string {
	return fmt.Sprintf("---\nsession_id: %q\nagent: %q\nkind: %q\ntime: %s\n---\n",
		sessionID, agentName, kind, t.Format(time.RFC3339))
}

// renderSessionEvent returns a Markdown heading + bullet list for
// session-level events (start / input / output / error / end). The body
// is optional; pass "" to skip it.
func renderSessionEvent(event, sessionID, agentName string, t time.Time, body string) string {
	hdr := fmt.Sprintf("\n## %s @ %s\n\n- session_id: `%s`\n- agent: `%s`\n",
		event, t.Format(time.RFC3339), sessionID, agentName)
	if body != "" {
		hdr += "\n" + body + "\n"
	}
	return hdr
}

// escape trims newlines so a context string can sit inline in a Markdown
// list. Long strings are truncated.
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