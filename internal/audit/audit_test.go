package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
		Action: "backend.deploy",
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
