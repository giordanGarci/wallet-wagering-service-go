# Implementation notes for agents

Conventions established while implementing `.scratch/wallet-wagering-service/`. Read before changing code; update when you add a convention.

## Environment (Windows host)

- Use Git Bash. No gcc on the host: `go test -race` only via `scripts/test-race.sh [-tags=integration] ./...` (Docker, joins `wallet_default`).
- Host port 8081 is taken by another process: run compose with `KEYCLOAK_PORT=8082` and host tests with `TEST_KEYCLOAK_URL=http://localhost:8082` (issuer follows `KEYCLOAK_PORT`).
- Test infra: `KEYCLOAK_PORT=8082 docker compose up -d postgres keycloak ministack migrate sqs-init`. Tear down with `docker compose down -v` when done.
- Several agents may work at once. Only `git add` the files you changed; never `git add -A`.

## Layout

- `cmd/app` entry; `internal/bootstrap` Fx composition (`roleModules` map for `api`, `consumer`, `outbox`, `pending`).
- `internal/config` (`config.Load`, `cfg.OIDC`, `cfg.SQS`, `cfg.Roles`, `cfg.ShutdownTimeout`).
- `internal/domain/{money,wallet,ledger,wager,event}`: pure domain, stdlib + uuid only (a hygiene test enforces it).
- `internal/app`: use cases and ports (`UnitOfWork.Do(ctx, func(ctx, tx app.Tx) error)`, `Tx` exposes repositories), sentinels `ErrInvalidCommand`, `ErrWalletNotFound`, `ErrWalletAlreadyExists`, `ErrTransient`.
- `internal/postgres`: pgx repositories, `UnitOfWork` (`pgx.BeginFunc`), `classify()` maps retryable errors to `app.ErrTransient`, constraint names map to typed errors.
- `internal/httpapi`: routes via `httpapi.AsRoute(ctor)` in `httpapi.Module`; auth via `auth.Internal(h)` / `auth.Provider(h)`; `httpapi.PrincipalFrom(ctx)`; errors via `WriteError`, `WriteAppError` (extend its switch), strict `DecodeJSON`.
- `internal/health` readiness via `health.AsCheck`; `internal/observability` gives a per-app `*prometheus.Registry` (never the default registerer) and JSON `*slog.Logger`.
- `internal/sqs` SQS client module; `internal/worker` for outbox and pending workers; `migrations/NNNNNN_desc.{up,down}.sql` (golang-migrate), embedded via `migrations.FS`.

## Domain API cheat sheet

- `wager.NewCommand(CommandInput)` (400 on error, `wager.ErrInvalidCommand`), `wager.PayloadHash`, `wager.CheckReplay(existing, cmd)`.
- `wager.NewExternal(cmd, now)`; `tx.Apply(wallet /*nil = not found*/, wager.Reference{Transaction, Compensated}, now) (*ledger.Entry, error)`: business rejections are recorded on tx (nil error); read `tx.Status()` after.
- `wager.Open(wallet, initial, now)` returns the PROCESSED Opening + credit entry (nil, nil for zero); `wallet.ApplyOpening` keeps version 1.
- `tx.RecordReferenceMiss(policy, now)` for the pending worker; `wager.DefaultPendingPolicy()`.
- `tx.Snapshot()` / `wager.Rehydrate(Snapshot)` map to persisted columns.
- `compensatedBetID` is set only on PROCESSED REFUND/ROLLBACK of a BET: index `UNIQUE (compensated_bet_id) WHERE compensated_bet_id IS NOT NULL`. Also `UNIQUE (reference_transaction_id) WHERE kind IN ('REFUND','ROLLBACK') AND status='PROCESSED'`.
- `event.ForTransaction(tx, entry, walletVersion, meta)` gives the Outbox Events for a transaction; `aggregateId` is always the walletId; `Event.MarshalJSON` is the snapshot payload.
- `wager.FailurePermanentError` (`PERMANENT_ERROR`) is the Failure Code for FAILED.

## Testing

- Integration tests: `//go:build integration`, `testsupport.NewInfra(t)` (isolated DB + queues), `testsupport.StartApp(t, cfg)`, `testsupport.Token(t, testsupport.ProviderAClient)`, `app.Do(t, method, path, token, body)`, `testsupport.NewForger(t)` for crafted tokens.
- Observe behaviour only: HTTP responses, queue messages, DB rows via SQL. No repository mocks.
