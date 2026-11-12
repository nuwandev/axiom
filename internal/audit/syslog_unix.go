//go:build unix

package audit

import (
	"fmt"
	"log/syslog"
)

// syslogTag is the SYSLOG_IDENTIFIER under which records appear in
// journald/syslog, so a collector can select Axiom's audit stream.
const syslogTag = "axiom-audit"

type syslogSink struct {
	w *syslog.Writer
}

// NewSyslogSink connects to the local syslog socket (journald serves
// /dev/log on systemd hosts) and returns a Sink that emits each audit record
// as one structured JSON message at facility AUTH, severity INFO. It fails if
// the socket is unavailable, so a misconfigured host refuses to start rather
// than silently running without the forwarded trail.
func NewSyslogSink() (Sink, error) {
	return dialSyslogSink("", "")
}

// dialSyslogSink connects to network/addr, or to the local syslog socket when
// both are empty. Split out so tests can point it at a private socket.
func dialSyslogSink(network, addr string) (Sink, error) {
	w, err := syslog.Dial(network, addr, syslog.LOG_AUTH|syslog.LOG_INFO, syslogTag)
	if err != nil {
		return nil, fmt.Errorf("connecting to syslog: %w", err)
	}
	return &syslogSink{w: w}, nil
}

func (s *syslogSink) WriteRecord(line []byte) error {
	_, err := s.w.Write(line)
	return err
}

func (s *syslogSink) Close() error { return s.w.Close() }
