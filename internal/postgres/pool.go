// Package postgres owns the pgx connection pool and the adapters built on
// it: repositories with explicit SQL, the unit of work, and the mapping of
// constraint violations and transient failures to app errors.
package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/health"
)

// Module provides *pgxpool.Pool, the "postgres" readiness check, the
// app.UnitOfWork and the pool-bound app.WalletRepository and
// app.LedgerRepository (reads outside a transaction).
//
// The pool is created lazily in the constructor; OnStart pings it so the
// app fails fast when the database is unreachable, and OnStop closes it.
// Because every component that uses the pool depends on it, Fx appends their
// hooks after this one and stops them first, so the pool closes last.
var Module = fx.Module("postgres",
	fx.Provide(
		NewPool,
		health.AsCheck(NewReadinessCheck),
		fx.Annotate(NewUnitOfWork, fx.As(new(app.UnitOfWork))),
		fx.Annotate(func(pool *pgxpool.Pool) *WalletRepository { return NewWalletRepository(pool) },
			fx.As(new(app.WalletRepository))),
		fx.Annotate(func(pool *pgxpool.Pool) *LedgerRepository { return NewLedgerRepository(pool) },
			fx.As(new(app.LedgerRepository))),
		fx.Annotate(func(pool *pgxpool.Pool) *TransactionRepository { return NewTransactionRepository(pool) },
			fx.As(new(app.TransactionRepository))),
		fx.Annotate(NewReconciliationReader, fx.As(new(app.ReconciliationReader))),
	),
)

// NewPool builds the pool and registers its lifecycle hooks.
func NewPool(lc fx.Lifecycle, cfg config.Config, log *slog.Logger) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse DATABASE_URL: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), pc)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", err)
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := pool.Ping(ctx); err != nil {
				pool.Close()
				return fmt.Errorf("postgres: ping: %w", err)
			}
			log.InfoContext(ctx, "postgres pool ready",
				"host", pc.ConnConfig.Host, "database", pc.ConnConfig.Database, "max_conns", pc.MaxConns)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			pool.Close()
			log.InfoContext(ctx, "postgres pool closed")
			return nil
		},
	})
	return pool, nil
}

// NewReadinessCheck pings the database.
func NewReadinessCheck(pool *pgxpool.Pool) health.Check {
	return health.Check{Name: "postgres", Check: pool.Ping}
}
