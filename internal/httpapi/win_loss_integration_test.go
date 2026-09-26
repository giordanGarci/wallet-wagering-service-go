//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

// eventTypesOf returns the Outbox Event types of transactionID, in order.
func (e *env) eventTypesOf(t *testing.T, transactionID string) []string {
	t.Helper()
	rows, err := e.db.Query(context.Background(), `
		SELECT event_type FROM outbox_events
		WHERE payload::jsonb -> 'data' ->> 'transactionId' = $1 ORDER BY seq`, transactionID)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		types = append(types, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("events: %v", err)
	}
	return types
}

// winFor returns a provider-a WIN of amount on w referencing ref's BET.
func winFor(w walletBody, amount string, ref wager) wager {
	b := operation(w, "WIN", amount)
	b.RoundID = ref.RoundID
	b.Reference = ref.ExternalTransactionID
	return b
}

func TestWinCreditsTheWalletWithLedgerAndEvents(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	win := operation(w, "WIN", "40.50")

	got := e.send(t, win, http.StatusOK)
	if got.Status != "PROCESSED" || got.IdempotentReplay || got.FailureCode != "" ||
		got.Balance == nil || *got.Balance != (moneyBody{"140.50", "BRL"}) {
		t.Fatalf("WIN = %+v (balance %+v), want PROCESSED 140.50", got, got.Balance)
	}
	if balance, version := e.walletState(t, w.ID); balance != 14050 || version != 2 {
		t.Errorf("wallet = (%d, v%d), want (14050, v2)", balance, version)
	}

	var dir string
	var amount, before, after int64
	err := e.db.QueryRow(context.Background(), `
		SELECT direction, amount_minor, balance_before_minor, balance_after_minor
		FROM ledger_entries WHERE transaction_id = $1`, got.TransactionID).Scan(&dir, &amount, &before, &after)
	if err != nil {
		t.Fatalf("read ledger entry: %v", err)
	}
	if dir != "CREDIT" || amount != 4050 || before != 10000 || after != 14050 {
		t.Errorf("ledger entry = (%s, %d, %d, %d), want CREDIT 4050 10000 14050", dir, amount, before, after)
	}

	if types := e.eventTypesOf(t, got.TransactionID); !reflect.DeepEqual(types, []string{"WagerTransactionProcessed", "WalletBalanceChanged"}) {
		t.Errorf("events = %v, want WagerTransactionProcessed, WalletBalanceChanged", types)
	}
	rows := e.outboxRows(t, uuid.MustParse(w.ID))
	data := decode[struct {
		WalletVersion int64     `json:"walletVersion"`
		Direction     string    `json:"direction"`
		BalanceAfter  moneyBody `json:"balanceAfter"`
	}](t, decode[outboxEnvelope](t, []byte(rows[len(rows)-1].Payload)).Data)
	if data.WalletVersion != 2 || data.Direction != "CREDIT" || data.BalanceAfter != (moneyBody{"140.50", "BRL"}) {
		t.Errorf("WalletBalanceChanged data = %+v", data)
	}

	// Replay: same result, no second credit.
	e.send(t, bet(w, "0.50"), http.StatusOK)
	requireReplayOf(t, e.send(t, win, http.StatusOK), got)
	if balance, _ := e.walletState(t, w.ID); balance != 14000 {
		t.Errorf("balance = %d, want 14000", balance)
	}
	if n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1 AND direction = 'CREDIT'", w.ID); n != 2 {
		t.Errorf("credits = %d, want 2 (opening + WIN)", n)
	}
	if v := e.metricValue(t, `wallet_wager_transactions_total{kind="WIN",status="PROCESSED"}`); v != 1 {
		t.Errorf("processed WINs = %v, want 1", v)
	}
	e.requireConsistent(t, w.ID)
}

