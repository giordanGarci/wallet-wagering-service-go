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

func TestOpenWalletWithZeroBalanceAndGetIt(t *testing.T) {
	e := newEnv(t)
	token := testsupport.Token(t, testsupport.InternalClient)
	playerID := uuid.New()

	resp, body := e.app.Do(t, http.MethodPost, "/wallets", token, openBody(playerID, "0.00", "BRL"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /wallets = %d %s, want 201", resp.StatusCode, body)
	}
	w := decode[walletBody](t, body)
	id, err := uuid.Parse(w.ID)
	if err != nil || id.Version() != 7 {
		t.Errorf("id = %q, want a UUIDv7", w.ID)
	}
	if w.PlayerID != playerID.String() || w.Balance.Amount != "0.00" || w.Balance.Currency != "BRL" || w.Version != 1 {
		t.Errorf("wallet = %+v, want player %s, 0.00 BRL, version 1", w, playerID)
	}
	created, err := time.Parse(time.RFC3339Nano, w.CreatedAt)
	if err != nil || time.Since(created) > time.Minute || created.Location() != time.UTC {
		t.Errorf("createdAt = %q, want a recent UTC RFC 3339 timestamp", w.CreatedAt)
	}
	if loc := resp.Header.Get("Location"); loc != "/wallets/"+w.ID {
		t.Errorf("Location = %q, want /wallets/%s", loc, w.ID)
	}

	resp, got := e.app.Do(t, http.MethodGet, "/wallets/"+w.ID, token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d %s, want 200", resp.StatusCode, got)
	}
	if string(got) != string(body) {
		t.Errorf("GET body %s differs from POST body %s", got, body)
	}

	var balance, version int64
	var currency string
	err = e.app.Pool.QueryRow(context.Background(),
		"SELECT balance, version, currency FROM wallets WHERE id = $1 AND player_id = $2", id, playerID).
		Scan(&balance, &version, &currency)
	if err != nil || balance != 0 || version != 1 || currency != "BRL" {
		t.Errorf("row = (%d, %d, %q, %v), want (0, 1, BRL)", balance, version, currency, err)
	}
}

func TestOpenWalletTwiceForSamePairConflicts(t *testing.T) {
	e := newEnv(t)
	token := testsupport.Token(t, testsupport.InternalClient)
	playerID := uuid.New()

	if resp, body := e.app.Do(t, http.MethodPost, "/wallets", token, openBody(playerID, "0.00", "BRL")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first open = %d %s", resp.StatusCode, body)
	}
	resp, body := e.app.Do(t, http.MethodPost, "/wallets", token, openBody(playerID, "0.00", "BRL"))
	expectError(t, resp, body, http.StatusConflict, "WALLET_ALREADY_EXISTS")

	// Another currency for the same player is a different Wallet.
	if resp, body := e.app.Do(t, http.MethodPost, "/wallets", token, openBody(playerID, "0.00", "USD")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("open in USD = %d %s, want 201", resp.StatusCode, body)
	}
	if n := e.walletCount(t); n != 2 {
		t.Errorf("wallets = %d, want 2", n)
	}
}

func TestConcurrentOpeningsForSamePairCreateOneWallet(t *testing.T) {
	e := newEnv(t)
	token := testsupport.Token(t, testsupport.InternalClient)
	body := openBody(uuid.New(), "0.00", "BRL")

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

	created, conflicts := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		}
	}
	if created != 1 || conflicts != n-1 {
		t.Errorf("codes = %v, want one 201 and %d 409", codes, n-1)
	}
	if got := e.walletCount(t); got != 1 {
		t.Errorf("wallets = %d, want 1", got)
	}
}

func TestGetWalletNotFoundAndInvalidID(t *testing.T) {
	e := newEnv(t)
	token := testsupport.Token(t, testsupport.InternalClient)

	resp, body := e.app.Do(t, http.MethodGet, "/wallets/"+uuid.Must(uuid.NewV7()).String(), token, "")
	expectError(t, resp, body, http.StatusNotFound, "NOT_FOUND")

	resp, body = e.app.Do(t, http.MethodGet, "/wallets/not-a-uuid", token, "")
	expectError(t, resp, body, http.StatusBadRequest, "INVALID_REQUEST")
}

func TestOpenWalletRejectsMalformedRequests(t *testing.T) {
	e := newEnv(t)
	token := testsupport.Token(t, testsupport.InternalClient)
	player := uuid.New().String()

	tests := []struct{ name, body string }{
		{"empty body", ""},
		{"not json", "{"},
		{"array", "[]"},
		{"unknown field", `{"playerId":"` + player + `","initialBalance":{"amount":"0.00","currency":"BRL"},"extra":1}`},
		{"trailing data", `{"playerId":"` + player + `","initialBalance":{"amount":"0.00","currency":"BRL"}} {}`},
		{"missing playerId", `{"initialBalance":{"amount":"0.00","currency":"BRL"}}`},
		{"nil playerId", `{"playerId":"00000000-0000-0000-0000-000000000000","initialBalance":{"amount":"0.00","currency":"BRL"}}`},
		{"playerId not uuid", `{"playerId":"player-1","initialBalance":{"amount":"0.00","currency":"BRL"}}`},
		{"missing initialBalance", `{"playerId":"` + player + `"}`},
		{"null initialBalance", `{"playerId":"` + player + `","initialBalance":null}`},
		{"amount as number", `{"playerId":"` + player + `","initialBalance":{"amount":0,"currency":"BRL"}}`},
		{"non-canonical amount", `{"playerId":"` + player + `","initialBalance":{"amount":"0","currency":"BRL"}}`},
		{"negative amount", `{"playerId":"` + player + `","initialBalance":{"amount":"-1.00","currency":"BRL"}}`},
		{"unknown currency", `{"playerId":"` + player + `","initialBalance":{"amount":"0.00","currency":"XXX"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := e.app.Do(t, http.MethodPost, "/wallets", token, tt.body)
			expectError(t, resp, body, http.StatusBadRequest, "INVALID_REQUEST")
		})
	}
	if n := e.walletCount(t); n != 0 {
		t.Errorf("wallets = %d, want 0", n)
	}
}

// TestOpenWalletWithPositiveBalanceIsNotImplementedYet pins the temporary
// behaviour until the Opening lands (ticket 04).
func TestOpenWalletWithPositiveBalanceIsNotImplementedYet(t *testing.T) {
	e := newEnv(t)
	resp, body := e.app.Do(t, http.MethodPost, "/wallets", testsupport.Token(t, testsupport.InternalClient),
		openBody(uuid.New(), "10.00", "BRL"))
	expectError(t, resp, body, http.StatusNotImplemented, "NOT_IMPLEMENTED")
	if n := e.walletCount(t); n != 0 {
		t.Errorf("wallets = %d, want 0", n)
	}
}
