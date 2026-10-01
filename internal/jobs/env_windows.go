//go:build windows

package jobs

import "os"

// windowsBaseEnvNames is the fixed allowlist of environment variables a
// capability's child process (powershell.exe, and whatever it invokes such
// as docker or kubectl) needs in order to function. Values are taken from
// Axiom's own process environment, which on a service is supplied by the
// Service Control Manager from the system environment — it is not
// caller-influenced. Nothing outside this list is inherited.
//
// This is the Windows analogue of the unix build's fixed PATH plus the
// HOME / private-tmp that systemd provides there (see packaging/axiom.service).
var windowsBaseEnvNames = []string{
	"SystemRoot",
	"windir",
	"SystemDrive",
	"Path",
	"PATHEXT",
	"ComSpec",
	"TEMP",
	"TMP",
	"USERPROFILE",
	"HOMEDRIVE",
	"HOMEPATH",
	"APPDATA",
	"LOCALAPPDATA",
	"ProgramData",
	"ProgramFiles",
	"ProgramFiles(x86)",
	"ProgramW6432",
	"NUMBER_OF_PROCESSORS",
	"PROCESSOR_ARCHITECTURE",
	"COMPUTERNAME",
	"USERDOMAIN",
}

// baseEnv is the fixed base environment every capability process starts
// from on Windows.
func baseEnv() []string {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}

	seen := make(map[string]struct{}, len(windowsBaseEnvNames))
	env := make([]string, 0, len(windowsBaseEnvNames)+3)
	for _, name := range windowsBaseEnvNames {
		if v, ok := os.LookupEnv(name); ok && v != "" {
			env = append(env, name+"="+v)
			seen[name] = struct{}{}
		}
	}

	// Deterministic fallbacks for the few variables a fresh service context
	// can be missing but that common tooling hard-requires.
	if _, ok := seen["SystemRoot"]; !ok {
		env = append(env, "SystemRoot="+systemRoot)
	}
	if _, ok := seen["Path"]; !ok {
		env = append(env, "Path="+systemRoot+`\System32;`+systemRoot+`;`+systemRoot+`\System32\WindowsPowerShell\v1.0`)
	}
	if _, ok := seen["PATHEXT"]; !ok {
		env = append(env, "PATHEXT=.COM;.EXE;.BAT;.CMD;.PS1")
	}
	if _, ok := seen["ComSpec"]; !ok {
		env = append(env, "ComSpec="+systemRoot+`\System32\cmd.exe`)
	}
	return env
}
