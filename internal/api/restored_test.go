//go:build unix

package api

import (
	"crypto/x509"
	"io"
	"net/http"
	"strings"
	"testing"
)

const restoredJobID = "01AAAAAAAAAAAAAAAAAAAAAAAA"

func (e *testEnv) get(client *http.Client, path string) (int, string) {
	e.t.Helper()
	resp, err := client.Get(e.server.URL + path)
	if err != nil {
		e.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestRestoredJob_StatusIsAvailableAndLogsAreGone(t *testing.T) {
	env := newTestEnv(t, &testEnvOptions{restoredJobID: restoredJobID})
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 60)
	client := env.clientFor(certPEM, keyPEM)

	status, body := env.get(client, "/v1/jobs/"+restoredJobID)
	if status != http.StatusOK || !strings.Contains(body, `"status":"succeeded"`) {
		t.Fatalf("job status = %d %s, want 200 with status succeeded", status, body)
	}

	status, body = env.get(client, "/v1/jobs/"+restoredJobID+"/logs")
	if status != http.StatusGone || !strings.Contains(body, "not retained") {
		t.Fatalf("logs = %d %s, want 410 saying output is not retained", status, body)
	}
}

func TestRestoredJob_StillOnlyVisibleToTheIdentityThatRanIt(t *testing.T) {
	env := newTestEnv(t, &testEnvOptions{restoredJobID: restoredJobID})
	certPEM, keyPEM := env.ca.issue(t, "someone-else", x509.ExtKeyUsageClientAuth, 61)
	other := env.clientFor(certPEM, keyPEM)

	if status, _ := env.get(other, "/v1/jobs/"+restoredJobID); status != http.StatusNotFound && status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Fatalf("another identity got status %d for a restored job it does not own", status)
	}
}

func TestLiveJob_LogsStillWorkWhenStateIsEnabled(t *testing.T) {
	env := newTestEnv(t, &testEnvOptions{restoredJobID: restoredJobID})
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 62)
	client := env.clientFor(certPEM, keyPEM)

	started := env.trigger(client, "sample.action", "")
	if started.status != http.StatusAccepted {
		t.Fatalf("trigger = %+v", started)
	}
	// A job this process ran is not "restored": its logs answer normally.
	status, _ := env.get(client, "/v1/jobs/"+started.jobID+"/logs")
	if status == http.StatusGone {
		t.Fatal("logs of a job run by this process must not be reported as gone")
	}
}
