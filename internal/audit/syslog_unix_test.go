//go:build unix

package audit

import (
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSyslogSink_EmitsTaggedStructuredJSON(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "log.sock")
	srv, err := net.ListenPacket("unixgram", sock)
	if err != nil {
		t.Fatalf("listening on %s: %v", sock, err)
	}
	defer srv.Close()

	sink, err := dialSyslogSink("unixgram", sock)
	if err != nil {
		t.Fatalf("dialSyslogSink: %v", err)
	}
	l, err := Open(filepath.Join(t.TempDir(), "audit.log"), "test-agent", sink)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	if err := l.Write(Record{
		Event:      EventAccepted,
		Identity:   "ci-jenkins",
		Action:     "sample.action",
		Parameters: map[string]string{"image_tag": "uat-1", "api_token": "super-secret-value"},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	buf := make([]byte, 65536)
	_ = srv.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := srv.ReadFrom(buf)
	if err != nil {
		t.Fatalf("no syslog datagram received: %v", err)
	}
	msg := string(buf[:n])

	if !strings.Contains(msg, syslogTag) {
		t.Errorf("datagram lacks the %q tag: %q", syslogTag, msg)
	}
	if strings.Contains(msg, "super-secret-value") {
		t.Errorf("datagram leaked a sensitive parameter value: %q", msg)
	}
	start := strings.Index(msg, "{")
	if start < 0 {
		t.Fatalf("datagram carries no JSON body: %q", msg)
	}
	var rec Record
	if err := json.Unmarshal([]byte(msg[start:]), &rec); err != nil {
		t.Fatalf("body is not valid JSON: %v (%q)", err, msg[start:])
	}
	if rec.Event != EventAccepted || rec.Identity != "ci-jenkins" || rec.Action != "sample.action" {
		t.Errorf("unexpected record: %+v", rec)
	}
}

func TestSyslogSink_FailsToStartWhenSocketMissing(t *testing.T) {
	if _, err := dialSyslogSink("unixgram", filepath.Join(t.TempDir(), "absent.sock")); err == nil {
		t.Fatalf("expected an error when the syslog socket does not exist")
	}
}
