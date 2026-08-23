package jobs

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"regexp"
	"sync"
	"time"
)

// crockfordEncoding is Crockford's base32 alphabet, used for compact,
// lexicographically-sortable job identifiers (ULID-style: a 48-bit
// millisecond timestamp followed by 80 bits of randomness) without pulling
// in a third-party dependency for something this small.
var crockfordEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// idPattern matches the exact shape NewID produces: 26 Crockford base32
// characters. Handlers use it to reject a malformed job_id with a cheap,
// clean check before doing anything else with client-supplied input.
var idPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// ValidID reports whether s has the shape of an ID produced by NewID.
func ValidID(s string) bool {
	return idPattern.MatchString(s)
}

var (
	idMu        sync.Mutex
	lastMillis  int64
	lastEntropy [10]byte
)

// NewID returns a new lexicographically-sortable job identifier: a 48-bit
// millisecond timestamp followed by 80 bits of randomness, Crockford
// base32-encoded (26 characters), ULID-compatible in shape.
func NewID() string {
	idMu.Lock()
	defer idMu.Unlock()

	now := time.Now().UnixMilli()
	var entropy [10]byte
	if now == lastMillis {
		// Monotonic within the same millisecond: increment the previous
		// entropy so IDs generated in a tight loop still sort in order.
		entropy = lastEntropy
		incrementEntropy(&entropy)
	} else {
		if _, err := rand.Read(entropy[:]); err != nil {
			panic(fmt.Sprintf("jobs: reading random entropy: %v", err))
		}
	}
	lastMillis = now
	lastEntropy = entropy

	var buf [16]byte
	buf[0] = byte(now >> 40)
	buf[1] = byte(now >> 32)
	buf[2] = byte(now >> 24)
	buf[3] = byte(now >> 16)
	buf[4] = byte(now >> 8)
	buf[5] = byte(now)
	copy(buf[6:], entropy[:])

	return crockfordEncoding.EncodeToString(buf[:])
}

func incrementEntropy(e *[10]byte) {
	for i := len(e) - 1; i >= 0; i-- {
		e[i]++
		if e[i] != 0 {
			return
		}
	}
}
