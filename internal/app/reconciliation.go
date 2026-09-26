package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// ReconciliationSnapshot is what a Reconciliation reads from one consistent
// view of a Wallet: its stored balance and the aggregate of its Ledger
// Entries.
type ReconciliationSnapshot struct {
	// StoredBalance is the Wallet's balance column, in its currency.
	StoredBalance money.Money
	// CalculatedMinor is sum(credits) - sum(debits) of the Wallet's Ledger
	// Entries in minor units (0 without entries).
	CalculatedMinor int64
	// CheckedEntries is the number of Ledger Entries aggregated.
	CheckedEntries int64
}

// ReconciliationReader reads a ReconciliationSnapshot. It is a port of its
// own, apart from Tx, because it runs in a read-only snapshot transaction
// (REPEATABLE READ READ ONLY) instead of the unit of work's read-write one.
type ReconciliationReader interface {
	// Read returns the snapshot of walletID, or ErrWalletNotFound, or
	// ErrTransient. The stored balance and the aggregate come from the same
	// snapshot, so concurrent movements never show up half-applied.
	Read(ctx context.Context, walletID uuid.UUID) (ReconciliationSnapshot, error)
}

// ReconciliationMetrics records Reconciliation outcomes.
type ReconciliationMetrics interface {
	// Observe counts one Reconciliation, consistent or divergent.
	Observe(consistent bool)
}

// ReconciliationReport compares a Wallet's stored balance with the balance
// rebuilt from its Ledger Entries.
type ReconciliationReport struct {
	WalletID          uuid.UUID
	StoredBalance     money.Money
	CalculatedBalance money.Money
	// Difference is StoredBalance - CalculatedBalance; negative when the
	// ledger holds more than the Wallet.
	Difference     money.Money
	Consistent     bool
	CheckedEntries int64
}

// Reconcile checks that a Wallet's stored balance equals credits minus debits
// of its Ledger Entries (the Opening included). It only reads: a divergence
// is reported (response, ERROR log, metric) and never repaired.
type Reconcile struct {
	reader  ReconciliationReader
	metrics ReconciliationMetrics
	log     *slog.Logger
}

// NewReconcile builds the use case.
func NewReconcile(reader ReconciliationReader, metrics ReconciliationMetrics, log *slog.Logger) *Reconcile {
	return &Reconcile{reader: reader, metrics: metrics, log: log}
}

// Execute reconciles walletID. Errors: ErrWalletNotFound, ErrTransient, or
// an internal error when a sum does not fit in Money.
func (u *Reconcile) Execute(ctx context.Context, walletID uuid.UUID) (ReconciliationReport, error) {
	s, err := u.reader.Read(ctx, walletID)
	if err != nil {
		return ReconciliationReport{}, err
	}
	calculated, err := money.FromMinorUnits(s.CalculatedMinor, s.StoredBalance.Currency())
	if err != nil {
		return ReconciliationReport{}, fmt.Errorf("app: reconcile wallet %s: %w", walletID, err)
	}
	difference, err := s.StoredBalance.Sub(calculated)
	if err != nil {
		return ReconciliationReport{}, fmt.Errorf("app: reconcile wallet %s: difference: %w", walletID, err)
	}
	r := ReconciliationReport{
		WalletID:          walletID,
		StoredBalance:     s.StoredBalance,
		CalculatedBalance: calculated,
		Difference:        difference,
		Consistent:        difference.IsZero(),
		CheckedEntries:    s.CheckedEntries,
	}
	u.metrics.Observe(r.Consistent)
	if !r.Consistent {
		u.log.ErrorContext(ctx, "reconciliation divergence",
			"walletId", walletID.String(),
			"storedBalance", r.StoredBalance.Amount(),
			"calculatedBalance", r.CalculatedBalance.Amount(),
			"difference", r.Difference.Amount(),
			"currency", r.StoredBalance.Currency().Code(),
			"checkedEntries", r.CheckedEntries)
	}
	return r, nil
}
