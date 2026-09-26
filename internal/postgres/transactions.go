package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// Idempotency constraints of wager_transactions; violating either means
// another request recorded the same operation first.
const (
	txProviderIdempotencyKey = "wager_transactions_provider_idempotency_key"
	txProviderExternalID     = "wager_transactions_provider_external_transaction_id"
)

// Compensation indexes of wager_transactions (ADR-0002): violating either
// means another Reversal consumed the Compensation Slot first.
const (
	txCompensatedBet             = "wager_transactions_compensated_bet_key"
	txProcessedReversalReference = "wager_transactions_processed_reversal_reference_key"
)

// TransactionRepository implements app.TransactionRepository over the
// wager_transactions table, whose columns mirror wager.Snapshot.
type TransactionRepository struct {
	db DBTX
}

// NewTransactionRepository binds the repository to db (pool or transaction).
func NewTransactionRepository(db DBTX) *TransactionRepository {
	return &TransactionRepository{db: db}
}

var _ app.TransactionRepository = (*TransactionRepository)(nil)

// Insert stores a new Wager Transaction. Absent optional values (see
// wager.Snapshot) are written as NULL. A PENDING_REFERENCE row must use
// InsertPendingReference.
func (r *TransactionRepository) Insert(ctx context.Context, tx *wager.Transaction) error {
	if tx.Status() == wager.StatusPendingReference {
		return fmt.Errorf("postgres: PENDING_REFERENCE transaction %s needs InsertPendingReference", tx.ID())
	}
	return r.insert(ctx, tx, nil)
}

// InsertPendingReference stores a PENDING_REFERENCE Transaction with its
// first retry due at nextAttemptAt.
func (r *TransactionRepository) InsertPendingReference(ctx context.Context, tx *wager.Transaction, nextAttemptAt time.Time) error {
	if tx.Status() != wager.StatusPendingReference {
		return fmt.Errorf("postgres: transaction %s is %s, not PENDING_REFERENCE", tx.ID(), tx.Status())
	}
	return r.insert(ctx, tx, &nextAttemptAt)
}

func (r *TransactionRepository) insert(ctx context.Context, tx *wager.Transaction, nextAttemptAt *time.Time) error {
	s := tx.Snapshot()
	balanceMinor, balanceCurrency := nullMoney(s.BalanceAfter)
	_, err := r.db.Exec(ctx, `
		INSERT INTO wager_transactions (
			id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
			balance_after_minor, balance_after_currency, failure_code,
			provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
			reference_external_transaction_id, reference_transaction_id, compensated_bet_id,
			attempts, next_attempt_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)`,
		s.ID, string(s.Origin), string(s.Kind), string(s.Status), s.WalletID, s.PlayerID,
		s.Money.MinorUnits(), s.Money.Currency().Code(),
		balanceMinor, balanceCurrency, nullString(string(s.FailureCode)),
		nullString(s.ProviderID), nullString(s.ExternalTransactionID), nullString(s.IdempotencyKey),
		nullString(s.PayloadHash), nullString(s.RoundID), nullString(s.GameID),
		nullString(s.ReferenceExternalTransactionID), nullUUID(s.ReferenceTransactionID), nullUUID(s.CompensatedBetID),
		s.Attempts, nextAttemptAt, s.CreatedAt, s.UpdatedAt)
	if uniqueViolationOn(err, txProviderIdempotencyKey) || uniqueViolationOn(err, txProviderExternalID) {
		return fmt.Errorf("%w: provider %q, key %q, external id %q", app.ErrDuplicateTransaction,
			s.ProviderID, s.IdempotencyKey, s.ExternalTransactionID)
	}
	if compensationConflict(err) {
		return fmt.Errorf("%w: reference %s, compensated bet %s", app.ErrCompensationConflict,
			s.ReferenceTransactionID, s.CompensatedBetID)
	}
	if err != nil {
		return classify(fmt.Errorf("postgres: insert wager transaction %s: %w", s.ID, err))
	}
	return nil
}

const selectTransaction = `
	SELECT id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
		balance_after_minor, balance_after_currency, failure_code,
		provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
		reference_external_transaction_id, reference_transaction_id, compensated_bet_id,
		attempts, created_at, updated_at
	FROM wager_transactions `

// Get reads a Transaction by id.
func (r *TransactionRepository) Get(ctx context.Context, id uuid.UUID) (*wager.Transaction, error) {
	return scanTransaction(r.db.QueryRow(ctx, selectTransaction+`WHERE id = $1`, id))
}

