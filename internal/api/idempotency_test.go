//go:build unix

package api

import (
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type triggerResult struct {
	status int
	jobID  string
	body   string
	replay string
}

// trigger posts to /v1/actions/{action} with optional Idempotency-Key headers
// and a JSON parameters body.
func (e *testEnv) trigger(client *http.Client, action, body string, keys ...string) triggerResult {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.server.URL+"/v1/actions/"+action, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, k := range keys {
		req.Header.Add("Idempotency-Key", k)
	}
	resp, err := client.Do(req)
	if err != nil {
		e.t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var parsed struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(raw, &parsed)
	return triggerResult{status: resp.StatusCode, jobID: parsed.JobID, body: string(raw), replay: resp.Header.Get("Idempotent-Replay")}
}

func idempotencyEnv(t *testing.T) (*testEnv, *http.Client) {
	t.Helper()
	env := newTestEnv(t, &testEnvOptions{paramAction: true})
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 40)
	return env, env.clientFor(certPEM, keyPEM)
}

func TestIdempotency_RetryReturnsTheSameJobAndMarksTheReplay(t *testing.T) {
	env, client := idempotencyEnv(t)

	first := env.trigger(client, "sample.action", "", "deploy-42")
	if first.status != http.StatusAccepted || first.jobID == "" || first.replay != "" {
		t.Fatalf("first = %+v", first)
	}
	again := env.trigger(client, "sample.action", "", "deploy-42")
	if again.status != http.StatusAccepted || again.jobID != first.jobID || again.replay != "true" {
		t.Fatalf("retry = %+v, want 202 with job %s and Idempotent-Replay: true", again, first.jobID)
	}
	// The replay reports the job as it is now, not a stale "queued".
	if !strings.Contains(again.body, `"status"`) {
		t.Fatalf("replay body has no status: %s", again.body)
	}
}

func TestIdempotency_WithoutAKeyEveryRequestStartsAJob(t *testing.T) {
	env, client := idempotencyEnv(t)
	a := env.trigger(client, "sample.action", "")
	b := env.trigger(client, "sample.action", "")
	if a.jobID == "" || a.jobID == b.jobID {
		t.Fatalf("a = %+v, b = %+v: requests without a key must stay independent", a, b)
	}
}

func TestIdempotency_SameKeyWithDifferentParametersIs422(t *testing.T) {
	env, client := idempotencyEnv(t)

	if r := env.trigger(client, "param.action", `{"parameters":{"tag":"v1"}}`, "k"); r.status != http.StatusAccepted {
		t.Fatalf("first = %+v", r)
	}
	if r := env.trigger(client, "param.action", `{"parameters":{"tag":"v1"}}`, "k"); r.status != http.StatusAccepted || r.replay != "true" {
		t.Fatalf("identical retry = %+v", r)
	}
	if r := env.trigger(client, "param.action", `{"parameters":{"tag":"v2"}}`, "k"); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("changed parameters = %+v, want 422", r)
	}
}

func TestIdempotency_MalformedOrRepeatedKeyIs400(t *testing.T) {
	env, client := idempotencyEnv(t)
	for name, keys := range map[string][]string{
		"space":    {"has space"},
		"slash":    {"a/b"},
		"too long": {strings.Repeat("a", 129)},
		"repeated": {"one", "two"},
	} {
		if r := env.trigger(client, "sample.action", "", keys...); r.status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", name, r.status, r.body)
		}
	}
	// A refused key must not have started a job or burned the key.
	if r := env.trigger(client, "sample.action", "", "fine"); r.status != http.StatusAccepted || r.replay != "" {
		t.Fatalf("valid key after refusals = %+v", r)
	}
}

func TestIdempotency_KeyOnlyReplaysForTheIdentityThatUsedIt(t *testing.T) {
	env, jenkins := idempotencyEnv(t)
	otherCert, otherKey := env.ca.issue(t, "ci-other", x509.ExtKeyUsageClientAuth, 41)
	other := env.clientFor(otherCert, otherKey)

	first := env.trigger(jenkins, "sample.action", "", "shared")
	// ci-other is not allowlisted, so it is refused by authorization before
	// the key is ever consulted: it learns nothing about ci-jenkins's keys.
	if r := env.trigger(other, "sample.action", "", "shared"); r.status != http.StatusForbidden || r.jobID == first.jobID {
		t.Fatalf("other identity = %+v, want 403 and no job id", r)
	}
}
