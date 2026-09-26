//go:build integration

package httpapi_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

// Ticket 11: Pending References and their worker (role pending).

// pendingSettings are fast worker settings for tests: retries every few
// milliseconds and enough attempts for a Reference sent shortly after its
// dependent.
func pendingSettings() config.Pending {
	return config.Pending{
		PollInterval: 20 * time.Millisecond,
		BatchSize:    20,
		Lease:        5 * time.Second,
		MaxAttempts:  200,
		RetryBase:    20 * time.Millisecond,
		RetryMax:     100 * time.Millisecond,
	}
}

// pendingConfig returns cfg with the given roles and pending settings.
func pendingConfig(e *env, p config.Pending, roles ...config.Role) config.Config {
	cfg := e.infra.Config
	cfg.OIDC.JWKSURL = e.forger.JWKSURL()
	cfg.Roles = roles
	cfg.Pending = p
	return cfg
}

// newPendingEnv starts an app with roles api and pending and settings p.
func newPendingEnv(t *testing.T, p config.Pending, extra ...fx.Option) *env {
	t.Helper()
	infra := testsupport.NewInfra(t)
	forger := testsupport.NewForger(t)
	e := &env{forger: forger, infra: infra, db: testsupport.OpenPool(t, infra.DatabaseURL)}
	e.app = testsupport.StartApp(t, pendingConfig(e, p, config.RoleAPI, config.RolePending), extra...)
	return e
}

// restartWith stops the current app and starts a new one with cfg.
func (e *env) restartWith(t *testing.T, cfg config.Config) {
	t.Helper()
	e.app.Stop()
	e.app = testsupport.StartApp(t, cfg)
}

// pendingRow is the persisted state of a Wager Transaction as the worker
// sees it.
type pendingRow struct {
	status        string
	failureCode   *string
	attempts      int
	nextAttemptAt *time.Time
	lockedUntil   *time.Time
	balanceMinor  *int64
	referenceID   *uuid.UUID
	compensatedID *uuid.UUID
}

func (e *env) pendingRow(t *testing.T, id string) pendingRow {
	t.Helper()
	var r pendingRow
	err := e.db.QueryRow(context.Background(), `
		SELECT status, failure_code, attempts, next_attempt_at, locked_until, balance_after_minor,
		       reference_transaction_id, compensated_bet_id
		FROM wager_transactions WHERE id = $1`, id).
		Scan(&r.status, &r.failureCode, &r.attempts, &r.nextAttemptAt, &r.lockedUntil, &r.balanceMinor,
			&r.referenceID, &r.compensatedID)
	if err != nil {
		t.Fatalf("read transaction %s: %v", id, err)
	}
	return r
}

