package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/event"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

// OpenWalletCommand opens a Wallet for a player with an initial balance
// whose currency is the Wallet's currency.
type OpenWalletCommand struct {
	PlayerID       uuid.UUID
	InitialBalance money.Money
}

// OpenWallet creates a Wallet at version 1. A zero initial balance creates
// only the Wallet; a positive one also records, in the same SQL
// transaction, the PROCESSED Opening, its credit Ledger Entry and the
// WagerTransactionProcessed and WalletBalanceChanged Outbox Events. The
// Wallet stays at version 1 either way.
type OpenWallet struct {
	uow   UnitOfWork
	now   func() time.Time
	newID func() (uuid.UUID, error)
}

// NewOpenWallet builds the use case with the system clock and UUIDv7 ids.
func NewOpenWallet(uow UnitOfWork) *OpenWallet {
	return &OpenWallet{uow: uow, now: time.Now, newID: uuid.NewV7}
}

// Execute opens the Wallet. The events carry CorrelationID(ctx), or a
// fresh one when ctx has none. Errors: ErrInvalidCommand,
// ErrWalletAlreadyExists (also for the losers of concurrent openings of
// the same pair), ErrTransient.
func (u *OpenWallet) Execute(ctx context.Context, cmd OpenWalletCommand) (*wallet.Wallet, error) {
	if cmd.PlayerID == uuid.Nil {
		return nil, fmt.Errorf("%w: playerId is required", ErrInvalidCommand)
	}
	if !cmd.InitialBalance.IsValid() || cmd.InitialBalance.IsNegative() {
		return nil, fmt.Errorf("%w: initialBalance must be a non-negative amount", ErrInvalidCommand)
	}
	ctx, correlationID := ensureCorrelationID(ctx)

	id, err := u.newID()
	if err != nil {
		return nil, fmt.Errorf("app: new wallet id: %w", err)
	}
	// PostgreSQL keeps microseconds; truncating keeps the returned Wallet
	// equal to what a later read returns.
	now := dbTime(u.now())
	w, err := wallet.New(id, cmd.PlayerID, cmd.InitialBalance.Currency(), now)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidCommand, err)
	}
	opening, entry, err := wager.Open(w, cmd.InitialBalance, now)
	if err != nil {
		return nil, fmt.Errorf("app: open wallet %s: %w", id, err)
	}
	err = u.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		// The Wallet goes first: its (player, currency) key decides races
		// between concurrent openings before anything else is written.
		if err := tx.Wallets().Insert(ctx, w); err != nil {
			return err
		}
		if opening == nil {
			return nil
		}
		if err := tx.Transactions().Insert(ctx, opening); err != nil {
			return err
		}
		stored, err := tx.Ledger().Append(ctx, *entry)
		if err != nil {
			return err
		}
		events, err := event.ForTransaction(opening, &stored, w.Version(), event.Meta{CorrelationID: correlationID})
		if err != nil {
			return fmt.Errorf("app: opening events: %w", err)
		}
		return tx.Outbox().Append(ctx, events...)
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
