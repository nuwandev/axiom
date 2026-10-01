//go:build windows

package winsec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	sidUsers      = "S-1-5-32-545"
	sidEveryone   = "S-1-1-0"
	sidAuthUsers  = "S-1-5-11"
	sidRandomUser = "S-1-5-21-100-200-300-1001"
	sidService    = "S-1-5-80-3139157870-2983391045-3678747466-658725712-1809339113"
)

const (
	maskRead     = 0x00120089 // FILE_GENERIC_READ
	maskExecute  = 0x001200A0 // FILE_GENERIC_EXECUTE
	maskReadExec = maskRead | maskExecute
	maskWrite    = 0x00000002 // FILE_WRITE_DATA
	maskModify   = 0x001301BF // FILE_GENERIC_WRITE-ish
	maskAll      = 0x001F01FF // FILE_ALL_ACCESS
)

func allow(sid string, mask uint32) aclEntry { return aclEntry{sid: sid, allow: true, mask: mask} }
func deny(sid string, mask uint32) aclEntry  { return aclEntry{sid: sid, allow: false, mask: mask} }
func inheritOnly(sid string, mask uint32) aclEntry {
	return aclEntry{sid: sid, allow: true, inheritOnly: true, mask: mask}
}

const sidCreatorOwner = "S-1-3-0"

// TestEvaluate_InheritOnlyAcesAreIgnored covers the validation-discovered
// case: real Windows directories carry inherit-only CREATOR OWNER and
// "children get full control" ACEs that do NOT apply to the directory
// itself. Flagging them caused Axiom to refuse to start against an
// otherwise-correct layout.
func TestEvaluate_InheritOnlyAcesAreIgnored(t *testing.T) {
	base := []aclEntry{allow(sidAdministrators, maskAll), allow(sidLocalSystem, maskAll)}

	dirWithInheritOnly := fileSec{owner: sidAdministrators, dacl: append(append([]aclEntry{}, base...),
		inheritOnly(sidCreatorOwner, 0x10000000), // GENERIC_ALL, inherit-only — must be ignored
		inheritOnly(sidUsers, 0x10000000),        // "children get full control" — must be ignored
		allow(sidUsers, maskReadExec),            // effective: read+execute only — fine
	)}
	if err := evaluateProtectedDir(dirWithInheritOnly, sidService); err != nil {
		t.Errorf("directory with only inherit-only dangerous ACEs rejected: %v", err)
	}

	fileWithInheritOnly := fileSec{owner: sidTrustedInstaller, dacl: append(append([]aclEntry{}, base...),
		inheritOnly(sidCreatorOwner, maskAll),
		allow(sidUsers, maskReadExec),
	)}
	if err := evaluateProtectedFile(fileWithInheritOnly, false, false, "x.ps1", ".ps1"); err != nil {
		t.Errorf("file with an inherit-only CREATOR OWNER ACE rejected: %v", err)
	}

	// An EFFECTIVE (non-inherit-only) Users:write ACE must still be caught.
	dirWithRealWrite := fileSec{owner: sidAdministrators, dacl: append(append([]aclEntry{}, base...),
		inheritOnly(sidCreatorOwner, 0x10000000),
		allow(sidUsers, 0x00000116), // create files / write — effective — must reject
	)}
	if err := evaluateProtectedDir(dirWithRealWrite, sidService); err == nil {
		t.Errorf("directory with an effective Users create-files ACE not rejected")
	}
}

