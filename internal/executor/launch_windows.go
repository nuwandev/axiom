//go:build windows

package executor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows/registry"

	"github.com/nuwandev/axiom/internal/winsec"
)

// powerShellPath is the fixed Windows PowerShell 5.1 interpreter that ships
// in-box on Windows Server 2022. It is deliberately not configurable and is
// never resolved via PATH: an attacker who can influence the interpreter
// path could run arbitrary code regardless of every other control.
func powerShellPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, `System32`, `WindowsPowerShell`, `v1.0`, `powershell.exe`)
}

// buildCommand runs the approved capability script through a fixed PowerShell
// invocation. Every element of the argument vector except the trailing
// script path is a compile-time constant. The script path is Spec.Command,
// which internal/config has already validated (absolute, clean, .ps1,
// integrity-checked). There is no Spec field for extra arguments and this
// package constructs none.
//
//   - -File  (never -Command): the argument is a script path, not code.
//   - -NoProfile: machine/user profile scripts do not run in the capability
//     process.
//   - -NonInteractive: a stray Read-Host / credential prompt fails fast
//     instead of hanging until the action timeout.
//   - -ExecutionPolicy Bypass: scoped to this one, fixed invocation only --
//     it does not call Set-ExecutionPolicy, does not touch any persisted
//     machine/user policy, and has no effect on any other process on the
//     box. VALIDATION-DISCOVERED: Windows 10/11 Pro ships with the local
//     policy Restricted by default (Windows Server ships RemoteSigned),
//     which otherwise blocks this exact invocation unconditionally, every
//     time, regardless of script signature -- making the product simply
//     not work out of the box on a large share of real target machines
//     unless an operator first runs Set-ExecutionPolicy themselves, which
//     is not something this project should require of every customer. This
//     is safe to do because execution policy was never Axiom's security
//     boundary in the first place (see docs/windows-security.md): the
//     script path is still fixed by server-side config, never the caller;
//     its content is still integrity-checked by NTFS ACLs
//     (internal/winsec), which this flag does not touch; no arbitrary
//     command or argument reaches the command line either way. The one
//     thing this flag genuinely cannot override is a real Group Policy
//     (MachinePolicy/UserPolicy scope always wins over any command-line
//     -ExecutionPolicy, by PowerShell's own design) -- CheckPlatformPrerequisites
//     checks for exactly that case and fails startup with a clear message,
//     since no software fix exists for an administrator's own GPO.
func buildCommand(spec Spec) *exec.Cmd {
	return exec.Command(powerShellPath(),
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", spec.Command)
}

var (
	prereqOnce sync.Once
	prereqErr  error
)

// CheckPlatformPrerequisites verifies at startup that the fixed PowerShell
// interpreter exists and cannot be substituted or redirected by an
// unauthorized local principal, and that no Group Policy enforces an
// execution policy that would block every capability invocation outright
// (see checkExecutionPolicy). Axiom refuses to start otherwise.
func CheckPlatformPrerequisites() error {
	prereqOnce.Do(func() {
		p := powerShellPath()
		if _, err := os.Stat(p); err != nil {
			prereqErr = fmt.Errorf("Windows PowerShell not found at %q: %w", p, err)
			return
		}
		if err := winsec.ValidateProtectedFile(p); err != nil {
			prereqErr = fmt.Errorf("PowerShell interpreter integrity: %w", err)
			return
		}
		prereqErr = checkExecutionPolicy()
	})
	return prereqErr
}

// gpoPolicyKey is where the "Turn on Script Execution" Group Policy setting
// is stored, under HKLM for the machine scope and HKCU for the user scope.
// These are the same values PowerShell's own MachinePolicy / UserPolicy
// scopes read.
const gpoPolicyKey = `SOFTWARE\Policies\Microsoft\Windows\PowerShell`

// checkExecutionPolicy fails closed only if a real Group Policy
// (MachinePolicy or UserPolicy scope) enforces an execution policy that
// would block Axiom's capability invocation. Those two scopes are the one
// thing buildCommand's own "-ExecutionPolicy Bypass" genuinely cannot
// override -- Group Policy always wins over any command-line
// -ExecutionPolicy, by PowerShell's own design -- so this is the one case
// that is a real, unfixable-by-us precondition rather than something the
// product should just handle. Any *local*, non-GPO policy (LocalMachine or
// CurrentUser scope, including the Restricted that Windows 10/11 Pro ships
// with by default) is intentionally not checked here: buildCommand's own
// Bypass flag already handles it, and asking every customer to change their
// own machine's local policy just to make the product work is not
// something this project does (see buildCommand's comment and
// docs/windows-security.md for why that is safe to rely on).
//
// The Group Policy registry values are read directly rather than by
// spawning powershell.exe and calling Get-ExecutionPolicy.
// VALIDATION-DISCOVERED: the spawned form failed on GitHub's hosted
// windows-latest CI runner with "CouldNotAutoloadMatchingModule" for
// Microsoft.PowerShell.Security (where Get-ExecutionPolicy lives), and the
// failure persisted with -ExecutionPolicy Bypass, so it was not the local
// policy gating the autoload. Reading the registry has no dependency on
// PowerShell's module system at all, and also removes a cold powershell.exe
// spawn from the startup path.
func checkExecutionPolicy() error {
	machine, err := gpoExecutionPolicy(registry.LOCAL_MACHINE, gpoPolicyKey)
	if err != nil {
		return fmt.Errorf("checking machine Group Policy for PowerShell execution policy: %w", err)
	}
	user, err := gpoExecutionPolicy(registry.CURRENT_USER, gpoPolicyKey)
	if err != nil {
		return fmt.Errorf("checking user Group Policy for PowerShell execution policy: %w", err)
	}

	// Machine scope takes precedence over user scope, matching PowerShell's
	// own resolution order.
	gpo := machine
	if gpo == "" {
		gpo = user
	}
	if gpo == "Restricted" || gpo == "AllSigned" {
		return fmt.Errorf(
			"Group Policy enforces the PowerShell execution policy %q machine-wide, which blocks Axiom's "+
				"capability scripts -- Axiom always invokes its own fixed, ACL-protected script with "+
				"-ExecutionPolicy Bypass, but Group Policy always overrides that, so no setting on Axiom's "+
				"side can work around it. Ask your Windows administrator to allow RemoteSigned via Group "+
				"Policy, or (if the policy is AllSigned) Authenticode-sign your capability scripts", gpo)
	}
	return nil
}

// gpoExecutionPolicy returns the execution policy a Group Policy sets under
// root, or "" if none is set. "Turn on Script Execution" set to Disabled
// writes EnableScripts=0, which blocks every script the same way Restricted
// does, so it is reported as Restricted. A missing key or value means no
// policy is set; any other registry error is returned rather than treated as
// "no policy", so an unreadable policy fails closed instead of silently
// passing.
func gpoExecutionPolicy(root registry.Key, path string) (string, error) {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer k.Close()

	if enabled, _, err := k.GetIntegerValue("EnableScripts"); err == nil {
		if enabled == 0 {
			return "Restricted", nil
		}
	} else if !errors.Is(err, registry.ErrNotExist) {
		return "", err
	}

	policy, _, err := k.GetStringValue("ExecutionPolicy")
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return policy, nil
}
