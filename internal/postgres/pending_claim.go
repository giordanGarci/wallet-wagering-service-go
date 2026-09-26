package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// PendingStore is the worker side of the Pending References: claim due
// PENDING_REFERENCE rows under a lease, release unstarted ones, and measure
// the backlog. The retry itself runs in app.ResumePendingReference through
// PendingReferenceRepository.
//
// The lease token works as in OutboxStore: one claim sets the same
// locked_until on all its rows and returns it; a row is only reclaimed after
// it expired, so a later claim sets a strictly greater value, and every
// write after the claim is guarded by locked_until = token.
type PendingStore struct {
	pool *pgxpool.Pool
}

// NewPendingStore binds the store to pool.
func NewPendingStore(pool *pgxpool.Pool) *PendingStore {
	return &PendingStore{pool: pool}
}

// PendingClaim is the result of one Claim, in next_attempt_at order.
type PendingClaim struct {
	Items []app.PendingItem
	Token time.Time
}

// Claim leases up to limit due Pending References (status PENDING_REFERENCE,
// next_attempt_at <= now(), no live lease), oldest due first, with
// FOR UPDATE SKIP LOCKED so concurrent workers split the work. It is a single
// statement: row locks last only for the claim, the lease for lease.
func (s *PendingStore) Claim(ctx context.Context, limit int, lease time.Duration) (PendingClaim, error) {
	rows, err := s.pool.Query(ctx, `
		WITH candidates AS (
			SELECT id, next_attempt_at
			FROM wager_transactions
			WHERE status = 'PENDING_REFERENCE'
			  AND next_attempt_at <= now()
			  AND (locked_until IS NULL OR locked_until < now())
			ORDER BY next_attempt_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE wager_transactions t
		SET locked_until = now() + $2::float8 * interval '1 second'
		FROM candidates c
		WHERE t.id = c.id
		RETURNING t.id, t.wallet_id, t.locked_until, c.next_attempt_at`,
		limit, lease.Seconds())
	if err != nil {
		return PendingClaim{}, classify(fmt.Errorf("postgres: claim pending references: %w", err))
	}
	defer rows.Close()
	var (
		claim PendingClaim
		due   []time.Time
	)
	for rows.Next() {
		var (
			item app.PendingItem
			at   time.Time
		)
		if err := rows.Scan(&item.TransactionID, &item.WalletID, &item.Lease, &at); err != nil {
			return PendingClaim{}, fmt.Errorf("postgres: scan claimed pending reference: %w", err)
		}
		claim.Token = item.Lease
		claim.Items = append(claim.Items, item)
		due = append(due, at)
	}
	if err := rows.Err(); err != nil {
		return PendingClaim{}, classify(fmt.Errorf("postgres: claim pending references: %w", err))
	}
	sortByDue(claim.Items, due)
	return claim, nil
}

// sortByDue orders items by their next_attempt_at (insertion sort: batches
// are small), since UPDATE ... RETURNING does not keep the CTE order.
func sortByDue(items []app.PendingItem, due []time.Time) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && due[j].Before(due[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
			due[j], due[j-1] = due[j-1], due[j]
		}
	}
}

// Release drops the lease of claimed rows that were not attempted
// (shutdown, lease running out), so any worker can take them at once.
// Attempts and schedule are unchanged. It returns how many were released.
func (s *PendingStore) Release(ctx context.Context, ids []uuid.UUID, token time.Time) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE wager_transactions SET locked_until = NULL
		WHERE id = ANY($1) AND locked_until = $2 AND status = 'PENDING_REFERENCE'`,
		ids, token)
	if err != nil {
		return 0, classify(fmt.Errorf("postgres: release pending reference leases: %w", err))
	}
	return tag.RowsAffected(), nil
}

// Backlog counts the PENDING_REFERENCE rows (served by the partial index
// wager_transactions_pending_reference_due).
func (s *PendingStore) Backlog(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM wager_transactions
		WHERE status = 'PENDING_REFERENCE' AND next_attempt_at IS NOT NULL`).Scan(&n)
	if err != nil {
		return 0, classify(fmt.Errorf("postgres: pending reference backlog: %w", err))
	}
	return n, nil
}

// PendingReferenceRepository implements app.PendingReferenceRepository
// inside one transaction.
type PendingReferenceRepository struct {
	db DBTX
}

// NewPendingReferenceRepository binds the repository to db (a transaction).
func NewPendingReferenceRepository(db DBTX) *PendingReferenceRepository {
	return &PendingReferenceRepository{db: db}
}

var _ app.PendingReferenceRepository = (*PendingReferenceRepository)(nil)

// GetLeased reads the row with FOR UPDATE when it is still PENDING_REFERENCE
// under token.
func (r *PendingReferenceRepository) GetLeased(ctx context.Context, id uuid.UUID, token time.Time) (*wager.Transaction, error) {
	t, err := scanTransaction(r.db.QueryRow(ctx,
		selectTransaction+`WHERE id = $1 AND status = 'PENDING_REFERENCE' AND locked_until = $2 FOR UPDATE`, id, token))
	if errors.Is(err, app.ErrTransactionNotFound) {
		return nil, fmt.Errorf("%w: transaction %s", app.ErrLeaseLost, id)
	}
	return t, err
}

// Save writes the retry outcome of t guarded by id, status and lease, and
// drops the lease. next_attempt_at becomes nextAttemptAt (NULL once
// resolved).
func (r *PendingReferenceRepository) Save(ctx context.Context, t *wager.Transaction, nextAttemptAt *time.Time, token time.Time) error {
	s := t.Snapshot()
	if (s.Status == wager.StatusPendingReference) != (nextAttemptAt != nil) {
		return fmt.Errorf("postgres: transaction %s is %s: next attempt %v does not fit", s.ID, s.Status, nextAttemptAt)
	}
	balanceMinor, balanceCurrency := nullMoney(s.BalanceAfter)
	tag, err := r.db.Exec(ctx, `
		UPDATE wager_transactions
		SET status = $2, balance_after_minor = $3, balance_after_currency = $4, failure_code = $5,
		    reference_transaction_id = $6, compensated_bet_id = $7, attempts = $8, updated_at = $9,
		    next_attempt_at = $10, locked_until = NULL
		WHERE id = $1 AND status = 'PENDING_REFERENCE' AND locked_until = $11`,
		s.ID, string(s.Status), balanceMinor, balanceCurrency, nullString(string(s.FailureCode)),
		nullUUID(s.ReferenceTransactionID), nullUUID(s.CompensatedBetID), s.Attempts, s.UpdatedAt,
		nextAttemptAt, token)
	if compensationConflict(err) {
		// Another Reversal consumed the slot first (the Wallet lock normally
		// prevents it); the retry sees it and rejects.
		return fmt.Errorf("%w: reference %s, compensated bet %s", app.ErrCompensationConflict,
			s.ReferenceTransactionID, s.CompensatedBetID)
	}
	if err != nil {
		return classify(fmt.Errorf("postgres: save pending reference %s: %w", s.ID, err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: transaction %s", app.ErrLeaseLost, s.ID)
	}
	return nil
}
