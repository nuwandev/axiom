package config

import (
	"strings"
	"testing"
)

func TestParameterEnvVar(t *testing.T) {
	for name, want := range map[string]string{
		"image_tag": "AXIOM_PARAM_IMAGE_TAG",
		"Tag":       "AXIOM_PARAM_TAG",
		"_x":        "AXIOM_PARAM__X",
	} {
		if got := ParameterEnvVar(name); got != want {
			t.Errorf("ParameterEnvVar(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestCheckParameterEnvNames_RejectsCaseCollision(t *testing.T) {
	err := checkParameterEnvNames(map[string]Parameter{"tag": {}, "Tag": {}, "other": {}})
	if err == nil {
		t.Fatal("expected a collision error for \"Tag\" and \"tag\"")
	}
	for _, want := range []string{`"Tag"`, `"tag"`, "AXIOM_PARAM_TAG"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestCheckParameterEnvNames_ErrorIsDeterministic(t *testing.T) {
	params := map[string]Parameter{"aB": {}, "Ab": {}, "ab": {}, "AB": {}}
	first := checkParameterEnvNames(params).Error()
	for i := 0; i < 50; i++ {
		if got := checkParameterEnvNames(params).Error(); got != first {
			t.Fatalf("error changed between calls: %q vs %q", first, got)
		}
	}
}

func TestCheckParameterEnvNames_AcceptsDistinctNames(t *testing.T) {
	if err := checkParameterEnvNames(map[string]Parameter{"image_tag": {}, "environment": {}, "Region2": {}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := checkParameterEnvNames(nil); err != nil {
		t.Fatalf("nil map: %v", err)
	}
}
