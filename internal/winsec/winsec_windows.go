//go:build windows

// Package winsec implements the Windows-native filesystem security checks
// that stand in for Axiom's POSIX ownership/mode-bit checks on unix.
//
// The property being enforced is the same on both platforms: an
// unauthorized local principal must not be able to modify a capability
// script, the agent configuration, or the TLS key material and thereby
// obtain Axiom's execution context. On Windows that means:
//
//   - the file/directory is not a reparse point (junction/symlink);
//   - it is owned by Administrators, SYSTEM, or TrustedInstaller;
//   - its DACL grants write/modify/delete/take-ownership to no principal
//     other than those three (for a capability script, config or key) —
//     the Axiom service account included, so the agent can execute its
//     capabilities but never rewrite them;
//   - some ancestor directory has an explicit, inheritance-protected DACL
//     (SE_DACL_PROTECTED), so the weak default %ProgramData% ACL — which
//     lets any user create files — cannot reach Axiom's own tree. The
//     ancestry walk stops at that protected directory rather than climbing
//     to the volume root.
//
// Inherit-only ACEs (INHERIT_ONLY_ACE) are ignored: they do not grant
// access to the object they are attached to, only to children propagated
// into it, and those children are validated on their own. CREATOR OWNER /
// CREATOR GROUP ACEs, present on nearly every real Windows directory, are
// all inherit-only.
//
// The pure decision logic (evaluateProtectedFile / evaluateProtectedDir) is
// separated from the Win32 calls so it can be unit-tested with synthetic
// security descriptors without requiring an ACL-configured fixture on disk.
package winsec

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Well-known SIDs that are permitted to own, and hold write access to,
// Axiom's protected files. These are stable across every Windows install.
const (
	sidAdministrators   = "S-1-5-32-544"
	sidLocalSystem      = "S-1-5-18"
	sidTrustedInstaller = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
)

// dangerousAccessMask is the set of access rights that would let a principal
// modify a file's contents, delete it, replace it, or rewrite its ACL. For a
// directory the same bit values mean create-file / create-subdirectory /
// delete-child, which is exactly the create right the default %ProgramData%
// ACL hands to Users and which a protected Axiom directory must not inherit.
const dangerousAccessMask uint32 = 0x00000002 | // FILE_WRITE_DATA / FILE_ADD_FILE
	0x00000004 | // FILE_APPEND_DATA / FILE_ADD_SUBDIRECTORY
	0x00000040 | // FILE_DELETE_CHILD
	0x00010000 | // DELETE
	0x00040000 | // WRITE_DAC
	0x00080000 | // WRITE_OWNER
	0x40000000 | // GENERIC_WRITE
	0x10000000 // GENERIC_ALL

func isTrusted(sid string) bool {
	switch sid {
	case sidAdministrators, sidLocalSystem, sidTrustedInstaller:
		return true
	}
	return false
}

// inheritOnlyAce is the ACE_HEADER flag marking an ACE that does not apply
// to the object it is attached to — only to children it propagates into.
// CREATOR OWNER / CREATOR GROUP ACEs (and the "children get full control"
// propagation ACEs) on real Windows directories carry this flag.
const inheritOnlyAce = 0x08 // INHERIT_ONLY_ACE

// aclEntry is one resolved ACE.
type aclEntry struct {
	sid         string
	allow       bool
	inheritOnly bool
	mask        uint32
}

// fileSec is the resolved security state of one path.
type fileSec struct {
	owner     string
	dacl      []aclEntry
	daclNil   bool
	protected bool
}

// ValidateCapabilityScript verifies that path is a PowerShell capability
// script Axiom may execute and that no unauthorized local principal can
// modify or redirect it. It fails closed.
func ValidateCapabilityScript(path string) error {
	return validateLeaf(path, ".ps1")
}

// ValidateProtectedFile verifies that path (e.g. the PowerShell interpreter)
// cannot be modified or substituted by an unauthorized local principal. It
// does not require a particular extension.
func ValidateProtectedFile(path string) error {
	return validateLeaf(path, "")
}