func TestEvaluateProtectedFile(t *testing.T) {
	okDACL := []aclEntry{
		allow(sidAdministrators, maskAll),
		allow(sidLocalSystem, maskAll),
		allow(sidService, maskReadExec),
		allow(sidUsers, maskReadExec),
	}

	cases := []struct {
		name       string
		fs         fileSec
		isReparse  bool
		isDir      bool
		base       string
		ext        string
		wantErr    bool
		wantSubstr string
	}{
		{
			name: "ok admin-owned ps1, service read+execute only",
			fs:   fileSec{owner: sidAdministrators, dacl: okDACL},
			base: "sample-action.ps1", ext: ".ps1", wantErr: false,
		},
		{
			name: "ok trusted-installer owned", fs: fileSec{owner: sidTrustedInstaller, dacl: okDACL},
			base: "x.ps1", ext: ".ps1", wantErr: false,
		},
		{
			name: "reparse point rejected", fs: fileSec{owner: sidAdministrators, dacl: okDACL},
			isReparse: true, base: "x.ps1", ext: ".ps1", wantErr: true, wantSubstr: "reparse point",
		},
		{
			name: "directory rejected", fs: fileSec{owner: sidAdministrators, dacl: okDACL},
			isDir: true, base: "x.ps1", ext: ".ps1", wantErr: true, wantSubstr: "directory",
		},
		{
			name: "wrong extension rejected", fs: fileSec{owner: sidAdministrators, dacl: okDACL},
			base: "deploy.bat", ext: ".ps1", wantErr: true, wantSubstr: "ps1",
		},
		{
			name: "extension case-insensitive", fs: fileSec{owner: sidAdministrators, dacl: okDACL},
			base: "Deploy.PS1", ext: ".ps1", wantErr: false,
		},
		{
			name: "untrusted owner rejected",
			fs:   fileSec{owner: sidRandomUser, dacl: okDACL},
			base: "x.ps1", ext: ".ps1", wantErr: true, wantSubstr: "owned by",
		},
		{
			name: "Users write ACE rejected",
			fs:   fileSec{owner: sidAdministrators, dacl: append(append([]aclEntry{}, okDACL...), allow(sidUsers, maskWrite))},
			base: "x.ps1", ext: ".ps1", wantErr: true, wantSubstr: "write/modify access to " + sidUsers,
		},
		{
			name: "Everyone full-control ACE rejected",
			fs:   fileSec{owner: sidAdministrators, dacl: []aclEntry{allow(sidEveryone, maskAll)}},
			base: "x.ps1", ext: ".ps1", wantErr: true, wantSubstr: "write/modify",
		},
		{
			name: "service account write ACE rejected (agent must not rewrite its scripts)",
			fs:   fileSec{owner: sidAdministrators, dacl: []aclEntry{allow(sidAdministrators, maskAll), allow(sidService, maskModify)}},
			base: "x.ps1", ext: ".ps1", wantErr: true, wantSubstr: "write/modify access to " + sidService,
		},
		{
			name: "deny ACE for Users is ignored (safe)",
			fs:   fileSec{owner: sidAdministrators, dacl: append(append([]aclEntry{}, okDACL...), deny(sidUsers, maskAll))},
			base: "x.ps1", ext: ".ps1", wantErr: false,
		},
		{
			name: "nil DACL rejected", fs: fileSec{owner: sidAdministrators, daclNil: true},
			base: "x.ps1", ext: ".ps1", wantErr: true, wantSubstr: "no DACL",
		},
		{
			name: "GENERIC_WRITE bit for AuthUsers rejected",
			fs:   fileSec{owner: sidLocalSystem, dacl: []aclEntry{allow(sidAdministrators, maskAll), allow(sidAuthUsers, 0x40000000)}},
			base: "x.ps1", ext: ".ps1", wantErr: true, wantSubstr: "write/modify",
		},
		{
			name: "no extension requirement (interpreter check)",
			fs:   fileSec{owner: sidTrustedInstaller, dacl: okDACL},
			base: "powershell.exe", ext: "", wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := evaluateProtectedFile(tc.fs, tc.isReparse, tc.isDir, tc.base, tc.ext)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if tc.wantSubstr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantSubstr)) {
				t.Fatalf("error %v does not contain %q", err, tc.wantSubstr)
			}
		})
	}
}

