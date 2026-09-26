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
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

// Channel is the adapter an external operation arrived through.
type Channel string

const (
	ChannelHTTP Channel = "HTTP"
	ChannelSQS  Channel = "SQS"
)

// WagerRequest is one external operation handed to ProcessWagerTransaction
// by an adapter. The correlation id travels in the context
// (WithCorrelationID).
type WagerRequest struct {
	// Command is the validated operation (wager.NewCommand). For SQS the
	// Idempotency Key is data.idempotencyKey.
	Command wager.Command
	// Channel labels metrics and logs.
	Channel Channel
	// CausationID is copied into the Outbox Events (e.g. the SQS messageId);
	// optional.
	CausationID string
	// InTx, when set, runs inside the SQL transaction that records (or
	// replays) the operation, after its writes, with the result. The SQS
	// consumer uses it to write the Inbox Message atomically with the
	// operation. An error rolls everything back. It may run more than once
	// when the insert race is retried, and it does not run for the separate
	// transaction that records a FAILED operation.
	InTx func(ctx context.Context, tx Tx, res WagerResult) error
}

// WagerResult is the outcome returned to the adapter.
type WagerResult struct {
	// Transaction is the recorded operation: new, or the persisted one on a
	// Replay. Status, FailureCode and BalanceAfter are what the Provider
	// sees.
	Transaction *wager.Transaction
	// Replay is true when Transaction was recorded by an earlier request
	// with the same Idempotency Key and Payload Hash.
	Replay bool
}

// ProcessWagerTransaction is the use case shared by HTTP and SQS (ADR-0001,
// ADR-0003). In one SQL transaction it:
//
//  1. looks up (provider, idempotency key), then (provider, external id):
//     a match is a Replay or an idempotency conflict (wager.CheckReplay);
//  2. locks the Wallet with SELECT ... FOR UPDATE (nil when absent) and
//     repeats the lookup, so requests queued on the lock see the winner;
//  3. resolves the Reference by (provider, referenceExternalTransactionId)
//     when the operation has one, with its Compensated flag;
//  4. applies the operation (wager.Transaction.Apply) and writes, in order,
//     the Wallet (versioned UPDATE), the Wager Transaction, the Ledger Entry
//     and the Outbox Events (event.ForTransaction).
//
// A loser of the insert race on the idempotency constraints (e.g. two
// requests for an unknown Wallet, which has no row to lock) rolls back and
// retries once, answering from the winner; so does a Reversal that hits a
// compensation index (ErrCompensationConflict), whose retry sees the slot
// consumed. Errors: wrapped
// wager.ErrIdempotencyKeyConflict / wager.ErrExternalTransactionConflict
// (409), ErrTransient (503 / redelivery; includes ErrConcurrencyConflict),
// ErrInvalidCommand. A permanent failure (anything else, e.g. an overflow)
// is recorded as FAILED PERMANENT_ERROR in a separate transaction and
// returned as a result with a nil error.
type ProcessWagerTransaction struct {
	uow     UnitOfWork
	policy  wager.PendingPolicy
	metrics WagerMetrics
	log     *slog.Logger
	now     func() time.Time
}

// NewProcessWagerTransaction builds the use case with the Pending Reference
// policy (PENDING_*; it schedules the first retry of a parked operation) and
// the system clock.
func NewProcessWagerTransaction(uow UnitOfWork, policy wager.PendingPolicy, metrics WagerMetrics, log *slog.Logger) *ProcessWagerTransaction {
	return &ProcessWagerTransaction{
		uow:     uow,
		policy:  policy,
		metrics: metrics,
		log:     log,
		now:     time.Now,
	}
}

// Execute processes req. See ProcessWagerTransaction.
func (u *ProcessWagerTransaction) Execute(ctx context.Context, req WagerRequest) (WagerResult, error) {
	started := time.Now()
	cmd := req.Command
	if !cmd.IsValid() {
		return WagerResult{}, fmt.Errorf("%w: command was not built by wager.NewCommand", ErrInvalidCommand)
	}
	ctx, _ = ensureCorrelationID(ctx)

	res, err := u.attempt(ctx, req)
	if errors.Is(err, ErrDuplicateTransaction) {
		// Lost the insert race: everything rolled back; the retry finds the
		// winner in step 1.
		res, err = u.attempt(ctx, req)
		if errors.Is(err, ErrDuplicateTransaction) {
			// Lost twice: nothing was written, so answer as a transient
			// failure (503 / message back to the queue) and let the
			// caller's retry find the winner.
			err = fmt.Errorf("%w: %w", ErrTransient, err)
		}
	}
	if err != nil && isPermanent(err) {
		u.log.ErrorContext(ctx, "wager transaction failed permanently",
			"providerId", cmd.ProviderID(), "externalTransactionId", cmd.ExternalTransactionID(),
			"walletId", cmd.WalletID(), "error", err)
		res, err = u.recordFailure(ctx, req)
	}
	u.report(ctx, req, res, err, time.Since(started))
	return res, err
}

