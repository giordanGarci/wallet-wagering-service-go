//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

// Reversals (ticket 10): REFUND and ROLLBACK of a Reference that is already
// recorded, and the Compensation Slot of ADR-0002.

// reversalOf builds a provider-a REFUND or ROLLBACK that fully undoes ref:
// same player, Wallet, Round, game and Money, with a fresh external id.
func reversalOf(ref wager, kind string) wager {
	r := ref
	r.Kind = kind
	r.ExternalTransactionID = "rv-" + kind + "-" + uuid.NewString()
	r.Reference = ref.ExternalTransactionID
	return r
}

// rvWin is a reference-less provider-a WIN of amount BRL in round-1 of w.
func rvWin(w walletBody, amount string) wager {
	b := bet(w, amount)
	b.Kind = "WIN"
	b.ExternalTransactionID = "win-" + uuid.NewString()
	return b
}

// rvLoss is a provider-a LOSS (amount 0.00) in round-1 of w.
func rvLoss(w walletBody) wager {
	b := bet(w, "0.00")
	b.Kind = "LOSS"
	b.ExternalTransactionID = "loss-" + uuid.NewString()
	return b
}

func withMoney(b wager, amount, currency string) wager {
	b.Money = json.RawMessage(`{"amount":"` + amount + `","currency":"` + currency + `"}`)
	return b
}

// expectBalance asserts a response balance.
func expectBalance(t *testing.T, got wagerResponse, amount string) {
	t.Helper()
	if got.Balance == nil || *got.Balance != (moneyBody{amount, "BRL"}) {
		t.Errorf("balance = %+v, want %s BRL", got.Balance, amount)
	}
}

// expectRejected sends b and asserts 422 with code and the unchanged
// balance.
func (e *env) expectRejected(t *testing.T, b wager, code, balance string) wagerResponse {
	t.Helper()
	got := e.send(t, b, http.StatusUnprocessableEntity)
	if got.Status != "REJECTED" || got.FailureCode != code {
		t.Fatalf("%s of %s = %+v, want REJECTED %s", b.Kind, b.Reference, got, code)
	}
	expectBalance(t, got, balance)
	return got
}

// reversalRow is the persisted resolution of a Reversal.
type reversalRow struct {
	status        string
	failureCode   *string
	referenceID   *uuid.UUID
	compensatedID *uuid.UUID
}

func (e *env) reversalRow(t *testing.T, id string) reversalRow {
	t.Helper()
	var r reversalRow
	err := e.db.QueryRow(context.Background(), `
		SELECT status, failure_code, reference_transaction_id, compensated_bet_id
		FROM wager_transactions WHERE id = $1`, id).Scan(&r.status, &r.failureCode, &r.referenceID, &r.compensatedID)
	if err != nil {
		t.Fatalf("read transaction %s: %v", id, err)
	}
	return r
}

// ledgerOf returns the direction and amount of the Ledger Entry of a
// transaction; ok is false when there is none.
func (e *env) ledgerOf(t *testing.T, txID string) (direction string, amount int64, ok bool) {
	t.Helper()
	n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", txID)
	if n == 0 {
		return "", 0, false
	}
	err := e.db.QueryRow(context.Background(),
		"SELECT direction, amount_minor FROM ledger_entries WHERE transaction_id = $1", txID).Scan(&direction, &amount)
	if err != nil {
		t.Fatalf("read ledger entry of %s: %v", txID, err)
	}
	return direction, amount, true
}

// walletEventTypesOf lists the Outbox Event types about transaction txID, in
// publication order.
func (e *env) walletEventTypesOf(t *testing.T, walletID, txID string) []string {
	t.Helper()
	var types []string
	for _, row := range e.outboxRows(t, uuid.MustParse(walletID)) {
		var env outboxEnvelope
		if err := json.Unmarshal([]byte(row.Payload), &env); err != nil {
			t.Fatalf("decode outbox payload: %v", err)
		}
		var data struct {
			TransactionID string `json:"transactionId"`
		}
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatalf("decode event data: %v", err)
		}
		if data.TransactionID == txID {
			types = append(types, row.EventType)
		}
	}
	return types
}

