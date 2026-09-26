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

// operation returns a provider-a operation of kind and amount BRL on w with a
// fresh external id (a BET body with another kind).
func operation(w walletBody, kind, amount string) wager {
	b := bet(w, amount)
	b.Kind = kind
	b.ExternalTransactionID = strings.ToLower(kind) + "-" + uuid.NewString()
	return b
}

// outboxCount counts the Outbox Events of walletID.
func (e *env) outboxCount(t *testing.T, walletID string) int {
	t.Helper()
	return e.count(t, "SELECT count(*) FROM outbox_events WHERE aggregate_id = $1", walletID)
}

// ledgerCount counts the Ledger Entries of walletID.
func (e *env) ledgerCount(t *testing.T, walletID string) int {
	t.Helper()
	return e.count(t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1", walletID)
}

// requireReplayOf asserts that got is the Replay of original.
func requireReplayOf(t *testing.T, got, original wagerResponse) {
	t.Helper()
	original.IdempotentReplay = true
	if got.TransactionID != original.TransactionID || got.Status != original.Status ||
		got.FailureCode != original.FailureCode || !got.IdempotentReplay ||
		(got.Balance == nil) != (original.Balance == nil) ||
		(got.Balance != nil && *got.Balance != *original.Balance) {
		t.Fatalf("replay = %+v (balance %+v), want %+v (balance %+v)", got, got.Balance, original, original.Balance)
	}
}

func TestReplayReturnsTheOriginalBalanceAfterLaterMovements(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	first := bet(w, "10.00")

	original := e.send(t, first, http.StatusOK)
	e.send(t, operation(w, "WIN", "50.00"), http.StatusOK)
	e.send(t, bet(w, "20.00"), http.StatusOK)
	e.send(t, operation(w, "LOSS", "0.00"), http.StatusOK)

	ledgerBefore, outboxBefore := e.ledgerCount(t, w.ID), e.outboxCount(t, w.ID)
	balanceBefore, versionBefore := e.walletState(t, w.ID)
	for range 3 {
		got := e.send(t, first, http.StatusOK)
		requireReplayOf(t, got, original)
		if *got.Balance != (moneyBody{"90.00", "BRL"}) {
			t.Fatalf("replay balance = %+v, want the original 90.00 (current wallet is 120.00)", got.Balance)
		}
	}

	balance, version := e.walletState(t, w.ID)
	if balance != balanceBefore || version != versionBefore || balance != 12000 {
		t.Errorf("wallet = (%d, v%d), want untouched (%d, v%d) = 120.00", balance, version, balanceBefore, versionBefore)
	}
	if n := e.ledgerCount(t, w.ID); n != ledgerBefore {
		t.Errorf("ledger entries = %d, want %d: replays write nothing", n, ledgerBefore)
	}
	if n := e.outboxCount(t, w.ID); n != outboxBefore {
		t.Errorf("outbox events = %d, want %d: replays emit nothing", n, outboxBefore)
	}
	if v := e.metricValue(t, `wallet_wager_duplicates_total{outcome="replay"}`); v != 3 {
		t.Errorf("replay duplicates = %v, want 3", v)
	}
	if v := e.metricValue(t, `wallet_wager_transactions_total{kind="BET",status="PROCESSED"}`); v != 2 {
		t.Errorf("processed BETs = %v, want 2 (replays are not new outcomes)", v)
	}
	e.requireConsistent(t, w.ID)
}

func TestReplayOfARejectionKeepsTheFailureCodeAfterTheWalletChanges(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "10.00")

	tooBig := bet(w, "50.00")
	rejected := e.send(t, tooBig, http.StatusUnprocessableEntity)
	if rejected.FailureCode != "INSUFFICIENT_FUNDS" || rejected.Balance == nil || *rejected.Balance != (moneyBody{"10.00", "BRL"}) {
		t.Fatalf("rejection = %+v (balance %+v)", rejected, rejected.Balance)
	}

	// The Wallet can now afford the BET, yet the replay keeps the recorded
	// rejection and does not debit.
	e.send(t, operation(w, "WIN", "100.00"), http.StatusOK)
	requireReplayOf(t, e.send(t, tooBig, http.StatusUnprocessableEntity), rejected)

	// A rejection without balance (another player's Wallet) replays the same.
	other := bet(w, "1.00")
	other.PlayerID = uuid.NewString()
	mismatch := e.send(t, other, http.StatusUnprocessableEntity)
	if mismatch.FailureCode != "WALLET_PLAYER_MISMATCH" || mismatch.Balance != nil {
		t.Fatalf("player mismatch = %+v (balance %+v)", mismatch, mismatch.Balance)
	}
	requireReplayOf(t, e.send(t, other, http.StatusUnprocessableEntity), mismatch)

	// A rejection for an unknown Wallet replays too.
	unknown := bet(walletBody{ID: uuid.NewString(), PlayerID: w.PlayerID}, "1.00")
	notFound := e.send(t, unknown, http.StatusUnprocessableEntity)
	if notFound.FailureCode != "WALLET_NOT_FOUND" {
		t.Fatalf("unknown wallet = %+v", notFound)
	}
	requireReplayOf(t, e.send(t, unknown, http.StatusUnprocessableEntity), notFound)

	if balance, _ := e.walletState(t, w.ID); balance != 11000 {
		t.Errorf("balance = %d, want 11000 (opening + WIN only)", balance)
	}
	if n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", w.ID); n != 0 {
		t.Errorf("debits = %d, want 0", n)
	}
	e.requireConsistent(t, w.ID)
}