func (u *ProcessWagerTransaction) attempt(ctx context.Context, req WagerRequest) (WagerResult, error) {
	cmd := req.Command
	var res WagerResult
	err := u.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		res = WagerResult{}
		existing, err := findExisting(ctx, tx.Transactions(), cmd)
		if err != nil {
			return err
		}
		if existing == nil {
			w, err := tx.Wallets().GetForUpdate(ctx, cmd.WalletID())
			if errors.Is(err, ErrWalletNotFound) {
				w, err = nil, nil
			}
			if err != nil {
				return err
			}
			// Requests queued on the Wallet lock see the winner here.
			if existing, err = findExisting(ctx, tx.Transactions(), cmd); err != nil {
				return err
			}
			if existing == nil {
				t, err := u.apply(ctx, tx, req, w)
				if err != nil {
					return err
				}
				res = WagerResult{Transaction: t}
			}
		}
		if existing != nil {
			if err := wager.CheckReplay(existing, cmd); err != nil {
				return err
			}
			res = WagerResult{Transaction: existing, Replay: true}
		}
		if req.InTx != nil {
			return req.InTx(ctx, tx, res)
		}
		return nil
	})
	return res, err
}

// findExisting looks up by (provider, key) first, then (provider, external
// id); nil when neither exists.
func findExisting(ctx context.Context, repo TransactionRepository, cmd wager.Command) (*wager.Transaction, error) {
	t, err := repo.FindByIdempotencyKey(ctx, cmd.ProviderID(), cmd.IdempotencyKey())
	if errors.Is(err, ErrTransactionNotFound) {
		t, err = repo.FindByExternalID(ctx, cmd.ProviderID(), cmd.ExternalTransactionID())
	}
	if errors.Is(err, ErrTransactionNotFound) {
		return nil, nil
	}
	return t, err
}

