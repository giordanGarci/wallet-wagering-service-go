//go:build integration

package httpapi_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

const (
	seriesConsistent  = `wallet_reconciliations_total{result="consistent"}`
	seriesDivergent   = `wallet_reconciliations_total{result="divergent"}`
	seriesDivergences = `wallet_reconciliation_divergences_total`
)

func TestReconciliationIsConsistentAfterOpeningAndMovements(t *testing.T) {
	e := newEnv(t)
	playerID := uuid.New()
	w := e.openWalletWith(t, playerID, "100.00")
	walletID := uuid.MustParse(w.ID)

	// The Opening alone.
	r := e.requireConsistent(t, w.ID)
	if r.WalletID != w.ID || r.StoredBalance != (moneyBody{"100.00", "BRL"}) || r.CheckedEntries != 1 {
		t.Errorf("reconciliation after opening = %+v, want 100.00 BRL over 1 entry", r)
	}

	// Credits keep the stored balance in step with the ledger.
	balance := int64(10000)
	at := time.Now().UTC().Truncate(time.Microsecond)
	for _, amount := range []int64{250, 1, 99999} {
		e.appendCredit(t, walletID, playerID, balance, amount, at)
		balance += amount
		at = at.Add(time.Millisecond)
	}
	if _, err := e.db.Exec(context.Background(), "UPDATE wallets SET balance = $1, version = version + 3 WHERE id = $2",
		balance, walletID); err != nil {
		t.Fatalf("update balance: %v", err)
	}

	r = e.requireConsistent(t, w.ID)
	want := reconciliationBody{
		WalletID:          w.ID,
		StoredBalance:     moneyBody{"1102.50", "BRL"},
		CalculatedBalance: moneyBody{"1102.50", "BRL"},
		Difference:        moneyBody{"0.00", "BRL"},
		Consistent:        true,
		CheckedEntries:    4,
	}
	if r != want {
		t.Errorf("reconciliation = %+v, want %+v", r, want)
	}
	if got := e.metricValue(t, seriesConsistent); got != 2 {
		t.Errorf("%s = %v, want 2", seriesConsistent, got)
	}
	if got := e.metricValue(t, seriesDivergences); got != 0 {
		t.Errorf("%s = %v, want 0", seriesDivergences, got)
	}
}

func TestReconciliationOfZeroWalletWithoutEntries(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "0.00")

	r := e.reconcile(t, w.ID)
	want := reconciliationBody{
		WalletID:          w.ID,
		StoredBalance:     moneyBody{"0.00", "BRL"},
		CalculatedBalance: moneyBody{"0.00", "BRL"},
		Difference:        moneyBody{"0.00", "BRL"},
		Consistent:        true,
		CheckedEntries:    0,
	}
	if r != want {
		t.Errorf("reconciliation = %+v, want %+v", r, want)
	}
}

