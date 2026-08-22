// Package audit writes a synchronous, append-only audit trail of every
// action request the agent handles: accepted, started, finished (success,
// failure, timeout, or cancellation), and rejected (authentication,
// authorization, or validation failure).
//
// Every write is flushed to disk before returning so that a normal-path
// event cannot be silently lost to buffering. This package makes no claim
// about surviving a disk failure, an OS crash mid-write, or the process
// being SIGKILLed between the write and its fsync returning — those are
// infrastructure concerns outside what an application-level log can
// promise.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sync"
	"time"
)

// Event names recorded in the audit trail.
const (
	EventRejected = "rejected"
	EventAccepted = "accepted"
	EventStarted  = "started"
	EventFinished = "finished"
)

// Record is one audit trail entry.
type Record struct {
	Timestamp  time.Time         `json:"timestamp"`
	Agent      string            `json:"agent"`
	Event      string            `json:"event"`
	JobID      string            `json:"job_id,omitempty"`
	Identity   string            `json:"identity,omitempty"`
	Action     string            `json:"action,omitempty"`
	Parameters map[string]string `json:"parameters,omitempty"`
	Result     string            `json:"result,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	DurationMs int64             `json:"duration_ms,omitempty"`
}

// sensitiveKey matches parameter names whose values should never be written
// verbatim to the audit trail, even though the action config already limits
// what parameters exist at all.
var sensitiveKey = regexp.MustCompile(`(?i)(token|secret|password|passwd|key|credential)`)

// Logger appends Records to a single audit log file, one JSON object per
// line, serializing writes and fsyncing each one before returning.
type Logger struct {
	agent string
	mu    sync.Mutex
	file  *os.File
}

// Open opens (creating if necessary) the audit log at path for appending.
func Open(path, agent string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, fmt.Errorf("opening audit log %q: %w", path, err)
	}
	return &Logger{agent: agent, file: f}, nil
}

// Close closes the underlying file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

// Write redacts sensitive parameter values, appends rec as a single JSON
// line, and fsyncs before returning. The caller MUST treat a returned error
// as the write having failed to persist — see the package doc for what
// callers are expected to do about that on the accept vs. completion paths.
func (l *Logger) Write(rec Record) error {
	rec.Agent = l.agent
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now().UTC()
	}
	if rec.Parameters != nil {
		redacted := make(map[string]string, len(rec.Parameters))
		for k, v := range rec.Parameters {
			if sensitiveKey.MatchString(k) {
				redacted[k] = "***redacted***"
			} else {
				redacted[k] = v
			}
		}
		rec.Parameters = redacted
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshalling audit record: %w", err)
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, err := l.file.Write(line); err != nil {
		return fmt.Errorf("writing audit record: %w", err)
	}
	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("syncing audit record: %w", err)
	}
	return nil
}
