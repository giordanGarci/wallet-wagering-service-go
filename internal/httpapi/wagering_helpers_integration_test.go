//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

// wagerResponse is the body of POST /wagering/transactions.
type wagerResponse struct {
	TransactionID    string     `json:"transactionId"`
	Status           string     `json:"status"`
	Balance          *moneyBody `json:"balance"`
	FailureCode      string     `json:"failureCode"`
	IdempotentReplay bool       `json:"idempotentReplay"`
}

// wagerView is the body of the GET transaction endpoints.
type wagerView struct {
	TransactionID                  string     `json:"transactionId"`
	ProviderID                     string     `json:"providerId"`
	ExternalTransactionID          string     `json:"externalTransactionId"`
	Kind                           string     `json:"kind"`
	Status                         string     `json:"status"`
	WalletID                       string     `json:"walletId"`
	PlayerID                       string     `json:"playerId"`
	RoundID                        string     `json:"roundId"`
	GameID                         string     `json:"gameId"`
	Money                          moneyBody  `json:"money"`
	Balance                        *moneyBody `json:"balance"`
	FailureCode                    string     `json:"failureCode"`
	ReferenceExternalTransactionID string     `json:"referenceExternalTransactionId"`
	CreatedAt                      string     `json:"createdAt"`
	UpdatedAt                      string     `json:"updatedAt"`
}

// wager is a POST /wagering/transactions body. Empty fields are omitted
// so tests can build "missing field" cases.
type wager struct {
	ProviderID            string          `json:"providerId,omitempty"`
	ExternalTransactionID string          `json:"externalTransactionId,omitempty"`
	PlayerID              string          `json:"playerId,omitempty"`
	WalletID              string          `json:"walletId,omitempty"`
	RoundID               string          `json:"roundId,omitempty"`
	GameID                string          `json:"gameId,omitempty"`
	Kind                  string          `json:"kind,omitempty"`
	Money                 json.RawMessage `json:"money,omitempty"`
	Reference             string          `json:"referenceExternalTransactionId,omitempty"`
}

// bet returns a provider-a BET of amount BRL on w with a fresh external id.
func bet(w walletBody, amount string) wager {
	return wager{
		ProviderID:            "provider-a",
		ExternalTransactionID: "bet-" + uuid.NewString(),
		PlayerID:              w.PlayerID,
		WalletID:              w.ID,
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Money:                 json.RawMessage(`{"amount":"` + amount + `","currency":"BRL"}`),
	}
}

func (b wager) json(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal wager: %v", err)
	}
	return string(raw)
}

// key is the conventional Idempotency-Key of b.
func (b wager) key() string { return b.ProviderID + ":" + b.ExternalTransactionID }

// postWager sends body with the client's token and Idempotency-Key key
// ("" sends no header).
func (e *env) postWager(t *testing.T, client, key, body string) (*http.Response, []byte) {
	t.Helper()
	h := http.Header{"Authorization": {"Bearer " + testsupport.Token(t, client)}}
	if key != "" {
		h.Set("Idempotency-Key", key)
	}
	return e.app.DoWith(t, http.MethodPost, "/wagering/transactions", body, h)
}

// send posts b as provider-a with its conventional key and decodes the
// response, asserting the HTTP status.
func (e *env) send(t *testing.T, b wager, status int) wagerResponse {
	t.Helper()
	resp, body := e.postWager(t, testsupport.ProviderAClient, b.key(), b.json(t))
	if resp.StatusCode != status {
		t.Fatalf("POST /wagering/transactions = %d %s, want %d", resp.StatusCode, body, status)
	}
	return decode[wagerResponse](t, body)
}

// wagerRows counts the Wager Transactions of provider-a on walletID.
func (e *env) wagerRows(t *testing.T, walletID string) int {
	t.Helper()
	return e.count(t, "SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND origin = 'EXTERNAL'", walletID)
}

// totalExternalRows counts every external Wager Transaction.
func (e *env) totalExternalRows(t *testing.T) int {
	t.Helper()
	return e.count(t, "SELECT count(*) FROM wager_transactions WHERE origin = 'EXTERNAL'")
}

// walletState reads the stored balance (minor units) and version.
func (e *env) walletState(t *testing.T, walletID string) (balance, version int64) {
	t.Helper()
	err := e.db.QueryRow(context.Background(), "SELECT balance, version FROM wallets WHERE id = $1", walletID).
		Scan(&balance, &version)
	if err != nil {
		t.Fatalf("read wallet %s: %v", walletID, err)
	}
	return balance, version
}

// assertReconciled checks the Reconciliation invariant with SQL (the stored
// balance equals credits minus debits of the ledger) and through the
// Reconciliation endpoint (e.requireConsistent).
func (e *env) assertReconciled(t *testing.T, walletID string) {
	t.Helper()
	var stored, calculated int64
	err := e.db.QueryRow(context.Background(), `
		SELECT w.balance,
		       COALESCE(SUM(CASE l.direction WHEN 'CREDIT' THEN l.amount_minor ELSE -l.amount_minor END), 0)
		FROM wallets w LEFT JOIN ledger_entries l ON l.wallet_id = w.id
		WHERE w.id = $1 GROUP BY w.balance`, walletID).Scan(&stored, &calculated)
	if err != nil {
		t.Fatalf("reconcile %s: %v", walletID, err)
	}
	if stored != calculated {
		t.Errorf("wallet %s: stored balance %d != ledger credits - debits %d", walletID, stored, calculated)
	}
	e.requireConsistent(t, walletID)
}
