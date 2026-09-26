//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

func TestBetProcessedDebitsWalletWithLedgerAndEvents(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "1000.00")
	b := bet(w, "25.00")
	const correlationID = "corr-bet-1"

	resp, body := e.app.DoWith(t, http.MethodPost, "/wagering/transactions", b.json(t), http.Header{
		"Authorization":    {"Bearer " + testsupport.Token(t, testsupport.ProviderAClient)},
		"Idempotency-Key":  {b.key()},
		"X-Correlation-Id": {correlationID},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST = %d %s, want 200", resp.StatusCode, body)
	}
	got := decode[wagerResponse](t, body)
	if got.Status != "PROCESSED" || got.IdempotentReplay || got.FailureCode != "" {
		t.Errorf("response = %+v, want PROCESSED, no replay, no failureCode", got)
	}
	if got.Balance == nil || *got.Balance != (moneyBody{"975.00", "BRL"}) {
		t.Errorf("balance = %+v, want 975.00 BRL", got.Balance)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(body, &raw)
	for _, k := range []string{"transactionId", "status", "balance", "idempotentReplay"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("response lacks %q: %s", k, body)
		}
	}
	if _, ok := raw["failureCode"]; ok {
		t.Errorf("processed response carries failureCode: %s", body)
	}

	balance, version := e.walletState(t, w.ID)
	if balance != 97500 || version != 2 {
		t.Errorf("wallet = (%d, v%d), want (97500, v2)", balance, version)
	}

	var status, idemKey, hash string
	var balanceAfter int64
	err := e.db.QueryRow(context.Background(), `
		SELECT status, idempotency_key, payload_hash, balance_after_minor
		FROM wager_transactions WHERE id = $1 AND provider_id = 'provider-a' AND kind = 'BET'`,
		got.TransactionID).Scan(&status, &idemKey, &hash, &balanceAfter)
	if err != nil {
		t.Fatalf("read transaction: %v", err)
	}
	if status != "PROCESSED" || idemKey != b.key() || len(hash) != 64 || balanceAfter != 97500 {
		t.Errorf("row = (%s, %q, %q, %d)", status, idemKey, hash, balanceAfter)
	}

	var dir string
	var amount, before, after int64
	err = e.db.QueryRow(context.Background(), `
		SELECT direction, amount_minor, balance_before_minor, balance_after_minor
		FROM ledger_entries WHERE transaction_id = $1 AND wallet_id = $2`, got.TransactionID, w.ID).
		Scan(&dir, &amount, &before, &after)
	if err != nil {
		t.Fatalf("read ledger entry: %v", err)
	}
	if dir != "DEBIT" || amount != 2500 || before != 100000 || after != 97500 {
		t.Errorf("ledger entry = (%s, %d, %d, %d), want DEBIT 2500 100000 97500", dir, amount, before, after)
	}

	rows := e.outboxRows(t, uuid.MustParse(w.ID))
	if len(rows) != 4 {
		t.Fatalf("outbox has %d events for the wallet, want 4 (opening + bet)", len(rows))
	}
	if rows[2].EventType != "WagerTransactionProcessed" || rows[3].EventType != "WalletBalanceChanged" {
		t.Fatalf("bet events = %s, %s", rows[2].EventType, rows[3].EventType)
	}
	for _, r := range rows[2:] {
		env := decode[outboxEnvelope](t, []byte(r.Payload))
		if env.CorrelationID != correlationID {
			t.Errorf("%s correlationId = %q, want %q", r.EventType, env.CorrelationID, correlationID)
		}
		data := decode[struct {
			TransactionID string    `json:"transactionId"`
			WalletVersion int64     `json:"walletVersion"`
			BalanceAfter  moneyBody `json:"balanceAfter"`
			Direction     string    `json:"direction"`
		}](t, env.Data)
		if data.TransactionID != got.TransactionID {
			t.Errorf("%s transactionId = %s, want %s", r.EventType, data.TransactionID, got.TransactionID)
		}
		if r.EventType == "WalletBalanceChanged" &&
			(data.WalletVersion != 2 || data.Direction != "DEBIT" || data.BalanceAfter != (moneyBody{"975.00", "BRL"})) {
			t.Errorf("WalletBalanceChanged data = %+v", data)
		}
	}
	e.assertReconciled(t, w.ID)

	status2, metrics := e.app.Get(t, "/metrics")
	if status2 != http.StatusOK || !strings.Contains(string(metrics), `wallet_wager_transactions_total{kind="BET",status="PROCESSED"} 1`) {
		t.Errorf("metrics lack the processed BET counter")
	}
}

