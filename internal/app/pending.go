package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/event"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// ErrLeaseLost reports that a Pending Reference is no longer held by the
// caller's lease: another worker reclaimed it after the lease expired, or it
// is no longer PENDING_REFERENCE. Nothing was written.
var ErrLeaseLost = errors.New("app: pending reference lease lost")

// PendingReferenceRepository is the Pending Reference side of
// wager_transactions, bound to one SQL transaction (Tx.Pending). Every
// method is guarded by the lease token returned by the claim (the
// locked_until value it set): a worker that lost its lease changes nothing.
type PendingReferenceRepository interface {
	// GetLeased reads and row-locks the PENDING_REFERENCE Transaction id
	// still leased with token; ErrLeaseLost otherwise.
	GetLeased(ctx context.Context, id uuid.UUID, token time.Time) (*wager.Transaction, error)
	// Save writes the outcome of a retry of t (status, balance, Failure
	// Code, Reference ids, Compensation Slot, attempts, updatedAt) and drops
	// the lease. nextAttemptAt schedules the next retry when t is still
	// PENDING_REFERENCE and must be nil otherwise. ErrLeaseLost when the row
	// is no longer PENDING_REFERENCE under token. A compensation index
	// violation (the slot was consumed concurrently) fails with
	// ErrCompensationConflict.
	Save(ctx context.Context, t *wager.Transaction, nextAttemptAt *time.Time, token time.Time) error
}

// PendingItem is one claimed Pending Reference.
type PendingItem struct {
	TransactionID uuid.UUID
	WalletID      uuid.UUID
	// Lease is the claim's token (the locked_until it set).
	Lease time.Time
}

// PendingOutcome is what one retry of a Pending Reference did.
type PendingOutcome string

const (
	// PendingProcessed: the Reference was usable and the operation applied.
	PendingProcessed PendingOutcome = "processed"
	// PendingRejected: a business rejection (REFERENCE_UNSUCCESSFUL,
	// REFERENCE_ALREADY_COMPENSATED, REVERSAL_INSUFFICIENT_FUNDS, ...).
	PendingRejected PendingOutcome = "rejected"
	// PendingExpired: PENDING_MAX_ATTEMPTS reached, REJECTED
	// REFERENCE_NOT_FOUND.
	PendingExpired PendingOutcome = "expired"
	// PendingRetry: the Reference is still absent or pending; the next
	// retry is scheduled with backoff.
	PendingRetry PendingOutcome = "retry"
	// PendingFailed: a permanent error (not transient); FAILED
	// PERMANENT_ERROR.
	PendingFailed PendingOutcome = "failed"
	// PendingSkipped: the lease was lost or the row was already resolved;
	// nothing was written.
	PendingSkipped PendingOutcome = "skipped"
)

// ResumePendingReference retries one claimed PENDING_REFERENCE Wager
// Transaction. In one SQL transaction it:
//
//  1. locks the Wallet with SELECT ... FOR UPDATE (as ProcessWagerTransaction
//     does, so it serializes with every other operation on the Wallet);
//  2. re-reads the Transaction under the claim's lease (skipped when lost or
//     no longer PENDING_REFERENCE);
//  3. looks the Reference up within the Provider, with its Compensated flag,
//     and runs wager.Transaction.Apply;
//  4. resolved (PROCESSED or REJECTED): writes the Wallet (versioned UPDATE),
//     the Transaction, the Ledger Entry and the Outbox Events, like a new
//     operation; still waiting: RecordReferenceMiss either schedules the
//     next retry (no event) or expires the Transaction as REJECTED
//     REFERENCE_NOT_FOUND with WagerTransactionRejected.
//
// Replays of the operation read the row, so after a resolution they return
// the final result.
type ResumePendingReference struct {
	uow     UnitOfWork
	policy  wager.PendingPolicy
	metrics PendingMetrics
	// wagerMetrics counts versioned Wallet update conflicts in
	// wallet_concurrency_conflicts_total, shared with ProcessWagerTransaction.
	wagerMetrics WagerMetrics
	log          *slog.Logger
	now          func() time.Time
}

// NewResumePendingReference builds the use case with the system clock.
func NewResumePendingReference(uow UnitOfWork, policy wager.PendingPolicy, metrics PendingMetrics, wagerMetrics WagerMetrics, log *slog.Logger) (*ResumePendingReference, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &ResumePendingReference{uow: uow, policy: policy, metrics: metrics, wagerMetrics: wagerMetrics, log: log, now: time.Now}, nil
}

