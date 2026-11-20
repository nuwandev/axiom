//go:build windows

package audit

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/eventlog"
)

type fakeEventLog struct {
	infos    map[uint32][]string
	warnings map[uint32][]string
	err      error
	closed   bool
}

func newFakeEventLog() *fakeEventLog {
	return &fakeEventLog{infos: map[uint32][]string{}, warnings: map[uint32][]string{}}
}

func (f *fakeEventLog) Info(eid uint32, msg string) error {
	f.infos[eid] = append(f.infos[eid], msg)
	return f.err
}

func (f *fakeEventLog) Warning(eid uint32, msg string) error {
	f.warnings[eid] = append(f.warnings[eid], msg)
	return f.err
}

func (f *fakeEventLog) Close() error { f.closed = true; return nil }

func TestEventLogSink_MapsEventsToIDsAndLevels(t *testing.T) {
	f := newFakeEventLog()
	s := &eventLogSink{w: f}
	for _, ev := range []string{EventAccepted, EventStarted, EventFinished, EventRejected} {
		if err := s.WriteRecord([]byte(`{"event":"` + ev + `","job_id":"J"}`)); err != nil {
			t.Fatalf("WriteRecord(%s): %v", ev, err)
		}
	}
	for ev, id := range map[string]uint32{EventAccepted: eventIDAccepted, EventStarted: eventIDStarted, EventFinished: eventIDFinished} {
		if len(f.infos[id]) != 1 || !strings.Contains(f.infos[id][0], `"event":"`+ev+`"`) {
			t.Errorf("%s: want one informational event with ID %d, got %v", ev, id, f.infos[id])
		}
	}
	if len(f.warnings[eventIDRejected]) != 1 {
		t.Errorf("rejected: want one warning with ID %d, got %v", eventIDRejected, f.warnings)
	}
}

func TestEventLogSink_DeliversTheRecordVerbatim(t *testing.T) {
	f := newFakeEventLog()
	line := `{"event":"accepted","identity":"ci","parameters":{"tag":"x"}}`
	if err := (&eventLogSink{w: f}).WriteRecord([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if got := f.infos[eventIDAccepted][0]; got != line {
		t.Errorf("event text = %q, want %q", got, line)
	}
}

func TestEventLogSink_PropagatesWriteFailure(t *testing.T) {
	f := newFakeEventLog()
	f.err = errors.New("event log unavailable")
	if err := (&eventLogSink{w: f}).WriteRecord([]byte(`{"event":"started"}`)); err == nil {
		t.Fatal("a failed event-log write must be returned so the caller fails closed")
	}
}

func TestEventLogSink_RejectsRecordOverTheEventLimit(t *testing.T) {
	f := newFakeEventLog()
	line := `{"event":"accepted","pad":"` + strings.Repeat("a", maxEventLogMessageChars) + `"}`
	if err := (&eventLogSink{w: f}).WriteRecord([]byte(line)); err == nil {
		t.Fatal("an oversized record must fail instead of being truncated")
	}
	if len(f.infos[eventIDAccepted]) != 0 {
		t.Error("nothing may be written for an oversized record")
	}
}

func TestEventLogSink_RejectsMalformedAndUnknownRecords(t *testing.T) {
	s := &eventLogSink{w: newFakeEventLog()}
	for _, line := range []string{`not json`, `{"event":"mystery"}`, `{}`} {
		if err := s.WriteRecord([]byte(line)); err == nil {
			t.Errorf("WriteRecord(%q) = nil, want an error", line)
		}
	}
}

func TestEventLogSink_CloseClosesTheWriter(t *testing.T) {
	f := newFakeEventLog()
	if err := (&eventLogSink{w: f}).Close(); err != nil || !f.closed {
		t.Fatalf("Close: err=%v closed=%v", err, f.closed)
	}
}

func TestOpenEventLogSink_RefusesAnUnregisteredSource(t *testing.T) {
	if _, err := openEventLogSink("axiom-audit-source-that-is-not-registered"); err == nil {
		t.Fatal("an unregistered source must be refused so the host does not start without the forwarded trail")
	}
}

// TestEventLogSink_RealEventLog writes through the real Windows event log and
// reads the entry back. It registers a throwaway source, which needs an
// elevated process, so it is skipped otherwise (the GitHub windows runner is
// elevated; a developer shell usually is not).
func TestEventLogSink_RealEventLog(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("registering an event source needs an elevated process")
	}
	const source = "axiom-audit-test"
	if err := eventlog.InstallAsEventCreate(source, eventlog.Info|eventlog.Warning|eventlog.Error); err != nil {
		t.Fatalf("registering test source: %v", err)
	}
	t.Cleanup(func() { _ = eventlog.Remove(source) })

	sink, err := openEventLogSink(source)
	if err != nil {
		t.Fatalf("openEventLogSink: %v", err)
	}
	l, err := Open(filepath.Join(t.TempDir(), "audit.log"), "test-agent", sink)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Write(Record{Event: EventRejected, Identity: "ci-test", Action: "x", Reason: "real event log check"}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	script := `Get-WinEvent -FilterHashtable @{LogName='Application';ProviderName='` + source + `'} -MaxEvents 5 | ForEach-Object { "$($_.Id)|$($_.LevelDisplayName)|$($_.Message)" }`
	var out []byte
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		out, _ = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Output()
		if strings.Contains(string(out), "real event log check") {
			break
		}
	}
	got := string(out)
	if !strings.Contains(got, "104|Warning|") || !strings.Contains(got, `"event":"rejected"`) {
		t.Fatalf("event not found in the Application log with ID 104 and level Warning; Get-WinEvent returned:\n%s", got)
	}
}
