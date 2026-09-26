// Package ledger holds the Ledger Entry: the immutable record of one debit or
// credit on a Wallet, carrying the balance before and after the movement.
// Entries are built by the Wallet aggregate (wallet.Debit / wallet.Credit);
// New validates balanceAfter = balanceBefore ± money and Rehydrate restores a
// persisted entry under the same invariants.
package ledger

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// ErrInvalidEntry reports a Ledger Entry built from inconsistent data.
var ErrInvalidEntry = errors.New("ledger: invalid entry")

// Direction is the side of a Ledger Entry from the Wallet's point of view.
type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

// ParseDirection accepts the canonical upper-case names only.
func ParseDirection(s string) (Direction, error) {
	d := Direction(s)
	if !d.IsValid() {
		return "", fmt.Errorf("%w: unknown direction %q", ErrInvalidEntry, s)
	}
	return d, nil
}

// IsValid reports whether d is DEBIT or CREDIT.
func (d Direction) IsValid() bool { return d == Debit || d == Credit }

// String implements fmt.Stringer.
func (d Direction) String() string { return string(d) }

// Entry is an immutable Ledger Entry. The zero value is invalid; build it
// with New or Rehydrate.
type Entry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     Direction
	money         money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	createdAt     time.Time
}

// New builds an entry with a fresh UUIDv7. amount must be positive, all three
// values must share a currency, both balances must be non-negative and
// balanceAfter must equal balanceBefore + amount (CREDIT) or
// balanceBefore - amount (DEBIT). createdAt is stored in UTC with microsecond
// precision (what PostgreSQL keeps).
func New(walletID, transactionID uuid.UUID, direction Direction, amount, balanceBefore, balanceAfter money.Money, createdAt time.Time) (Entry, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Entry{}, fmt.Errorf("ledger: generate id: %w", err)
	}
	return build(id, walletID, transactionID, direction, amount, balanceBefore, balanceAfter, createdAt)
}

// Rehydrate restores a persisted entry, re-checking its invariants.
func Rehydrate(id, walletID, transactionID uuid.UUID, direction Direction, amount, balanceBefore, balanceAfter money.Money, createdAt time.Time) (Entry, error) {
	return build(id, walletID, transactionID, direction, amount, balanceBefore, balanceAfter, createdAt)
}

func build(id, walletID, transactionID uuid.UUID, direction Direction, amount, before, after money.Money, createdAt time.Time) (Entry, error) {
	if id == uuid.Nil || walletID == uuid.Nil || transactionID == uuid.Nil {
		return Entry{}, fmt.Errorf("%w: id, walletId and transactionId are required", ErrInvalidEntry)
	}
	if !direction.IsValid() {
		return Entry{}, fmt.Errorf("%w: unknown direction %q", ErrInvalidEntry, direction)
	}
	if !amount.IsValid() || !before.IsValid() || !after.IsValid() {
		return Entry{}, fmt.Errorf("%w: money, balanceBefore and balanceAfter are required", ErrInvalidEntry)
	}
	if amount.IsZero() || amount.IsNegative() {
		return Entry{}, fmt.Errorf("%w: amount must be positive", ErrInvalidEntry)
	}
	if before.IsNegative() || after.IsNegative() {
		return Entry{}, fmt.Errorf("%w: balances must not be negative", ErrInvalidEntry)
	}
	var expected money.Money
	var err error
	if direction == Credit {
		expected, err = before.Add(amount)
	} else {
		expected, err = before.Sub(amount)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("%w: %w", ErrInvalidEntry, err)
	}
	if !expected.Equal(after) {
		return Entry{}, fmt.Errorf("%w: %s does not match balanceBefore and amount", ErrInvalidEntry, direction)
	}
	return Entry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		money:         amount,
		balanceBefore: before,
		balanceAfter:  after,
		createdAt:     createdAt.UTC().Truncate(time.Microsecond),
	}, nil
}

func (e Entry) ID() uuid.UUID              { return e.id }
func (e Entry) WalletID() uuid.UUID        { return e.walletID }
func (e Entry) TransactionID() uuid.UUID   { return e.transactionID }
func (e Entry) Direction() Direction       { return e.direction }
func (e Entry) Money() money.Money         { return e.money }
func (e Entry) BalanceBefore() money.Money { return e.balanceBefore }
func (e Entry) BalanceAfter() money.Money  { return e.balanceAfter }
func (e Entry) CreatedAt() time.Time       { return e.createdAt }

// SignedAmount returns +money for a CREDIT and -money for a DEBIT, the
// contribution of the entry to the Wallet balance (used by Reconciliation).
func (e Entry) SignedAmount() money.Money {
	if e.direction == Debit {
		neg, _ := e.money.Neg() // money is positive, Neg cannot overflow
		return neg
	}
	return e.money
}