// Execute retries item. Errors are transient or unexpected: nothing was
// written and the row is retried once its lease expires (or is released).
func (u *ResumePendingReference) Execute(ctx context.Context, item PendingItem) (PendingOutcome, error) {
	ctx, _ = ensureCorrelationID(ctx)
	var (
		outcome PendingOutcome
		t       *wager.Transaction
	)
	attempt := func() error {
		return u.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
			var err error
			outcome, t, err = u.resume(ctx, tx, item)
			return err
		})
	}
	err := attempt()
	if errors.Is(err, ErrCompensationConflict) {
		// Another Reversal consumed the Compensation Slot first; everything
		// rolled back and the lease is still held: the retry sees the slot
		// consumed and rejects with REFERENCE_ALREADY_COMPENSATED.
		err = attempt()
	}
	if err != nil && !errors.Is(err, ErrLeaseLost) && isPermanent(err) {
		u.log.ErrorContext(ctx, "pending reference failed permanently",
			"transactionId", item.TransactionID, "walletId", item.WalletID, "error", err)
		outcome, t, err = u.recordFailure(ctx, item)
	}
	if errors.Is(err, ErrLeaseLost) {
		outcome, err = PendingSkipped, nil
	}
	if err != nil {
		u.log.WarnContext(ctx, "pending reference retry failed",
			"transactionId", item.TransactionID, "walletId", item.WalletID, "error", err)
		return "", err
	}
	u.report(ctx, item, outcome, t)
	return outcome, nil
}

func (u *ResumePendingReference) resume(ctx context.Context, tx Tx, item PendingItem) (PendingOutcome, *wager.Transaction, error) {
	w, err := tx.Wallets().GetForUpdate(ctx, item.WalletID)
	if errors.Is(err, ErrWalletNotFound) {
		w, err = nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	t, err := tx.Pending().GetLeased(ctx, item.TransactionID, item.Lease)
	if err != nil {
		return "", nil, err
	}
	var ref wager.Reference
	if w != nil {
		if ref, err = lookupReference(ctx, tx.Transactions(), t); err != nil {
			return "", nil, err
		}
	}

	now := dbTime(u.now())
	a, err := applyTo(t, w, ref, now)
	if err != nil {
		return "", nil, err
	}

	outcome := PendingProcessed
	switch t.Status() {
	case wager.StatusPendingReference:
		next, exhausted, err := t.RecordReferenceMiss(u.policy, now)
		if err != nil {
			return "", nil, err
		}
		if !exhausted {
			if err := tx.Pending().Save(ctx, t, &next, item.Lease); err != nil {
				return "", nil, err
			}
			return PendingRetry, t, nil
		}
		outcome = PendingExpired
	case wager.StatusRejected:
		outcome = PendingRejected
	case wager.StatusProcessed:
	default:
		return "", nil, fmt.Errorf("app: pending reference %s ended %s", t.ID(), t.Status())
	}

	err = persist(ctx, tx, a, u.wagerMetrics, event.Meta{CorrelationID: CorrelationID(ctx)}, func() error {
		return tx.Pending().Save(ctx, t, nil, item.Lease)
	})
	if err != nil {
		return "", nil, err
	}
	return outcome, t, nil
}

// recordFailure concludes the Transaction as FAILED PERMANENT_ERROR in a
// separate SQL transaction (the retry keeps failing the same way), so it is
// not retried forever and Replays return the outcome. FAILED emits no event.
func (u *ResumePendingReference) recordFailure(ctx context.Context, item PendingItem) (PendingOutcome, *wager.Transaction, error) {
	var t *wager.Transaction
	err := u.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		if t, err = tx.Pending().GetLeased(ctx, item.TransactionID, item.Lease); err != nil {
			return err
		}
		if err := t.Fail(wager.FailurePermanentError, dbTime(u.now())); err != nil {
			return err
		}
		return tx.Pending().Save(ctx, t, nil, item.Lease)
	})
	if err != nil {
		return "", nil, err
	}
	return PendingFailed, t, nil
}

// report updates the metrics and logs the outcome with identifiers only.
func (u *ResumePendingReference) report(ctx context.Context, item PendingItem, outcome PendingOutcome, t *wager.Transaction) {
	attrs := []any{"transactionId", item.TransactionID, "walletId", item.WalletID, "outcome", string(outcome)}
	switch outcome {
	case PendingSkipped:
		u.log.InfoContext(ctx, "pending reference skipped: lease lost or already resolved", attrs...)
		return
	case PendingRetry:
		u.metrics.Retry()
		u.log.DebugContext(ctx, "pending reference still waiting", append(attrs, "attempts", t.Attempts())...)
		return
	}
	u.metrics.Resolved(outcome)
	attrs = append(attrs, "providerId", t.ProviderID(), "externalTransactionId", t.ExternalTransactionID(),
		"kind", string(t.Kind()), "status", string(t.Status()), "attempts", t.Attempts())
	if t.FailureCode() != "" {
		attrs = append(attrs, "failureCode", string(t.FailureCode()))
	}
	u.log.InfoContext(ctx, "pending reference resolved", attrs...)
}
