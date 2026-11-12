//go:build unix

package config

func defaultAuditLogPath() string { return DefaultAuditLogPath }

// syslogSupported reports whether audit.syslog can be honoured on this
// platform.
const syslogSupported = true