// FindByIdempotencyKey reads the Transaction of providerID recorded under key.
func (r *TransactionRepository) FindByIdempotencyKey(ctx context.Context, providerID, key string) (*wager.Transaction, error) {
	return scanTransaction(r.db.QueryRow(ctx,
		selectTransaction+`WHERE provider_id = $1 AND idempotency_key = $2`, providerID, key))
}

// FindByExternalID reads the Transaction of providerID with externalID.
func (r *TransactionRepository) FindByExternalID(ctx context.Context, providerID, externalID string) (*wager.Transaction, error) {
	return scanTransaction(r.db.QueryRow(ctx,
		selectTransaction+`WHERE provider_id = $1 AND external_transaction_id = $2`, providerID, externalID))
}

// IsCompensated reports whether a PROCESSED REFUND or ROLLBACK references id
// or consumed its Compensation Slot. Both predicates are served by the
// partial unique indexes on reference_transaction_id and compensated_bet_id.
func (r *TransactionRepository) IsCompensated(ctx context.Context, id uuid.UUID) (bool, error) {
	var found bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM wager_transactions WHERE compensated_bet_id = $1)
		    OR EXISTS (SELECT 1 FROM wager_transactions
		               WHERE reference_transaction_id = $1 AND kind IN ('REFUND', 'ROLLBACK') AND status = 'PROCESSED')`,
		id).Scan(&found)
	if err != nil {
		return false, classify(fmt.Errorf("postgres: compensation of %s: %w", id, err))
	}
	return found, nil
}

func scanTransaction(row pgx.Row) (*wager.Transaction, error) {
	var (
		s                                  wager.Snapshot
		origin, kind, status, currency     string
		amount                             int64
		balanceMinor                       *int64
		balanceCurrency, failureCode       *string
		providerID, externalID, key, hash  *string
		roundID, gameID, referenceExternal *string
		referenceID, compensatedBetID      *uuid.UUID
	)
	err := row.Scan(&s.ID, &origin, &kind, &status, &s.WalletID, &s.PlayerID, &amount, &currency,
		&balanceMinor, &balanceCurrency, &failureCode,
		&providerID, &externalID, &key, &hash, &roundID, &gameID,
		&referenceExternal, &referenceID, &compensatedBetID,
		&s.Attempts, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrTransactionNotFound
	}
	if err != nil {
		return nil, classify(fmt.Errorf("postgres: read wager transaction: %w", err))
	}
	s.Origin, s.Kind, s.Status = wager.Origin(origin), wager.Kind(kind), wager.Status(status)
	if s.Money, err = moneyOf(amount, currency); err != nil {
		return nil, fmt.Errorf("postgres: wager transaction %s: %w", s.ID, err)
	}
	if balanceMinor != nil && balanceCurrency != nil {
		if s.BalanceAfter, err = moneyOf(*balanceMinor, *balanceCurrency); err != nil {
			return nil, fmt.Errorf("postgres: wager transaction %s: %w", s.ID, err)
		}
	}
	s.FailureCode = wager.FailureCode(deref(failureCode))
	s.ProviderID, s.ExternalTransactionID, s.IdempotencyKey = deref(providerID), deref(externalID), deref(key)
	s.PayloadHash, s.RoundID, s.GameID = deref(hash), deref(roundID), deref(gameID)
	s.ReferenceExternalTransactionID = deref(referenceExternal)
	if referenceID != nil {
		s.ReferenceTransactionID = *referenceID
	}
	if compensatedBetID != nil {
		s.CompensatedBetID = *compensatedBetID
	}
	return wager.Rehydrate(s)
}

func moneyOf(minor int64, code string) (money.Money, error) {
	c, err := money.ParseCurrency(code)
	if err != nil {
		return money.Money{}, err
	}
	return money.FromMinorUnits(minor, c)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// nullString maps "" to NULL.
func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullUUID maps uuid.Nil to NULL.
func nullUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// nullMoney maps the invalid zero money.Money to (NULL, NULL).
func nullMoney(m money.Money) (*int64, *string) {
	if !m.IsValid() {
		return nil, nil
	}
	minor, code := m.MinorUnits(), m.Currency().Code()
	return &minor, &code
}

// compensationConflict reports a violation of a Compensation Slot index: the
// Bet or the Reference was already compensated by another Reversal.
func compensationConflict(err error) bool {
	return uniqueViolationOn(err, txCompensatedBet) || uniqueViolationOn(err, txProcessedReversalReference)
}