func TestWinWithAReferenceToABetOfTheSameRound(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "10.00")
	b.RoundID = "round-7"
	placed := e.send(t, b, http.StatusOK)

	win := winFor(w, "35.00", b) // the WIN amount is free
	got := e.send(t, win, http.StatusOK)
	if got.Status != "PROCESSED" || got.Balance == nil || *got.Balance != (moneyBody{"125.00", "BRL"}) {
		t.Fatalf("WIN = %+v (balance %+v), want PROCESSED 125.00", got, got.Balance)
	}

	var refID *string
	var refExt string
	err := e.db.QueryRow(context.Background(), `
		SELECT reference_transaction_id::text, reference_external_transaction_id
		FROM wager_transactions WHERE id = $1`, got.TransactionID).Scan(&refID, &refExt)
	if err != nil {
		t.Fatalf("read WIN: %v", err)
	}
	if refID == nil || *refID != placed.TransactionID || refExt != b.ExternalTransactionID {
		t.Errorf("WIN reference = (%v, %q), want (%s, %q)", refID, refExt, placed.TransactionID, b.ExternalTransactionID)
	}
	resp, body := e.app.Do(t, http.MethodGet, "/wagering/transactions/"+got.TransactionID,
		testsupport.Token(t, testsupport.ProviderAClient), "")
	if v := decode[wagerView](t, body); resp.StatusCode != http.StatusOK || v.Kind != "WIN" ||
		v.ReferenceExternalTransactionID != b.ExternalTransactionID {
		t.Errorf("GET WIN = %d %s", resp.StatusCode, body)
	}
	if types := e.eventTypesOf(t, got.TransactionID); !reflect.DeepEqual(types, []string{"WagerTransactionProcessed", "WalletBalanceChanged"}) {
		t.Errorf("events = %v", types)
	}

	// A second WIN for the same BET is allowed (it is not a Reversal).
	e.send(t, winFor(w, "1.00", b), http.StatusOK)
	requireReplayOf(t, e.send(t, win, http.StatusOK), got)

	if balance, _ := e.walletState(t, w.ID); balance != 12600 {
		t.Errorf("balance = %d, want 12600", balance)
	}
	e.requireConsistent(t, w.ID)
}

func TestWinReferenceRejections(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	other := e.openWalletWith(t, uuid.New(), "100.00")

	b := bet(w, "10.00")
	e.send(t, b, http.StatusOK)
	loss := operation(w, "LOSS", "0.00")
	e.send(t, loss, http.StatusOK)
	win := operation(w, "WIN", "5.00")
	e.send(t, win, http.StatusOK)
	rejectedBet := bet(w, "1000.00")
	e.send(t, rejectedBet, http.StatusUnprocessableEntity)

	otherRound := winFor(w, "5.00", b)
	otherRound.RoundID = "round-other"
	otherPlayer := winFor(other, "5.00", b) // BET of another player's Wallet

	cases := []struct {
		name string
		body wager
		code string
	}{
		{"BET of another round", otherRound, "REFERENCE_MISMATCH"},
		{"BET of another player and wallet", otherPlayer, "REFERENCE_MISMATCH"},
		{"reference is a LOSS", winFor(w, "5.00", loss), "REFERENCE_MISMATCH"},
		{"reference is a WIN", winFor(w, "5.00", win), "REFERENCE_MISMATCH"},
		{"reference is a rejected BET", winFor(w, "5.00", rejectedBet), "REFERENCE_UNSUCCESSFUL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			walletID := c.body.WalletID
			balance, version := e.walletState(t, walletID)
			got := e.send(t, c.body, http.StatusUnprocessableEntity)
			if got.Status != "REJECTED" || got.FailureCode != c.code {
				t.Fatalf("WIN = %+v, want REJECTED %s", got, c.code)
			}
			if types := e.eventTypesOf(t, got.TransactionID); !reflect.DeepEqual(types, []string{"WagerTransactionRejected"}) {
				t.Errorf("events = %v, want WagerTransactionRejected only", types)
			}
			if n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", got.TransactionID); n != 0 {
				t.Errorf("ledger entries = %d, want 0", n)
			}
			if b2, v2 := e.walletState(t, walletID); b2 != balance || v2 != version {
				t.Errorf("wallet moved: (%d, v%d) -> (%d, v%d)", balance, version, b2, v2)
			}
			requireReplayOf(t, e.send(t, c.body, http.StatusUnprocessableEntity), got)
			e.requireConsistent(t, walletID)
		})
	}
}

func TestWinWithAnAbsentReferenceWaitsAsPendingReference(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")

	// The BET was never sent to this service; a BET with the same external
	// id from another Provider does not count (References are per Provider).
	missing := bet(w, "10.00")
	fromB := missing
	fromB.ProviderID = "provider-b"
	resp, body := e.postWager(t, testsupport.ProviderBClient, "b-key", fromB.json(t))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("provider-b BET = %d %s", resp.StatusCode, body)
	}
	balance, version := e.walletState(t, w.ID)

	win := winFor(w, "30.00", missing)
	before := time.Now()
	got := e.send(t, win, http.StatusAccepted)
	if got.Status != "PENDING_REFERENCE" || got.FailureCode != "" || got.IdempotentReplay {
		t.Fatalf("WIN = %+v, want PENDING_REFERENCE", got)
	}

	var status string
	var attempts int
	var next *time.Time
	var refID *string
	err := e.db.QueryRow(context.Background(), `
		SELECT status, attempts, next_attempt_at, reference_transaction_id::text
		FROM wager_transactions WHERE id = $1`, got.TransactionID).Scan(&status, &attempts, &next, &refID)
	if err != nil {
		t.Fatalf("read WIN: %v", err)
	}
	if status != "PENDING_REFERENCE" || next == nil || next.Before(before.Add(-time.Second)) || refID != nil {
		t.Errorf("row = (%s, attempts %d, next %v, ref %v), want PENDING_REFERENCE with next_attempt_at and no reference",
			status, attempts, next, refID)
	}
	if types := e.eventTypesOf(t, got.TransactionID); !reflect.DeepEqual(types, []string{"WagerTransactionPendingReference"}) {
		t.Errorf("events = %v, want WagerTransactionPendingReference", types)
	}
	if b2, v2 := e.walletState(t, w.ID); b2 != balance || v2 != version {
		t.Errorf("wallet moved: (%d, v%d) -> (%d, v%d)", balance, version, b2, v2)
	}
	if n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", got.TransactionID); n != 0 {
		t.Errorf("ledger entries = %d, want 0", n)
	}

	requireReplayOf(t, e.send(t, win, http.StatusAccepted), got)
	e.requireConsistent(t, w.ID)
}

