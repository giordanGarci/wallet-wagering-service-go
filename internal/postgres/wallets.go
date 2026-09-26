package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

// DBTX is what repositories need from a connection: *pgxpool.Pool outside a
// transaction, pgx.Tx inside one.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const walletPlayerCurrencyKey = "wallets_player_currency_key"

// WalletRepository implements app.WalletRepository with explicit SQL.
type WalletRepository struct {
	db DBTX
}

// NewWalletRepository binds the repository to db (pool or transaction).
func NewWalletRepository(db DBTX) *WalletRepository {
	return &WalletRepository{db: db}
}

var _ app.WalletRepository = (*WalletRepository)(nil)

// Insert stores a new Wallet; the (player, currency) unique constraint maps
// to app.ErrWalletAlreadyExists.
func (r *WalletRepository) Insert(ctx context.Context, w *wallet.Wallet) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO wallets (id, player_id, currency, balance, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		w.ID(), w.PlayerID(), w.Currency().Code(), w.Balance().MinorUnits(), w.Version(), w.CreatedAt(), w.UpdatedAt())
	if uniqueViolationOn(err, walletPlayerCurrencyKey) {
		return fmt.Errorf("%w: player %s already has a %s wallet", app.ErrWalletAlreadyExists, w.PlayerID(), w.Currency())
	}
	if err != nil {
		return classify(fmt.Errorf("postgres: insert wallet: %w", err))
	}
	return nil
}

// Get reads a Wallet by id.
func (r *WalletRepository) Get(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, player_id, currency, balance, version, created_at, updated_at
		FROM wallets WHERE id = $1`, id)
	return scanWallet(row)
}

func scanWallet(row pgx.Row) (*wallet.Wallet, error) {
	var (
		id, playerID         uuid.UUID
		currencyCode         string
		balance, version     int64
		createdAt, updatedAt time.Time
	)
	err := row.Scan(&id, &playerID, &currencyCode, &balance, &version, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrWalletNotFound
	}
	if err != nil {
		return nil, classify(fmt.Errorf("postgres: read wallet: %w", err))
	}
	currency, err := money.ParseCurrency(currencyCode)
	if err != nil {
		return nil, fmt.Errorf("postgres: wallet %s: %w", id, err)
	}
	amount, err := money.FromMinorUnits(balance, currency)
	if err != nil {
		return nil, fmt.Errorf("postgres: wallet %s: %w", id, err)
	}
	return wallet.Rehydrate(id, playerID, amount, version, createdAt, updatedAt)
}

// GetForUpdate reads a Wallet and locks its row until the enclosing
// transaction ends (ADR-0003).
func (r *WalletRepository) GetForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, player_id, currency, balance, version, created_at, updated_at
		FROM wallets WHERE id = $1 FOR UPDATE`, id)
	return scanWallet(row)
}

// Update writes the balance, version and updatedAt of w, guarded by the
// version read under the lock. Zero affected rows is a lost update in the
// making and fails with app.ErrConcurrencyConflict.
func (r *WalletRepository) Update(ctx context.Context, w *wallet.Wallet, expectedVersion int64) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE wallets SET balance = $2, version = $3, updated_at = $4
		WHERE id = $1 AND version = $5`,
		w.ID(), w.Balance().MinorUnits(), w.Version(), w.UpdatedAt(), expectedVersion)
	if err != nil {
		return classify(fmt.Errorf("postgres: update wallet %s: %w", w.ID(), err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: wallet %s is no longer at version %d", app.ErrConcurrencyConflict, w.ID(), expectedVersion)
	}
	return nil
}