func expectEvents(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

// expectReplay resends b and asserts the original outcome comes back as a
// Replay without new rows or events.
func (e *env) expectReplay(t *testing.T, b wager, status int, original wagerResponse) {
	t.Helper()
	walletID := b.WalletID
	rows, events := e.wagerRows(t, walletID), len(e.outboxRows(t, uuid.MustParse(walletID)))
	got := e.send(t, b, status)
	if !got.IdempotentReplay || got.TransactionID != original.TransactionID || got.Status != original.Status ||
		got.FailureCode != original.FailureCode {
		t.Errorf("replay = %+v, want replay of %+v", got, original)
	}
	if e.wagerRows(t, walletID) != rows || len(e.outboxRows(t, uuid.MustParse(walletID))) != events {
		t.Errorf("replay of %s recorded new rows or events", b.ExternalTransactionID)
	}
}

// expectUniqueViolation asserts err is a 23505 on one of constraints.
func expectUniqueViolation(t *testing.T, err error, constraints ...string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" || !slices.Contains(constraints, pgErr.ConstraintName) {
		t.Fatalf("err = %v, want unique violation on one of %v", err, constraints)
	}
}

func TestReversalWithoutReferenceIsInvalid(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	for _, kind := range []string{"REFUND", "ROLLBACK"} {
		b := bet(w, "10.00")
		b.Kind = kind
		resp, body := e.postWager(t, testsupport.ProviderAClient, b.key(), b.json(t))
		expectError(t, resp, body, http.StatusBadRequest, "INVALID_REQUEST")
	}
	if n := e.wagerRows(t, w.ID); n != 0 {
		t.Errorf("rows = %d, want none for invalid reversals", n)
	}
}

func TestRefundOfBetCreditsAndConsumesTheSlot(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "30.00")
	placed := e.send(t, b, http.StatusOK)

	r := reversalOf(b, "REFUND")
	got := e.send(t, r, http.StatusOK)
	if got.Status != "PROCESSED" || got.FailureCode != "" || got.IdempotentReplay {
		t.Fatalf("refund = %+v, want PROCESSED", got)
	}
	expectBalance(t, got, "100.00")

	row := e.reversalRow(t, got.TransactionID)
	if row.referenceID == nil || row.referenceID.String() != placed.TransactionID ||
		row.compensatedID == nil || row.compensatedID.String() != placed.TransactionID {
		t.Errorf("refund row = %+v, want reference and compensated BET %s", row, placed.TransactionID)
	}
	if dir, amount, ok := e.ledgerOf(t, got.TransactionID); !ok || dir != "CREDIT" || amount != 3000 {
		t.Errorf("ledger = (%s, %d, %v), want CREDIT 3000", dir, amount, ok)
	}
	expectEvents(t, e.walletEventTypesOf(t, w.ID, got.TransactionID), "WagerTransactionProcessed", "WalletBalanceChanged")
	if balance, version := e.walletState(t, w.ID); balance != 10000 || version != 3 {
		t.Errorf("wallet = (%d, v%d), want (10000, v3)", balance, version)
	}

	// The response to the Provider carries the Reference through the query.
	resp, body := e.app.Do(t, http.MethodGet, "/wagering/transactions/"+got.TransactionID,
		testsupport.Token(t, testsupport.ProviderAClient), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET refund = %d %s", resp.StatusCode, body)
	}
	if v := decode[wagerView](t, body); v.Kind != "REFUND" || v.ReferenceExternalTransactionID != b.ExternalTransactionID {
		t.Errorf("view = %+v, want REFUND of %s", v, b.ExternalTransactionID)
	}

	e.expectReplay(t, r, http.StatusOK, got)
	e.requireConsistent(t, w.ID)
}