// awaitResolution polls until transaction id leaves PENDING_REFERENCE and
// returns its row.
func (e *env) awaitResolution(t *testing.T, id string) pendingRow {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		r := e.pendingRow(t, id)
		if r.status != "PENDING_REFERENCE" {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("transaction %s still PENDING_REFERENCE after 15s (attempts %d)", id, r.attempts)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sendPending sends b and asserts 202 PENDING_REFERENCE.
func (e *env) sendPending(t *testing.T, b wager) wagerResponse {
	t.Helper()
	got := e.send(t, b, http.StatusAccepted)
	if got.Status != "PENDING_REFERENCE" || got.FailureCode != "" || got.IdempotentReplay || got.Balance != nil {
		t.Fatalf("%s = %+v, want PENDING_REFERENCE without balance", b.Kind, got)
	}
	return got
}

func (r pendingRow) code() string {
	if r.failureCode == nil {
		return ""
	}
	return *r.failureCode
}

func TestReversalsWithoutTheirReferenceWaitAsPendingReference(t *testing.T) {
	// Only the api role: nothing resolves them.
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	missing := bet(w, "10.00")

	refund := reversalOf(missing, "REFUND")
	rollback := reversalOf(missing, "ROLLBACK")
	gotRefund := e.sendPending(t, refund)
	gotRollback := e.sendPending(t, rollback)
	// A ROLLBACK of a REFUND that is itself pending waits too.
	chained := e.sendPending(t, reversalOf(refund, "ROLLBACK"))

	for _, id := range []string{gotRefund.TransactionID, gotRollback.TransactionID, chained.TransactionID} {
		r := e.pendingRow(t, id)
		if r.status != "PENDING_REFERENCE" || r.attempts != 0 || r.nextAttemptAt == nil || r.lockedUntil != nil || r.referenceID != nil {
			t.Errorf("row %s = %+v, want PENDING_REFERENCE, 0 attempts, scheduled, no lease, no reference", id, r)
		}
		expectEvents(t, e.walletEventTypesOf(t, w.ID, id), "WagerTransactionPendingReference")
	}
	// Replay of a pending operation: 202 with the current state.
	e.expectReplay(t, refund, http.StatusAccepted, gotRefund)
	if balance, version := e.walletState(t, w.ID); balance != 10000 || version != 1 {
		t.Errorf("wallet = (%d, v%d), want untouched (10000, v1)", balance, version)
	}
	if n := e.ledgerCount(t, w.ID); n != 1 {
		t.Errorf("ledger entries = %d, want only the Opening", n)
	}
	e.requireConsistent(t, w.ID)
}

func TestPendingRefundIsProcessedWhenItsBetArrives(t *testing.T) {
	e := newPendingEnv(t, pendingSettings())
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "10.00")
	refund := reversalOf(b, "REFUND")

	pending := e.sendPending(t, refund)
	e.expectReplay(t, refund, http.StatusAccepted, pending)

	placed := e.send(t, b, http.StatusOK)
	expectBalance(t, placed, "90.00")

	r := e.awaitResolution(t, pending.TransactionID)
	if r.status != "PROCESSED" || r.failureCode != nil || r.balanceMinor == nil || *r.balanceMinor != 10000 ||
		r.nextAttemptAt != nil || r.lockedUntil != nil {
		t.Fatalf("refund row = %+v, want PROCESSED with balance 100.00 and no schedule or lease", r)
	}
	if r.referenceID == nil || r.referenceID.String() != placed.TransactionID ||
		r.compensatedID == nil || r.compensatedID.String() != placed.TransactionID {
		t.Errorf("reference / compensated bet = %v / %v, want the BET %s", r.referenceID, r.compensatedID, placed.TransactionID)
	}
	if dir, amount, ok := e.ledgerOf(t, pending.TransactionID); !ok || dir != "CREDIT" || amount != 1000 {
		t.Errorf("ledger = (%s, %d, %v), want CREDIT 1000", dir, amount, ok)
	}
	if balance, version := e.walletState(t, w.ID); balance != 10000 || version != 3 {
		t.Errorf("wallet = (%d, v%d), want (10000, v3)", balance, version)
	}
	expectEvents(t, e.walletEventTypesOf(t, w.ID, pending.TransactionID),
		"WagerTransactionPendingReference", "WagerTransactionProcessed", "WalletBalanceChanged")

	// Replay now returns the persisted, final result.
	final := e.send(t, refund, http.StatusOK)
	if !final.IdempotentReplay || final.TransactionID != pending.TransactionID || final.Status != "PROCESSED" {
		t.Errorf("replay after resolution = %+v, want PROCESSED replay", final)
	}
	expectBalance(t, final, "100.00")
	resp, body := e.app.Do(t, http.MethodGet, "/wagering/transactions/"+pending.TransactionID,
		testsupport.Token(t, testsupport.ProviderAClient), "")
	if view := decode[wagerView](t, body); resp.StatusCode != http.StatusOK || view.Status != "PROCESSED" {
		t.Errorf("GET = %d %s, want PROCESSED", resp.StatusCode, body)
	}
	// A second REFUND of the same BET is already compensated.
	e.expectRejected(t, reversalOf(b, "REFUND"), "REFERENCE_ALREADY_COMPENSATED", "100.00")

	if v := e.metricValue(t, `wallet_pending_reference_resolutions_total{outcome="processed"}`); v != 1 {
		t.Errorf("processed resolutions = %v, want 1", v)
	}
	e.requireConsistent(t, w.ID)
}

func TestPendingWinIsProcessedWhenItsBetArrives(t *testing.T) {
	e := newPendingEnv(t, pendingSettings())
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "10.00")
	win := winFor(w, "30.00", b)

	pending := e.sendPending(t, win)
	e.send(t, b, http.StatusOK)

	r := e.awaitResolution(t, pending.TransactionID)
	if r.status != "PROCESSED" || r.balanceMinor == nil || *r.balanceMinor != 12000 || r.compensatedID != nil {
		t.Fatalf("win row = %+v, want PROCESSED 120.00 without compensated bet", r)
	}
	if dir, amount, ok := e.ledgerOf(t, pending.TransactionID); !ok || dir != "CREDIT" || amount != 3000 {
		t.Errorf("ledger = (%s, %d, %v), want CREDIT 3000", dir, amount, ok)
	}
	expectEvents(t, e.walletEventTypesOf(t, w.ID, pending.TransactionID),
		"WagerTransactionPendingReference", "WagerTransactionProcessed", "WalletBalanceChanged")
	e.requireConsistent(t, w.ID)
}

