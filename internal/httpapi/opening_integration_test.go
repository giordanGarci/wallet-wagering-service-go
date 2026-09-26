//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

type moneyBody struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// outboxEnvelope is the Outbox Event snapshot stored in outbox_events.payload.
type outboxEnvelope struct {
	EventID       string          `json:"eventId"`
	EventType     string          `json:"eventType"`
	AggregateID   string          `json:"aggregateId"`
	CorrelationID string          `json:"correlationId"`
	OccurredAt    string          `json:"occurredAt"`
	Version       int             `json:"version"`
	Data          json.RawMessage `json:"data"`
}

type outboxRow struct {
	EventID     uuid.UUID
	AggregateID uuid.UUID
	EventType   string
	Version     int
	Payload     string
	Attempts    int
	Published   bool
}

func (e *env) outboxRows(t *testing.T, walletID uuid.UUID) []outboxRow {
	t.Helper()
	rows, err := e.db.Query(context.Background(), `
		SELECT event_id, aggregate_id, event_type, version, payload, attempts, published_at IS NOT NULL
		FROM outbox_events WHERE aggregate_id = $1 ORDER BY seq`, walletID)
	if err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.EventID, &r.AggregateID, &r.EventType, &r.Version, &r.Payload, &r.Attempts, &r.Published); err != nil {
			t.Fatalf("scan outbox: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("outbox rows: %v", err)
	}
	return out
}

