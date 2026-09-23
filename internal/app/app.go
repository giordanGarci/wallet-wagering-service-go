// Package app holds the use cases shared by the HTTP API and the SQS
// consumer, and the ports they need from infrastructure (repositories and
// the unit of work). It depends on the domain only; internal/postgres
// implements the ports.
package app

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"go.uber.org/fx"

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
	// ErrNotImplemented reports behaviour that a later ticket delivers.
	ErrNotImplemented = errors.New("app: not implemented yet")
	// ErrTransient reports an infrastructure failure worth retrying
	// (connection lost, timeout, serialization failure, deadlock). It never
	// produces a terminal record.
	ErrTransient = errors.New("app: transient failure")
)

// WalletRepository persists Wallets.
type WalletRepository interface {
	// Insert stores a new Wallet. It fails with ErrWalletAlreadyExists when
	// the (player, currency) pair is taken.
	Insert(ctx context.Context, w *wallet.Wallet) error
	// Get returns the Wallet or ErrWalletNotFound.
	Get(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
}

// Tx gives the repositories bound to one SQL transaction. Later tickets add
// the ledger, wager transaction and outbox repositories here, so everything
// a use case writes commits or rolls back together.
type Tx interface {
	Wallets() WalletRepository
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
	),
)
