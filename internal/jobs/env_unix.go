//go:build unix

package jobs

// baseEnv is the fixed base environment every capability process starts
// from on unix. The Axiom process's own environment is deliberately not
// inherited; systemd (packaging/axiom.service) supplies HOME and a private
// /tmp for the child tools that need them.
func baseEnv() []string {
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
}
