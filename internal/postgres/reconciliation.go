package postgres

import (
	"context"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junglegaming/backend-challenge-go/internal/app"
)

// ReconciliationReader implements app.ReconciliationReader.
type ReconciliationReader struct {
	pool *pgxpool.Pool
}

// NewReconciliationReader builds the reader over the pool.
func NewReconciliationReader(pool *pgxpool.Pool) *ReconciliationReader {
	return &ReconciliationReader{pool: pool}
}

var _ app.ReconciliationReader = (*ReconciliationReader)(nil)

// Read runs in one REPEATABLE READ READ ONLY transaction, so the stored
// balance and the ledger aggregate come from the same snapshot even while
// movements commit concurrently, and the database rejects any write.
//
// The sum is computed as numeric so it cannot overflow inside PostgreSQL; a
// result outside int64 is reported as an error (it cannot be Money).
func (r *ReconciliationReader) Read(ctx context.Context, walletID uuid.UUID) (app.ReconciliationSnapshot, error) {
	var snap app.ReconciliationSnapshot
	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	err := pgx.BeginTxFunc(ctx, r.pool, opts, func(tx pgx.Tx) error {
		w, err := NewWalletRepository(tx).Get(ctx, walletID)
		if err != nil {
			return err
		}
		snap.StoredBalance = w.Balance()

		var sum string
		err = tx.QueryRow(ctx, `
			SELECT coalesce(sum(CASE direction
					WHEN 'CREDIT' THEN amount_minor::numeric
					WHEN 'DEBIT' THEN -amount_minor::numeric
				END), 0)::text,
				count(*)
			FROM ledger_entries WHERE wallet_id = $1`, walletID).Scan(&sum, &snap.CheckedEntries)
		if err != nil {
			return fmt.Errorf("postgres: aggregate ledger of wallet %s: %w", walletID, err)
		}
		snap.CalculatedMinor, err = strconv.ParseInt(sum, 10, 64)
		if err != nil {
			return fmt.Errorf("postgres: ledger of wallet %s sums to %s minor units, outside int64: %w", walletID, sum, err)
		}
		return nil
	})
	if err != nil {
		return app.ReconciliationSnapshot{}, classify(err)
	}
	return snap, nil
}
