package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nuwandev/axiom/internal/config"
)

// syncBuffer is a log sink that the serve goroutine writes and the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A reload whose configuration cannot be loaded must be reported and must not
// disturb the running agent.
func TestServeWithReload_FailedReloadIsLoggedAndAgentKeepsServing(t *testing.T) {
	cfg := testConfig(t)
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))

	reload := make(chan struct{}, 1)
	loadErr := errors.New("config file is broken")
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- serveWithReload(ctx, cfg, logger, reload, func() (*config.Config, error) { return nil, loadErr })
	}()

	hc := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   2 * time.Second,
	}
	url := "https://" + net.JoinHostPort(cfg.ListenAddress, strconv.Itoa(cfg.ListenPort)) + "/health"
	health := func() bool {
		resp, err := hc.Get(url)
		if err != nil {
			return false
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}

	waitFor(t, "agent to start serving", health)

	reload <- struct{}{}
	waitFor(t, "failed reload to be logged", func() bool {
		return strings.Contains(logs.String(), "configuration reload failed") &&
			strings.Contains(logs.String(), loadErr.Error())
	})

	if !health() {
		t.Fatal("agent stopped answering after a failed reload")
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("serve returned error on clean shutdown: %v", err)
		}
	case <-time.After(35 * time.Second):
		t.Fatal("serve did not return after context cancellation")
	}
}