func TestRollbackOfBetCredits(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "40.00")
	placed := e.send(t, b, http.StatusOK)

	r := reversalOf(b, "ROLLBACK")
	got := e.send(t, r, http.StatusOK)
	expectBalance(t, got, "100.00")
	row := e.reversalRow(t, got.TransactionID)
	if row.status != "PROCESSED" || row.compensatedID == nil || row.compensatedID.String() != placed.TransactionID {
		t.Errorf("rollback row = %+v, want PROCESSED compensating %s", row, placed.TransactionID)
	}
	if dir, amount, ok := e.ledgerOf(t, got.TransactionID); !ok || dir != "CREDIT" || amount != 4000 {
		t.Errorf("ledger = (%s, %d, %v), want CREDIT 4000", dir, amount, ok)
	}
	expectEvents(t, e.walletEventTypesOf(t, w.ID, got.TransactionID), "WagerTransactionProcessed", "WalletBalanceChanged")
	e.expectReplay(t, r, http.StatusOK, got)
	e.requireConsistent(t, w.ID)
}

func TestRollbackOfWinDebitsWithoutClaimingASlot(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	e.send(t, bet(w, "10.00"), http.StatusOK)
	win := rvWin(w, "50.00")
	won := e.send(t, win, http.StatusOK)
	expectBalance(t, won, "140.00")

	r := reversalOf(win, "ROLLBACK")
	got := e.send(t, r, http.StatusOK)
	expectBalance(t, got, "90.00")
	row := e.reversalRow(t, got.TransactionID)
	if row.referenceID == nil || row.referenceID.String() != won.TransactionID || row.compensatedID != nil {
		t.Errorf("rollback row = %+v, want reference %s and no compensated BET", row, won.TransactionID)
	}
	if dir, amount, ok := e.ledgerOf(t, got.TransactionID); !ok || dir != "DEBIT" || amount != 5000 {
		t.Errorf("ledger = (%s, %d, %v), want DEBIT 5000", dir, amount, ok)
	}
	expectEvents(t, e.walletEventTypesOf(t, w.ID, got.TransactionID), "WagerTransactionProcessed", "WalletBalanceChanged")
	e.expectReplay(t, r, http.StatusOK, got)

	// A WIN is rolled back at most once.
	e.expectRejected(t, reversalOf(win, "ROLLBACK"), "REFERENCE_ALREADY_COMPENSATED", "90.00")
	e.requireConsistent(t, w.ID)
}

