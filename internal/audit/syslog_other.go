//go:build !unix

package audit

import "errors"

// NewSyslogSink is unavailable off unix; config rejects audit.syslog there
// before this is reached.
func NewSyslogSink() (Sink, error) {
	return nil, errors.New("syslog audit sink is not supported on this platform")
}