func TestIdempotencyKeyIsUsedExactlyAsSent(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "10.00")
	// Mixed case, inner spaces, symbols and an upper-case UUID: nothing is
	// normalized or replaced.
	key := "Prov-A/Retry Key #1:" + strings.ToUpper(uuid.NewString())

	resp, body := e.postWager(t, testsupport.ProviderAClient, key, b.json(t))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST = %d %s", resp.StatusCode, body)
	}
	first := decode[wagerResponse](t, body)

	var stored string
	if err := e.db.QueryRow(context.Background(),
		"SELECT idempotency_key FROM wager_transactions WHERE id = $1", first.TransactionID).Scan(&stored); err != nil {
		t.Fatalf("read key: %v", err)
	}
	if stored != key {
		t.Errorf("stored idempotency_key = %q, want %q verbatim", stored, key)
	}

	resp, body = e.postWager(t, testsupport.ProviderAClient, key, b.json(t))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay = %d %s", resp.StatusCode, body)
	}
	requireReplayOf(t, decode[wagerResponse](t, body), first)

	// A key differing only in case is another key: the same operation under
	// it is an externalTransactionId conflict.
	resp, body = e.postWager(t, testsupport.ProviderAClient, strings.ToLower(key), b.json(t))
	expectError(t, resp, body, http.StatusConflict, "EXTERNAL_TRANSACTION_CONFLICT")

	if n := e.wagerRows(t, w.ID); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
	e.requireConsistent(t, w.ID)
}