func TestBetAboveBalanceIsRejectedWithInsufficientFunds(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "10.00")

	got := e.send(t, bet(w, "10.01"), http.StatusUnprocessableEntity)
	if got.Status != "REJECTED" || got.FailureCode != "INSUFFICIENT_FUNDS" || got.IdempotentReplay {
		t.Errorf("response = %+v, want REJECTED INSUFFICIENT_FUNDS", got)
	}
	if got.Balance == nil || *got.Balance != (moneyBody{"10.00", "BRL"}) {
		t.Errorf("balance = %+v, want 10.00 BRL observed at rejection", got.Balance)
	}
	if n := e.count(t, "SELECT count(*) FROM wager_transactions WHERE id = $1 AND status = 'REJECTED' AND failure_code = 'INSUFFICIENT_FUNDS'", got.TransactionID); n != 1 {
		t.Errorf("REJECTED rows = %d, want 1", n)
	}
	if n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", got.TransactionID); n != 0 {
		t.Errorf("ledger entries for the rejection = %d, want 0", n)
	}
	balance, version := e.walletState(t, w.ID)
	if balance != 1000 || version != 1 {
		t.Errorf("wallet = (%d, v%d), want untouched (1000, v1)", balance, version)
	}
	rows := e.outboxRows(t, uuid.MustParse(w.ID))
	if len(rows) != 3 || rows[2].EventType != "WagerTransactionRejected" {
		t.Fatalf("outbox = %+v, want opening events + WagerTransactionRejected", rows)
	}
	data := decode[struct {
		FailureCode string `json:"failureCode"`
	}](t, decode[outboxEnvelope](t, []byte(rows[2].Payload)).Data)
	if data.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Errorf("rejected event failureCode = %q", data.FailureCode)
	}
	e.assertReconciled(t, w.ID)
}

func TestBetBusinessRejections(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "50.00")

	unknown := bet(w, "1.00")
	unknown.WalletID = uuid.NewString()
	otherPlayer := bet(w, "1.00")
	otherPlayer.PlayerID = uuid.NewString()
	usd := bet(w, "1.00")
	usd.Money = json.RawMessage(`{"amount":"1.00","currency":"USD"}`)

	for _, tc := range []struct {
		name    string
		body    wager
		code    string
		balance *moneyBody
	}{
		{"wallet not found", unknown, "WALLET_NOT_FOUND", nil},
		{"player mismatch", otherPlayer, "WALLET_PLAYER_MISMATCH", nil},
		{"currency mismatch", usd, "CURRENCY_MISMATCH", &moneyBody{"50.00", "BRL"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := e.send(t, tc.body, http.StatusUnprocessableEntity)
			if got.Status != "REJECTED" || got.FailureCode != tc.code {
				t.Fatalf("response = %+v, want REJECTED %s", got, tc.code)
			}
			if (got.Balance == nil) != (tc.balance == nil) || (tc.balance != nil && *got.Balance != *tc.balance) {
				t.Errorf("balance = %+v, want %+v", got.Balance, tc.balance)
			}
			if n := e.count(t, "SELECT count(*) FROM wager_transactions WHERE id = $1 AND status = 'REJECTED' AND failure_code = $2 AND wallet_id = $3",
				got.TransactionID, tc.code, tc.body.WalletID); n != 1 {
				t.Errorf("REJECTED rows = %d, want 1", n)
			}
			if n := e.count(t, "SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionRejected' AND payload::jsonb->'data'->>'transactionId' = $1", got.TransactionID); n != 1 {
				t.Errorf("rejected events = %d, want 1", n)
			}
		})
	}
	if n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1", w.ID); n != 1 {
		t.Errorf("ledger entries = %d, want only the opening", n)
	}
	balance, version := e.walletState(t, w.ID)
	if balance != 5000 || version != 1 {
		t.Errorf("wallet = (%d, v%d), want untouched", balance, version)
	}
	e.assertReconciled(t, w.ID)
}