func TestPendingReferenceExpiresAsReferenceNotFound(t *testing.T) {
	p := pendingSettings()
	p.MaxAttempts, p.RetryBase, p.RetryMax = 2, 10*time.Millisecond, 20*time.Millisecond
	e := newPendingEnv(t, p)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	refund := reversalOf(bet(w, "10.00"), "REFUND")

	pending := e.sendPending(t, refund)
	r := e.awaitResolution(t, pending.TransactionID)
	if r.status != "REJECTED" || r.code() != "REFERENCE_NOT_FOUND" || r.attempts != 2 ||
		r.balanceMinor != nil || r.nextAttemptAt != nil || r.lockedUntil != nil {
		t.Fatalf("row = %+v (code %s), want REJECTED REFERENCE_NOT_FOUND after 2 attempts, no schedule or lease", r, r.code())
	}
	expectEvents(t, e.walletEventTypesOf(t, w.ID, pending.TransactionID),
		"WagerTransactionPendingReference", "WagerTransactionRejected")
	if _, _, ok := e.ledgerOf(t, pending.TransactionID); ok {
		t.Error("expired Pending Reference has a Ledger Entry")
	}
	if balance, version := e.walletState(t, w.ID); balance != 10000 || version != 1 {
		t.Errorf("wallet = (%d, v%d), want untouched", balance, version)
	}

	final := e.send(t, refund, http.StatusUnprocessableEntity)
	if !final.IdempotentReplay || final.Status != "REJECTED" || final.FailureCode != "REFERENCE_NOT_FOUND" {
		t.Errorf("replay = %+v, want REJECTED REFERENCE_NOT_FOUND replay", final)
	}
	if v := e.metricValue(t, `wallet_pending_reference_resolutions_total{outcome="expired"}`); v != 1 {
		t.Errorf("expired resolutions = %v, want 1", v)
	}
	if v := e.metricValue(t, `wallet_pending_reference_retries_total`); v != 1 {
		t.Errorf("retries = %v, want 1 (the second attempt expires)", v)
	}
	if v := e.metricValue(t, `wallet_pending_reference_backlog`); v != 0 {
		t.Errorf("backlog = %v, want 0", v)
	}
	e.requireConsistent(t, w.ID)
}

func TestPendingReversalOfARejectedBetIsReferenceUnsuccessful(t *testing.T) {
	e := newPendingEnv(t, pendingSettings())
	w := e.openWalletWith(t, uuid.New(), "5.00")
	b := bet(w, "10.00")
	refund := reversalOf(b, "REFUND")

	pending := e.sendPending(t, refund)
	e.expectRejected(t, b, "INSUFFICIENT_FUNDS", "5.00")

	r := e.awaitResolution(t, pending.TransactionID)
	if r.status != "REJECTED" || r.code() != "REFERENCE_UNSUCCESSFUL" || r.balanceMinor == nil || *r.balanceMinor != 500 {
		t.Fatalf("row = %+v (code %s), want REJECTED REFERENCE_UNSUCCESSFUL with balance 5.00", r, r.code())
	}
	expectEvents(t, e.walletEventTypesOf(t, w.ID, pending.TransactionID),
		"WagerTransactionPendingReference", "WagerTransactionRejected")
	if _, _, ok := e.ledgerOf(t, pending.TransactionID); ok {
		t.Error("rejected Pending Reference has a Ledger Entry")
	}
	final := e.send(t, refund, http.StatusUnprocessableEntity)
	if !final.IdempotentReplay || final.FailureCode != "REFERENCE_UNSUCCESSFUL" {
		t.Errorf("replay = %+v, want REFERENCE_UNSUCCESSFUL replay", final)
	}
	expectBalance(t, final, "5.00")
	e.requireConsistent(t, w.ID)
}

