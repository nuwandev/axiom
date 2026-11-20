package jobs

import (
	"errors"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestRateLimiter_AllowsABurstThenDenies(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := newRateLimiter(3, clk.now)
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("ci"); !ok {
			t.Fatalf("trigger %d was denied inside the burst", i+1)
		}
	}
	ok, wait := l.allow("ci")
	if ok {
		t.Fatal("the fourth trigger must be denied")
	}
	if want := 20 * time.Second; wait != want { // 3 per minute: one token every 20 s
		t.Errorf("retry after = %s, want %s", wait, want)
	}
}

func TestRateLimiter_RefillsOverTime(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := newRateLimiter(6, clk.now) // one token every 10 s
	for i := 0; i < 6; i++ {
		l.allow("ci")
	}
	if ok, _ := l.allow("ci"); ok {
		t.Fatal("bucket should be empty")
	}
	clk.advance(9 * time.Second)
	if ok, _ := l.allow("ci"); ok {
		t.Fatal("no token is due after 9 s")
	}
	clk.advance(1 * time.Second)
	if ok, _ := l.allow("ci"); !ok {
		t.Fatal("a token is due after 10 s")
	}
}

func TestRateLimiter_NeverExceedsTheBurst(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := newRateLimiter(2, clk.now)
	clk.advance(24 * time.Hour) // a long idle period must not bank extra tokens
	allowed := 0
	for i := 0; i < 10; i++ {
		if ok, _ := l.allow("ci"); ok {
			allowed++
		}
	}
	if allowed != 2 {
		t.Errorf("allowed %d triggers after a long idle period, want the burst of 2", allowed)
	}
}

func TestRateLimiter_IdentitiesAreIndependent(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := newRateLimiter(1, clk.now)
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("first trigger for identity a")
	}
	if ok, _ := l.allow("a"); ok {
		t.Fatal("identity a is over its limit")
	}
	if ok, _ := l.allow("b"); !ok {
		t.Fatal("identity b must have its own allowance")
	}
}

func TestRateLimitError_MatchesTheSentinel(t *testing.T) {
	err := error(&RateLimitError{RetryAfter: time.Second})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatal("errors.Is(err, ErrRateLimited) = false")
	}
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != time.Second {
		t.Fatalf("errors.As lost the retry-after: %+v", rl)
	}
}