func TestOpenWalletWithPositiveBalanceRecordsOpeningLedgerAndEvents(t *testing.T) {
	e := newEnv(t)
	playerID := uuid.New()
	const correlationID = "corr-opening-1"

	resp, body := e.app.DoWith(t, http.MethodPost, "/wallets", openBody(playerID, "1000.00", "BRL"), http.Header{
		"Authorization":    {"Bearer " + testsupport.Token(t, testsupport.InternalClient)},
		"X-Correlation-Id": {correlationID},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /wallets = %d %s, want 201", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Correlation-Id"); got != correlationID {
		t.Errorf("X-Correlation-Id = %q, want %q echoed", got, correlationID)
	}
	w := decode[walletBody](t, body)
	walletID := uuid.MustParse(w.ID)
	if w.Balance.Amount != "1000.00" || w.Balance.Currency != "BRL" || w.Version != 1 {
		t.Errorf("wallet = %+v, want 1000.00 BRL at version 1", w)
	}
	ctx := context.Background()

	var balance, version int64
	if err := e.db.QueryRow(ctx, "SELECT balance, version FROM wallets WHERE id = $1", walletID).Scan(&balance, &version); err != nil {
		t.Fatalf("read wallet: %v", err)
	}
	if balance != 100000 || version != 1 {
		t.Errorf("wallet row = (%d, %d), want (100000, 1)", balance, version)
	}

	// Exactly one Opening, PROCESSED, internal, without external metadata.
	if n := e.count(t, "SELECT count(*) FROM wager_transactions WHERE wallet_id = $1", walletID); n != 1 {
		t.Fatalf("wager transactions = %d, want 1", n)
	}
	var (
		txID                                   uuid.UUID
		origin, kind, status, currency         string
		txPlayer                               uuid.UUID
		amount, balanceAfter                   int64
		balanceAfterCurrency                   string
		attempts                               int
		externalMetadataAbsent, failureAbsent  bool
		referencesAbsent, pendingColumnsAbsent bool
	)
	err := e.db.QueryRow(ctx, `
		SELECT id, origin, kind, status, player_id, amount_minor, currency, balance_after_minor, balance_after_currency,
		       attempts,
		       num_nonnulls(provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
		                    reference_external_transaction_id) = 0,
		       failure_code IS NULL,
		       reference_transaction_id IS NULL AND compensated_bet_id IS NULL,
		       next_attempt_at IS NULL AND locked_until IS NULL
		FROM wager_transactions WHERE wallet_id = $1`, walletID).
		Scan(&txID, &origin, &kind, &status, &txPlayer, &amount, &currency, &balanceAfter, &balanceAfterCurrency,
			&attempts, &externalMetadataAbsent, &failureAbsent, &referencesAbsent, &pendingColumnsAbsent)
	if err != nil {
		t.Fatalf("read opening: %v", err)
	}
	if origin != "INTERNAL" || kind != "OPENING" || status != "PROCESSED" || txPlayer != playerID ||
		amount != 100000 || currency != "BRL" || balanceAfter != 100000 || balanceAfterCurrency != "BRL" || attempts != 0 {
		t.Errorf("opening = (%s %s %s player %s %d %s after %d %s attempts %d), want INTERNAL OPENING PROCESSED 100000 BRL after 100000 BRL",
			origin, kind, status, txPlayer, amount, currency, balanceAfter, balanceAfterCurrency, attempts)
	}
	if !externalMetadataAbsent || !failureAbsent || !referencesAbsent || !pendingColumnsAbsent {
		t.Errorf("opening carries external metadata, failure, references or pending columns")
	}
	if txID.Version() != 7 {
		t.Errorf("transaction id %s is not a UUIDv7", txID)
	}

	// Exactly one credit Ledger Entry for the Opening.
	if n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1", walletID); n != 1 {
		t.Fatalf("ledger entries = %d, want 1", n)
	}
	var (
		entryTx                    uuid.UUID
		direction, entryCurrency   string
		entryAmount, before, after int64
	)
	err = e.db.QueryRow(ctx, `
		SELECT transaction_id, direction, amount_minor, currency, balance_before_minor, balance_after_minor
		FROM ledger_entries WHERE wallet_id = $1`, walletID).
		Scan(&entryTx, &direction, &entryAmount, &entryCurrency, &before, &after)
	if err != nil {
		t.Fatalf("read ledger entry: %v", err)
	}
	if entryTx != txID || direction != "CREDIT" || entryAmount != 100000 || entryCurrency != "BRL" || before != 0 || after != 100000 {
		t.Errorf("ledger entry = (%s %s %d %s %d -> %d), want (%s CREDIT 100000 BRL 0 -> 100000)",
			entryTx, direction, entryAmount, entryCurrency, before, after, txID)
	}

	// Two unpublished Outbox Events, in order, with the exact snapshot.
	rows := e.outboxRows(t, walletID)
	if len(rows) != 2 {
		t.Fatalf("outbox rows = %d, want 2", len(rows))
	}
	wantTypes := []string{"WagerTransactionProcessed", "WalletBalanceChanged"}
	for i, r := range rows {
		if r.EventType != wantTypes[i] || r.AggregateID != walletID || r.Version != 1 || r.Attempts != 0 || r.Published {
			t.Errorf("outbox[%d] = %+v, want %s v1 for wallet %s, unpublished", i, r, wantTypes[i], walletID)
		}
		env := decode[outboxEnvelope](t, []byte(r.Payload))
		if env.EventID != r.EventID.String() || env.EventType != r.EventType || env.AggregateID != walletID.String() ||
			env.CorrelationID != correlationID || env.Version != 1 || env.OccurredAt == "" {
			t.Errorf("outbox[%d] envelope = %+v, want it to match the row and correlationId %q", i, env, correlationID)
		}
	}

	processed := decode[struct {
		TransactionID string    `json:"transactionId"`
		Origin        string    `json:"origin"`
		Kind          string    `json:"kind"`
		Status        string    `json:"status"`
		WalletID      string    `json:"walletId"`
		PlayerID      string    `json:"playerId"`
		Money         moneyBody `json:"money"`
		BalanceAfter  moneyBody `json:"balanceAfter"`
		ProviderID    *string   `json:"providerId"`
	}](t, decode[outboxEnvelope](t, []byte(rows[0].Payload)).Data)
	if processed.TransactionID != txID.String() || processed.Origin != "INTERNAL" || processed.Kind != "OPENING" ||
		processed.Status != "PROCESSED" || processed.WalletID != walletID.String() || processed.PlayerID != playerID.String() ||
		processed.Money != (moneyBody{"1000.00", "BRL"}) || processed.BalanceAfter != (moneyBody{"1000.00", "BRL"}) ||
		processed.ProviderID != nil {
		t.Errorf("WagerTransactionProcessed data = %+v", processed)
	}

	changed := decode[struct {
		WalletID      string    `json:"walletId"`
		TransactionID string    `json:"transactionId"`
		Direction     string    `json:"direction"`
		Money         moneyBody `json:"money"`
		BalanceBefore moneyBody `json:"balanceBefore"`
		BalanceAfter  moneyBody `json:"balanceAfter"`
		WalletVersion int64     `json:"walletVersion"`
	}](t, decode[outboxEnvelope](t, []byte(rows[1].Payload)).Data)
	if changed.WalletID != walletID.String() || changed.TransactionID != txID.String() || changed.Direction != "CREDIT" ||
		changed.Money != (moneyBody{"1000.00", "BRL"}) || changed.BalanceBefore != (moneyBody{"0.00", "BRL"}) ||
		changed.BalanceAfter != (moneyBody{"1000.00", "BRL"}) || changed.WalletVersion != 1 {
		t.Errorf("WalletBalanceChanged data = %+v", changed)
	}
}