func validateLeaf(path, requiredExt string) error {
	if err := checkPathCharset(path); err != nil {
		return fmt.Errorf("%q: %w", path, err)
	}
	fs, isReparse, isDir, err := readFileSec(path)
	if err != nil {
		return fmt.Errorf("%q: %w", path, err)
	}
	if err := evaluateProtectedFile(fs, isReparse, isDir, filepath.Base(path), requiredExt); err != nil {
		return fmt.Errorf("%q %w", path, err)
	}
	return nil
}

// RejectReparsePoint refuses a path whose final component is a reparse point
// (symlink or directory junction), matching rejectSymlink on unix.
func RejectReparsePoint(path string) error {
	attrs, err := fileAttributes(path)
	if err != nil {
		return fmt.Errorf("%q: not accessible: %w", path, err)
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%q is a reparse point (symlink/junction); configured paths must be real files", path)
	}
	return nil
}

// RequireProtectedAncestry walks the directories above path and requires
// each to be owned by a trusted principal with no untrusted write access,
// stopping at (and requiring) an ancestor with an inheritance-protected
// DACL. It returns an error if the walk reaches the volume root without
// finding one.
func RequireProtectedAncestry(path string) error {
	self := currentSID()
	dir := filepath.Dir(path)
	for {
		ds, _, isDir, err := readFileSec(dir)
		if err != nil {
			return fmt.Errorf("checking directory %q: %w", dir, err)
		}
		if !isDir {
			return fmt.Errorf("%q is not a directory", dir)
		}
		if err := evaluateProtectedDir(ds, self); err != nil {
			return fmt.Errorf("directory %q %w", dir, err)
		}
		if ds.protected {
			return nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fmt.Errorf("no inheritance-protected directory found above %q; the Axiom data directory must have ACL inheritance disabled (icacls <dir> /inheritance:r)", path)
		}
		dir = parent
	}
}

// evaluateProtectedFile is the pure decision logic for a leaf file.
func evaluateProtectedFile(fs fileSec, isReparse, isDir bool, base, requiredExt string) error {
	if isReparse {
		return fmt.Errorf("is a reparse point (symlink/junction); configured paths must be real files")
	}
	if isDir {
		return fmt.Errorf("is a directory, expected a file")
	}
	if requiredExt != "" && !strings.EqualFold(filepath.Ext(base), requiredExt) {
		return fmt.Errorf("must be a %s file, got %q", requiredExt, base)
	}
	if fs.daclNil {
		return fmt.Errorf("has no DACL (grants everyone full control)")
	}
	if !isTrusted(fs.owner) {
		return fmt.Errorf("is owned by %s; must be owned by Administrators, SYSTEM, or TrustedInstaller", fs.owner)
	}
	for _, e := range fs.dacl {
		// An inherit-only ACE does not grant access to this object, only to
		// children it propagates into — and those children are validated on
		// their own when Axiom reads them.
		if !e.allow || e.inheritOnly || isTrusted(e.sid) {
			continue
		}
		if e.mask&dangerousAccessMask != 0 {
			return fmt.Errorf("grants write/modify access to %s; only Administrators, SYSTEM, and TrustedInstaller may modify it (the Axiom service account must not)", e.sid)
		}
	}
	return nil
}

// evaluateProtectedDir is the pure decision logic for an ancestor directory.
// Unlike a leaf file, the running Axiom account (self) is permitted to hold
// write access — the audit-log directory legitimately needs it, mirroring
// the unix check's allowance for a directory owned by the service account.
func evaluateProtectedDir(ds fileSec, self string) error {
	if ds.daclNil {
		return fmt.Errorf("has no DACL (grants everyone full control)")
	}
	if !isTrusted(ds.owner) {
		return fmt.Errorf("is owned by %s; must be owned by Administrators, SYSTEM, or TrustedInstaller", ds.owner)
	}
	for _, e := range ds.dacl {
		// Inherit-only ACEs (e.g. CREATOR OWNER, which is present on nearly
		// every real Windows directory) do not apply to this directory
		// itself — skip them. A child created here is validated separately.
		if !e.allow || e.inheritOnly || isTrusted(e.sid) || (self != "" && e.sid == self) {
			continue
		}
		if e.mask&dangerousAccessMask != 0 {
			return fmt.Errorf("grants write access to %s", e.sid)
		}
	}
	return nil
}