func TestPendingChainResolvesInOrder(t *testing.T) {
	e := newPendingEnv(t, pendingSettings())
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "10.00")
	refund := reversalOf(b, "REFUND")
	rollback := reversalOf(refund, "ROLLBACK")

	pendingRefund := e.sendPending(t, refund)
	pendingRollback := e.sendPending(t, rollback)
	e.send(t, b, http.StatusOK)

	rr := e.awaitResolution(t, pendingRefund.TransactionID)
	rb := e.awaitResolution(t, pendingRollback.TransactionID)
	if rr.status != "PROCESSED" || rb.status != "PROCESSED" {
		t.Fatalf("refund = %+v, rollback = %+v (code %s), want both PROCESSED", rr, rb, rb.code())
	}
	if rb.referenceID == nil || rb.referenceID.String() != pendingRefund.TransactionID || rb.compensatedID != nil {
		t.Errorf("rollback reference / compensated = %v / %v, want the REFUND and no slot", rb.referenceID, rb.compensatedID)
	}
	if dir, amount, ok := e.ledgerOf(t, pendingRollback.TransactionID); !ok || dir != "DEBIT" || amount != 1000 {
		t.Errorf("rollback ledger = (%s, %d, %v), want DEBIT 1000", dir, amount, ok)
	}
	// The REFUND was applied first: its balance is 100.00, the ROLLBACK's 90.00.
	if *rr.balanceMinor != 10000 || *rb.balanceMinor != 9000 {
		t.Errorf("balances = %d / %d, want 10000 / 9000", *rr.balanceMinor, *rb.balanceMinor)
	}
	if balance, version := e.walletState(t, w.ID); balance != 9000 || version != 4 {
		t.Errorf("wallet = (%d, v%d), want (9000, v4)", balance, version)
	}
	e.requireConsistent(t, w.ID)
}

func TestPendingReferencesResumeAfterRestart(t *testing.T) {
	e := newEnv(t) // api only: the pending rows stay parked
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "10.00")
	pending := e.sendPending(t, reversalOf(b, "REFUND"))
	e.send(t, b, http.StatusOK)
	time.Sleep(100 * time.Millisecond)
	if r := e.pendingRow(t, pending.TransactionID); r.status != "PENDING_REFERENCE" {
		t.Fatalf("resolved without the pending role: %+v", r)
	}

	e.restartWith(t, pendingConfig(e, pendingSettings(), config.RoleAPI, config.RolePending))
	if r := e.awaitResolution(t, pending.TransactionID); r.status != "PROCESSED" {
		t.Fatalf("after restart = %+v, want PROCESSED", r)
	}
	if balance, _ := e.walletState(t, w.ID); balance != 10000 {
		t.Errorf("balance = %d, want 10000", balance)
	}
	e.requireConsistent(t, w.ID)
}

func TestLiveLeaseIsRespectedAndAbandonedLeaseIsTakenOver(t *testing.T) {
	e := newEnv(t) // api only while the lease is set up
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "10.00")
	pending := e.sendPending(t, reversalOf(b, "REFUND"))
	e.send(t, b, http.StatusOK)

	// Another instance holds a live lease on the due row.
	setLease := func(sql string) {
		t.Helper()
		if _, err := e.db.Exec(context.Background(),
			"UPDATE wager_transactions SET next_attempt_at = now(), locked_until = "+sql+" WHERE id = $1",
			pending.TransactionID); err != nil {
			t.Fatalf("set lease: %v", err)
		}
	}
	setLease("now() + interval '1 hour'")
	e.restartWith(t, pendingConfig(e, pendingSettings(), config.RoleAPI, config.RolePending))
	time.Sleep(300 * time.Millisecond)
	if r := e.pendingRow(t, pending.TransactionID); r.status != "PENDING_REFERENCE" {
		t.Fatalf("row under a live lease was processed: %+v", r)
	}

	// That instance died: its lease expired.
	setLease("now() - interval '1 second'")
	if r := e.awaitResolution(t, pending.TransactionID); r.status != "PROCESSED" || r.lockedUntil != nil {
		t.Fatalf("abandoned lease = %+v, want PROCESSED without lease", r)
	}
	if n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", pending.TransactionID); n != 1 {
		t.Errorf("ledger entries = %d, want 1", n)
	}
	e.requireConsistent(t, w.ID)
}