func TestOpenWalletGeneratesCorrelationIDWhenAbsent(t *testing.T) {
	e := newEnv(t)
	resp, body := e.app.Do(t, http.MethodPost, "/wallets", testsupport.Token(t, testsupport.InternalClient),
		openBody(uuid.New(), "5.00", "BRL"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /wallets = %d %s, want 201", resp.StatusCode, body)
	}
	got := resp.Header.Get("X-Correlation-Id")
	id, err := uuid.Parse(got)
	if err != nil || id.Version() != 7 {
		t.Fatalf("X-Correlation-Id = %q, want a generated UUIDv7", got)
	}
	walletID := uuid.MustParse(decode[walletBody](t, body).ID)
	for i, r := range e.outboxRows(t, walletID) {
		if env := decode[outboxEnvelope](t, []byte(r.Payload)); env.CorrelationID != got {
			t.Errorf("outbox[%d] correlationId = %q, want %q", i, env.CorrelationID, got)
		}
	}
}

func TestOpenWalletRejectsInvalidCorrelationID(t *testing.T) {
	e := newEnv(t)
	for _, bad := range []string{"has space", strings.Repeat("a", 129)} {
		resp, body := e.app.DoWith(t, http.MethodPost, "/wallets", openBody(uuid.New(), "5.00", "BRL"), http.Header{
			"Authorization":    {"Bearer " + testsupport.Token(t, testsupport.InternalClient)},
			"X-Correlation-Id": {bad},
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST /wallets = %d %s, want 201 (invalid ids are replaced, not rejected)", resp.StatusCode, body)
		}
		if got := resp.Header.Get("X-Correlation-Id"); got == bad || uuid.MustParse(got).Version() != 7 {
			t.Errorf("X-Correlation-Id = %q, want a generated UUIDv7 replacing %q", got, bad)
		}
	}
}

func TestOpenWalletWithZeroBalanceRecordsNoFinancialRows(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "0.00")
	id := uuid.MustParse(w.ID)
	for _, q := range []string{
		"SELECT count(*) FROM wager_transactions WHERE wallet_id = $1",
		"SELECT count(*) FROM ledger_entries WHERE wallet_id = $1",
		"SELECT count(*) FROM outbox_events WHERE aggregate_id = $1",
	} {
		if n := e.count(t, q, id); n != 0 {
			t.Errorf("%s = %d, want 0", q, n)
		}
	}
}

func TestConcurrentPositiveOpeningsCreditOnce(t *testing.T) {
	e := newEnv(t)
	token := testsupport.Token(t, testsupport.InternalClient)
	body := openBody(uuid.New(), "50.00", "BRL")

	const n = 10
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			resp, _ := e.app.Do(t, http.MethodPost, "/wallets", token, body)
			codes[i] = resp.StatusCode
		})
	}
	wg.Wait()

	created := 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if created != 1 {
		t.Errorf("codes = %v, want exactly one 201", codes)
	}
	for q, want := range map[string]int{
		"SELECT count(*) FROM wallets":            1,
		"SELECT count(*) FROM wager_transactions": 1,
		"SELECT count(*) FROM ledger_entries":     1,
		"SELECT count(*) FROM outbox_events":      2,
	} {
		if got := e.count(t, q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
}
