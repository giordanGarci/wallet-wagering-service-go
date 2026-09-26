package worker

import (
	"math/rand/v2"
	"time"
)

// Backoff is exponential backoff with jitter and a cap: the attempt-th
// retry waits a random duration in [d/2, d], where d = min(Base*2^(attempt-1), Max).
// The lower bound keeps retries apart; the jitter spreads out retries of
// events that failed together (e.g. during an SQS outage).
type Backoff struct {
	Base time.Duration
	Max  time.Duration
}

// Delay returns the wait before retry number attempt (1 for the first
// retry). Values below 1 are treated as 1.
func (b Backoff) Delay(attempt int) time.Duration {
	d := b.Ceiling(attempt)
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + time.Duration(rand.Int64N(int64(d-half)+1))
}

// Ceiling is the un-jittered delay for attempt: min(Base*2^(attempt-1), Max).
func (b Backoff) Ceiling(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := b.Base
	for i := 1; i < attempt && d < b.Max; i++ {
		d *= 2
	}
	return min(d, b.Max)
}