func TestRollbackOfRefundDebitsAndKeepsTheSlotConsumed(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "30.00")
	placed := e.send(t, b, http.StatusOK)
	refund := reversalOf(b, "REFUND")
	refunded := e.send(t, refund, http.StatusOK)
	expectBalance(t, refunded, "100.00")

	rb := reversalOf(refund, "ROLLBACK")
	got := e.send(t, rb, http.StatusOK)
	expectBalance(t, got, "70.00")
	row := e.reversalRow(t, got.TransactionID)
	if row.referenceID == nil || row.referenceID.String() != refunded.TransactionID || row.compensatedID != nil {
		t.Errorf("rollback row = %+v, want reference %s and no compensated BET", row, refunded.TransactionID)
	}
	if dir, amount, ok := e.ledgerOf(t, got.TransactionID); !ok || dir != "DEBIT" || amount != 3000 {
		t.Errorf("ledger = (%s, %d, %v), want DEBIT 3000", dir, amount, ok)
	}
	expectEvents(t, e.walletEventTypesOf(t, w.ID, got.TransactionID), "WagerTransactionProcessed", "WalletBalanceChanged")

	// ADR-0002: the BET's slot stays consumed by the REFUND.
	again := e.expectRejected(t, reversalOf(b, "REFUND"), "REFERENCE_ALREADY_COMPENSATED", "70.00")
	if r := e.reversalRow(t, again.TransactionID); r.referenceID == nil || r.referenceID.String() != placed.TransactionID || r.compensatedID != nil {
		t.Errorf("rejected refund row = %+v, want reference %s for audit and no slot", r, placed.TransactionID)
	}
	if _, _, ok := e.ledgerOf(t, again.TransactionID); ok {
		t.Error("rejected refund has a ledger entry")
	}
	expectEvents(t, e.walletEventTypesOf(t, w.ID, again.TransactionID), "WagerTransactionRejected")
	e.expectRejected(t, reversalOf(b, "ROLLBACK"), "REFERENCE_ALREADY_COMPENSATED", "70.00")
	// The REFUND itself was already rolled back.
	e.expectRejected(t, reversalOf(refund, "ROLLBACK"), "REFERENCE_ALREADY_COMPENSATED", "70.00")
	// And the ROLLBACK cannot be rolled back.
	e.expectRejected(t, reversalOf(rb, "ROLLBACK"), "REFERENCE_NOT_REVERSIBLE", "70.00")

	if n := e.count(t, "SELECT count(*) FROM wager_transactions WHERE compensated_bet_id = $1", placed.TransactionID); n != 1 {
		t.Errorf("compensations of the BET = %d, want 1", n)
	}
	if balance, _ := e.walletState(t, w.ID); balance != 7000 {
		t.Errorf("balance = %d, want 7000", balance)
	}
	e.requireConsistent(t, w.ID)
}

func TestSecondReversalOfTheSameBetIsAlreadyCompensated(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "25.00")
	e.send(t, b, http.StatusOK)
	first := e.send(t, reversalOf(b, "REFUND"), http.StatusOK)
	expectBalance(t, first, "100.00")

	second := reversalOf(b, "REFUND")
	rejected := e.expectRejected(t, second, "REFERENCE_ALREADY_COMPENSATED", "100.00")
	expectEvents(t, e.walletEventTypesOf(t, w.ID, rejected.TransactionID), "WagerTransactionRejected")
	e.expectReplay(t, second, http.StatusUnprocessableEntity, rejected)
	e.expectRejected(t, reversalOf(b, "ROLLBACK"), "REFERENCE_ALREADY_COMPENSATED", "100.00")

	if n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1 AND direction = 'CREDIT'", w.ID); n != 2 {
		t.Errorf("credits = %d, want opening + one refund", n)
	}
	e.requireConsistent(t, w.ID)
}

func TestIrreversibleReferencesAreRejected(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "10.00")
	e.send(t, b, http.StatusOK)
	rb := reversalOf(b, "ROLLBACK")
	e.send(t, rb, http.StatusOK)
	b2 := bet(w, "20.00")
	e.send(t, b2, http.StatusOK)
	refund := reversalOf(b2, "REFUND")
	e.send(t, refund, http.StatusOK)
	win := rvWin(w, "5.00")
	e.send(t, win, http.StatusOK)
	loss := rvLoss(w)
	e.send(t, loss, http.StatusOK)
	const balance = "105.00"

	tests := []struct {
		name string
		b    wager
	}{
		{"ROLLBACK of ROLLBACK", reversalOf(rb, "ROLLBACK")},
		{"REFUND of ROLLBACK", reversalOf(rb, "REFUND")},
		{"REFUND of WIN", reversalOf(win, "REFUND")},
		{"REFUND of REFUND", reversalOf(refund, "REFUND")},
		{"REFUND of LOSS", withMoney(reversalOf(loss, "REFUND"), "5.00", "BRL")},
		{"ROLLBACK of LOSS", withMoney(reversalOf(loss, "ROLLBACK"), "5.00", "BRL")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.expectRejected(t, tt.b, "REFERENCE_NOT_REVERSIBLE", balance)
			expectEvents(t, e.walletEventTypesOf(t, w.ID, got.TransactionID), "WagerTransactionRejected")
		})
	}
	e.requireConsistent(t, w.ID)
}