func TestTheSameKeyFromAnotherProviderIsAnotherOperation(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	a := bet(w, "10.00")
	fromA := e.send(t, a, http.StatusOK)

	// provider-b cannot replay (or even submit) provider-a's operation.
	resp, body := e.postWager(t, testsupport.ProviderBClient, a.key(), a.json(t))
	expectError(t, resp, body, http.StatusForbidden, "FORBIDDEN")

	// The same Idempotency Key and externalTransactionId under provider-b
	// are a new operation: uniqueness is per Provider, so nothing of
	// provider-a's result leaks.
	b := a
	b.ProviderID = "provider-b"
	resp, body = e.postWager(t, testsupport.ProviderBClient, a.key(), b.json(t))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("provider-b POST = %d %s", resp.StatusCode, body)
	}
	fromB := decode[wagerResponse](t, body)
	if fromB.IdempotentReplay || fromB.TransactionID == fromA.TransactionID ||
		fromB.Balance == nil || *fromB.Balance != (moneyBody{"80.00", "BRL"}) {
		t.Fatalf("provider-b result = %+v (balance %+v), want a new PROCESSED debit to 80.00", fromB, fromB.Balance)
	}

	tokenB := testsupport.Token(t, testsupport.ProviderBClient)
	resp, body = e.app.Do(t, http.MethodGet, "/wagering/transactions/"+fromA.TransactionID, tokenB, "")
	expectError(t, resp, body, http.StatusNotFound, "NOT_FOUND")
	resp, body = e.app.Do(t, http.MethodGet, "/providers/provider-b/wagering/transactions/"+a.ExternalTransactionID, tokenB, "")
	if v := decode[wagerView](t, body); resp.StatusCode != http.StatusOK || v.TransactionID != fromB.TransactionID {
		t.Errorf("provider-b GET by external id = %d %s, want its own transaction", resp.StatusCode, body)
	}

	// Each Provider keeps replaying its own result.
	requireReplayOf(t, e.send(t, a, http.StatusOK), fromA)
	resp, body = e.postWager(t, testsupport.ProviderBClient, a.key(), b.json(t))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("provider-b replay = %d %s", resp.StatusCode, body)
	}
	requireReplayOf(t, decode[wagerResponse](t, body), fromB)

	if balance, _ := e.walletState(t, w.ID); balance != 8000 {
		t.Errorf("balance = %d, want 8000 (one debit per Provider)", balance)
	}
	e.requireConsistent(t, w.ID)
}

func TestIdempotencySurvivesAnApplicationRestart(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "25.00")
	original := e.send(t, b, http.StatusOK)
	rejectedBet := bet(w, "500.00")
	rejected := e.send(t, rejectedBet, http.StatusUnprocessableEntity)

	// Stop every instance and start a new one on the same database: nothing
	// idempotency-related lives in memory.
	e.app.Stop()
	cfg := e.infra.Config
	cfg.OIDC.JWKSURL = e.forger.JWKSURL()
	e.app = testsupport.StartApp(t, cfg)

	requireReplayOf(t, e.send(t, b, http.StatusOK), original)
	requireReplayOf(t, e.send(t, rejectedBet, http.StatusUnprocessableEntity), rejected)

	changed := b
	changed.Money = json.RawMessage(`{"amount":"26.00","currency":"BRL"}`)
	resp, body := e.postWager(t, testsupport.ProviderAClient, b.key(), changed.json(t))
	expectError(t, resp, body, http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT")
	resp, body = e.postWager(t, testsupport.ProviderAClient, "after-restart", b.json(t))
	expectError(t, resp, body, http.StatusConflict, "EXTERNAL_TRANSACTION_CONFLICT")

	if n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", w.ID); n != 1 {
		t.Errorf("debits = %d, want 1", n)
	}
	if balance, _ := e.walletState(t, w.ID); balance != 7500 {
		t.Errorf("balance = %d, want 7500", balance)
	}
	e.requireConsistent(t, w.ID)
}

func TestDuplicatesMetricCountsReplaysAndConflicts(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "10.00")
	e.send(t, b, http.StatusOK)
	e.send(t, b, http.StatusOK)
	e.send(t, b, http.StatusOK)

	changed := b
	changed.RoundID = "round-2"
	resp, body := e.postWager(t, testsupport.ProviderAClient, b.key(), changed.json(t))
	expectError(t, resp, body, http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT")
	resp, body = e.postWager(t, testsupport.ProviderAClient, "other-key", b.json(t))
	expectError(t, resp, body, http.StatusConflict, "EXTERNAL_TRANSACTION_CONFLICT")

	for series, want := range map[string]float64{
		`wallet_wager_duplicates_total{outcome="replay"}`:                        2,
		`wallet_wager_duplicates_total{outcome="idempotency_key_conflict"}`:      1,
		`wallet_wager_duplicates_total{outcome="external_transaction_conflict"}`: 1,
	} {
		if v := e.metricValue(t, series); v != want {
			t.Errorf("%s = %v, want %v", series, v, want)
		}
	}
	e.requireConsistent(t, w.ID)
}
