//go:build integration

package httpapi_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

// TestInvalidTokensAreUnauthenticated: every way a token can be wrong gets
// 401 on the internal routes, and nothing is written. The forged-but-valid
// control proves the forger's key is trusted, so each 401 comes from the
// claim under test and not from the signature.
func TestInvalidTokensAreUnauthenticated(t *testing.T) {
	e := newEnv(t)
	f := e.forger
	existing := e.openWallet(t)
	before := e.walletCount(t)

	with := func(mutate func(jwt.MapClaims)) string {
		c := f.Claims(testsupport.InternalClient)
		mutate(c)
		return f.Sign(t, c)
	}
	past := time.Now().Add(-10 * time.Minute)

	tests := []struct {
		name   string
		header string // full Authorization header; "" sends none
	}{
		{"missing header", ""},
		{"not bearer", "Basic d2FsbGV0LWludGVybmFsOnNlY3JldA=="},
		{"empty bearer", "Bearer "},
		{"garbage", "Bearer not-a-jwt"},
		{"expired", "Bearer " + with(func(c jwt.MapClaims) { c["iat"] = past.Unix(); c["exp"] = past.Add(5 * time.Minute).Unix() })},
		{"missing exp", "Bearer " + with(func(c jwt.MapClaims) { delete(c, "exp") })},
		{"not yet valid", "Bearer " + with(func(c jwt.MapClaims) { c["nbf"] = time.Now().Add(10 * time.Minute).Unix() })},
		{"wrong audience", "Bearer " + with(func(c jwt.MapClaims) { c["aud"] = []string{"account", "other-api"} })},
		{"missing audience", "Bearer " + with(func(c jwt.MapClaims) { delete(c, "aud") })},
		{"wrong issuer", "Bearer " + with(func(c jwt.MapClaims) { c["iss"] = "http://evil.example/realms/wallet" })},
		{"bad signature", "Bearer " + f.SignWithUntrustedKey(t, f.Claims(testsupport.InternalClient))},
		{"tampered keycloak token", "Bearer " + tamper(testsupport.Token(t, testsupport.InternalClient))},
		{"alg none", "Bearer " + unsigned(f.Claims(testsupport.InternalClient))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, req := range []struct{ method, path, body string }{
				{http.MethodPost, "/wallets", openBody(uuid.New(), "0.00", "BRL")},
				{http.MethodGet, "/wallets/" + existing.ID, ""},
			} {
				resp, body := e.app.DoAuth(t, req.method, req.path, tt.header, req.body)
				expectError(t, resp, body, http.StatusUnauthorized, "UNAUTHENTICATED")
				if !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Bearer") {
					t.Errorf("%s %s: WWW-Authenticate = %q, want a Bearer challenge", req.method, req.path, resp.Header.Get("WWW-Authenticate"))
				}
			}
		})
	}

	if n := e.walletCount(t); n != before {
		t.Errorf("wallets = %d after rejected requests, want %d", n, before)
	}

	t.Run("control: valid forged internal token", func(t *testing.T) {
		resp, body := e.app.Do(t, http.MethodPost, "/wallets", f.Sign(t, f.Claims(testsupport.InternalClient)), openBody(uuid.New(), "0.00", "BRL"))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d %s, want 201", resp.StatusCode, body)
		}
	})
}

// TestProviderTokensAreForbiddenOnInternalRoutes: real Keycloak tokens of
// both providers get 403 on the internal wallet routes and create nothing.
func TestProviderTokensAreForbiddenOnInternalRoutes(t *testing.T) {
	e := newEnv(t)
	existing := e.openWallet(t)
	before := e.walletCount(t)

	for _, client := range []string{testsupport.ProviderAClient, testsupport.ProviderBClient} {
		t.Run(client, func(t *testing.T) {
			token := testsupport.Token(t, client)

			resp, body := e.app.Do(t, http.MethodPost, "/wallets", token, openBody(uuid.New(), "0.00", "BRL"))
			expectError(t, resp, body, http.StatusForbidden, "FORBIDDEN")

			resp, body = e.app.Do(t, http.MethodGet, "/wallets/"+existing.ID, token, "")
			expectError(t, resp, body, http.StatusForbidden, "FORBIDDEN")
			if strings.Contains(string(body), existing.PlayerID) {
				t.Errorf("403 body leaks the wallet: %s", body)
			}
		})
	}

	if n := e.walletCount(t); n != before {
		t.Errorf("wallets = %d after forbidden requests, want %d", n, before)
	}
}

// TestUnknownClientIsForbidden: a valid token of a client that is neither a
// Provider nor the internal service is authenticated but not authorized.
func TestUnknownClientIsForbidden(t *testing.T) {
	e := newEnv(t)
	c := e.forger.Claims("some-other-client")
	resp, body := e.app.Do(t, http.MethodPost, "/wallets", e.forger.Sign(t, c), openBody(uuid.New(), "0.00", "BRL"))
	expectError(t, resp, body, http.StatusForbidden, "FORBIDDEN")
	if n := e.walletCount(t); n != 0 {
		t.Errorf("wallets = %d, want 0", n)
	}
}

// tamper rewrites the payload of a real Keycloak token (azp changed) while
// keeping its header and signature.
func tamper(token string) string {
	parts := strings.Split(token, ".")
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		panic(err)
	}
	claims["azp"] = "someone-else"
	payload, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	return parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + parts[2]
}

func unsigned(claims jwt.MapClaims) string {
	s, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		panic(err)
	}
	return s
}
