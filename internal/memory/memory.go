// Package memory persists per-agent trace data (LLM inputs / outputs,
// tool calls, errors) to local Markdown files under <workdir>/memory/.
//
// Files are bucketed by day + hour so a single run rarely crosses multiple
// files; each bucket contains YAML front-matter at the top of every entry so
// downstream scripts (or humans) can parse metadata without regex.
//
// The package exposes:
//
//   - A small Recorder interface that the rest of the codebase talks to.
//   - A global recorder (set once at startup) so middlewares can record
//     without holding a pointer.
//   - A MarkdownRecorder implementation that satisfies the interface and
//     fans out to four sibling directories: sessions/, inputs/, outputs/,
//     errors/.
//
// Concurrency: writes are serialized by a sync.Mutex inside MarkdownRecorder
// so the file is opened, appended, and closed atomically. Failure is logged
// at warning level and never bubbles back to the agent run.
package memory

import (
	"log"
	"sync"
)

const memoryLogPrefix = "[memory]"

// Recorder is the contract every memory backend must satisfy.
//
// All methods MUST be safe to call from concurrent goroutines and MUST NOT
// panic on empty arguments. Returning a non-nil error signals that the
// recording failed; callers (typically middlewares) are expected to log
// the error but otherwise proceed with the agent run.
type Recorder interface {
	// RecordSessionStart records the beginning of an agent run. Some
	// implementations may treat this as a no-op (e.g. session_id empty).
	RecordSessionStart(sessionID, agentName string)

	// RecordInput persists the LLM-bound payload (system prompt + history +
	// user message + tool calls) before a model call.
	RecordInput(sessionID, agentName string, content []byte) error

	// RecordOutput persists the model response (assistant text and / or
	// tool_call requests) right after a model call returns.
	RecordOutput(sessionID, agentName string, content []byte) error

	// RecordError captures a tool error, a model stream error, or any
	// other agent-level error. ctx is a short free-form label describing
	// where the error originated (e.g. "tool=local_command args=...").
	RecordError(sessionID, agentName string, err error, ctx string) error

	// RecordSessionEnd marks a successful terminal state.
	RecordSessionEnd(sessionID, agentName string) error

	// Flush is a no-op for backends that fsync on every write, but the
	// hook stays in the interface so future async writers (batching,
	// buffered flush) can implement it.
	Flush() error
}

// global recorder singleton. Nil means "no memory configured".
var (
	globalMu sync.RWMutex
	globalR  Recorder
)

// SetRecorder installs the package-wide recorder. Passing nil disables
// recording (middlewares will short-circuit).
func SetRecorder(r Recorder) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalR = r
}

// GetRecorder returns the active recorder (or nil if none is set).
func GetRecorder() Recorder {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalR
}

// Reset removes the active recorder. Intended for tests.
func Reset() {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalR = nil
}

// safeInput wraps RecordInput so the caller doesn't need to nil-check
// the recorder; errors are logged but never propagated. Used by hooks
// that prefer fire-and-forget semantics.
func safeInput(sessionID, agentName string, content []byte) {
	r := GetRecorder()
	if r == nil {
		return
	}
	if err := r.RecordInput(sessionID, agentName, content); err != nil {
		log.Printf("%s RecordInput session=%s agent=%s err=%v",
			memoryLogPrefix, sessionID, agentName, err)
	}
}

// safeOutput mirrors safeInput for output payloads.
func safeOutput(sessionID, agentName string, content []byte) {
	r := GetRecorder()
	if r == nil {
		return
	}
	if err := r.RecordOutput(sessionID, agentName, content); err != nil {
		log.Printf("%s RecordOutput session=%s agent=%s err=%v",
			memoryLogPrefix, sessionID, agentName, err)
	}
}

// safeError mirrors safeInput for tool / agent errors.
func safeError(sessionID, agentName string, err error, ctx string) {
	r := GetRecorder()
	if r == nil || err == nil {
		return
	}
	if recErr := r.RecordError(sessionID, agentName, err, ctx); recErr != nil {
		log.Printf("%s RecordError session=%s agent=%s err=%v",
			memoryLogPrefix, sessionID, agentName, recErr)
	}
}