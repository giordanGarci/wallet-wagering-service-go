package config

import (
	"fmt"
	"time"
)

// SQS consumer defaults (CONSUMER_* variables).
const (
	DefaultConsumerName              = "wager-transactions"
	DefaultConsumerConcurrency       = 10
	DefaultConsumerWaitTime          = 20 * time.Second
	DefaultConsumerVisibilityTimeout = 30 * time.Second
	DefaultConsumerRetryBase         = 1 * time.Second
	DefaultConsumerRetryMax          = 2 * time.Minute
	MaxConsumerConcurrency           = 100
	// MaxConsumerWaitTime is the SQS long-polling limit.
	MaxConsumerWaitTime = 20 * time.Second
	// MaxConsumerVisibility is the SQS visibility timeout limit (12h).
	MaxConsumerVisibility = 12 * time.Hour
)

// Consumer holds the SQS consumer settings (role consumer).
type Consumer struct {
	// Name is the consumer_name of its Inbox Messages. Instances sharing a
	// queue must share it, so a redelivery to another instance is
	// recognized.
	Name string
	// Concurrency bounds the messages handled at once. Messages of one
	// MessageGroupId (one Wallet) are always handled one at a time, in
	// order.
	Concurrency int
	// WaitTime is the long-polling wait of ReceiveMessage (0 to 20s).
	WaitTime time.Duration
	// VisibilityTimeout is requested on every receive and renewed by a
	// heartbeat every VisibilityTimeout/3 while a message is handled. Whole
	// seconds, 1s to 12h.
	VisibilityTimeout time.Duration
	// RetryBase and RetryMax bound the exponential backoff (with jitter)
	// applied with ChangeMessageVisibility after a transient failure, by
	// ApproximateReceiveCount.
	RetryBase time.Duration
	RetryMax  time.Duration
}

// loadConsumer reads the CONSUMER_* variables, recording problems in errs.
func loadConsumer(get func(key, def string) string, errs *[]error) Consumer {
	c := Consumer{
		Name:              get("CONSUMER_NAME", DefaultConsumerName),
		Concurrency:       boundedInt(get, "CONSUMER_CONCURRENCY", DefaultConsumerConcurrency, MaxConsumerConcurrency, errs),
		WaitTime:          DefaultConsumerWaitTime,
		VisibilityTimeout: positiveDuration(get, "CONSUMER_VISIBILITY_TIMEOUT", DefaultConsumerVisibilityTimeout, errs),
		RetryBase:         positiveDuration(get, "CONSUMER_RETRY_BASE", DefaultConsumerRetryBase, errs),
		RetryMax:          positiveDuration(get, "CONSUMER_RETRY_MAX", DefaultConsumerRetryMax, errs),
	}
	if raw := get("CONSUMER_WAIT_TIME", ""); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 || d > MaxConsumerWaitTime || d%time.Second != 0 {
			*errs = append(*errs, fmt.Errorf("CONSUMER_WAIT_TIME: must be whole seconds from 0s to 20s, got %q", raw))
		} else {
			c.WaitTime = d
		}
	}
	if v := c.VisibilityTimeout; v%time.Second != 0 || v > MaxConsumerVisibility {
		*errs = append(*errs, fmt.Errorf("CONSUMER_VISIBILITY_TIMEOUT: must be whole seconds from 1s to 12h, got %s", v))
	}
	if len(c.Name) > 80 {
		*errs = append(*errs, fmt.Errorf("CONSUMER_NAME: at most 80 characters"))
	}
	if c.RetryBase > c.RetryMax {
		*errs = append(*errs, fmt.Errorf("CONSUMER_RETRY_BASE: must not exceed CONSUMER_RETRY_MAX (%s > %s)", c.RetryBase, c.RetryMax))
	}
	if c.RetryMax > MaxConsumerVisibility {
		*errs = append(*errs, fmt.Errorf("CONSUMER_RETRY_MAX: must not exceed 12h, got %s", c.RetryMax))
	}
	return c
}
