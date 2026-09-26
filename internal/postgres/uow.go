package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junglegaming/backend-challenge-go/internal/app"
)

// UnitOfWork implements app.UnitOfWork with one pgx transaction per Do.
type UnitOfWork struct {
	pool *pgxpool.Pool
}

// NewUnitOfWork builds the unit of work over the pool.
func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork {
	return &UnitOfWork{pool: pool}
}

var _ app.UnitOfWork = (*UnitOfWork)(nil)

// Do runs fn in a READ COMMITTED transaction, committing when fn returns nil
// and rolling back otherwise (including on panic). Retryable failures come
// back wrapped with app.ErrTransient.
func (u *UnitOfWork) Do(ctx context.Context, fn func(ctx context.Context, tx app.Tx) error) error {
	return classify(pgx.BeginFunc(ctx, u.pool, func(tx pgx.Tx) error {
		return fn(ctx, txRepos{tx: tx})
	}))
}

// txRepos hands out repositories bound to one transaction.
type txRepos struct {
	tx pgx.Tx
}

func (r txRepos) Wallets() app.WalletRepository           { return NewWalletRepository(r.tx) }
func (r txRepos) Transactions() app.TransactionRepository { return NewTransactionRepository(r.tx) }
func (r txRepos) Ledger() app.LedgerRepository            { return NewLedgerRepository(r.tx) }
func (r txRepos) Outbox() app.OutboxRepository            { return NewOutboxRepository(r.tx) }
func (r txRepos) Pending() app.PendingReferenceRepository {
	return NewPendingReferenceRepository(r.tx)
}
func (r txRepos) Inbox() app.InboxRepository { return NewInboxRepository(r.tx) }
