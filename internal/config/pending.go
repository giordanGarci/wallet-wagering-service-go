package config

import (
	"fmt"
	"strconv"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// Pending Reference worker defaults (PENDING_* variables). The retry
// schedule defaults are the domain's (wager.DefaultPendingPolicy).
const (
	DefaultPendingPollInterval = time.Second
	DefaultPendingBatchSize    = 20
	DefaultPendingLease        = 30 * time.Second
	MaxPendingBatchSize        = 1000
	MaxPendingMaxAttempts      = 1000
)

// Pending holds the Pending Reference settings. The retry schedule
// (MaxAttempts, RetryBase, RetryMax) is used by every instance: the api and
// consumer roles schedule the first retry when they park an operation; the
// rest only by the pending role.
type Pending struct {
	// PollInterval is the pause between claims when the last one found no
	// due Pending Reference.
	PollInterval time.Duration
	// BatchSize is the maximum number of Pending References claimed at once.
	BatchSize int
	// Lease is how long a claim keeps other workers away from its rows; an
	// instance that dies holding it is replaced once it expires.
	Lease time.Duration
	// MaxAttempts is PENDING_MAX_ATTEMPTS: retries that still find no
	// usable Reference before REJECTED REFERENCE_NOT_FOUND.
	MaxAttempts int
	// RetryBase and RetryMax bound the exponential backoff between retries.
	RetryBase time.Duration
	RetryMax  time.Duration
}

// Policy is the domain retry policy built from the settings.
func (p Pending) Policy() wager.PendingPolicy {
	return wager.PendingPolicy{MaxAttempts: p.MaxAttempts, BaseDelay: p.RetryBase, MaxDelay: p.RetryMax}
}

// loadPending reads the PENDING_* variables, recording problems in errs.
func loadPending(get func(key, def string) string, errs *[]error) Pending {
	p := Pending{
		PollInterval: positiveDuration(get, "PENDING_POLL_INTERVAL", DefaultPendingPollInterval, errs),
		BatchSize:    boundedInt(get, "PENDING_BATCH_SIZE", DefaultPendingBatchSize, MaxPendingBatchSize, errs),
		Lease:        positiveDuration(get, "PENDING_LEASE_DURATION", DefaultPendingLease, errs),
		MaxAttempts:  boundedInt(get, "PENDING_MAX_ATTEMPTS", wager.DefaultPendingMaxAttempts, MaxPendingMaxAttempts, errs),
		RetryBase:    positiveDuration(get, "PENDING_RETRY_BASE", wager.DefaultPendingBaseDelay, errs),
		RetryMax:     positiveDuration(get, "PENDING_RETRY_MAX", wager.DefaultPendingMaxDelay, errs),
	}
	if p.RetryBase > p.RetryMax {
		*errs = append(*errs, fmt.Errorf("PENDING_RETRY_BASE: must not exceed PENDING_RETRY_MAX (%s > %s)", p.RetryBase, p.RetryMax))
	}
	return p
}

// boundedInt parses key as an integer from 1 to maxValue, returning def when
// it is unset and recording a problem in errs when it is invalid.
func boundedInt(get func(key, def string) string, key string, def, maxValue int, errs *[]error) int {
	raw := get(key, "")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxValue {
		*errs = append(*errs, fmt.Errorf("%s: must be an integer from 1 to %d, got %q", key, maxValue, raw))
		return def
	}
	return n
}
