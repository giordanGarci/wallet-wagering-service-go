package config

import (
	"fmt"
	"time"
)

// Outbox publisher defaults (OUTBOX_* variables).
const (
	DefaultOutboxPollInterval = 500 * time.Millisecond
	DefaultOutboxBatchSize    = 50
	DefaultOutboxLease        = 30 * time.Second
	DefaultOutboxRetryBase    = 1 * time.Second
	DefaultOutboxRetryMax     = 5 * time.Minute
	MaxOutboxBatchSize        = 1000
)

// Outbox holds the outbox publisher settings (role outbox).
type Outbox struct {
	// PollInterval is the pause between claims when the last one found
	// nothing to publish.
	PollInterval time.Duration
	// BatchSize is the maximum number of events claimed at once.
	BatchSize int
	// Lease is how long a claim keeps other publishers away from its events.
	// A publisher stops starting new sends once most of it has elapsed.
	Lease time.Duration
	// RetryBase and RetryMax bound the exponential backoff (with jitter)
	// scheduled after a failed publication.
	RetryBase time.Duration
	RetryMax  time.Duration
}

// loadOutbox reads the OUTBOX_* variables, recording problems in errs.
func loadOutbox(get func(key, def string) string, errs *[]error) Outbox {
	o := Outbox{
		PollInterval: positiveDuration(get, "OUTBOX_POLL_INTERVAL", DefaultOutboxPollInterval, errs),
		BatchSize:    boundedInt(get, "OUTBOX_BATCH_SIZE", DefaultOutboxBatchSize, MaxOutboxBatchSize, errs),
		Lease:        positiveDuration(get, "OUTBOX_LEASE_DURATION", DefaultOutboxLease, errs),
		RetryBase:    positiveDuration(get, "OUTBOX_RETRY_BASE", DefaultOutboxRetryBase, errs),
		RetryMax:     positiveDuration(get, "OUTBOX_RETRY_MAX", DefaultOutboxRetryMax, errs),
	}
	if o.RetryBase > o.RetryMax {
		*errs = append(*errs, fmt.Errorf("OUTBOX_RETRY_BASE: must not exceed OUTBOX_RETRY_MAX (%s > %s)", o.RetryBase, o.RetryMax))
	}
	return o
}

// positiveDuration parses key as a positive time.Duration, returning def when
// it is unset and recording a problem in errs when it is invalid.
func positiveDuration(get func(key, def string) string, key string, def time.Duration, errs *[]error) time.Duration {
	raw := get(key, "")
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	switch {
	case err != nil:
		*errs = append(*errs, fmt.Errorf("%s: %q is not a duration (e.g. 500ms, 30s)", key, raw))
	case d <= 0:
		*errs = append(*errs, fmt.Errorf("%s: must be positive, got %s", key, raw))
	default:
		return d
	}
	return def
}
