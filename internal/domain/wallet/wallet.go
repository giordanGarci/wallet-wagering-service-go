// Package wallet holds the Wallet aggregate: a player's balance in one
// currency. Creation (New) and rehydration (Rehydrate) are separate; only
// New enforces creation rules, Rehydrate just restores persisted state.
package wallet

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// InitialVersion is the version of a freshly opened Wallet.
const InitialVersion int64 = 1

// ErrInvalidWallet reports a Wallet built from inconsistent data.
var ErrInvalidWallet = errors.New("wallet: invalid wallet")

// Wallet is the balance of a player in one currency.
type Wallet struct {
	id        uuid.UUID
	playerID  uuid.UUID
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// New opens a Wallet with a zero balance at InitialVersion. Crediting a
// positive initial balance is the Opening's job, which keeps the version at 1.
func New(id, playerID uuid.UUID, currency money.Currency, now time.Time) (*Wallet, error) {
	if id == uuid.Nil || playerID == uuid.Nil {
		return nil, fmt.Errorf("%w: id and playerId are required", ErrInvalidWallet)
	}
	zero, err := money.Zero(currency)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidWallet, err)
	}
	now = now.UTC()
	return &Wallet{id: id, playerID: playerID, balance: zero, version: InitialVersion, createdAt: now, updatedAt: now}, nil
}

// Rehydrate restores a persisted Wallet without applying creation rules.
func Rehydrate(id, playerID uuid.UUID, balance money.Money, version int64, createdAt, updatedAt time.Time) (*Wallet, error) {
	if id == uuid.Nil || playerID == uuid.Nil || !balance.IsValid() || version < InitialVersion {
		return nil, fmt.Errorf("%w: cannot rehydrate wallet %s", ErrInvalidWallet, id)
	}
	return &Wallet{id: id, playerID: playerID, balance: balance, version: version, createdAt: createdAt.UTC(), updatedAt: updatedAt.UTC()}, nil
}

func (w *Wallet) ID() uuid.UUID            { return w.id }
func (w *Wallet) PlayerID() uuid.UUID      { return w.playerID }
func (w *Wallet) Currency() money.Currency { return w.balance.Currency() }
func (w *Wallet) Balance() money.Money     { return w.balance }
func (w *Wallet) Version() int64           { return w.version }
func (w *Wallet) CreatedAt() time.Time     { return w.createdAt }
func (w *Wallet) UpdatedAt() time.Time     { return w.updatedAt }