func TestEvaluateProtectedDir(t *testing.T) {
	const self = sidService

	cases := []struct {
		name       string
		ds         fileSec
		wantErr    bool
		wantSubstr string
	}{
		{
			name: "ok admin-owned, service read+execute",
			ds:   fileSec{owner: sidAdministrators, dacl: []aclEntry{allow(sidAdministrators, maskAll), allow(sidLocalSystem, maskAll), allow(self, maskReadExec)}},
		},
		{
			name: "ok service account holds Modify on this dir (audit-log tree)",
			ds:   fileSec{owner: sidAdministrators, dacl: []aclEntry{allow(sidAdministrators, maskAll), allow(self, maskModify)}},
		},
		{
			name:       "untrusted owner rejected",
			ds:         fileSec{owner: sidRandomUser, dacl: []aclEntry{allow(sidAdministrators, maskAll)}},
			wantErr:    true,
			wantSubstr: "owned by",
		},
		{
			name:       "Users create-file (weak %ProgramData% default) rejected",
			ds:         fileSec{owner: sidAdministrators, dacl: []aclEntry{allow(sidAdministrators, maskAll), allow(sidUsers, 0x00000004)}},
			wantErr:    true,
			wantSubstr: "write access to " + sidUsers,
		},
		{
			name:       "nil DACL rejected",
			ds:         fileSec{owner: sidAdministrators, daclNil: true},
			wantErr:    true,
			wantSubstr: "no DACL",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := evaluateProtectedDir(tc.ds, self)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if tc.wantSubstr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantSubstr)) {
				t.Fatalf("error %v does not contain %q", err, tc.wantSubstr)
			}
		})
	}
}

func TestCheckPathCharset(t *testing.T) {
	ok := []string{
		`C:\ProgramData\Axiom\capabilities\sample-action.ps1`,
		`C:\Program Files\Axiom\Invoke.ps1`,
		`D:\apps\deploy_v2 (staging).ps1`,
	}
	for _, p := range ok {
		if err := checkPathCharset(p); err != nil {
			t.Errorf("checkPathCharset(%q) = %v, want nil", p, err)
		}
	}
	bad := []string{
		`C:\x\a";calc.ps1`,
		"C:\\x\\a`b.ps1",
		`C:\x\$env.ps1`,
		`C:\x\a;b.ps1`,
		`C:\x\a&b.ps1`,
		`C:\x\a|b.ps1`,
		`C:\x\%TEMP%.ps1`,
		"C:\\x\\a\nb.ps1",
	}
	for _, p := range bad {
		if err := checkPathCharset(p); err == nil {
			t.Errorf("checkPathCharset(%q) = nil, want error", p)
		}
	}
}

// TestRealFilesystem exercises the Win32 read path (readFileSec) plus the
// pure evaluator against real files. It does not require elevation: an
// ordinary temp file is owned by the current (non-trusted) user, which is
// exactly the "insecure" condition the check must reject. The extension
// check runs before the owner check, so the .ps1-enforcement assertion also
// works here.
func TestRealFilesystem(t *testing.T) {
	dir := t.TempDir()

	bat := filepath.Join(dir, "capability.bat")
	if err := os.WriteFile(bat, []byte("echo hi\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCapabilityScript(bat); err == nil || !strings.Contains(err.Error(), "ps1") {
		t.Errorf("ValidateCapabilityScript(.bat) = %v, want a .ps1 error", err)
	}

	ps1 := filepath.Join(dir, "capability.ps1")
	if err := os.WriteFile(ps1, []byte("exit 0\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCapabilityScript(ps1); err == nil {
		t.Errorf("ValidateCapabilityScript on a user-owned temp .ps1 = nil, want an ownership/ACL error")
	} else if !strings.Contains(err.Error(), "owned by") && !strings.Contains(err.Error(), "write") {
		t.Errorf("unexpected error shape: %v", err)
	}

	if err := RejectReparsePoint(ps1); err != nil {
		t.Errorf("RejectReparsePoint on a real file = %v, want nil", err)
	}

	// A directory junction is a reparse point and must be rejected.
	target := filepath.Join(dir, "realdir")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(dir, "junc")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Skipf("cannot create junction for test: %v (%s)", err, out)
	}
	if err := RejectReparsePoint(junction); err == nil {
		t.Errorf("RejectReparsePoint on a junction = nil, want error")
	}
}
