//go:build unix

package config

import (
	"os"
	"testing"
)

func TestEvaluateScriptSecurity(t *testing.T) {
	const me = 1000

	cases := []struct {
		name        string
		isDir       bool
		mode        os.FileMode
		ownerUID    uint32
		expectedUID uint32
		wantErr     bool
	}{
		{"ok root owned 0755", false, 0o755, 0, me, false},
		{"ok owned by service account 0750", false, 0o750, me, me, false},
		{"rejects directory", true, 0o755, 0, me, true},
		{"rejects non-executable", false, 0o644, 0, me, true},
		{"rejects group writable", false, 0o775, 0, me, true},
		{"rejects world writable", false, 0o757, 0, me, true},
		{"rejects wrong owner", false, 0o755, 1234, me, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := evaluateScriptSecurity("/opt/axiom/actions/x.sh", tc.isDir, tc.mode, tc.ownerUID, tc.expectedUID)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}