// apply runs a new operation against the locked Wallet w (nil when absent)
// and writes its effects.
func (u *ProcessWagerTransaction) apply(ctx context.Context, tx Tx, req WagerRequest, w *wallet.Wallet) (*wager.Transaction, error) {
	now := dbTime(u.now())
	t, err := wager.NewExternal(req.Command, now)
	if err != nil {
		return nil, err
	}
	var ref wager.Reference
	if t.HasReference() && w != nil {
		if ref, err = lookupReference(ctx, tx.Transactions(), t); err != nil {
			return nil, err
		}
	}
	a, err := applyTo(t, w, ref, now)
	if err != nil {
		return nil, err
	}
	err = persist(ctx, tx, a, u.metrics, eventMeta(ctx, req), func() error {
		if t.Status() == wager.StatusPendingReference {
			return tx.Transactions().InsertPendingReference(ctx, t, u.policy.NextAttemptAt(t.Attempts(), now))
		}
		return tx.Transactions().Insert(ctx, t)
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// lookupReference resolves t's Reference within its Provider. Call it with
// the Wallet locked.
func lookupReference(ctx context.Context, repo TransactionRepository, t *wager.Transaction) (wager.Reference, error) {
	r, err := repo.FindByExternalID(ctx, t.ProviderID(), t.ReferenceExternalTransactionID())
	if errors.Is(err, ErrTransactionNotFound) {
		return wager.Reference{}, nil
	}
	if err != nil {
		return wager.Reference{}, err
	}
	compensated, err := repo.IsCompensated(ctx, r.ID())
	if err != nil {
		return wager.Reference{}, err
	}
	return wager.Reference{Transaction: r, Compensated: compensated}, nil
}

// recordFailure stores the operation as FAILED PERMANENT_ERROR, for audit
// and so that Replays return the same outcome. If another request recorded
// the operation meanwhile, its result is returned instead.
func (u *ProcessWagerTransaction) recordFailure(ctx context.Context, req WagerRequest) (WagerResult, error) {
	now := dbTime(u.now())
	t, err := wager.NewExternal(req.Command, now)
	if err != nil {
		return WagerResult{}, err
	}
	if err := t.Fail(wager.FailurePermanentError, now); err != nil {
		return WagerResult{}, err
	}
	err = u.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		return tx.Transactions().Insert(ctx, t)
	})
	if errors.Is(err, ErrDuplicateTransaction) {
		return u.attempt(ctx, req)
	}
	if err != nil {
		return WagerResult{}, err
	}
	return WagerResult{Transaction: t}, nil
}

// isPermanent reports errors that retrying cannot fix: everything except
// transient infrastructure failures, cancellation, idempotency outcomes,
// lost races and invalid commands.
func isPermanent(err error) bool {
	for _, known := range []error{
		ErrTransient, ErrDuplicateTransaction, ErrInvalidCommand,
		wager.ErrIdempotencyKeyConflict, wager.ErrExternalTransactionConflict,
		context.Canceled, context.DeadlineExceeded,
	} {
		if errors.Is(err, known) {
			return false
		}
	}
	return true
}

func eventMeta(ctx context.Context, req WagerRequest) event.Meta {
	return event.Meta{CorrelationID: CorrelationID(ctx), CausationID: req.CausationID}
}

// report updates the metrics and logs the outcome with its identifiers only
// (no amounts or balances).
func (u *ProcessWagerTransaction) report(ctx context.Context, req WagerRequest, res WagerResult, err error, d time.Duration) {
	cmd := req.Command
	attrs := []any{
		"channel", string(req.Channel), "providerId", cmd.ProviderID(),
		"externalTransactionId", cmd.ExternalTransactionID(), "walletId", cmd.WalletID(), "kind", string(cmd.Kind()),
	}
	switch {
	case err == nil:
		t := res.Transaction
		attrs = append(attrs, "transactionId", t.ID(), "status", string(t.Status()), "idempotentReplay", res.Replay)
		if t.FailureCode() != "" {
			attrs = append(attrs, "failureCode", string(t.FailureCode()))
		}
		if res.Replay {
			u.metrics.Duplicate(DuplicateReplay)
			u.metrics.Observe(req.Channel, "replay", d)
		} else {
			u.metrics.Recorded(t.Kind(), t.Status())
			u.metrics.Observe(req.Channel, string(t.Status()), d)
		}
		u.log.InfoContext(ctx, "wager transaction handled", attrs...)
	case errors.Is(err, wager.ErrIdempotencyKeyConflict):
		u.metrics.Duplicate(DuplicateKeyConflict)
		u.metrics.Observe(req.Channel, "conflict", d)
		u.log.WarnContext(ctx, "idempotency key conflict", attrs...)
	case errors.Is(err, wager.ErrExternalTransactionConflict):
		u.metrics.Duplicate(DuplicateExternalConflict)
		u.metrics.Observe(req.Channel, "conflict", d)
		u.log.WarnContext(ctx, "external transaction conflict", attrs...)
	case errors.Is(err, ErrTransient):
		u.metrics.Observe(req.Channel, "transient", d)
		u.log.WarnContext(ctx, "wager transaction transient failure", append(attrs, "error", err)...)
	default:
		u.metrics.Observe(req.Channel, "error", d)
		u.log.ErrorContext(ctx, "wager transaction error", append(attrs, "error", err)...)
	}
}

// GetWagerTransaction reads a Provider's own Wager Transactions. Another
// Provider's transaction is reported as ErrTransactionNotFound.
type GetWagerTransaction struct {
	transactions TransactionRepository
}

// NewGetWagerTransaction builds the query over a non-transactional
// repository.
func NewGetWagerTransaction(transactions TransactionRepository) *GetWagerTransaction {
	return &GetWagerTransaction{transactions: transactions}
}

// ByID returns transaction id if it belongs to providerID.
func (q *GetWagerTransaction) ByID(ctx context.Context, providerID string, id uuid.UUID) (*wager.Transaction, error) {
	t, err := q.transactions.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Origin() != wager.OriginExternal || t.ProviderID() != providerID {
		return nil, ErrTransactionNotFound
	}
	return t, nil
}

// ByExternalID returns providerID's transaction with that
// externalTransactionId.
func (q *GetWagerTransaction) ByExternalID(ctx context.Context, providerID, externalID string) (*wager.Transaction, error) {
	return q.transactions.FindByExternalID(ctx, providerID, externalID)
}
