package jobs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/nuwandev/axiom/internal/config"
)

// MaxIdempotencyKeys bounds how many keys are remembered at once. Each entry
// holds a hash and a job ID, never the job itself, so the store costs about a
// couple of megabytes at the bound and cannot pin captured job output.
const MaxIdempotencyKeys = 10000

var (
	// ErrIdempotencyKeyReused means the key was already used for this action
	// with different parameters. That is a client bug, and returning the old
	// job would hide it, so the request is refused.
	ErrIdempotencyKeyReused = fmt.Errorf("idempotency key was already used with different parameters")
	// ErrIdempotencyStoreFull means MaxIdempotencyKeys unexpired keys are held.
	// New keys are refused instead of evicting an old one, because evicting
	// would silently drop the guarantee for a key still inside its window.
	ErrIdempotencyStoreFull = fmt.Errorf("too many outstanding idempotency keys; try again later")
)

type idempotencyKey struct {
	identity, action, key string
}

type idempotencyEntry struct {
	paramsHash [sha256.Size]byte
	jobID      string
	expires    time.Time
}

// idempotencyStore remembers which job answered a key, so a retried trigger
// returns that job instead of starting another run. It is in memory only: a
// restart forgets every key, like the job history itself.
type idempotencyStore struct {
	// mu is held across the whole check-trigger-record sequence, so two
	// concurrent requests with the same key cannot both start a job. Only
	// requests that carry a key take it.
	mu      sync.Mutex
	window  time.Duration
	now     func() time.Time
	entries map[idempotencyKey]idempotencyEntry
}

func newIdempotencyStore(window time.Duration, now func() time.Time) *idempotencyStore {
	return &idempotencyStore{window: window, now: now, entries: make(map[idempotencyKey]idempotencyEntry)}
}

func (s *idempotencyStore) purgeExpired(now time.Time) {
	for k, e := range s.entries {
		if !now.Before(e.expires) {
			delete(s.entries, k)
		}
	}
}

// hashParameters is order-independent and unambiguous: names and values are
// NUL-separated, and NUL cannot appear in a validated parameter.
func hashParameters(params map[string]string) [sha256.Size]byte {
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write([]byte(params[name]))
		h.Write([]byte{0})
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// TriggerIdempotent is Trigger for a request that carries an idempotency key.
// Within the configured window, the same identity, action and key returns the
// job that the first request started, with replayed set, and starts nothing.
// A replay is not charged to the rate limit or the job cap, since it does no
// work. Only a request that actually started a job is remembered: a refusal
// (busy, rate limited, invalid parameter, audit failure) can be retried with
// the same key.
func (m *Manager) TriggerIdempotent(ctx context.Context, actionName, identity, key string, params map[string]string) (jobID string, replayed bool, err error) {
	s := m.idem
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	k := idempotencyKey{identity: identity, action: actionName, key: key}
	hash := hashParameters(params)

	if e, ok := s.entries[k]; ok {
		if now.Before(e.expires) {
			if e.paramsHash != hash {
				return "", false, ErrIdempotencyKeyReused
			}
			return e.jobID, true, nil
		}
		delete(s.entries, k)
	}
	if len(s.entries) >= MaxIdempotencyKeys {
		s.purgeExpired(now)
		if len(s.entries) >= MaxIdempotencyKeys {
			return "", false, ErrIdempotencyStoreFull
		}
	}

	job, err := m.Trigger(ctx, actionName, identity, params)
	if err != nil {
		return "", false, err
	}
	s.entries[k] = idempotencyEntry{paramsHash: hash, jobID: job.ID, expires: now.Add(s.window)}
	return job.ID, false, nil
}

func idempotencyWindow(cfg *config.Config) time.Duration {
	if cfg.IdempotencyWindow == 0 {
		return config.DefaultIdempotencyWindow
	}
	return cfg.IdempotencyWindow
}