func TestReversalDivergingFromReferenceIsMismatch(t *testing.T) {
	e := newEnv(t)
	player := uuid.New()
	w := e.openWalletWith(t, player, "100.00")
	b := bet(w, "30.00")
	e.send(t, b, http.StatusOK)

	other := e.openWalletWith(t, uuid.New(), "100.00")
	onOther := reversalOf(b, "REFUND")
	onOther.PlayerID, onOther.WalletID = other.PlayerID, other.ID

	// Same player, second Wallet (in USD): the Wallet diverges.
	usd := e.openWalletUSD(t, player)
	onUSD := withMoney(reversalOf(b, "REFUND"), "30.00", "USD")
	onUSD.WalletID = usd.ID

	otherRound := reversalOf(b, "ROLLBACK")
	otherRound.RoundID = "round-2"

	tests := []struct {
		name, code, balance string
		b                   wager
	}{
		{"player and wallet", "REFERENCE_MISMATCH", "100.00", onOther},
		{"wallet", "REFERENCE_MISMATCH", "", onUSD},
		{"round", "REFERENCE_MISMATCH", "70.00", otherRound},
		{"amount", "REFERENCE_AMOUNT_MISMATCH", "70.00", withMoney(reversalOf(b, "REFUND"), "29.99", "BRL")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.send(t, tt.b, http.StatusUnprocessableEntity)
			if got.Status != "REJECTED" || got.FailureCode != tt.code {
				t.Fatalf("got %+v, want REJECTED %s", got, tt.code)
			}
			if tt.balance != "" {
				expectBalance(t, got, tt.balance)
			}
			expectEvents(t, e.walletEventTypesOf(t, tt.b.WalletID, got.TransactionID), "WagerTransactionRejected")
		})
	}

	// Rejections consume nothing: the corrected Reversal succeeds.
	got := e.send(t, reversalOf(b, "REFUND"), http.StatusOK)
	expectBalance(t, got, "100.00")
	for _, id := range []string{w.ID, other.ID, usd.ID} {
		e.requireConsistent(t, id)
	}
}

// openWalletUSD opens a zero-balance USD Wallet for playerID.
func (e *env) openWalletUSD(t *testing.T, playerID uuid.UUID) walletBody {
	t.Helper()
	resp, body := e.app.Do(t, http.MethodPost, "/wallets", testsupport.Token(t, testsupport.InternalClient),
		openBody(playerID, "0.00", "USD"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open USD wallet = %d %s, want 201", resp.StatusCode, body)
	}
	return decode[walletBody](t, body)
}

func TestReversalOfRejectedReferenceIsUnsuccessful(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "500.00")
	e.expectRejected(t, b, "INSUFFICIENT_FUNDS", "100.00")

	for _, kind := range []string{"REFUND", "ROLLBACK"} {
		got := e.expectRejected(t, reversalOf(b, kind), "REFERENCE_UNSUCCESSFUL", "100.00")
		expectEvents(t, e.walletEventTypesOf(t, w.ID, got.TransactionID), "WagerTransactionRejected")
	}
	e.requireConsistent(t, w.ID)
}

