//go:build integration

package httpapi_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

type sendResult struct {
	status int
	body   wagerResponse
	raw    string
}

// sendConcurrently posts every body at once (released by a shared start
// signal) and returns the results in input order.
func (e *env) sendConcurrently(t *testing.T, bodies []wager) []sendResult {
	t.Helper()
	token := testsupport.Token(t, testsupport.ProviderAClient)
	results := make([]sendResult, len(bodies))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, b := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, body := e.app.DoWith(t, http.MethodPost, "/wagering/transactions", b.json(t), http.Header{
				"Authorization":   {"Bearer " + token},
				"Idempotency-Key": {b.key()},
			})
			results[i] = sendResult{status: resp.StatusCode, raw: string(body)}
			if resp.StatusCode < 500 {
				results[i].body = decode[wagerResponse](t, body)
			}
		}()
	}
	close(start)
	wg.Wait()
	return results
}

func TestTwoSimultaneousBetsOnTheSameWalletNeverOverdraw(t *testing.T) {
	e := newEnv(t)
	for round := 0; round < 5; round++ {
		w := e.openWalletWith(t, uuid.New(), "100.00")
		results := e.sendConcurrently(t, []wager{bet(w, "80.00"), bet(w, "80.00")})

		var processed, rejected int
		for _, r := range results {
			switch {
			case r.status == http.StatusOK && r.body.Status == "PROCESSED":
				processed++
				if *r.body.Balance != (moneyBody{"20.00", "BRL"}) {
					t.Errorf("processed balance = %+v, want 20.00", r.body.Balance)
				}
			case r.status == http.StatusUnprocessableEntity && r.body.FailureCode == "INSUFFICIENT_FUNDS":
				rejected++
				if *r.body.Balance != (moneyBody{"20.00", "BRL"}) {
					t.Errorf("rejected balance = %+v, want 20.00 observed after the winner", r.body.Balance)
				}
			default:
				t.Errorf("unexpected result %d %s", r.status, r.raw)
			}
		}
		if processed != 1 || rejected != 1 {
			t.Fatalf("round %d: processed=%d rejected=%d, want 1 and 1", round, processed, rejected)
		}
		balance, version := e.walletState(t, w.ID)
		if balance != 2000 || version != 2 {
			t.Errorf("wallet = (%d, v%d), want (2000, v2)", balance, version)
		}
		if n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", w.ID); n != 1 {
			t.Errorf("debits = %d, want exactly 1", n)
		}
		e.assertReconciled(t, w.ID)
	}
}

func TestTheSameBetSentFiftyTimesInParallelDebitsOnce(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "25.00")
	bodies := make([]wager, 50)
	for i := range bodies {
		bodies[i] = b
	}
	results := e.sendConcurrently(t, bodies)

	ids := map[string]bool{}
	replays := 0
	for _, r := range results {
		if r.status != http.StatusOK || r.body.Status != "PROCESSED" || *r.body.Balance != (moneyBody{"75.00", "BRL"}) {
			t.Fatalf("result = %d %s, want 200 PROCESSED 75.00", r.status, r.raw)
		}
		ids[r.body.TransactionID] = true
		if r.body.IdempotentReplay {
			replays++
		}
	}
	if len(ids) != 1 || replays != 49 {
		t.Errorf("distinct ids = %d, replays = %d; want 1 and 49", len(ids), replays)
	}
	if n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", w.ID); n != 1 {
		t.Errorf("debits = %d, want 1", n)
	}
	if n := e.wagerRows(t, w.ID); n != 1 {
		t.Errorf("wager rows = %d, want 1", n)
	}
	balance, _ := e.walletState(t, w.ID)
	if balance != 7500 {
		t.Errorf("balance = %d, want 7500", balance)
	}
	e.assertReconciled(t, w.ID)
}

func TestBetsOnDistinctWalletsProgressInParallel(t *testing.T) {
	e := newEnv(t)
	a := e.openWalletWith(t, uuid.New(), "100.00")
	b := e.openWalletWith(t, uuid.New(), "100.00")
	ctx := context.Background()

	// Hold wallet A's row lock in an open transaction, as a long BET would.
	lock, err := e.db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, "SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE", a.ID); err != nil {
		t.Fatalf("lock wallet A: %v", err)
	}

	testsupport.Token(t, testsupport.ProviderAClient) // warm the token cache
	blocked := make(chan sendResult, 1)
	go func() {
		ba := bet(a, "10.00")
		resp, body := e.postWager(t, testsupport.ProviderAClient, ba.key(), ba.json(t))
		blocked <- sendResult{status: resp.StatusCode, raw: string(body)}
	}()

	startB := time.Now()
	got := e.send(t, bet(b, "10.00"), http.StatusOK)
	if got.Status != "PROCESSED" {
		t.Fatalf("BET on wallet B = %+v", got)
	}
	if d := time.Since(startB); d > 3*time.Second {
		t.Errorf("BET on wallet B took %s while wallet A was locked", d)
	}

	select {
	case r := <-blocked:
		t.Fatalf("BET on locked wallet A finished while its lock was held: %d %s", r.status, r.raw)
	case <-time.After(300 * time.Millisecond):
	}
	if err := lock.Rollback(ctx); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	select {
	case r := <-blocked:
		if r.status != http.StatusOK {
			t.Fatalf("BET on wallet A after release = %d %s", r.status, r.raw)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("BET on wallet A did not finish after the lock was released")
	}
	for _, w := range []walletBody{a, b} {
		if balance, _ := e.walletState(t, w.ID); balance != 9000 {
			t.Errorf("wallet %s balance = %d, want 9000", w.ID, balance)
		}
		e.assertReconciled(t, w.ID)
	}
}

