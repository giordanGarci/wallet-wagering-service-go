package wager

import (
	"fmt"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// Defaults of PendingPolicy. With them a Pending Reference is retried 8
// times, 2s, 4s, 8s, ... capped at 2m, and expires about 6 minutes after it
// was parked.
const (
	DefaultPendingMaxAttempts = 8
	DefaultPendingBaseDelay   = 2 * time.Second
	DefaultPendingMaxDelay    = 2 * time.Minute
)

// PendingPolicy is the retry schedule of Pending References: exponential
// backoff without jitter (the lease-based claim already spreads work across
// instances), capped at MaxDelay, and expiry after MaxAttempts retries.
type PendingPolicy struct {
	MaxAttempts int           // PENDING_MAX_ATTEMPTS; must be >= 1
	BaseDelay   time.Duration // delay before the first retry; must be > 0
	MaxDelay    time.Duration // cap; must be >= BaseDelay
}

// DefaultPendingPolicy returns the documented defaults.
func DefaultPendingPolicy() PendingPolicy {
	return PendingPolicy{
		MaxAttempts: DefaultPendingMaxAttempts,
		BaseDelay:   DefaultPendingBaseDelay,
		MaxDelay:    DefaultPendingMaxDelay,
	}
}

// Validate checks the policy, for configuration validation at startup.
func (p PendingPolicy) Validate() error {
	if p.MaxAttempts < 1 || p.BaseDelay <= 0 || p.MaxDelay < p.BaseDelay {
		return fmt.Errorf("wager: invalid pending policy %+v", p)
	}
	return nil
}

// Delay returns the wait after `attempts` failed retries:
// BaseDelay * 2^attempts, capped at MaxDelay (overflow-safe).
func (p PendingPolicy) Delay(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	d := p.BaseDelay
	for i := 0; i < attempts; i++ {
		if d >= p.MaxDelay || d > p.MaxDelay/2 {
			return p.MaxDelay
		}
		d *= 2
	}
	return min(d, p.MaxDelay)
}

// NextAttemptAt returns when the next retry is due for a Transaction that has
// already failed `attempts` retries: parked Transactions (0 attempts) are
// first retried at now + BaseDelay.
func (p PendingPolicy) NextAttemptAt(attempts int, now time.Time) time.Time {
	return normalizeTime(now.Add(p.Delay(attempts)))
}

// Exhausted reports whether `attempts` failed retries reach the limit.
func (p PendingPolicy) Exhausted(attempts int) bool { return attempts >= p.MaxAttempts }

// RecordReferenceMiss registers a retry of a PENDING_REFERENCE Transaction
// whose Reference is still unusable (absent or itself PENDING_REFERENCE; Apply
// left the status unchanged). It increments Attempts; when the policy is
// exhausted it rejects the Transaction with REFERENCE_NOT_FOUND and returns
// exhausted = true (emit WagerTransactionRejected); otherwise it returns the
// time of the next retry.
func (t *Transaction) RecordReferenceMiss(p PendingPolicy, now time.Time) (next time.Time, exhausted bool, err error) {
	if err := p.Validate(); err != nil {
		return time.Time{}, false, err
	}
	if t.s.Status != StatusPendingReference {
		return time.Time{}, false, fmt.Errorf("%w: reference miss recorded on %s transaction %s", ErrInvalidTransition, t.s.Status, t.s.ID)
	}
	attempts := t.s.Attempts + 1
	if p.Exhausted(attempts) {
		if err := t.Reject(FailureReferenceNotFound, money.Money{}, now); err != nil {
			return time.Time{}, false, err
		}
		t.s.Attempts = attempts
		return time.Time{}, true, nil
	}
	t.s.Attempts = attempts
	t.s.UpdatedAt = normalizeTime(now)
	return p.NextAttemptAt(attempts, now), false, nil
}
