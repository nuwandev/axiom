//go:build unix

package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTriggerIdempotent_ReplayReturnsTheSameJobAndStartsNothing(t *testing.T) {
	m := newRateLimitedManager(t, 0)
	ctx := context.Background()

	first, replayed, err := m.TriggerIdempotent(ctx, "ok", "ci", "key-1", nil)
	if err != nil || replayed {
		t.Fatalf("first: id=%q replayed=%v err=%v", first, replayed, err)
	}
	again, replayed, err := m.TriggerIdempotent(ctx, "ok", "ci", "key-1", nil)
	if err != nil || !replayed || again != first {
		t.Fatalf("replay: id=%q replayed=%v err=%v, want id %q replayed", again, replayed, err, first)
	}
	if n := jobCount(m); n != 1 {
		t.Fatalf("jobs = %d, want 1: a replay must not start a job", n)
	}
}

func TestTriggerIdempotent_KeyIsScopedToIdentityAndAction(t *testing.T) {
	m := newRateLimitedManager(t, 0)
	ctx := context.Background()

	a, _, err := m.TriggerIdempotent(ctx, "ok", "ci-a", "shared-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, replayed, err := m.TriggerIdempotent(ctx, "ok", "ci-b", "shared-key", nil)
	if err != nil || replayed || b == a {
		t.Fatalf("another identity reusing the key: id=%q replayed=%v err=%v", b, replayed, err)
	}
}

func TestTriggerIdempotent_SameKeyWithDifferentParametersIsRefused(t *testing.T) {
	m := newRateLimitedManager(t, 0)
	m.cfg.Actions["ok"].Parameters = nil
	ctx := context.Background()

	if _, _, err := m.TriggerIdempotent(ctx, "ok", "ci", "k", nil); err != nil {
		t.Fatal(err)
	}
	_, _, err := m.TriggerIdempotent(ctx, "ok", "ci", "k", map[string]string{"tag": "v2"})
	if !errors.Is(err, ErrIdempotencyKeyReused) {
		t.Fatalf("err = %v, want ErrIdempotencyKeyReused", err)
	}
	if n := jobCount(m); n != 1 {
		t.Fatalf("jobs = %d, want 1", n)
	}
}

func TestTriggerIdempotent_AFailedAttemptIsNotRemembered(t *testing.T) {
	m := newRateLimitedManager(t, 0)
	ctx := context.Background()

	// "ok" declares no parameters, so this is refused before any job exists.
	if _, _, err := m.TriggerIdempotent(ctx, "ok", "ci", "k", map[string]string{"x": "y"}); !errors.Is(err, ErrUnknownParameter) {
		t.Fatalf("err = %v, want ErrUnknownParameter", err)
	}
	id, replayed, err := m.TriggerIdempotent(ctx, "ok", "ci", "k", nil)
	if err != nil || replayed || id == "" {
		t.Fatalf("retry after a refusal: id=%q replayed=%v err=%v", id, replayed, err)
	}
}

func TestTriggerIdempotent_KeyExpiresAfterTheWindow(t *testing.T) {
	m := newRateLimitedManager(t, 0)
	clock := time.Now()
	m.idem.now = func() time.Time { return clock }
	ctx := context.Background()

	first, _, err := m.TriggerIdempotent(ctx, "ok", "ci", "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(m.idem.window - time.Second)
	if id, replayed, _ := m.TriggerIdempotent(ctx, "ok", "ci", "k", nil); !replayed || id != first {
		t.Fatalf("inside the window: id=%q replayed=%v", id, replayed)
	}
	clock = clock.Add(2 * time.Second)
	id, replayed, err := m.TriggerIdempotent(ctx, "ok", "ci", "k", nil)
	if err != nil || replayed || id == first {
		t.Fatalf("after the window: id=%q replayed=%v err=%v, want a new job", id, replayed, err)
	}
}

func TestTriggerIdempotent_ReplayIsNotChargedToTheRateLimit(t *testing.T) {
	m := newRateLimitedManager(t, 1)
	ctx := context.Background()

	if _, _, err := m.TriggerIdempotent(ctx, "ok", "ci", "k", nil); err != nil {
		t.Fatal(err)
	}
	// The single token is spent; a replay must still succeed.
	if _, replayed, err := m.TriggerIdempotent(ctx, "ok", "ci", "k", nil); err != nil || !replayed {
		t.Fatalf("replay: replayed=%v err=%v", replayed, err)
	}
	// A new key is a new trigger and is limited.
	if _, _, err := m.TriggerIdempotent(ctx, "ok", "ci", "other", nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("new key: err = %v, want ErrRateLimited", err)
	}
}

func TestTriggerIdempotent_ConcurrentRequestsWithOneKeyStartOneJob(t *testing.T) {
	m := newRateLimitedManager(t, 0)
	const n = 25
	ids := make([]string, n)
	replays := make([]bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, replayed, err := m.TriggerIdempotent(context.Background(), "ok", "ci", "same", nil)
			if err != nil {
				t.Errorf("request %d: %v", i, err)
			}
			ids[i], replays[i] = id, replayed
		}(i)
	}
	wg.Wait()

	started := 0
	for i := range ids {
		if !replays[i] {
			started++
		}
		if ids[i] != ids[0] {
			t.Fatalf("request %d got job %q, request 0 got %q", i, ids[i], ids[0])
		}
	}
	if started != 1 || jobCount(m) != 1 {
		t.Fatalf("started = %d, jobs = %d, want exactly 1 of each", started, jobCount(m))
	}
}

func TestTriggerIdempotent_FullStoreRefusesNewKeysButKeepsOldOnes(t *testing.T) {
	m := newRateLimitedManager(t, 0)
	ctx := context.Background()
	now := time.Now()
	m.idem.now = func() time.Time { return now }

	first, _, err := m.TriggerIdempotent(ctx, "ok", "ci", "keep", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; len(m.idem.entries) < MaxIdempotencyKeys; i++ {
		m.idem.entries[idempotencyKey{identity: "fill", action: "ok", key: string(rune('a'+i%26)) + time.Duration(i).String()}] =
			idempotencyEntry{jobID: "x", expires: now.Add(time.Hour)}
	}

	if _, _, err := m.TriggerIdempotent(ctx, "ok", "ci", "new", nil); !errors.Is(err, ErrIdempotencyStoreFull) {
		t.Fatalf("new key: err = %v, want ErrIdempotencyStoreFull", err)
	}
	if id, replayed, _ := m.TriggerIdempotent(ctx, "ok", "ci", "keep", nil); !replayed || id != first {
		t.Fatalf("an existing key must still replay: id=%q replayed=%v", id, replayed)
	}

	// Once the held keys expire, room is made without refusing anyone.
	now = now.Add(2 * time.Hour)
	if _, _, err := m.TriggerIdempotent(ctx, "ok", "ci", "new", nil); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

func TestHashParameters_IgnoresOrderAndSeparatesNamesFromValues(t *testing.T) {
	a := hashParameters(map[string]string{"a": "1", "b": "2"})
	b := hashParameters(map[string]string{"b": "2", "a": "1"})
	if a != b {
		t.Error("hash depends on map order")
	}
	if hashParameters(map[string]string{"ab": "c"}) == hashParameters(map[string]string{"a": "bc"}) {
		t.Error("name/value boundary is ambiguous")
	}
	if hashParameters(nil) != hashParameters(map[string]string{}) {
		t.Error("nil and empty parameters must hash alike")
	}
}
