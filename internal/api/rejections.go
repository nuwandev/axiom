package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nuwandev/axiom/internal/audit"
)

const (
	// rejectionWindow is the period audit.max_rejections_per_minute counts
	// over. Each key's window starts at its first rejection.
	rejectionWindow = time.Minute
	// rejectionSweep is how often ended windows are summarised when no new
	// rejection arrives to trigger it.
	rejectionSweep = 10 * time.Second
	// maxRejectionKeys bounds the limiter's memory. Keys are identities from
	// CA-verified certificates, so this is only reached by a CA issuing many
	// distinct names; past it, new names share overflowRejectionKey.
	maxRejectionKeys = 10000
	// overflowRejectionKey cannot collide with a Common Name or with the empty
	// key used for unauthenticated peers.
	overflowRejectionKey = "\x00overflow"
)

type rejectionCount struct {
	start      time.Time
	written    int
	suppressed int
}

type rejectionSummary struct {
	key        string
	start, end time.Time
	suppressed int
}

// rejectionLimiter caps how many rejected-request audit records one key can
// cause per window. Every rejected request is still refused; past the cap it
// is counted instead of written, and the count is written as one summary
// record when the window ends, so the trail shows that it happened and how
// often. The key is the identity, or "" for every unauthenticated peer: an
// unauthenticated peer can claim any name, so peers cannot be told apart.
type rejectionLimiter struct {
	mu        sync.Mutex
	perWindow int
	counts    map[string]*rejectionCount
}

// newRejectionLimiter returns nil when perMinute is 0, which writes every
// rejection.
func newRejectionLimiter(perMinute int) *rejectionLimiter {
	if perMinute == 0 {
		return nil
	}
	return &rejectionLimiter{perWindow: perMinute, counts: make(map[string]*rejectionCount)}
}

// admit reports whether a rejection for key should be written, and returns
// the summary of key's previous window if it ended with suppressed records.
func (l *rejectionLimiter) admit(key string, now time.Time) (bool, []rejectionSummary) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, tracked := l.counts[key]; !tracked && len(l.counts) >= maxRejectionKeys {
		key = overflowRejectionKey
	}

	var ended []rejectionSummary
	c := l.counts[key]
	if c != nil && !now.Before(c.start.Add(rejectionWindow)) {
		if c.suppressed > 0 {
			ended = append(ended, summaryOf(key, c))
		}
		c = nil
	}
	if c == nil {
		c = &rejectionCount{start: now}
		l.counts[key] = c
	}
	if c.written < l.perWindow {
		c.written++
		return true, ended
	}
	c.suppressed++
	return false, ended
}

// sweep removes ended windows, or every window when all is set, and returns
// the summaries of those that suppressed records.
func (l *rejectionLimiter) sweep(now time.Time, all bool) []rejectionSummary {
	l.mu.Lock()
	defer l.mu.Unlock()

	var out []rejectionSummary
	for key, c := range l.counts {
		if !all && now.Before(c.start.Add(rejectionWindow)) {
			continue
		}
		if c.suppressed > 0 {
			s := summaryOf(key, c)
			if all && now.Before(s.end) {
				s.end = now
			}
			out = append(out, s)
		}
		delete(l.counts, key)
	}
	return out
}

func summaryOf(key string, c *rejectionCount) rejectionSummary {
	return rejectionSummary{key: key, start: c.start, end: c.start.Add(rejectionWindow), suppressed: c.suppressed}
}

func (s rejectionSummary) record(perWindow int) audit.Record {
	from := "identity"
	identity := s.key
	switch s.key {
	case "":
		from = "unauthenticated peers"
	case overflowRejectionKey:
		from = "identities past the tracked-identity limit"
		identity = ""
	}
	return audit.Record{
		Event:    audit.EventRejected,
		Identity: identity,
		Reason: fmt.Sprintf("audit.max_rejections_per_minute (%d) reached: %d further rejected requests from %s between %s and %s were refused and counted, not written one by one",
			perWindow, s.suppressed, from, s.start.UTC().Format(time.RFC3339), s.end.UTC().Format(time.RFC3339)),
	}
}

// writeRejection records a rejected request, subject to
// audit.max_rejections_per_minute. A failed write is logged; the request is
// refused either way.
func (s *Server) writeRejection(rec audit.Record) {
	if s.rejections == nil {
		s.writeAudit(rec)
		return
	}
	write, ended := s.rejections.admit(rec.Identity, time.Now())
	s.writeSummaries(ended)
	if write {
		s.writeAudit(rec)
	}
}

func (s *Server) writeAudit(rec audit.Record) {
	if err := s.audit.Write(rec); err != nil {
		s.logger.Error("audit write failed for rejected request", "error", err)
	}
}

func (s *Server) writeSummaries(summaries []rejectionSummary) {
	for _, sum := range summaries {
		s.writeAudit(sum.record(s.rejections.perWindow))
	}
}

// RunRejectionSummaries writes the summary of each ended window that
// suppressed records, until ctx ends. Without it, a flood that stops would
// leave its count unwritten until the same key was rejected again.
func (s *Server) RunRejectionSummaries(ctx context.Context) {
	if s.rejections == nil {
		return
	}
	t := time.NewTicker(rejectionSweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.writeSummaries(s.rejections.sweep(now, false))
		}
	}
}

// FlushRejectionSummaries writes the summary of every window still open. Call
// it once, after the HTTP server has stopped and before the audit log closes,
// so no count is lost on shutdown.
func (s *Server) FlushRejectionSummaries() {
	if s.rejections == nil {
		return
	}
	s.writeSummaries(s.rejections.sweep(time.Now(), true))
}
