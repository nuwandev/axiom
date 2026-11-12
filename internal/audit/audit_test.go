package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLogger_WriteAndRedact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := Open(path, "test-agent")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	err = l.Write(Record{
		Event:  EventFinished,
		JobID:  "01J000000000000000000000",
		Action: "sample.action",
		Parameters: map[string]string{
			"image_tag": "uat-123",
			"api_token": "super-secret-value",
		},
		Result: "succeeded",
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening audit log: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatalf("expected at least one line in audit log")
	}
	var rec Record
	if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
		t.Fatalf("unmarshalling record: %v", err)
	}
	if rec.Agent != "test-agent" {
		t.Errorf("Agent = %q", rec.Agent)
	}
	if rec.Parameters["image_tag"] != "uat-123" {
		t.Errorf("image_tag = %q, want unredacted", rec.Parameters["image_tag"])
	}
	if rec.Parameters["api_token"] != "***redacted***" {
		t.Errorf("api_token = %q, want redacted", rec.Parameters["api_token"])
	}
}

func TestLogger_AppendOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := Open(path, "test-agent")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	for i := 0; i < 3; i++ {
		if err := l.Write(Record{Event: EventAccepted, JobID: "job"}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading audit log: %v", err)
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 3 {
		t.Errorf("got %d lines, want 3", lines)
	}
}

type recordingSink struct {
	lines  [][]byte
	closed bool
}

func (s *recordingSink) WriteRecord(line []byte) error {
	s.lines = append(s.lines, append([]byte(nil), line...))
	return nil
}

func (s *recordingSink) Close() error { s.closed = true; return nil }

type failingSink struct{}

func (failingSink) WriteRecord([]byte) error { return errors.New("collector unreachable") }
func (failingSink) Close() error             { return nil }

func TestLogger_SinkReceivesRedactedJSONWithoutNewline(t *testing.T) {
	sink := &recordingSink{}
	l, err := Open(filepath.Join(t.TempDir(), "audit.log"), "test-agent", sink)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	err = l.Write(Record{
		Event:      EventAccepted,
		Action:     "sample.action",
		Parameters: map[string]string{"image_tag": "uat-1", "api_token": "super-secret-value"},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(sink.lines) != 1 {
		t.Fatalf("sink got %d records, want 1", len(sink.lines))
	}
	line := sink.lines[0]
	if bytes.HasSuffix(line, []byte("\n")) {
		t.Errorf("sink record must not carry a trailing newline: %q", line)
	}
	if bytes.Contains(line, []byte("super-secret-value")) {
		t.Errorf("sink record leaked a sensitive parameter value: %s", line)
	}
	var rec Record
	if err := json.Unmarshal(line, &rec); err != nil {
		t.Fatalf("sink record is not valid JSON: %v", err)
	}
	if rec.Parameters["image_tag"] != "uat-1" || rec.Agent != "test-agent" {
		t.Errorf("unexpected record: %+v", rec)
	}

	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !sink.closed {
		t.Errorf("Close did not close the sink")
	}
}

func TestLogger_SinkFailureFailsTheWrite(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "audit.log"), "test-agent", failingSink{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	if err := l.Write(Record{Event: EventAccepted}); err == nil {
		t.Fatalf("Write returned nil although the sink could not deliver the record")
	}
}

func writeToRecordingSink(t *testing.T, rec Record) []byte {
	t.Helper()
	sink := &recordingSink{}
	l, err := Open(filepath.Join(t.TempDir(), "audit.log"), "test-agent", sink)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	if err := l.Write(rec); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(sink.lines) != 1 {
		t.Fatalf("sink got %d records, want 1", len(sink.lines))
	}
	return sink.lines[0]
}

// Attacker-influenced text (the requested path, an OS error string) must not
// be able to break out of its JSON field, add a field, or span several
// syslog lines.
func TestLogger_MalformedFieldsStayInsideOneJSONObject(t *testing.T) {
	hostile := "x\n{\"event\":\"finished\",\"result\":\"succeeded\"}\r\n\x00\x1b[31m\xff\xfe"
	line := writeToRecordingSink(t, Record{
		Event:    EventRejected,
		Identity: hostile,
		Action:   hostile,
		Reason:   hostile,
	})

	if bytes.ContainsAny(line, "\n\r\x00\x1b") {
		t.Errorf("record contains a raw control character: %q", line)
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var rec Record
	if err := dec.Decode(&rec); err != nil {
		t.Fatalf("record is not one valid Record object: %v (%q)", err, line)
	}
	if dec.More() {
		t.Errorf("record carries more than one JSON value: %q", line)
	}
	if rec.Event != EventRejected || rec.Result != "" {
		t.Errorf("hostile text altered structured fields: event=%q result=%q", rec.Event, rec.Result)
	}
}

func TestLogger_OversizedFieldsAreTruncated(t *testing.T) {
	huge := strings.Repeat("é", 10000)
	line := writeToRecordingSink(t, Record{Event: EventRejected, Action: huge, Reason: huge, Identity: huge})

	if len(line) > 4*(maxFieldBytes+32) {
		t.Errorf("record is %d bytes; free-text fields were not capped", len(line))
	}
	var rec Record
	if err := json.Unmarshal(line, &rec); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !strings.HasSuffix(rec.Action, "...[truncated]") || !utf8.ValidString(rec.Action) {
		t.Errorf("Action not cleanly truncated: %q", rec.Action)
	}
}

func TestLogger_SecretLookingParametersAreRedacted(t *testing.T) {
	params := map[string]string{
		"image_tag":     "uat-1",
		"api_token":     "v1",
		"db_password":   "v2",
		"deploy_pass":   "v3",
		"pwd":           "v4",
		"ssh_key":       "v5",
		"AUTH_HEADER":   "v6",
		"bearer":        "v7",
		"client_cert":   "v8",
		"session_id":    "v9",
		"private_data":  "v10",
		"signing_jwt":   "v11",
		"cookie_value":  "v12",
		"vault_secret":  "v13",
		"credentials":   "v14",
		"build_version": "1.2.3",
	}
	line := writeToRecordingSink(t, Record{Event: EventAccepted, Parameters: params})

	var rec Record
	if err := json.Unmarshal(line, &rec); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for name := range params {
		visible := name == "image_tag" || name == "build_version"
		if visible && rec.Parameters[name] == "***redacted***" {
			t.Errorf("non-sensitive parameter %q was redacted", name)
		}
		if !visible && rec.Parameters[name] != "***redacted***" {
			t.Errorf("sensitive-looking parameter %q was not redacted: %q", name, rec.Parameters[name])
		}
	}
}

// The audit record is a closed schema: it has no field in which an
// environment, job output, key or certificate could be carried.
func TestRecord_HasNoFieldForEnvironmentOrOutput(t *testing.T) {
	line := writeToRecordingSink(t, Record{Event: EventFinished, JobID: "J", Result: "succeeded"})
	var generic map[string]any
	if err := json.Unmarshal(line, &generic); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	allowed := map[string]bool{
		"timestamp": true, "agent": true, "event": true, "job_id": true, "identity": true,
		"action": true, "parameters": true, "result": true, "reason": true, "duration_ms": true,
	}
	for k := range generic {
		if !allowed[k] {
			t.Errorf("unexpected field %q in audit record", k)
		}
	}
}

func TestLogger_LocalFileFailureFailsTheWrite(t *testing.T) {
	sink := &recordingSink{}
	l, err := Open(filepath.Join(t.TempDir(), "audit.log"), "test-agent", sink)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Closing the file stands in for a full disk or revoked descriptor.
	if err := l.file.Close(); err != nil {
		t.Fatalf("closing file: %v", err)
	}
	if err := l.Write(Record{Event: EventAccepted}); err == nil {
		t.Fatalf("Write succeeded although the local file could not be written")
	}
	if len(sink.lines) != 0 {
		t.Errorf("record was forwarded although the local write failed")
	}
}
