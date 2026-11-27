//go:build unix

package api

import (
	"crypto/x509"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func (e *testEnv) triggerJob(client *http.Client, action string) string {
	e.t.Helper()
	resp, err := client.Post(e.server.URL+"/v1/actions/"+action, "application/json", nil)
	if err != nil {
		e.t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		e.t.Fatalf("trigger %s: status %d, want 202", action, resp.StatusCode)
	}
	var body struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		e.t.Fatal(err)
	}
	return body.JobID
}

// After a reload removes an identity, its certificate can no longer read the
// jobs it started: their status and output may hold what the script printed.
func TestJobRead_RemovedIdentityCannotReadItsJobs(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 80)
	client := env.clientFor(certPEM, keyPEM)
	id := env.triggerJob(client, "sample.action")

	if got, _ := env.get(client, "/v1/jobs/"+id); got != http.StatusOK {
		t.Fatalf("before reload: status = %d, want 200", got)
	}

	if err := env.srv.Reload(env.editConfig("    ci-jenkins:\n", "    other:\n")); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	for _, path := range []string{"/v1/jobs/" + id, "/v1/jobs/" + id + "/logs"} {
		if got, _ := env.get(client, path); got != http.StatusForbidden {
			t.Fatalf("after reload: GET %s = %d, want 403", path, got)
		}
	}
	if !strings.Contains(env.auditLines(), "job read for an action no longer allowlisted") {
		t.Fatal("refused job read was not audited")
	}
}

// Losing one action hides that action's jobs but not the others.
func TestJobRead_LostActionHidesOnlyThatActionsJobs(t *testing.T) {
	env := newTestEnv(t, &testEnvOptions{paramAction: true})
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 81)
	client := env.clientFor(certPEM, keyPEM)
	sampleJob := env.triggerJob(client, "sample.action")

	next := env.editConfig("        - sample.action\n        - param.action\n", "        - param.action\n")
	if err := env.srv.Reload(next); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got, _ := env.get(client, "/v1/jobs/"+sampleJob); got != http.StatusForbidden {
		t.Fatalf("job of the removed action: status = %d, want 403", got)
	}
	// The ownership check still comes first: another identity's job, or an
	// unknown one, is a 404 as before, so IDs still cannot be probed.
	if got, _ := env.get(client, "/v1/jobs/01BBBBBBBBBBBBBBBBBBBBBBBB"); got != http.StatusNotFound {
		t.Fatalf("unknown job: status = %d, want 404", got)
	}
}

// With audit.max_rejections_per_minute set, a flood of rejected requests is
// still refused, but only the cap is written one by one; the rest are counted
// in a summary record.
func TestRejections_CapBoundsAuditRecordsAndSummarisesTheRest(t *testing.T) {
	env := newTestEnv(t, &testEnvOptions{maxRejectionsPerMinute: 2})
	certPEM, keyPEM := env.ca.issue(t, "intruder", x509.ExtKeyUsageClientAuth, 82)
	client := env.clientFor(certPEM, keyPEM)

	for i := 0; i < 5; i++ {
		if got := env.triggerStatus(client); got != http.StatusForbidden {
			t.Fatalf("request %d: status = %d, want 403", i, got)
		}
	}
	if n := strings.Count(env.auditLines(), "action not allowlisted"); n != 2 {
		t.Fatalf("%d individual rejection records, want 2", n)
	}

	env.srv.FlushRejectionSummaries()
	audit := env.auditLines()
	if !strings.Contains(audit, "3 further rejected requests from identity") || !strings.Contains(audit, `"identity":"intruder"`) {
		t.Fatalf("no summary record for the suppressed rejections:\n%s", audit)
	}
}

// Without the setting every rejection is written, as before.
func TestRejections_UncappedByDefault(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "intruder", x509.ExtKeyUsageClientAuth, 83)
	client := env.clientFor(certPEM, keyPEM)
	for i := 0; i < 5; i++ {
		env.triggerStatus(client)
	}
	if n := strings.Count(env.auditLines(), "action not allowlisted"); n != 5 {
		t.Fatalf("%d rejection records, want 5", n)
	}
}