func TestReversalBeyondBalanceIsReversalInsufficientFunds(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	e.send(t, bet(w, "10.00"), http.StatusOK)
	win := rvWin(w, "50.00")
	e.send(t, win, http.StatusOK)
	e.send(t, bet(w, "130.00"), http.StatusOK) // spends the WIN

	rb := reversalOf(win, "ROLLBACK")
	got := e.expectRejected(t, rb, "REVERSAL_INSUFFICIENT_FUNDS", "10.00")
	if _, _, ok := e.ledgerOf(t, got.TransactionID); ok {
		t.Error("rejected rollback has a ledger entry")
	}
	expectEvents(t, e.walletEventTypesOf(t, w.ID, got.TransactionID), "WagerTransactionRejected")
	e.expectReplay(t, rb, http.StatusUnprocessableEntity, got)

	// Same situation for a ROLLBACK of a REFUND.
	b := bet(w, "10.00")
	e.send(t, b, http.StatusOK)
	refund := reversalOf(b, "REFUND")
	e.send(t, refund, http.StatusOK)
	e.send(t, bet(w, "5.00"), http.StatusOK)
	e.expectRejected(t, reversalOf(refund, "ROLLBACK"), "REVERSAL_INSUFFICIENT_FUNDS", "5.00")

	// A BET beyond the balance keeps its own code.
	e.expectRejected(t, bet(w, "6.00"), "INSUFFICIENT_FUNDS", "5.00")

	// The rejection consumed nothing: with funds, the WIN can be rolled back.
	e.send(t, rvWin(w, "100.00"), http.StatusOK)
	ok := e.send(t, reversalOf(win, "ROLLBACK"), http.StatusOK)
	expectBalance(t, ok, "55.00")
	e.requireConsistent(t, w.ID)
}

func TestReferenceIsResolvedOnlyWithinTheProvider(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "30.00")
	e.send(t, b, http.StatusOK)

	// provider-b cannot see provider-a's BET: to provider-b the Reference is
	// absent, so its REFUND waits in PENDING_REFERENCE and moves nothing.
	foreign := reversalOf(b, "REFUND")
	foreign.ProviderID = "provider-b"
	resp, body := e.postWager(t, testsupport.ProviderBClient, foreign.key(), foreign.json(t))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("provider-b refund = %d %s, want 202", resp.StatusCode, body)
	}
	got := decode[wagerResponse](t, body)
	if got.Status != "PENDING_REFERENCE" || got.FailureCode != "" {
		t.Fatalf("provider-b refund = %+v, want PENDING_REFERENCE", got)
	}
	if r := e.reversalRow(t, got.TransactionID); r.referenceID != nil || r.compensatedID != nil {
		t.Errorf("provider-b refund row = %+v, want no resolved reference", r)
	}
	expectEvents(t, e.walletEventTypesOf(t, w.ID, got.TransactionID), "WagerTransactionPendingReference")
	if balance, _ := e.walletState(t, w.ID); balance != 7000 {
		t.Errorf("balance = %d, want 7000", balance)
	}

	// The BET's slot is untouched: provider-a still refunds it.
	own := e.send(t, reversalOf(b, "REFUND"), http.StatusOK)
	expectBalance(t, own, "100.00")
	e.requireConsistent(t, w.ID)
}

func TestConcurrentReversalsOfTheSameBetCompensateOnce(t *testing.T) {
	e := newEnv(t)
	check := func(t *testing.T, w walletBody, results []sendResult) {
		t.Helper()
		var processed, compensated int
		for _, r := range results {
			switch {
			case r.status == http.StatusOK && r.body.Status == "PROCESSED":
				processed++
				expectBalance(t, r.body, "100.00")
			case r.status == http.StatusUnprocessableEntity && r.body.FailureCode == "REFERENCE_ALREADY_COMPENSATED":
				compensated++
				expectBalance(t, r.body, "100.00")
			default:
				t.Errorf("unexpected result %d %s", r.status, r.raw)
			}
		}
		if processed != 1 || compensated != len(results)-1 {
			t.Fatalf("processed=%d compensated=%d, want 1 and %d", processed, compensated, len(results)-1)
		}
		if balance, _ := e.walletState(t, w.ID); balance != 10000 {
			t.Errorf("balance = %d, want 10000 (one credit)", balance)
		}
		if n := e.count(t, `SELECT count(*) FROM ledger_entries l JOIN wager_transactions x ON x.id = l.transaction_id
			WHERE l.wallet_id = $1 AND x.kind IN ('REFUND', 'ROLLBACK')`, w.ID); n != 1 {
			t.Errorf("reversal ledger entries = %d, want 1", n)
		}
		e.requireConsistent(t, w.ID)
	}

	t.Run("REFUND and ROLLBACK together", func(t *testing.T) {
		for round := 0; round < 5; round++ {
			w := e.openWalletWith(t, uuid.New(), "100.00")
			b := bet(w, "30.00")
			e.send(t, b, http.StatusOK)
			check(t, w, e.sendConcurrently(t, []wager{reversalOf(b, "REFUND"), reversalOf(b, "ROLLBACK")}))
		}
	})
	t.Run("ten reversals", func(t *testing.T) {
		w := e.openWalletWith(t, uuid.New(), "100.00")
		b := bet(w, "30.00")
		e.send(t, b, http.StatusOK)
		bodies := make([]wager, 10)
		for i := range bodies {
			kind := "REFUND"
			if i%2 == 1 {
				kind = "ROLLBACK"
			}
			bodies[i] = reversalOf(b, kind)
		}
		check(t, w, e.sendConcurrently(t, bodies))
	})
}

