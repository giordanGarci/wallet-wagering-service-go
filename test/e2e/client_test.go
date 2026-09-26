//go:build e2e

package e2e_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

type money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type walletBody struct {
	ID       string `json:"id"`
	PlayerID string `json:"playerId"`
	Balance  money  `json:"balance"`
	Version  int64  `json:"version"`
}

// wagerResponse is the body of POST /wagering/transactions.
type wagerResponse struct {
	TransactionID    string `json:"transactionId"`
	Status           string `json:"status"`
	Balance          *money `json:"balance"`
	FailureCode      string `json:"failureCode"`
	IdempotentReplay bool   `json:"idempotentReplay"`
}

// wager is a POST /wagering/transactions body.
type wager struct {
	ProviderID            string `json:"providerId"`
	ExternalTransactionID string `json:"externalTransactionId"`
	PlayerID              string `json:"playerId"`
	WalletID              string `json:"walletId"`
	RoundID               string `json:"roundId"`
	GameID                string `json:"gameId"`
	Kind                  string `json:"kind"`
	Money                 money  `json:"money"`
	Reference             string `json:"referenceExternalTransactionId,omitempty"`
}

// bet is a provider-a BET of amount BRL on w with a fresh external id.
func bet(w walletBody, amount string) wager {
	return wager{
		ProviderID:            testsupport.ProviderAClient,
		ExternalTransactionID: "bet-" + uuid.NewString(),
		PlayerID:              w.PlayerID,
		WalletID:              w.ID,
		RoundID:               "round-" + uuid.NewString()[:8],
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Money:                 money{Amount: amount, Currency: "BRL"},
	}
}

// reversalOf is a REFUND or ROLLBACK of ref (same Round and amount).
func reversalOf(ref wager, kind string) wager {
	r := ref
	r.Kind = kind
	r.ExternalTransactionID = strings.ToLower(kind) + "-" + uuid.NewString()
	r.Reference = ref.ExternalTransactionID
	return r
}

// key is the conventional Idempotency-Key of b.
func (b wager) key() string { return b.ProviderID + ":" + b.ExternalTransactionID }

func (b wager) json() string {
	raw, err := json.Marshal(b)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// reply is one HTTP answer.
type reply struct {
	Status int
	Body   []byte
}

// do sends a request to inst without touching t (safe from goroutines).
func (c *Cluster) do(inst *Instance, method, path, token, body string, header http.Header) (reply, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, inst.BaseURL+path, rd)
	if err != nil {
		return reply{}, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return reply{}, fmt.Errorf("%s %s%s: %w", method, inst.Name, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return reply{}, fmt.Errorf("%s %s%s: read body: %w", method, inst.Name, path, err)
	}
	return reply{Status: resp.StatusCode, Body: b}, nil
}

// PostWager sends b to inst with a provider-a token and b's key. Safe from
// goroutines when token was fetched beforehand (testsupport.Token).
func (c *Cluster) PostWager(inst *Instance, token string, b wager) (reply, error) {
	return c.do(inst, http.MethodPost, "/wagering/transactions", token, b.json(),
		http.Header{"Idempotency-Key": {b.key()}})
}

// Send posts b to inst and requires status; it returns the decoded body.
func (c *Cluster) Send(t *testing.T, inst *Instance, b wager, status int) wagerResponse {
	t.Helper()
	r, err := c.PostWager(inst, testsupport.Token(t, testsupport.ProviderAClient), b)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != status {
		t.Fatalf("%s: POST %s %s = %d %s, want %d", inst.Name, b.Kind, b.ExternalTransactionID, r.Status, r.Body, status)
	}
	return decode[wagerResponse](t, r.Body)
}

// OpenWallet opens a wallet with initial BRL through inst.
func (c *Cluster) OpenWallet(t *testing.T, inst *Instance, initial string) walletBody {
	t.Helper()
	body := `{"playerId":"` + uuid.NewString() + `","initialBalance":{"amount":"` + initial + `","currency":"BRL"}}`
	r, err := c.do(inst, http.MethodPost, "/wallets", testsupport.Token(t, testsupport.InternalClient), body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != http.StatusCreated {
		t.Fatalf("%s: open wallet = %d %s, want 201", inst.Name, r.Status, r.Body)
	}
	return decode[walletBody](t, r.Body)
}

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}
