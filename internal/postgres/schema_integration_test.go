//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

// schema is an isolated, migrated database with a Wallet holding a
// PROCESSED Opening and its credit Ledger Entry. owner connects as the
// schema owner (TEST_DATABASE_URL user), app as the wallet_app role.
type schema struct {
	owner, app *pgxpool.Pool
	walletID   uuid.UUID
	playerID   uuid.UUID
	openingID  uuid.UUID
	entryID    uuid.UUID
}

func newSchema(t *testing.T) *schema {
	t.Helper()
	infra := testsupport.NewInfra(t)
	s := &schema{
		owner:     testsupport.OpenPool(t, infra.DatabaseURL),
		app:       testsupport.OpenPool(t, infra.AppDatabaseURL),
		walletID:  uuid.Must(uuid.NewV7()),
		playerID:  uuid.New(),
		openingID: uuid.Must(uuid.NewV7()),
		entryID:   uuid.Must(uuid.NewV7()),
	}
	s.mustExec(t, s.owner, `INSERT INTO wallets (id, player_id, currency, balance, version, created_at, updated_at)
		VALUES ($1, $2, 'BRL', 10000, 1, now(), now())`, s.walletID, s.playerID)
	s.mustExec(t, s.owner, insertOpening, s.openingID, s.walletID, s.playerID)
	s.mustExec(t, s.owner, insertEntry, s.entryID, s.walletID, s.openingID, "CREDIT", 10000, 0, 10000)
	return s
}

const (
	insertOpening = `INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
		balance_after_minor, balance_after_currency, created_at, updated_at)
		VALUES ($1, 'INTERNAL', 'OPENING', 'PROCESSED', $2, $3, 10000, 'BRL', 10000, 'BRL', now(), now())`
	insertEntry = `INSERT INTO ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency,
		balance_before_minor, balance_after_minor, created_at)
		VALUES ($1, $2, $3, $4, $5, 'BRL', $6, $7, now())`
	// insertExternal takes id, wallet, player, kind, status, provider,
	// external id, idempotency key, reference tx, compensated bet.
	insertExternal = `INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
		balance_after_minor, balance_after_currency, provider_id, external_transaction_id, idempotency_key, payload_hash,
		round_id, game_id, reference_external_transaction_id, reference_transaction_id, compensated_bet_id, created_at, updated_at)
		VALUES ($1, 'EXTERNAL', $4, $5, $2, $3, 100, 'BRL', 10000, 'BRL', $6, $7, $8, repeat('b', 64), 'round-1', 'game-1',
		CASE WHEN $9::uuid IS NULL THEN NULL ELSE 'ref-' || $9::text END, $9, $10, now(), now())`
)

func (s *schema) mustExec(t *testing.T, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", strings.Join(strings.Fields(sql), " "), err)
	}
}

// expectPgError asserts err is a PostgreSQL error with code and, when
// constraint is not empty, that constraint name.
func expectPgError(t *testing.T, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err = %v, want PostgreSQL error %s", err, code)
	}
	if pgErr.Code != code || (constraint != "" && pgErr.ConstraintName != constraint) {
		t.Fatalf("err = %s %q (constraint %q), want %s on %q", pgErr.Code, pgErr.Message, pgErr.ConstraintName, code, constraint)
	}
}

func TestLedgerRejectsUpdateDeleteAndTruncateEvenForTheOwner(t *testing.T) {
	s := newSchema(t)
	ctx := context.Background()
	for _, sql := range []string{
		"UPDATE ledger_entries SET amount_minor = 1 WHERE id = $1",
		"DELETE FROM ledger_entries WHERE id = $1",
	} {
		_, err := s.owner.Exec(ctx, sql, s.entryID)
		expectPgError(t, err, "P0001", "")
		if !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: error %q does not explain the ledger is append-only", sql, err)
		}
	}
	_, err := s.owner.Exec(ctx, "TRUNCATE ledger_entries")
	expectPgError(t, err, "P0001", "")

	var amount int64
	if err := s.owner.QueryRow(ctx, "SELECT amount_minor FROM ledger_entries WHERE id = $1", s.entryID).Scan(&amount); err != nil || amount != 10000 {
		t.Errorf("entry amount = %d (%v), want the original 10000", amount, err)
	}
}

