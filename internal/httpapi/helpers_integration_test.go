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

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type walletBody struct {
	ID       string `json:"id"`
	PlayerID string `json:"playerId"`
	Balance  struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"balance"`
	Version   int64  `json:"version"`
	CreatedAt string `json:"createdAt"`
}

// env is one app with a Forger-backed JWKS, so both real Keycloak tokens
// and forged ones are accepted when valid.
type env struct {
	app    *testsupport.App
	forger *testsupport.Forger
}

func newEnv(t *testing.T) *env {
	t.Helper()
	infra := testsupport.NewInfra(t)
	forger := testsupport.NewForger(t)
	cfg := infra.Config
	cfg.OIDC.JWKSURL = forger.JWKSURL()
	return &env{app: testsupport.StartApp(t, cfg), forger: forger}
}

func openBody(playerID uuid.UUID, amount, currency string) string {
	return `{"playerId":"` + playerID.String() + `","initialBalance":{"amount":"` + amount + `","currency":"` + currency + `"}}`
}

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}

// expectError asserts the status and the stable error code of the body.
func expectError(t *testing.T, resp *http.Response, body []byte, status int, code string) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, status, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	e := decode[errorBody](t, body)
	if e.Code != code || e.Message == "" {
		t.Errorf("error body = %+v, want code %s and a message", e, code)
	}
}

func (e *env) walletCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.app.Pool.QueryRow(context.Background(), "SELECT count(*) FROM wallets").Scan(&n); err != nil {
		t.Fatalf("count wallets: %v", err)
	}
	return n
}

// openWallet opens a zero-balance BRL wallet with the internal client.
func (e *env) openWallet(t *testing.T) walletBody {
	t.Helper()
	resp, body := e.app.Do(t, http.MethodPost, "/wallets", testsupport.Token(t, testsupport.InternalClient),
		openBody(uuid.New(), "0.00", "BRL"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open wallet = %d %s, want 201", resp.StatusCode, body)
	}
	return decode[walletBody](t, body)
}