// --- Win32 plumbing -------------------------------------------------------

func fileAttributes(path string) (uint32, error) {
	p16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.GetFileAttributes(p16)
}

func readFileSec(path string) (fs fileSec, isReparse, isDir bool, err error) {
	attrs, err := fileAttributes(path)
	if err != nil {
		return fileSec{}, false, false, fmt.Errorf("not accessible: %w", err)
	}
	isReparse = attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
	isDir = attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0

	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fileSec{}, isReparse, isDir, fmt.Errorf("reading security info: %w", err)
	}

	owner, _, err := sd.Owner()
	if err != nil {
		return fileSec{}, isReparse, isDir, fmt.Errorf("reading owner: %w", err)
	}
	control, _, err := sd.Control()
	if err != nil {
		return fileSec{}, isReparse, isDir, fmt.Errorf("reading control flags: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fileSec{}, isReparse, isDir, fmt.Errorf("reading DACL: %w", err)
	}

	fs = fileSec{
		owner:     owner.String(),
		protected: control&windows.SE_DACL_PROTECTED != 0,
	}
	if dacl == nil {
		fs.daclNil = true
	} else {
		fs.dacl, err = readEntries(dacl)
		if err != nil {
			return fileSec{}, isReparse, isDir, err
		}
	}
	// Keep sd (which owns the ACL/SID buffers) alive until every SID string
	// has been extracted above.
	runtime.KeepAlive(sd)
	return fs, isReparse, isDir, nil
}

// Simple, well-understood ACE types. Anything else (object ACEs, conditional
// "callback" ACEs from Dynamic Access Control) has a different in-memory
// layout — the SID is not where ACCESS_ALLOWED_ACE.SidStart points — and
// could grant access in ways this check cannot reason about. Rather than
// misread one, we reject the ACL and require the operator to simplify it
// (the installer only ever writes type-0 allow ACEs).
const (
	aceAccessAllowed = 0
	aceAccessDenied  = 1
	aceSystemAudit   = 2
	aceSystemAlarm   = 3
)

func readEntries(acl *windows.ACL) ([]aclEntry, error) {
	out := make([]aclEntry, 0, acl.AceCount)
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return nil, fmt.Errorf("reading ACE %d: %w", i, err)
		}
		switch ace.Header.AceType {
		case aceAccessAllowed, aceAccessDenied:
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			out = append(out, aclEntry{
				sid:         sid.String(),
				allow:       ace.Header.AceType == aceAccessAllowed,
				inheritOnly: ace.Header.AceFlags&inheritOnlyAce != 0,
				mask:        uint32(ace.Mask),
			})
		case aceSystemAudit, aceSystemAlarm:
			// SACL-style entries; not access grants. Ignore.
		default:
			return nil, fmt.Errorf("ACE %d is an unsupported type (%d); the ACL must use only simple allow/deny entries", i, ace.Header.AceType)
		}
	}
	return out, nil
}

var (
	selfOnce sync.Once
	selfSID  string
)

// currentSID is the SID of the account Axiom is running as — the analogue of
// os.Geteuid() in the unix checks.
func currentSID() string {
	selfOnce.Do(func() {
		if tu, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
			selfSID = tu.User.Sid.String()
		}
	})
	return selfSID
}

func checkPathCharset(path string) error {
	for _, r := range path {
		if r < 0x20 {
			return fmt.Errorf("contains a control character")
		}
		switch r {
		case '"', '\'', '`', '$', ';', '&', '|', '<', '>', '%', '*', '?':
			return fmt.Errorf("contains the disallowed character %q", r)
		}
	}
	return nil
}