func TestCompensationIndexesBackstopTheSlot(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	b := bet(w, "30.00")
	e.send(t, b, http.StatusOK)
	refund := e.send(t, reversalOf(b, "REFUND"), http.StatusOK)
	win := rvWin(w, "10.00")
	e.send(t, win, http.StatusOK)
	rollback := e.send(t, reversalOf(win, "ROLLBACK"), http.StatusOK)

	// Copies of the processed Reversals under new external ids: what a
	// second Reversal would write if the Wallet lock did not serialize them.
	copyRow := `INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
			balance_after_minor, balance_after_currency, provider_id, external_transaction_id, idempotency_key, payload_hash,
			round_id, game_id, reference_external_transaction_id, reference_transaction_id, compensated_bet_id,
			created_at, updated_at)
		SELECT gen_random_uuid(), origin, $2, status, wallet_id, player_id, amount_minor, currency,
			balance_after_minor, balance_after_currency, provider_id, 'copy-' || external_transaction_id,
			'copy-' || idempotency_key, payload_hash, round_id, game_id, reference_external_transaction_id,
			reference_transaction_id, compensated_bet_id, created_at, updated_at
		FROM wager_transactions WHERE id = $1`
	tests := []struct {
		name, id, kind string
		constraints    []string
	}{
		// Violates both indexes; PostgreSQL reports whichever it checks first.
		{"ROLLBACK of the refunded BET", refund.TransactionID, "ROLLBACK",
			[]string{"wager_transactions_compensated_bet_key", "wager_transactions_processed_reversal_reference_key"}},
		{"second ROLLBACK of the WIN", rollback.TransactionID, "ROLLBACK",
			[]string{"wager_transactions_processed_reversal_reference_key"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.db.Exec(context.Background(), copyRow, tt.id, tt.kind)
			expectUniqueViolation(t, err, tt.constraints...)
		})
	}
	// The compensated BET index alone: a row claiming the BET's slot while
	// referencing another transaction.
	_, err := e.db.Exec(context.Background(), `
		INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
			balance_after_minor, balance_after_currency, provider_id, external_transaction_id, idempotency_key, payload_hash,
			round_id, game_id, reference_external_transaction_id, reference_transaction_id, compensated_bet_id,
			created_at, updated_at)
		SELECT gen_random_uuid(), origin, kind, status, wallet_id, player_id, amount_minor, currency,
			balance_after_minor, balance_after_currency, provider_id, 'solo-' || external_transaction_id,
			'solo-' || idempotency_key, payload_hash, round_id, game_id, 'solo-' || reference_external_transaction_id,
			$2, compensated_bet_id, created_at, updated_at
		FROM wager_transactions WHERE id = $1`, refund.TransactionID, rollback.TransactionID)
	expectUniqueViolation(t, err, "wager_transactions_compensated_bet_key")
	e.requireConsistent(t, w.ID)
}