func TestTransientDatabaseFailureAnswers503WithoutRecord(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	ctx := context.Background()

	lock, err := e.db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, "SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE", w.ID); err != nil {
		t.Fatalf("lock wallet: %v", err)
	}

	b := bet(w, "10.00")
	token := testsupport.Token(t, testsupport.ProviderAClient)
	done := make(chan *http.Response, 1)
	var body []byte
	go func() {
		var resp *http.Response
		resp, body = e.app.DoWith(t, http.MethodPost, "/wagering/transactions", b.json(t), http.Header{
			"Authorization": {"Bearer " + token}, "Idempotency-Key": {b.key()},
		})
		done <- resp
	}()

	// Kill the app's connection while it waits for the Wallet lock, as a
	// PostgreSQL failover or restart would.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var killed int
		err := e.db.QueryRow(ctx, `
			SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
			WHERE datname = current_database() AND pid <> pg_backend_pid()
			  AND usename = 'wallet_app' AND wait_event_type = 'Lock'`).Scan(&killed)
		if err != nil {
			t.Fatalf("terminate backend: %v", err)
		}
		if killed > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the BET never waited for the wallet lock")
		}
		time.Sleep(20 * time.Millisecond)
	}

	resp := <-done
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("POST = %d (Retry-After %q) %s, want 503 with Retry-After", resp.StatusCode, resp.Header.Get("Retry-After"), body)
	}
	expectError(t, resp, body, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")
	_ = lock.Rollback(ctx)

	if n := e.wagerRows(t, w.ID); n != 0 {
		t.Errorf("wager rows = %d, want 0 after a transient failure", n)
	}
	// The Provider retries and the operation goes through once.
	got := e.send(t, b, http.StatusOK)
	if got.IdempotentReplay || *got.Balance != (moneyBody{"90.00", "BRL"}) {
		t.Errorf("retry = %+v", got)
	}
	e.assertReconciled(t, w.ID)
}

func TestPermanentFailureIsRecordedAsFailed(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "0.00")
	// A balance at the int64 limit makes any credit overflow: the operation
	// can never succeed, so it is recorded FAILED for audit.
	if _, err := e.db.Exec(context.Background(), "UPDATE wallets SET balance = 9223372036854775807 WHERE id = $1", w.ID); err != nil {
		t.Fatalf("set balance: %v", err)
	}
	win := bet(w, "1.00")
	win.Kind = "WIN"

	resp, body := e.postWager(t, testsupport.ProviderAClient, win.key(), win.json(t))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("POST = %d %s, want 500", resp.StatusCode, body)
	}
	got := decode[wagerResponse](t, body)
	if got.Status != "FAILED" || got.FailureCode != "PERMANENT_ERROR" || got.Balance != nil || got.TransactionID == "" {
		t.Errorf("response = %s, want FAILED PERMANENT_ERROR without balance", body)
	}
	if n := e.count(t, "SELECT count(*) FROM wager_transactions WHERE id = $1 AND status = 'FAILED' AND failure_code = 'PERMANENT_ERROR'", got.TransactionID); n != 1 {
		t.Errorf("FAILED rows = %d, want 1", n)
	}
	if n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1", w.ID); n != 0 {
		t.Errorf("ledger entries = %d, want 0", n)
	}
	// Replays return the recorded failure.
	resp, body = e.postWager(t, testsupport.ProviderAClient, win.key(), win.json(t))
	again := decode[wagerResponse](t, body)
	if resp.StatusCode != http.StatusInternalServerError || !again.IdempotentReplay || again.TransactionID != got.TransactionID {
		t.Errorf("replay = %d %s", resp.StatusCode, body)
	}
}

func TestParallelDuplicatesOnAnUnknownWalletResolveThroughTheConstraints(t *testing.T) {
	e := newEnv(t)
	w := walletBody{ID: uuid.NewString(), PlayerID: uuid.NewString()} // never opened: no row to lock
	b := bet(w, "5.00")
	bodies := make([]wager, 20)
	for i := range bodies {
		bodies[i] = b
	}
	results := e.sendConcurrently(t, bodies)
	ids := map[string]bool{}
	replays := 0
	for _, r := range results {
		if r.status != http.StatusUnprocessableEntity || r.body.FailureCode != "WALLET_NOT_FOUND" {
			t.Fatalf("result = %d %s, want 422 WALLET_NOT_FOUND", r.status, r.raw)
		}
		ids[r.body.TransactionID] = true
		if r.body.IdempotentReplay {
			replays++
		}
	}
	if len(ids) != 1 || replays != len(bodies)-1 {
		t.Errorf("distinct ids = %d, replays = %d", len(ids), replays)
	}
	if n := e.wagerRows(t, w.ID); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}
