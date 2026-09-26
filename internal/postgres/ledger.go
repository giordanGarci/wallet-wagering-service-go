package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// LedgerRepository implements app.LedgerRepository over the append-only
// ledger_entries table.
type LedgerRepository struct {
	db DBTX
}

// NewLedgerRepository binds the repository to db (pool or transaction).
func NewLedgerRepository(db DBTX) *LedgerRepository {
	return &LedgerRepository{db: db}
}

var _ app.LedgerRepository = (*LedgerRepository)(nil)

// Append inserts the entry. created_at is the entry's time, pushed past the
// Wallet's latest entry when an instance's clock lags behind another's: the
// caller holds the Wallet lock, so (created_at, id) order always matches the
// balance chain and a cursor never skips an entry committed later. The
// returned entry carries the created_at written.
func (r *LedgerRepository) Append(ctx context.Context, e ledger.Entry) (ledger.Entry, error) {
	var createdAt time.Time
	err := r.db.QueryRow(ctx, `
		INSERT INTO ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency,
			balance_before_minor, balance_after_minor, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, GREATEST($9::timestamptz,
			(SELECT max(created_at) + interval '1 microsecond' FROM ledger_entries WHERE wallet_id = $2)))
		RETURNING created_at`,
		e.ID(), e.WalletID(), e.TransactionID(), string(e.Direction()), e.Money().MinorUnits(), e.Money().Currency().Code(),
		e.BalanceBefore().MinorUnits(), e.BalanceAfter().MinorUnits(), e.CreatedAt()).Scan(&createdAt)
	if err != nil {
		return ledger.Entry{}, classify(fmt.Errorf("postgres: append ledger entry %s: %w", e.ID(), err))
	}
	return ledger.Rehydrate(e.ID(), e.WalletID(), e.TransactionID(), e.Direction(), e.Money(), e.BalanceBefore(), e.BalanceAfter(), createdAt)
}

// List reads up to limit entries of walletID in (created_at, id) order,
// after the given position when not nil (keyset pagination over the
// ledger_entries_wallet_created_id index).
func (r *LedgerRepository) List(ctx context.Context, walletID uuid.UUID, after *app.LedgerPosition, limit int) ([]ledger.Entry, error) {
	const columns = `id, wallet_id, transaction_id, direction, amount_minor, currency,
		balance_before_minor, balance_after_minor, created_at`
	var (
		rows pgx.Rows
		err  error
	)
	if after == nil {
		rows, err = r.db.Query(ctx, `SELECT `+columns+` FROM ledger_entries
			WHERE wallet_id = $1 ORDER BY created_at, id LIMIT $2`, walletID, limit)
	} else {
		rows, err = r.db.Query(ctx, `SELECT `+columns+` FROM ledger_entries
			WHERE wallet_id = $1 AND (created_at, id) > ($2, $3) ORDER BY created_at, id LIMIT $4`,
			walletID, after.CreatedAt, after.ID, limit)
	}
	if err != nil {
		return nil, classify(fmt.Errorf("postgres: list ledger of wallet %s: %w", walletID, err))
	}
	entries, err := pgx.CollectRows(rows, scanLedgerEntry)
	if err != nil {
		return nil, classify(fmt.Errorf("postgres: list ledger of wallet %s: %w", walletID, err))
	}
	return entries, nil
}

func scanLedgerEntry(row pgx.CollectableRow) (ledger.Entry, error) {
	var (
		id, walletID, transactionID          uuid.UUID
		direction, currencyCode              string
		amountMinor, beforeMinor, afterMinor int64
		createdAt                            time.Time
	)
	if err := row.Scan(&id, &walletID, &transactionID, &direction, &amountMinor, &currencyCode,
		&beforeMinor, &afterMinor, &createdAt); err != nil {
		return ledger.Entry{}, err
	}
	currency, err := money.ParseCurrency(currencyCode)
	if err != nil {
		return ledger.Entry{}, fmt.Errorf("ledger entry %s: %w", id, err)
	}
	dir, err := ledger.ParseDirection(direction)
	if err != nil {
		return ledger.Entry{}, fmt.Errorf("ledger entry %s: %w", id, err)
	}
	amount, err1 := money.FromMinorUnits(amountMinor, currency)
	before, err2 := money.FromMinorUnits(beforeMinor, currency)
	after, err3 := money.FromMinorUnits(afterMinor, currency)
	for _, err := range []error{err1, err2, err3} {
		if err != nil {
			return ledger.Entry{}, fmt.Errorf("ledger entry %s: %w", id, err)
		}
	}
	return ledger.Rehydrate(id, walletID, transactionID, dir, amount, before, after, createdAt)
}
