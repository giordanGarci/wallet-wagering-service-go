package wallet

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// Movement errors, classifiable with errors.Is. A failed movement leaves the
// Wallet untouched.
var (
	// ErrInsufficientFunds: a debit would take the balance below zero.
	ErrInsufficientFunds = errors.New("wallet: insufficient funds")
	// ErrCurrencyMismatch: the movement currency differs from the Wallet's.
	// The error chain also matches money.ErrCurrencyMismatch.
	ErrCurrencyMismatch = errors.New("wallet: currency mismatch")
	// ErrInvalidMovement: missing transaction id, non-positive or
	// uninitialized amount, or an Opening on a Wallet that already moved.
	ErrInvalidMovement = errors.New("wallet: invalid movement")
)

// Debit subtracts m from the balance, increments the version and returns the
// DEBIT Ledger Entry for transactionID. It fails with ErrInsufficientFunds
// when m exceeds the balance, so the balance never becomes negative.
func (w *Wallet) Debit(transactionID uuid.UUID, m money.Money, now time.Time) (ledger.Entry, error) {
	return w.move(transactionID, ledger.Debit, m, now, true)
}

// Credit adds m to the balance, increments the version and returns the
// CREDIT Ledger Entry for transactionID.
func (w *Wallet) Credit(transactionID uuid.UUID, m money.Money, now time.Time) (ledger.Entry, error) {
	return w.move(transactionID, ledger.Credit, m, now, true)
}

// ApplyOpening credits the initial balance of a freshly opened Wallet (zero
// balance at InitialVersion) without incrementing the version: the spec fixes
// the version of a Wallet opened with a positive balance at 1.
func (w *Wallet) ApplyOpening(transactionID uuid.UUID, m money.Money, now time.Time) (ledger.Entry, error) {
	if w.version != InitialVersion || !w.balance.IsZero() {
		return ledger.Entry{}, fmt.Errorf("%w: opening requires a fresh wallet (balance %s, version %d)", ErrInvalidMovement, w.balance, w.version)
	}
	return w.move(transactionID, ledger.Credit, m, now, false)
}

func (w *Wallet) move(transactionID uuid.UUID, dir ledger.Direction, m money.Money, now time.Time, bumpVersion bool) (ledger.Entry, error) {
	if transactionID == uuid.Nil {
		return ledger.Entry{}, fmt.Errorf("%w: transaction id is required", ErrInvalidMovement)
	}
	if !m.IsValid() || m.IsZero() || m.IsNegative() {
		return ledger.Entry{}, fmt.Errorf("%w: amount %s must be positive", ErrInvalidMovement, m)
	}
	if m.Currency() != w.Currency() {
		return ledger.Entry{}, fmt.Errorf("%w: %w: wallet %s, movement %s", ErrCurrencyMismatch, money.ErrCurrencyMismatch, w.Currency(), m.Currency())
	}
	var after money.Money
	var err error
	if dir == ledger.Debit {
		after, err = w.balance.Sub(m)
	} else {
		after, err = w.balance.Add(m)
	}
	if err != nil {
		return ledger.Entry{}, fmt.Errorf("wallet %s: %w", w.id, err)
	}
	if after.IsNegative() {
		return ledger.Entry{}, fmt.Errorf("%w: balance %s, debit %s", ErrInsufficientFunds, w.balance, m)
	}
	entry, err := ledger.New(w.id, transactionID, dir, m, w.balance, after, now)
	if err != nil {
		return ledger.Entry{}, err
	}
	w.balance = after
	if bumpVersion {
		w.version++
	}
	w.updatedAt = entry.CreatedAt()
	return entry, nil
}
