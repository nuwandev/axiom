// Package audit writes a synchronous audit trail of every action request the
// agent handles: accepted, started, finished (success, failure, timeout, or
// cancellation), and rejected (authentication, authorization, or validation
// failure).
//
// Two destinations exist, with different standing:
//
//   - The local file is an operational copy only. It is opened O_APPEND by the
//     Go writer, but the account that runs action scripts owns it and its
//     directory, so that account can alter or delete it.
//   - A Sink (journald/syslog) hands records to a collector outside the
//     service account's control. The operator's central collector is the
//     authoritative, tamper-resistant audit record.
//
// A record contains only the fields of Record. It never carries the process
// environment, action stdout/stderr, keys or certificates; parameter values
// whose names look secret are redacted and free-text fields are length-capped.
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
	"unicode/utf8"
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
var sensitiveKey = regexp.MustCompile(`(?i)(token|secret|pass|pwd|key|credential|auth|bearer|cookie|session|private|cert|signature|jwt)`)

// maxFieldBytes caps the free-text fields of a record. Some of them carry
// text an unauthenticated peer controls (the requested path) or an error
// string from the OS, and a journald/syslog datagram has a size limit that
// must not be reachable by a request.
const maxFieldBytes = 512

// truncate shortens s to at most maxFieldBytes without splitting a UTF-8
// sequence, marking the cut.
func truncate(s string) string {
	if len(s) <= maxFieldBytes {
		return s
	}
	cut := maxFieldBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "...[truncated]"
}

// Sink is an additional destination for audit records, such as the system
// journal. Each record is delivered as one JSON object without a trailing
// newline. A Sink must return an error if it could not hand the record off;
// Logger.Write propagates that error so callers fail closed.
type Sink interface {
	WriteRecord(line []byte) error
	Close() error
}

// Logger appends Records to a local audit log file, one JSON object per
// line, serializing writes and fsyncing each one before returning, and
// forwards every record to any configured Sinks.
//
// The local file is owned by the same service account that runs action
// scripts, so it is application-level evidence only: that account can alter
// or remove it. A Sink that forwards to a collector the service account
// cannot modify is what provides tamper resistance.
type Logger struct {
	agent string
	mu    sync.Mutex
	file  *os.File
	sinks []Sink
}

// Open opens (creating if necessary) the audit log at path for appending.
// Records are also delivered to each sink; Close closes them.
func Open(path, agent string, sinks ...Sink) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, fmt.Errorf("opening audit log %q: %w", path, err)
	}
	return &Logger{agent: agent, file: f, sinks: sinks}, nil
}

// Close closes the local file and every sink, returning the first error.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	firstErr := l.file.Close()
	for _, s := range l.sinks {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Write redacts sensitive parameter values, appends rec as a single JSON
// line, fsyncs, and delivers the record to every sink before returning. An
// error from the file or from any sink is returned: the caller MUST treat it
// as the record not having been durably delivered everywhere — see the
// package doc for what callers are expected to do about that on the accept
// vs. completion paths.
func (l *Logger) Write(rec Record) error {
	rec.Agent = l.agent
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now().UTC()
	}
	rec.Identity = truncate(rec.Identity)
	rec.Action = truncate(rec.Action)
	rec.Reason = truncate(rec.Reason)
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

	l.mu.Lock()
	defer l.mu.Unlock()

	// The cap-limited slice forces append to copy, so line stays free of the
	// newline that only the file format needs.
	if _, err := l.file.Write(append(line[:len(line):len(line)], '\n')); err != nil {
		return fmt.Errorf("writing audit record: %w", err)
	}
	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("syncing audit record: %w", err)
	}
	for _, s := range l.sinks {
		if err := s.WriteRecord(line); err != nil {
			return fmt.Errorf("forwarding audit record: %w", err)
		}
	}
	return nil
}
