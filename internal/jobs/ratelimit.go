package jobs

import (
	"math"
	"sync"
	"time"
)

// RateLimitError is returned by Manager.Trigger when an identity has used up
// its trigger allowance. RetryAfter is when the next trigger would be allowed.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return "too many trigger requests from this identity; try again later"
}

// Is lets errors.Is(err, ErrRateLimited) match any *RateLimitError.
func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// ErrRateLimited is the sentinel for a per-identity trigger rate limit.
var ErrRateLimited = &RateLimitError{}

// rateLimiter is a per-identity token bucket: each identity may trigger up to
// perMinute actions in a burst and regains one trigger every 60/perMinute
// seconds. Buckets exist only for identities that reached Trigger, which the
// API admits only for identities listed in the configuration, so the map is
// bounded by the number of configured identities.
type rateLimiter struct {
	mu      sync.Mutex
	burst   float64
	refill  float64 // tokens per second
	now     func() time.Time
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perMinute int, now func() time.Time) *rateLimiter {
	return &rateLimiter{
		burst:   float64(perMinute),
		refill:  float64(perMinute) / 60,
		now:     now,
		buckets: make(map[string]*bucket),
	}
}

// allow spends one token for identity. When none is available it returns false
// and how long until one is.
func (l *rateLimiter) allow(identity string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[identity]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[identity] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+elapsed*l.refill)
	}
	b.last = now

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration(math.Ceil((1-b.tokens)/l.refill*1e3)) * time.Millisecond
	return false, wait
}
