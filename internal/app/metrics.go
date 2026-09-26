package app

import (
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// Duplicate outcomes counted by WagerMetrics.Duplicate.
const (
	DuplicateReplay           = "replay"
	DuplicateKeyConflict      = "idempotency_key_conflict"
	DuplicateExternalConflict = "external_transaction_conflict"
)

// WagerMetrics records the outcomes of ProcessWagerTransaction
// (observability.WagerMetrics implements it on the app's registry).
type WagerMetrics interface {
	// Recorded counts a new Wager Transaction (replays excluded).
	Recorded(kind wager.Kind, status wager.Status)
	// Duplicate counts a replay or an idempotency conflict
	// (DuplicateReplay, DuplicateKeyConflict, DuplicateExternalConflict).
	Duplicate(outcome string)
	// ConcurrencyConflict counts a versioned Wallet update that affected no
	// row.
	ConcurrencyConflict()
	// Observe records the use case latency by channel and result.
	Observe(channel Channel, result string, d time.Duration)
}

// PendingMetrics records the Pending Reference worker
// (observability.PendingMetrics implements it on the app's registry).
type PendingMetrics interface {
	// Retry counts a retry that still found no usable Reference and
	// scheduled another one.
	Retry()
	// Resolved counts a concluded Pending Reference: processed, rejected,
	// expired or failed.
	Resolved(outcome PendingOutcome)
	// SetBacklog records the number of PENDING_REFERENCE rows.
	SetBacklog(n int64)
}
