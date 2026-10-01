//go:build windows

package jobs

import (
	"strings"
	"testing"
)

func TestBaseEnv_Windows(t *testing.T) {
	env := baseEnv()

	has := func(prefix string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}

	for _, required := range []string{"SystemRoot=", "PATHEXT="} {
		if !has(required) {
			t.Errorf("baseEnv() missing %q; got %v", required, env)
		}
	}
	if !has("Path=") && !has("PATH=") {
		t.Errorf("baseEnv() has no Path entry; got %v", env)
	}

	for _, e := range env {
		if strings.HasPrefix(e, "AXIOM_") {
			t.Errorf("baseEnv() must not contain AXIOM_ vars, found %q", e)
		}
	}
}

func TestBuildEnv_Windows_ParametersAndIdentifiers(t *testing.T) {
	env := buildEnv("01JOBID", "sample.action", map[string]string{
		"image_tag": "uat-20260101-abc",
	})

	want := map[string]bool{
		"AXIOM_JOB_ID=01JOBID":                   false,
		"AXIOM_ACTION=sample.action":             false,
		"AXIOM_PARAM_IMAGE_TAG=uat-20260101-abc": false,
	}
	for _, e := range env {
		if _, ok := want[e]; ok {
			want[e] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("buildEnv output missing %q; got %v", k, env)
		}
	}
}
