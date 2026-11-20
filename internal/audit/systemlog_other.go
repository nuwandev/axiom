//go:build !unix && !windows

package audit

import "errors"

// NewSystemLogSink is unavailable on platforms without a system log sink;
// config rejects audit.syslog there before this is reached.
func NewSystemLogSink() (Sink, error) {
	return nil, errors.New("system log audit sink is not supported on this platform")
}
