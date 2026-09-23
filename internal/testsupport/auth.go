//go:build integration || e2e

package testsupport

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Keycloak endpoints used by the harness:
//
//	TEST_KEYCLOAK_URL     Keycloak base URL (default http://localhost:8081)
//	TEST_OIDC_ISSUER_URL  issuer the app expects (default TEST_KEYCLOAK_URL/realms/wallet);
//	                      differs from TEST_KEYCLOAK_URL inside the compose network,
//	                      where Keycloak is reached as keycloak:8080 but still
//	                      issues iss=http://localhost:8081/realms/wallet
const (
	defaultKeycloakURL = "http://localhost:8081"
	realmPath          = "/realms/wallet"

	// InternalClient is the Keycloak client of the internal wallet service.
	InternalClient = "wallet-internal"
	// ProviderAClient and ProviderBClient are the Provider clients; their
	// tokens carry provider_id equal to the client id.
	ProviderAClient = "provider-a"
	ProviderBClient = "provider-b"
)

// KeycloakURL is the Keycloak base URL reachable from the test process.
func KeycloakURL() string {
	return strings.TrimSuffix(env("TEST_KEYCLOAK_URL", defaultKeycloakURL), "/")
}

// IssuerURL is the issuer the app is configured to expect.
func IssuerURL() string { return env("TEST_OIDC_ISSUER_URL", KeycloakURL()+realmPath) }

// KeycloakJWKSURL is the realm JWKS endpoint reachable from the test process.
func KeycloakJWKSURL() string { return KeycloakURL() + realmPath + "/protocol/openid-connect/certs" }

var tokenCache = struct {
	sync.Mutex
	m map[string]cachedToken
}{m: map[string]cachedToken{}}

type cachedToken struct {
	value   string
	expires time.Time
}

// Token returns a real access token for clientID obtained from Keycloak with
// client_credentials (secret "<clientID>-secret"). Tokens are cached per
// client until 30s before they expire.
func Token(t testing.TB, clientID string) string {
	t.Helper()
	tokenCache.Lock()
	defer tokenCache.Unlock()
	if c, ok := tokenCache.m[clientID]; ok && time.Now().Before(c.expires) {
		return c.value
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientID + "-secret"},
	}
	client := &http.Client{Transport: &http.Transport{}, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	resp, err := client.PostForm(KeycloakURL()+realmPath+"/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatalf("testsupport: token for %s: %v (is keycloak up?)", clientID, err)
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		t.Fatalf("testsupport: token for %s: status %d, decode error %v", clientID, resp.StatusCode, err)
	}
	tokenCache.m[clientID] = cachedToken{
		value:   body.AccessToken,
		expires: time.Now().Add(time.Duration(body.ExpiresIn)*time.Second - 30*time.Second),
	}
	return body.AccessToken
}

// Forger signs tokens with a test key that the app trusts, so tests can
// produce tokens Keycloak would never issue (expired, other audience, other
// issuer) and prove each claim is checked, not only the signature.
//
// It serves a JWKS with Keycloak's keys plus its own public key; point the
// app at it with cfg.OIDC.JWKSURL = f.JWKSURL(). Real Keycloak tokens keep
// working against that JWKS.
type Forger struct {
	key *rsa.PrivateKey
	kid string
	srv *httptest.Server
}

// NewForger generates the signing key and starts the JWKS server, closed on
// test cleanup.
func NewForger(t testing.TB) *Forger {
	t.Helper()
	f := &Forger{key: newRSAKey(t), kid: "test-forger-" + randomSuffix(t)}
	upstream := &http.Client{Transport: &http.Transport{}, Timeout: 10 * time.Second}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys, err := fetchJWKS(r, upstream)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		pub := f.key.PublicKey
		keys = append(keys, mustJSON(map[string]string{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": f.kid,
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	}))
	t.Cleanup(func() {
		f.srv.Close()
		upstream.CloseIdleConnections()
	})
	return f
}

// JWKSURL is the URL of the merged JWKS.
func (f *Forger) JWKSURL() string { return f.srv.URL }

// Claims returns claims shaped like a valid Keycloak access token for
// clientID: iss, aud wallet-api, azp, client_id, iat, exp in five minutes and
// provider_id for the provider clients. Tests mutate them before signing.
func (f *Forger) Claims(clientID string) jwt.MapClaims {
	now := time.Now()
	c := jwt.MapClaims{
		"iss":       IssuerURL(),
		"aud":       []string{"wallet-api", "account"},
		"sub":       "service-account-" + clientID,
		"azp":       clientID,
		"client_id": clientID,
		"iat":       now.Unix(),
		"exp":       now.Add(5 * time.Minute).Unix(),
		"typ":       "Bearer",
	}
	if strings.HasPrefix(clientID, "provider-") {
		c["provider_id"] = clientID
	}
	return c
}

// Sign returns an RS256 token over claims signed with the trusted test key.
func (f *Forger) Sign(t testing.TB, claims jwt.MapClaims) string {
	t.Helper()
	return sign(t, f.key, f.kid, claims)
}

// SignWithUntrustedKey signs claims with a throwaway key while advertising
// the trusted key id, so the signature does not verify.
func (f *Forger) SignWithUntrustedKey(t testing.TB, claims jwt.MapClaims) string {
	t.Helper()
	return sign(t, newRSAKey(t), f.kid, claims)
}

func sign(t testing.TB, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("testsupport: sign token: %v", err)
	}
	return s
}

func newRSAKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("testsupport: generate key: %v", err)
	}
	return key
}

func fetchJWKS(r *http.Request, client *http.Client) ([]json.RawMessage, error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, KeycloakJWKSURL(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keycloak jwks: status %d", resp.StatusCode)
	}
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, err
	}
	return set.Keys, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
