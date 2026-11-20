//go:build windows

package audit

import (
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/eventlog"
)

// eventLogSource is the Application event log source that
// `axiom.exe -install-service` registers (cmd/axiom/service_windows.go). The
// service's own start-up errors use the same source.
const eventLogSource = "axiom"

// Event IDs for the audit stream. The registered message file (EventCreate)
// accepts IDs 1-1000; 1 is used for service errors, so audit records use a
// separate, stable range an event-log collector can filter on.
const (
	eventIDAccepted uint32 = 101
	eventIDStarted  uint32 = 102
	eventIDFinished uint32 = 103
	eventIDRejected uint32 = 104
)

// maxEventLogMessageChars is the Windows limit for one event string. A record
// above it cannot be delivered, so the write fails (and the action is refused
// on the accept and start paths) instead of being truncated.
const maxEventLogMessageChars = 31839

// eventLogWriter is the subset of *eventlog.Log the sink uses.
type eventLogWriter interface {
	Info(eid uint32, msg string) error
	Warning(eid uint32, msg string) error
	Close() error
}

type eventLogSink struct {
	w eventLogWriter
}

// NewSystemLogSink returns a Sink that writes every audit record as one
// Application event-log entry from the "axiom" source: the JSON record is the
// event text, `rejected` records are warnings and the others informational.
// It fails if the source is not registered (the installer registers it), so a
// host that cannot deliver the forwarded trail refuses to start.
func NewSystemLogSink() (Sink, error) {
	return openEventLogSink(eventLogSource)
}

func openEventLogSink(source string) (Sink, error) {
	if err := requireEventSource(source); err != nil {
		return nil, err
	}
	l, err := eventlog.Open(source)
	if err != nil {
		return nil, fmt.Errorf("opening event log source %q: %w", source, err)
	}
	return &eventLogSink{w: l}, nil
}

// requireEventSource refuses an unregistered source: opening one succeeds but
// files events under a generic source, which would silently break collector
// filters.
func requireEventSource(source string) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Services\EventLog\Application\`+source, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("event log source %q is not registered; the service installer registers it, so reinstall the service (Uninstall-Axiom.ps1 then Install-Axiom.ps1)", source)
		}
		return fmt.Errorf("checking event log source %q: %w", source, err)
	}
	return k.Close()
}

func (s *eventLogSink) WriteRecord(line []byte) error {
	if len(line) > maxEventLogMessageChars {
		return fmt.Errorf("audit record is %d bytes, over the %d-character event log limit", len(line), maxEventLogMessageChars)
	}
	var head struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return fmt.Errorf("reading audit record event type: %w", err)
	}
	msg := string(line)
	switch head.Event {
	case EventAccepted:
		return s.w.Info(eventIDAccepted, msg)
	case EventStarted:
		return s.w.Info(eventIDStarted, msg)
	case EventFinished:
		return s.w.Info(eventIDFinished, msg)
	case EventRejected:
		return s.w.Warning(eventIDRejected, msg)
	default:
		return fmt.Errorf("unknown audit event %q", head.Event)
	}
}

func (s *eventLogSink) Close() error { return s.w.Close() }
