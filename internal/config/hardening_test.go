//go:build unix

package config

import (
	"strings"
	"testing"
	"time"
)

// loadWith loads baseYAML with edit applied to the YAML text first.
func loadWith(t *testing.T, edit func(yaml, dir string) string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\n", 0o750)
	ca := writeFixture(t, dir, "ca.crt", testCertPEM, 0o640)
	cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
	key := writeFixture(t, dir, "server.key", testKeyPEM, 0o600)
	content := edit(baseYAML(dir, script, ca, cert, key), dir)
	return Load(writeFixture(t, dir, "config.yaml", content, 0o640))
}

func TestLoad_IdempotencyWindow(t *testing.T) {
	for _, tc := range []struct {
		value   string
		want    time.Duration
		wantErr bool
	}{
		{"", DefaultIdempotencyWindow, false},
		{"30s", 30 * time.Second, false},
		{"1s", time.Second, false},
		{"24h", 24 * time.Hour, false},
		{"500ms", 0, true},
		{"25h", 0, true},
		{"-1m", 0, true},
	} {
		cfg, err := loadWith(t, func(y, _ string) string {
			if tc.value == "" {
				return y
			}
			return y + "\njobs:\n  idempotency_window: " + tc.value + "\n"
		})
		if (err != nil) != tc.wantErr {
			t.Errorf("idempotency_window=%q: err = %v, wantErr %v", tc.value, err, tc.wantErr)
			continue
		}
		if err == nil && cfg.IdempotencyWindow != tc.want {
			t.Errorf("idempotency_window=%q: loaded %s, want %s", tc.value, cfg.IdempotencyWindow, tc.want)
		}
	}
}

func TestLoad_MaxConnectionsBounds(t *testing.T) {
	for _, tc := range []struct {
		value   string
		want    int
		wantErr bool
	}{{"0", 0, false /* 0 = no cap */}, {"1", 1, false}, {"100000", 100000, false}, {"-1", 0, true}, {"100001", 0, true}} {
		cfg, err := loadWith(t, func(y, _ string) string {
			return strings.Replace(y, "    port: 8443\n", "    port: 8443\n    max_connections: "+tc.value+"\n", 1)
		})
		if (err != nil) != tc.wantErr {
			t.Errorf("max_connections=%s: err = %v, wantErr %v", tc.value, err, tc.wantErr)
			continue
		}
		if err == nil && cfg.MaxConnections != tc.want {
			t.Errorf("max_connections=%s: loaded %d", tc.value, cfg.MaxConnections)
		}
	}
}

func TestLoad_CRLFile(t *testing.T) {
	withCRL := func(path func(dir string) string) func(y, dir string) string {
		return func(y, dir string) string {
			return strings.Replace(y, "    key_file: ", "    crl_file: "+path(dir)+"\n    key_file: ", 1)
		}
	}

	t.Run("unset means no CRL", func(t *testing.T) {
		cfg, err := loadWith(t, func(y, _ string) string { return y })
		if err != nil || cfg.CRLFile != "" {
			t.Fatalf("err = %v, CRLFile = %q", err, cfg.CRLFile)
		}
	})
	t.Run("present file is accepted", func(t *testing.T) {
		var want string
		cfg, err := loadWith(t, withCRL(func(dir string) string {
			want = writeFixture(t, dir, "ca.crl", "placeholder", 0o640)
			return want
		}))
		if err != nil || cfg.CRLFile != want {
			t.Fatalf("err = %v, CRLFile = %q, want %q", err, cfg.CRLFile, want)
		}
	})
	t.Run("missing file is refused", func(t *testing.T) {
		_, err := loadWith(t, withCRL(func(dir string) string { return dir + "/absent.crl" }))
		if err == nil || !strings.Contains(err.Error(), "crl_file") {
			t.Fatalf("err = %v, want one naming crl_file", err)
		}
	})
	t.Run("world-writable file is refused like the CA", func(t *testing.T) {
		_, err := loadWith(t, withCRL(func(dir string) string {
			return writeFixture(t, dir, "ca.crl", "placeholder", 0o666)
		}))
		if err == nil || !strings.Contains(err.Error(), "crl_file") {
			t.Fatalf("err = %v, want one naming crl_file", err)
		}
	})
}
