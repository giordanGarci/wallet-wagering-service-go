package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

// OpenWalletCommand opens a Wallet for a player with an initial balance
// whose currency is the Wallet's currency.
type OpenWalletCommand struct {
	PlayerID       uuid.UUID
	InitialBalance money.Money
}

// OpenWallet creates a Wallet at version 1. A zero initial balance creates
// only the Wallet; a positive one will also record an Opening (credit,
// Ledger Entry and Outbox Events) in the same transaction.
type OpenWallet struct {
	uow   UnitOfWork
	now   func() time.Time
	newID func() (uuid.UUID, error)
}

// NewOpenWallet builds the use case with the system clock and UUIDv7 ids.
func NewOpenWallet(uow UnitOfWork) *OpenWallet {
	return &OpenWallet{uow: uow, now: time.Now, newID: uuid.NewV7}
}

// Execute opens the Wallet. Errors: ErrInvalidCommand,
// ErrWalletAlreadyExists, ErrNotImplemented (positive initial balance until
// the Opening exists), ErrTransient.
func (u *OpenWallet) Execute(ctx context.Context, cmd OpenWalletCommand) (*wallet.Wallet, error) {
	if cmd.PlayerID == uuid.Nil {
		return nil, fmt.Errorf("%w: playerId is required", ErrInvalidCommand)
	}
	if !cmd.InitialBalance.IsValid() || cmd.InitialBalance.IsNegative() {
		return nil, fmt.Errorf("%w: initialBalance must be a non-negative amount", ErrInvalidCommand)
	}
	if !cmd.InitialBalance.IsZero() {
		// Ticket 04: credit the Opening inside the Do below.
		return nil, fmt.Errorf("%w: opening a wallet with a positive initial balance is not available yet", ErrNotImplemented)
	}

	id, err := u.newID()
	if err != nil {
		return nil, fmt.Errorf("app: new wallet id: %w", err)
	}
	// PostgreSQL keeps microseconds; truncating keeps the returned Wallet
	// equal to what a later read returns.
	now := u.now().UTC().Truncate(time.Microsecond)
	w, err := wallet.New(id, cmd.PlayerID, cmd.InitialBalance.Currency(), now)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidCommand, err)
	}

	err = u.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		return tx.Wallets().Insert(ctx, w)
	})
	if err != nil {
		return nil, err
	}
	return w, nil
}

// GetWallet reads a Wallet with its current balance and version.
type GetWallet struct {
	wallets WalletRepository
}

// NewGetWallet builds the query over a non-transactional repository.
func NewGetWallet(wallets WalletRepository) *GetWallet {
	return &GetWallet{wallets: wallets}
}

// Execute returns the Wallet or ErrWalletNotFound / ErrTransient.
func (q *GetWallet) Execute(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	return q.wallets.Get(ctx, id)
}