func TestAppRoleHasLeastPrivilege(t *testing.T) {
	s := newSchema(t)
	ctx := context.Background()

	var user string
	if err := s.app.QueryRow(ctx, "SELECT current_user").Scan(&user); err != nil || user != testsupport.AppRole {
		t.Fatalf("app pool user = %q (%v), want %s", user, err, testsupport.AppRole)
	}

	// Ledger: INSERT and SELECT only.
	var n int
	if err := s.app.QueryRow(ctx, "SELECT count(*) FROM ledger_entries").Scan(&n); err != nil || n != 1 {
		t.Errorf("app SELECT ledger = %d (%v), want 1", n, err)
	}
	for _, sql := range []string{
		"UPDATE ledger_entries SET amount_minor = 1 WHERE id = $1",
		"DELETE FROM ledger_entries WHERE id = $1",
	} {
		_, err := s.app.Exec(ctx, sql, s.entryID)
		expectPgError(t, err, "42501", "")
	}
	_, err := s.app.Exec(ctx, "TRUNCATE ledger_entries")
	expectPgError(t, err, "42501", "")

	winID := uuid.Must(uuid.NewV7())
	s.mustExec(t, s.app, insertExternal, winID, s.walletID, s.playerID, "WIN", "PROCESSED", "provider-a", "win-1", "key-1", nil, nil)
	s.mustExec(t, s.app, insertEntry, uuid.Must(uuid.NewV7()), s.walletID, winID, "CREDIT", 100, 10000, 10100)

	// The other tables: no DELETE, no DDL.
	for _, sql := range []string{
		"DELETE FROM wallets",
		"DELETE FROM wager_transactions",
		"DELETE FROM outbox_events",
		"DELETE FROM inbox_messages",
		"CREATE TABLE app_owned (id int)",
		"DROP TABLE ledger_entries",
	} {
		_, err := s.app.Exec(ctx, sql)
		expectPgError(t, err, "42501", "")
	}

	// What the service needs does work.
	s.mustExec(t, s.app, "UPDATE wallets SET balance = 10100, version = 2 WHERE id = $1", s.walletID)
	s.mustExec(t, s.app, "UPDATE wager_transactions SET updated_at = now() WHERE id = $1", winID)
	s.mustExec(t, s.app, `INSERT INTO outbox_events (event_id, aggregate_id, event_type, version, correlation_id, payload, occurred_at)
		VALUES ($1, $2, 'WalletBalanceChanged', 1, 'c-1', '{"eventId":"x"}', now())`, uuid.Must(uuid.NewV7()), s.walletID)
	s.mustExec(t, s.app, "UPDATE outbox_events SET published_at = now(), attempts = attempts + 1")
	s.mustExec(t, s.app, `INSERT INTO inbox_messages (consumer_name, message_id, body_hash, received_at)
		VALUES ('wager-consumer', 'm-1', repeat('c', 64), now())`)
	s.mustExec(t, s.app, "UPDATE inbox_messages SET completed_at = now(), transaction_id = $1", winID)
}