func TestInvalidWagerRequestsAreRejectedWithoutRecord(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")

	valid := bet(w, "1.00")
	with := func(f func(*wager)) string {
		b := valid
		f(&b)
		return b.json(t)
	}
	cases := []struct {
		name string
		key  string
		body string
	}{
		{"missing idempotency key", "", valid.json(t)},
		{"empty body", valid.key(), ""},
		{"malformed json", valid.key(), `{"providerId":`},
		{"unknown field", valid.key(), strings.Replace(valid.json(t), `{`, `{"extra":1,`, 1)},
		{"trailing data", valid.key(), valid.json(t) + `{}`},
		{"numeric amount", valid.key(), with(func(b *wager) { b.Money = json.RawMessage(`{"amount":1.00,"currency":"BRL"}`) })},
		{"three decimals", valid.key(), with(func(b *wager) { b.Money = json.RawMessage(`{"amount":"1.000","currency":"BRL"}`) })},
		{"scientific notation", valid.key(), with(func(b *wager) { b.Money = json.RawMessage(`{"amount":"1e2","currency":"BRL"}`) })},
		{"negative amount", valid.key(), with(func(b *wager) { b.Money = json.RawMessage(`{"amount":"-1.00","currency":"BRL"}`) })},
		{"unknown currency", valid.key(), with(func(b *wager) { b.Money = json.RawMessage(`{"amount":"1.00","currency":"XXX"}`) })},
		{"null money", valid.key(), with(func(b *wager) { b.Money = json.RawMessage(`null`) })},
		{"zero amount", valid.key(), with(func(b *wager) { b.Money = json.RawMessage(`{"amount":"0.00","currency":"BRL"}`) })},
		{"missing money", valid.key(), with(func(b *wager) { b.Money = nil })},
		{"opening kind", valid.key(), with(func(b *wager) { b.Kind = "OPENING" })},
		{"unknown kind", valid.key(), with(func(b *wager) { b.Kind = "JACKPOT" })},
		{"missing kind", valid.key(), with(func(b *wager) { b.Kind = "" })},
		{"missing providerId", valid.key(), with(func(b *wager) { b.ProviderID = "" })},
		{"missing externalTransactionId", valid.key(), with(func(b *wager) { b.ExternalTransactionID = "" })},
		{"missing playerId", valid.key(), with(func(b *wager) { b.PlayerID = "" })},
		{"invalid walletId", valid.key(), with(func(b *wager) { b.WalletID = "not-a-uuid" })},
		{"missing roundId", valid.key(), with(func(b *wager) { b.RoundID = "" })},
		{"missing gameId", valid.key(), with(func(b *wager) { b.GameID = "" })},
		{"reference on BET", valid.key(), with(func(b *wager) { b.Reference = "other" })},
		{"numeric providerId", valid.key(), strings.Replace(valid.json(t), `"providerId":"provider-a"`, `"providerId":7`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := e.postWager(t, testsupport.ProviderAClient, tc.key, tc.body)
			expectError(t, resp, body, http.StatusBadRequest, "INVALID_REQUEST")
		})
	}
	if n := e.totalExternalRows(t); n != 0 {
		t.Errorf("external wager rows = %d, want 0 after invalid requests", n)
	}
	balance, version := e.walletState(t, w.ID)
	if balance != 10000 || version != 1 {
		t.Errorf("wallet = (%d, v%d), want untouched", balance, version)
	}
}

func TestWagerEndpointAuthorization(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "1.00")

	resp, body := e.app.DoWith(t, http.MethodPost, "/wagering/transactions", b.json(t), http.Header{"Idempotency-Key": {b.key()}})
	expectError(t, resp, body, http.StatusUnauthorized, "UNAUTHENTICATED")

	resp, body = e.postWager(t, testsupport.InternalClient, b.key(), b.json(t))
	expectError(t, resp, body, http.StatusForbidden, "FORBIDDEN")

	// provider-b sending a body that claims provider-a.
	resp, body = e.postWager(t, testsupport.ProviderBClient, b.key(), b.json(t))
	expectError(t, resp, body, http.StatusForbidden, "FORBIDDEN")

	if n := e.totalExternalRows(t); n != 0 {
		t.Errorf("external wager rows = %d, want 0 after unauthorized requests", n)
	}
	balance, version := e.walletState(t, w.ID)
	if balance != 10000 || version != 1 {
		t.Errorf("wallet = (%d, v%d), want untouched", balance, version)
	}
	if n := e.count(t, "SELECT count(*) FROM outbox_events WHERE aggregate_id = $1", w.ID); n != 2 {
		t.Errorf("outbox events = %d, want only the opening's 2", n)
	}
}