func TestLossIsRecordedWithoutMovingTheWallet(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	e.send(t, bet(w, "20.00"), http.StatusOK)
	balance, version := e.walletState(t, w.ID)
	ledgerBefore := e.ledgerCount(t, w.ID)

	loss := operation(w, "LOSS", "0.00")
	got := e.send(t, loss, http.StatusOK)
	if got.Status != "PROCESSED" || got.FailureCode != "" || got.Balance == nil || *got.Balance != (moneyBody{"80.00", "BRL"}) {
		t.Fatalf("LOSS = %+v (balance %+v), want PROCESSED with the current 80.00", got, got.Balance)
	}
	if b2, v2 := e.walletState(t, w.ID); b2 != balance || v2 != version {
		t.Errorf("wallet = (%d, v%d), want unchanged (%d, v%d)", b2, v2, balance, version)
	}
	if n := e.ledgerCount(t, w.ID); n != ledgerBefore {
		t.Errorf("ledger entries = %d, want %d", n, ledgerBefore)
	}
	if types := e.eventTypesOf(t, got.TransactionID); !reflect.DeepEqual(types, []string{"WagerTransactionProcessed"}) {
		t.Errorf("events = %v, want WagerTransactionProcessed only", types)
	}
	var amount, balanceAfter int64
	if err := e.db.QueryRow(context.Background(),
		"SELECT amount_minor, balance_after_minor FROM wager_transactions WHERE id = $1 AND kind = 'LOSS'",
		got.TransactionID).Scan(&amount, &balanceAfter); err != nil {
		t.Fatalf("read LOSS: %v", err)
	}
	if amount != 0 || balanceAfter != 8000 {
		t.Errorf("LOSS row = (amount %d, balanceAfter %d), want (0, 8000)", amount, balanceAfter)
	}

	// Replay after the balance moved keeps the original balance.
	e.send(t, operation(w, "WIN", "5.00"), http.StatusOK)
	requireReplayOf(t, e.send(t, loss, http.StatusOK), got)

	// A LOSS in another currency than the Wallet's is a business rejection.
	usd := operation(w, "LOSS", "0.00")
	usd.Money = json.RawMessage(`{"amount":"0.00","currency":"USD"}`)
	if r := e.send(t, usd, http.StatusUnprocessableEntity); r.FailureCode != "CURRENCY_MISMATCH" {
		t.Errorf("USD LOSS = %+v, want CURRENCY_MISMATCH", r)
	}
	if v := e.metricValue(t, `wallet_wager_transactions_total{kind="LOSS",status="PROCESSED"}`); v != 1 {
		t.Errorf("processed LOSSes = %v, want 1", v)
	}
	e.requireConsistent(t, w.ID)
}

func TestZeroPolicyForWinAndLossAnswers400WithoutRecord(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "10.00")
	e.send(t, b, http.StatusOK)

	lossWithRef := operation(w, "LOSS", "0.00")
	lossWithRef.Reference = b.ExternalTransactionID
	cases := map[string]wager{
		"WIN zero":            operation(w, "WIN", "0.00"),
		"WIN zero with ref":   winFor(w, "0.00", b),
		"LOSS one cent":       operation(w, "LOSS", "0.01"),
		"LOSS positive":       operation(w, "LOSS", "25.00"),
		"LOSS with reference": lossWithRef,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			resp, raw := e.postWager(t, testsupport.ProviderAClient, body.key(), body.json(t))
			expectError(t, resp, raw, http.StatusBadRequest, "INVALID_REQUEST")
		})
	}
	if n := e.wagerRows(t, w.ID); n != 1 {
		t.Errorf("rows = %d, want only the BET", n)
	}
	if balance, version := e.walletState(t, w.ID); balance != 9000 || version != 2 {
		t.Errorf("wallet = (%d, v%d), want (9000, v2)", balance, version)
	}
	e.requireConsistent(t, w.ID)
}
