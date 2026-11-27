package api

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRejectionLimiter_OffByDefault(t *testing.T) {
	if newRejectionLimiter(0) != nil {
		t.Fatal("a 0 cap must disable the limiter")
	}
}

func TestRejectionLimiter_CapsPerKeyAndSummarisesTheWindow(t *testing.T) {
	l := newRejectionLimiter(3)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var written int
	for i := 0; i < 10; i++ {
		ok, ended := l.admit("alice", t0.Add(time.Duration(i)*time.Second))
		if len(ended) != 0 {
			t.Fatalf("summary emitted inside the window: %+v", ended)
		}
		if ok {
			written++
		}
	}
	if written != 3 {
		t.Fatalf("wrote %d records in one window, want 3", written)
	}

	// Another key has its own count.
	if ok, _ := l.admit("bob", t0.Add(5*time.Second)); !ok {
		t.Fatal("a second identity was capped by the first one's count")
	}

	// The next rejection after the window ends opens a new window and hands
	// back the summary of the old one.
	ok, ended := l.admit("alice", t0.Add(rejectionWindow))
	if !ok {
		t.Fatal("first rejection of a new window was not written")
	}
	if len(ended) != 1 || ended[0].key != "alice" || ended[0].suppressed != 7 {
		t.Fatalf("ended = %+v, want alice with 7 suppressed", ended)
	}
	rec := ended[0].record(3)
	if rec.Identity != "alice" || !strings.Contains(rec.Reason, "7 further rejected requests") {
		t.Fatalf("summary record = %+v", rec)
	}
}

// A flood that stops still gets its count recorded, by the sweep.
func TestRejectionLimiter_SweepSummarisesEndedWindows(t *testing.T) {
	l := newRejectionLimiter(1)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		l.admit("", t0)
	}
	l.admit("quiet", t0) // written, nothing suppressed: no summary

	if got := l.sweep(t0.Add(30*time.Second), false); len(got) != 0 {
		t.Fatalf("sweep summarised an open window: %+v", got)
	}
	got := l.sweep(t0.Add(rejectionWindow), false)
	if len(got) != 1 || got[0].key != "" || got[0].suppressed != 3 {
		t.Fatalf("sweep = %+v, want the unauthenticated key with 3 suppressed", got)
	}
	if !strings.Contains(got[0].record(1).Reason, "unauthenticated peers") {
		t.Fatalf("unauthenticated summary does not say so: %q", got[0].record(1).Reason)
	}
	if len(l.counts) != 0 {
		t.Fatalf("ended windows were not removed: %d left", len(l.counts))
	}
}

// On shutdown every open window with suppressed records is summarised, with
// the window cut short at the shutdown time.
func TestRejectionLimiter_FlushAllOnShutdown(t *testing.T) {
	l := newRejectionLimiter(1)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.admit("alice", t0)
	l.admit("alice", t0)
	stop := t0.Add(10 * time.Second)
	got := l.sweep(stop, true)
	if len(got) != 1 || got[0].suppressed != 1 || !got[0].end.Equal(stop) {
		t.Fatalf("flush = %+v, want one summary ending at %s", got, stop)
	}
}

// Memory stays bounded: past maxRejectionKeys, new identities share one key.
func TestRejectionLimiter_BoundsTrackedIdentities(t *testing.T) {
	l := newRejectionLimiter(1)
	now := time.Now()
	for i := 0; i < maxRejectionKeys; i++ {
		l.admit(fmt.Sprintf("id-%d", i), now)
	}
	if ok, _ := l.admit("new-1", now); !ok {
		t.Fatal("first identity past the bound should use the overflow key's fresh count")
	}
	if ok, _ := l.admit("new-2", now); ok {
		t.Fatal("second identity past the bound must share the overflow count")
	}
	if len(l.counts) != maxRejectionKeys+1 {
		t.Fatalf("tracked %d keys, want %d plus the overflow key", len(l.counts), maxRejectionKeys)
	}
	got := l.sweep(now, true)
	var overflow bool
	for _, s := range got {
		if s.key == overflowRejectionKey {
			overflow = true
			if rec := s.record(1); rec.Identity != "" || !strings.Contains(rec.Reason, "past the tracked-identity limit") {
				t.Fatalf("overflow summary = %+v", rec)
			}
		}
	}
	if !overflow {
		t.Fatal("overflow key was not summarised")
	}
}