func TestReconciliationReportsDivergenceWithoutFixingIt(t *testing.T) {
	logs, capture := captureLogs()
	e := newEnv(t, capture)
	w := e.openWalletWith(t, uuid.New(), "100.00")
	walletID := uuid.MustParse(w.ID)
	ctx := context.Background()

	// Tamper with the stored balance as the schema owner (the app role
	// cannot bypass the use cases, but an operator or a bug could).
	setBalance := func(minor int64) {
		t.Helper()
		if _, err := e.db.Exec(ctx, "UPDATE wallets SET balance = $1 WHERE id = $2", minor, walletID); err != nil {
			t.Fatalf("tamper balance: %v", err)
		}
	}
	rows := func() int {
		return e.count(t, `SELECT (SELECT count(*) FROM wager_transactions) + (SELECT count(*) FROM ledger_entries)
			+ (SELECT count(*) FROM outbox_events)`)
	}
	before := rows()

	cases := []struct {
		stored     int64
		storedBody moneyBody
		difference moneyBody
	}{
		{12345, moneyBody{"123.45", "BRL"}, moneyBody{"23.45", "BRL"}},
		{0, moneyBody{"0.00", "BRL"}, moneyBody{"-100.00", "BRL"}},
	}
	for i, c := range cases {
		setBalance(c.stored)
		correlationID := "reconcile-divergence-" + uuid.NewString()
		h := http.Header{}
		h.Set("Authorization", "Bearer "+testsupport.Token(t, testsupport.InternalClient))
		h.Set("X-Correlation-Id", correlationID)
		resp, body := e.app.DoWith(t, http.MethodPost, "/wallets/"+w.ID+"/reconciliation", "", h)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("reconcile = %d %s, want 200", resp.StatusCode, body)
		}
		got := decode[reconciliationBody](t, body)
		want := reconciliationBody{
			WalletID:          w.ID,
			StoredBalance:     c.storedBody,
			CalculatedBalance: moneyBody{"100.00", "BRL"},
			Difference:        c.difference,
			Consistent:        false,
			CheckedEntries:    1,
		}
		if got != want {
			t.Errorf("reconciliation with stored %d = %+v, want %+v", c.stored, got, want)
		}

		// Reconciliation reports, it never repairs.
		var stored int64
		if err := e.db.QueryRow(ctx, "SELECT balance FROM wallets WHERE id = $1", walletID).Scan(&stored); err != nil {
			t.Fatalf("read balance: %v", err)
		}
		if stored != c.stored {
			t.Errorf("stored balance = %d after reconciliation, want it untouched at %d", stored, c.stored)
		}

		n := float64(i + 1)
		if v := e.metricValue(t, seriesDivergences); v != n {
			t.Errorf("%s = %v, want %v", seriesDivergences, v, n)
		}
		if v := e.metricValue(t, seriesDivergent); v != n {
			t.Errorf("%s = %v, want %v", seriesDivergent, v, n)
		}

		var found bool
		for _, rec := range logs.records(t, "reconciliation divergence") {
			if rec["correlationId"] != correlationID {
				continue
			}
			found = true
			if rec["level"] != "ERROR" || rec["walletId"] != w.ID ||
				rec["storedBalance"] != c.storedBody.Amount || rec["calculatedBalance"] != "100.00" ||
				rec["difference"] != c.difference.Amount || rec["currency"] != "BRL" {
				t.Errorf("divergence log = %v", rec)
			}
		}
		if !found {
			t.Errorf("no divergence log with correlationId %s", correlationID)
		}
	}

	if got := rows(); got != before {
		t.Errorf("financial rows = %d after reconciliations, want %d (no writes)", got, before)
	}
	if v := e.metricValue(t, seriesConsistent); v != 0 {
		t.Errorf("%s = %v, want 0", seriesConsistent, v)
	}
}

func TestReconciliationAuthorizationAndUnknownWallets(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "5.00")
	path := "/wallets/" + w.ID + "/reconciliation"

	resp, body := e.app.Do(t, http.MethodPost, path, "", "")
	expectError(t, resp, body, http.StatusUnauthorized, "UNAUTHENTICATED")

	for _, client := range []string{testsupport.ProviderAClient, testsupport.ProviderBClient} {
		resp, body := e.app.Do(t, http.MethodPost, path, testsupport.Token(t, client), "")
		expectError(t, resp, body, http.StatusForbidden, "FORBIDDEN")
	}

	internal := testsupport.Token(t, testsupport.InternalClient)
	resp, body = e.app.Do(t, http.MethodPost, "/wallets/"+uuid.NewString()+"/reconciliation", internal, "")
	expectError(t, resp, body, http.StatusNotFound, "NOT_FOUND")

	resp, body = e.app.Do(t, http.MethodPost, "/wallets/not-a-uuid/reconciliation", internal, "")
	expectError(t, resp, body, http.StatusBadRequest, "INVALID_REQUEST")

	if v := e.metricValue(t, seriesConsistent) + e.metricValue(t, seriesDivergent); v != 0 {
		t.Errorf("reconciliations counted = %v after rejected requests, want 0", v)
	}
}
