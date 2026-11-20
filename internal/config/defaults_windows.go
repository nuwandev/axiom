//go:build windows

package config

import (
	"os"
	"path/filepath"
)

// defaultAuditLogPath is the Windows default used when audit.path is not set
// in config: %ProgramData%\Axiom\logs\audit.log, matching the layout
// Install-Axiom.ps1 creates.
func defaultAuditLogPath() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "Axiom", "logs", "audit.log")
}

// syslogSupported reports whether audit.syslog can be honoured on this
// platform. On Windows it forwards each record to the Application event log.
const syslogSupported = true