func TestTwoWorkersNeverResolveTheSameRowTwice(t *testing.T) {
	e := newPendingEnv(t, pendingSettings())
	// A second instance with only the pending role, on the same database.
	other := testsupport.StartApp(t, pendingConfig(e, pendingSettings(), config.RolePending))

	w := e.openWalletWith(t, uuid.New(), "1000.00")
	const n = 20
	var bets []wager
	var pendings []wagerResponse
	for i := 0; i < n; i++ {
		b := bet(w, "10.00")
		bets = append(bets, b)
		pendings = append(pendings, e.sendPending(t, reversalOf(b, "REFUND")))
	}
	for _, b := range bets {
		e.send(t, b, http.StatusOK)
	}
	for _, p := range pendings {
		if r := e.awaitResolution(t, p.TransactionID); r.status != "PROCESSED" {
			t.Fatalf("refund %s = %+v (code %s), want PROCESSED", p.TransactionID, r, r.code())
		}
		if got := e.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", p.TransactionID); got != 1 {
			t.Errorf("refund %s has %d ledger entries, want 1", p.TransactionID, got)
		}
		expectEvents(t, e.walletEventTypesOf(t, w.ID, p.TransactionID),
			"WagerTransactionPendingReference", "WagerTransactionProcessed", "WalletBalanceChanged")
	}
	if balance, _ := e.walletState(t, w.ID); balance != 100000 {
		t.Errorf("balance = %d, want 100000", balance)
	}

	series := `wallet_pending_reference_resolutions_total{outcome="processed"}`
	mine := e.metricValue(t, series)
	theirs := (&env{app: other}).metricValueOr(t, series, 0)
	if mine+theirs != n {
		t.Errorf("processed resolutions = %v + %v, want %d in total", mine, theirs, n)
	}
	e.requireConsistent(t, w.ID)
}

func TestStoppingThePendingWorkerLeavesNoLiveLease(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "1000.00")
	const n = 40
	var pendings []wagerResponse
	for i := 0; i < n; i++ {
		b := bet(w, "1.00")
		pendings = append(pendings, e.sendPending(t, reversalOf(b, "REFUND")))
		e.send(t, b, http.StatusOK)
	}
	if _, err := e.db.Exec(context.Background(),
		"UPDATE wager_transactions SET next_attempt_at = now() WHERE status = 'PENDING_REFERENCE'"); err != nil {
		t.Fatalf("make due: %v", err)
	}

	p := pendingSettings()
	p.BatchSize = n
	// Slow retries (every SQL transaction takes 20ms more), so the batch is
	// still being worked on when the app stops.
	e.app.Stop()
	e.app = testsupport.StartApp(t, pendingConfig(e, p, config.RoleAPI, config.RolePending),
		fx.Decorate(func(u app.UnitOfWork) app.UnitOfWork { return slowUnitOfWork{u} }))
	deadline := time.Now().Add(10 * time.Second)
	for e.count(t, "SELECT count(*) FROM wager_transactions WHERE status = 'PROCESSED' AND kind = 'REFUND'") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no refund resolved")
		}
		time.Sleep(time.Millisecond)
	}
	e.app.Stop()

	// Every claimed row either finished its retry or was released.
	if live := e.count(t, `SELECT count(*) FROM wager_transactions
		WHERE status = 'PENDING_REFERENCE' AND locked_until IS NOT NULL`); live != 0 {
		t.Errorf("%d rows keep a lease after a graceful stop", live)
	}
	if left := e.count(t, "SELECT count(*) FROM wager_transactions WHERE status = 'PENDING_REFERENCE'"); left == 0 {
		t.Error("the whole batch was resolved before the stop; the release path was not exercised")
	}
	e.app = testsupport.StartApp(t, pendingConfig(e, pendingSettings(), config.RoleAPI, config.RolePending))
	for _, pd := range pendings {
		if r := e.awaitResolution(t, pd.TransactionID); r.status != "PROCESSED" {
			t.Fatalf("refund %s = %+v, want PROCESSED", pd.TransactionID, r)
		}
	}
	if got := e.count(t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1", w.ID); got != 1+2*n {
		t.Errorf("ledger entries = %d, want %d", got, 1+2*n)
	}
	e.requireConsistent(t, w.ID)
}

// slowUnitOfWork delays every SQL transaction.
type slowUnitOfWork struct{ app.UnitOfWork }

func (u slowUnitOfWork) Do(ctx context.Context, fn func(context.Context, app.Tx) error) error {
	time.Sleep(20 * time.Millisecond)
	return u.UnitOfWork.Do(ctx, fn)
}

// metricValueOr is metricValue that returns def when the series is absent
// (a counter with labels appears only once incremented).
func (e *env) metricValueOr(t *testing.T, series string, def float64) float64 {
	t.Helper()
	code, body := e.app.Get(t, "/metrics")
	if code != http.StatusOK {
		t.Fatalf("GET /metrics = %d", code)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if rest, ok := strings.CutPrefix(line, series+" "); ok {
			v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
			if err != nil {
				t.Fatalf("metric line %q: %v", line, err)
			}
			return v
		}
	}
	return def
}