func TestWagerTransactionConstraints(t *testing.T) {
	s := newSchema(t)
	ctx := context.Background()
	id := func() uuid.UUID { return uuid.Must(uuid.NewV7()) }

	betID := id()
	s.mustExec(t, s.owner, insertExternal, betID, s.walletID, s.playerID, "BET", "PROCESSED", "provider-a", "bet-1", "key-bet-1", nil, nil)
	refundID := id()
	s.mustExec(t, s.owner, insertExternal, refundID, s.walletID, s.playerID, "REFUND", "PROCESSED", "provider-a", "refund-1", "key-refund-1", betID, betID)

	tests := []struct {
		name, sql, code, constraint string
		args                        []any
	}{
		{
			name: "second opening for the wallet",
			sql:  insertOpening, args: []any{id(), s.walletID, s.playerID},
			code: "23505", constraint: "wager_transactions_one_opening_per_wallet",
		},
		{
			name: "opening with external metadata",
			sql: `INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
				balance_after_minor, balance_after_currency, provider_id, created_at, updated_at)
				VALUES ($1, 'INTERNAL', 'OPENING', 'PROCESSED', $2, $3, 1, 'BRL', 1, 'BRL', 'provider-a', now(), now())`,
			args: []any{id(), uuid.Must(uuid.NewV7()), s.playerID},
			code: "23514", constraint: "wager_transactions_origin_metadata",
		},
		{
			name: "external opening",
			sql:  insertExternal, args: []any{id(), s.walletID, s.playerID, "OPENING", "PROCESSED", "provider-a", "open-x", "key-open-x", nil, nil},
			code: "23514", constraint: "wager_transactions_origin_metadata",
		},
		{
			name: "external without metadata",
			sql: `INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
				created_at, updated_at)
				VALUES ($1, 'EXTERNAL', 'BET', 'PENDING', $2, $3, 1, 'BRL', now(), now())`,
			args: []any{id(), s.walletID, s.playerID},
			code: "23514", constraint: "wager_transactions_origin_metadata",
		},
		{
			name: "same idempotency key for the provider",
			sql:  insertExternal, args: []any{id(), s.walletID, s.playerID, "BET", "PROCESSED", "provider-a", "bet-2", "key-bet-1", nil, nil},
			code: "23505", constraint: "wager_transactions_provider_idempotency_key",
		},
		{
			name: "same external transaction id for the provider",
			sql:  insertExternal, args: []any{id(), s.walletID, s.playerID, "BET", "PROCESSED", "provider-a", "bet-1", "key-bet-3", nil, nil},
			code: "23505", constraint: "wager_transactions_provider_external_transaction_id",
		},
		{
			name: "second compensation of the same bet",
			sql:  insertExternal, args: []any{id(), s.walletID, s.playerID, "ROLLBACK", "PROCESSED", "provider-a", "rollback-1", "key-rollback-1", refundID, betID},
			code: "23505", constraint: "wager_transactions_compensated_bet_key",
		},
		{
			name: "second processed reversal of the same reference",
			sql:  insertExternal, args: []any{id(), s.walletID, s.playerID, "ROLLBACK", "PROCESSED", "provider-a", "rollback-2", "key-rollback-2", betID, nil},
			code: "23505", constraint: "wager_transactions_processed_reversal_reference_key",
		},
		{
			name: "rejected without failure code",
			sql:  insertExternal, args: []any{id(), s.walletID, s.playerID, "BET", "REJECTED", "provider-a", "bet-9", "key-bet-9", nil, nil},
			code: "23514", constraint: "wager_transactions_failure_code_status",
		},
		{
			name: "unknown kind",
			sql:  insertExternal, args: []any{id(), s.walletID, s.playerID, "DEPOSIT", "PROCESSED", "provider-a", "dep-1", "key-dep-1", nil, nil},
			code: "23514", constraint: "wager_transactions_kind_check",
		},
		{
			name: "ledger entry with inconsistent balances",
			sql:  insertEntry, args: []any{id(), s.walletID, betID, "DEBIT", 100, 10000, 10000},
			code: "23514", constraint: "ledger_entries_balance_arithmetic",
		},
		{
			name: "second ledger entry for the same transaction",
			sql:  insertEntry, args: []any{id(), s.walletID, s.openingID, "CREDIT", 1, 10000, 10001},
			code: "23505", constraint: "ledger_entries_wallet_transaction_key",
		},
		{
			name: "ledger entry with zero amount",
			sql:  insertEntry, args: []any{id(), s.walletID, refundID, "CREDIT", 0, 10000, 10000},
			code: "23514", constraint: "ledger_entries_amount_positive",
		},
		{
			name: "outbox payload that is not a JSON object",
			sql: `INSERT INTO outbox_events (event_id, aggregate_id, event_type, version, correlation_id, payload, occurred_at)
				VALUES ($1, $2, 'WalletBalanceChanged', 1, 'c', 'not json', now())`,
			args: []any{id(), s.walletID},
			code: "23514", constraint: "outbox_events_payload_json",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.owner.Exec(ctx, tt.sql, tt.args...)
			expectPgError(t, err, tt.code, tt.constraint)
		})
	}

	// The same idempotency key under another provider is a different operation.
	s.mustExec(t, s.owner, insertExternal, id(), s.walletID, s.playerID, "BET", "PROCESSED", "provider-b", "bet-1", "key-bet-1", nil, nil)

	s.mustExec(t, s.owner, `INSERT INTO inbox_messages (consumer_name, message_id, body_hash, received_at)
		VALUES ('wager-consumer', 'm-1', repeat('c', 64), now())`)
	_, err := s.owner.Exec(ctx, `INSERT INTO inbox_messages (consumer_name, message_id, body_hash, received_at)
		VALUES ('wager-consumer', 'm-1', repeat('d', 64), now())`)
	expectPgError(t, err, "23505", "inbox_messages_consumer_message_key")
}

func TestOutboxDefaultsSchedulePublicationAtInsert(t *testing.T) {
	s := newSchema(t)
	ctx := context.Background()
	payload := `{"eventId":"e","data":{"money":{"amount":"1.00","currency":"BRL"}}}`
	s.mustExec(t, s.app, `INSERT INTO outbox_events (event_id, aggregate_id, event_type, version, correlation_id, payload, occurred_at)
		VALUES ($1, $2, 'WalletBalanceChanged', 1, 'c', $3, now())`, uuid.Must(uuid.NewV7()), s.walletID, payload)

	var (
		stored              string
		attempts            int
		nextAttempt, create time.Time
		locked, published   bool
	)
	err := s.owner.QueryRow(ctx, `SELECT payload, attempts, next_attempt_at, created_at,
		locked_until IS NOT NULL, published_at IS NOT NULL FROM outbox_events`).
		Scan(&stored, &attempts, &nextAttempt, &create, &locked, &published)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if stored != payload {
		t.Errorf("payload = %q, want the exact bytes %q", stored, payload)
	}
	if attempts != 0 || !nextAttempt.Equal(create) || locked || published {
		t.Errorf("outbox row = attempts %d next %s created %s locked %v published %v, want due now and unclaimed",
			attempts, nextAttempt, create, locked, published)
	}
}