func TestGetWagerTransactionIsScopedToTheProvider(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "30.00")
	b.RoundID, b.GameID = "round-42", "game-42"
	sent := e.send(t, b, http.StatusOK)
	rejected := e.send(t, bet(w, "500.00"), http.StatusUnprocessableEntity)

	tokenA := testsupport.Token(t, testsupport.ProviderAClient)
	tokenB := testsupport.Token(t, testsupport.ProviderBClient)

	for _, path := range []string{
		"/wagering/transactions/" + sent.TransactionID,
		"/providers/provider-a/wagering/transactions/" + b.ExternalTransactionID,
	} {
		resp, body := e.app.Do(t, http.MethodGet, path, tokenA, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, resp.StatusCode, body)
		}
		v := decode[wagerView](t, body)
		want := wagerView{
			TransactionID: sent.TransactionID, ProviderID: "provider-a", ExternalTransactionID: b.ExternalTransactionID,
			Kind: "BET", Status: "PROCESSED", WalletID: w.ID, PlayerID: w.PlayerID, RoundID: "round-42", GameID: "game-42",
			Money: moneyBody{"30.00", "BRL"},
		}
		if v.Balance == nil || *v.Balance != (moneyBody{"70.00", "BRL"}) {
			t.Errorf("GET %s balance = %+v, want 70.00", path, v.Balance)
		}
		if v.CreatedAt == "" || v.UpdatedAt == "" {
			t.Errorf("GET %s lacks timestamps: %s", path, body)
		}
		v.Balance, v.CreatedAt, v.UpdatedAt = nil, "", ""
		if v != want {
			t.Errorf("GET %s = %+v, want %+v", path, v, want)
		}
	}

	resp, body := e.app.Do(t, http.MethodGet, "/wagering/transactions/"+rejected.TransactionID, tokenA, "")
	if v := decode[wagerView](t, body); resp.StatusCode != http.StatusOK || v.Status != "REJECTED" || v.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Errorf("GET rejected = %d %s", resp.StatusCode, body)
	}

	// Another Provider never sees it.
	resp, body = e.app.Do(t, http.MethodGet, "/wagering/transactions/"+sent.TransactionID, tokenB, "")
	expectError(t, resp, body, http.StatusNotFound, "NOT_FOUND")
	resp, body = e.app.Do(t, http.MethodGet, "/providers/provider-b/wagering/transactions/"+b.ExternalTransactionID, tokenB, "")
	expectError(t, resp, body, http.StatusNotFound, "NOT_FOUND")
	// Route providerId different from the token.
	resp, body = e.app.Do(t, http.MethodGet, "/providers/provider-a/wagering/transactions/"+b.ExternalTransactionID, tokenB, "")
	expectError(t, resp, body, http.StatusForbidden, "FORBIDDEN")

	resp, body = e.app.Do(t, http.MethodGet, "/wagering/transactions/"+uuid.NewString(), tokenA, "")
	expectError(t, resp, body, http.StatusNotFound, "NOT_FOUND")
	resp, body = e.app.Do(t, http.MethodGet, "/wagering/transactions/not-a-uuid", tokenA, "")
	expectError(t, resp, body, http.StatusBadRequest, "INVALID_REQUEST")
	resp, body = e.app.Do(t, http.MethodGet, "/wagering/transactions/"+sent.TransactionID, testsupport.Token(t, testsupport.InternalClient), "")
	expectError(t, resp, body, http.StatusForbidden, "FORBIDDEN")
	resp, body = e.app.Do(t, http.MethodGet, "/wagering/transactions/"+sent.TransactionID, "", "")
	expectError(t, resp, body, http.StatusUnauthorized, "UNAUTHENTICATED")
}

func TestResendingTheSameBetReplaysTheOriginalResult(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "10.00")

	first := e.send(t, b, http.StatusOK)
	e.send(t, bet(w, "5.00"), http.StatusOK) // moves the balance after the first BET
	again := e.send(t, b, http.StatusOK)
	if !again.IdempotentReplay || again.TransactionID != first.TransactionID || again.Status != "PROCESSED" ||
		again.Balance == nil || *again.Balance != (moneyBody{"90.00", "BRL"}) {
		t.Errorf("replay = %+v, want the original result with idempotentReplay", again)
	}

	rejected := bet(w, "1000.00")
	r1 := e.send(t, rejected, http.StatusUnprocessableEntity)
	r2 := e.send(t, rejected, http.StatusUnprocessableEntity)
	if !r2.IdempotentReplay || r2.TransactionID != r1.TransactionID || r2.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Errorf("rejection replay = %+v", r2)
	}

	balance, _ := e.walletState(t, w.ID)
	if balance != 8500 {
		t.Errorf("balance = %d, want 8500", balance)
	}
	if n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", w.ID); n != 2 {
		t.Errorf("debits = %d, want 2", n)
	}
	e.assertReconciled(t, w.ID)
}

func TestIdempotencyConflictsAnswer409WithoutEffect(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "10.00")
	e.send(t, b, http.StatusOK)

	changed := b
	changed.Money = json.RawMessage(`{"amount":"11.00","currency":"BRL"}`)
	resp, body := e.postWager(t, testsupport.ProviderAClient, b.key(), changed.json(t))
	expectError(t, resp, body, http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT")

	resp, body = e.postWager(t, testsupport.ProviderAClient, "another-key", b.json(t))
	expectError(t, resp, body, http.StatusConflict, "EXTERNAL_TRANSACTION_CONFLICT")

	if n := e.wagerRows(t, w.ID); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
	if balance, _ := e.walletState(t, w.ID); balance != 9000 {
		t.Errorf("balance = %d, want 9000", balance)
	}
	e.assertReconciled(t, w.ID)
}
