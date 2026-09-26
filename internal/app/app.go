// Package app holds the use cases shared by the HTTP API and the SQS
// consumer, and the ports they need from infrastructure (repositories, the
// unit of work and metrics). It depends on the domain, plus Fx for Module;
// internal/postgres implements the persistence ports and
// internal/observability the metrics ports.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/domain/event"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

// Errors returned by use cases and ports, classifiable with errors.Is.
// Adapters (HTTP, SQS) map them to responses; repositories wrap
// infrastructure failures into them.
var (
	// ErrInvalidCommand reports a command that fails format validation.
	ErrInvalidCommand = errors.New("app: invalid command")
	// ErrWalletNotFound reports a Wallet id with no Wallet.
	ErrWalletNotFound = errors.New("app: wallet not found")
	// ErrWalletAlreadyExists reports a second Wallet for the same
	// (player, currency) pair.
	ErrWalletAlreadyExists = errors.New("app: wallet already exists")
	// ErrTransient reports an infrastructure failure worth retrying
	// (connection lost, timeout, serialization failure, deadlock). It never
	// produces a terminal record.
	ErrTransient = errors.New("app: transient failure")
	// ErrTransactionNotFound reports a Wager Transaction that does not
	// exist or belongs to another Provider (404 either way).
	ErrTransactionNotFound = errors.New("app: wager transaction not found")
	// ErrDuplicateTransaction reports an insert that lost the race on
	// (provider, idempotency key) or (provider, external id); the loser
	// re-reads the winner.
	ErrDuplicateTransaction = errors.New("app: duplicate wager transaction")
	// ErrConcurrencyConflict reports a versioned Wallet update that affected
	// no row. It wraps ErrTransient: the caller retries (503 / redelivery).
	ErrConcurrencyConflict = fmt.Errorf("%w: wallet concurrency conflict", ErrTransient)
	// ErrCompensationConflict reports a Reversal insert that hit a
	// compensation index (wager_transactions_compensated_bet_key or
	// wager_transactions_processed_reversal_reference_key): another Reversal
	// consumed the Compensation Slot first. The Wallet lock normally prevents
	// it; the indexes are the backstop. It wraps ErrDuplicateTransaction, so
	// ProcessWagerTransaction retries once and the retry answers
	// REFERENCE_ALREADY_COMPENSATED.
	ErrCompensationConflict = fmt.Errorf("%w: compensation slot already consumed", ErrDuplicateTransaction)
)

// WalletRepository persists Wallets.
type WalletRepository interface {
	// Insert stores a new Wallet. It fails with ErrWalletAlreadyExists when
	// the (player, currency) pair is taken.
	Insert(ctx context.Context, w *wallet.Wallet) error
	// Get returns the Wallet or ErrWalletNotFound.
	Get(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	// GetForUpdate reads the Wallet with SELECT ... FOR UPDATE, holding its
	// row lock until the transaction ends (ADR-0003). ErrWalletNotFound when
	// absent. Only meaningful inside UnitOfWork.Do.
	GetForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	// Update stores the balance, version and updatedAt of w with
	// UPDATE ... WHERE id = $1 AND version = expectedVersion; zero affected
	// rows fail with ErrConcurrencyConflict.
	Update(ctx context.Context, w *wallet.Wallet, expectedVersion int64) error
}

// TransactionRepository persists Wager Transactions (columns mirror
// wager.Snapshot).
type TransactionRepository interface {
	// Insert stores a new Wager Transaction. A PENDING_REFERENCE row must go
	// through InsertPendingReference instead. The idempotency constraints
	// (provider, key) and (provider, external id) fail with
	// ErrDuplicateTransaction; the compensation indexes fail with
	// ErrCompensationConflict.
	Insert(ctx context.Context, tx *wager.Transaction) error
	// InsertPendingReference is Insert for a PENDING_REFERENCE Transaction,
	// scheduling its first Pending Reference retry at nextAttemptAt.
	InsertPendingReference(ctx context.Context, tx *wager.Transaction, nextAttemptAt time.Time) error
	// Get returns the Transaction or ErrTransactionNotFound.
	Get(ctx context.Context, id uuid.UUID) (*wager.Transaction, error)
	// FindByIdempotencyKey returns the external Transaction of providerID
	// recorded under key, or ErrTransactionNotFound.
	FindByIdempotencyKey(ctx context.Context, providerID, key string) (*wager.Transaction, error)
	// FindByExternalID returns the external Transaction of providerID with
	// that externalTransactionId, or ErrTransactionNotFound. It also
	// resolves References.
	FindByExternalID(ctx context.Context, providerID, externalID string) (*wager.Transaction, error)
	// IsCompensated reports whether a PROCESSED REFUND or ROLLBACK already
	// references the Transaction (or consumed its Compensation Slot), for
	// wager.Reference.Compensated. Call it with the Wallet locked.
	IsCompensated(ctx context.Context, id uuid.UUID) (bool, error)
}

// LedgerRepository appends and reads Ledger Entries. The ledger is
// append-only: there is no update or delete.
type LedgerRepository interface {
	// Append stores a new entry and returns it as written: the store may
	// move createdAt forward to keep the Wallet's entries ordered, and the
	// events must carry the stored time.
	Append(ctx context.Context, e ledger.Entry) (ledger.Entry, error)
	// List returns up to limit entries of walletID in (createdAt, id) order,
	// strictly after the position when after is not nil.
	List(ctx context.Context, walletID uuid.UUID, after *LedgerPosition, limit int) ([]ledger.Entry, error)
}

// OutboxRepository stores Outbox Events for later publication.
type OutboxRepository interface {
	// Append stores the events in order, each with its exact
	// event.Event.MarshalJSON bytes as the immutable payload.
	Append(ctx context.Context, events ...event.Event) error
}

// Tx gives the repositories bound to one SQL transaction, so everything a
// use case writes commits or rolls back together.
type Tx interface {
	Wallets() WalletRepository
	Transactions() TransactionRepository
	Ledger() LedgerRepository
	Outbox() OutboxRepository
	// Pending is the lease-guarded Pending Reference side of the Wager
	// Transactions, for ResumePendingReference.
	Pending() PendingReferenceRepository
	Inbox() InboxRepository
}

// UnitOfWork delimits one SQL transaction. Do commits when fn returns nil
// and rolls back otherwise; the returned error is fn's error or the commit
// error, with infrastructure failures wrapped as ErrTransient when
// retryable.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Module provides the use cases. Their ports come from postgres.Module.
var Module = fx.Module("app",
	fx.Provide(
		NewOpenWallet,
		NewGetWallet,
		NewListLedger,
		NewProcessWagerTransaction,
		NewGetWagerTransaction,
		NewReconcile,
	),
)
