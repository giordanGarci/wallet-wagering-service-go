package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/fx"
	"golang.org/x/time/rate"

	"github.com/junglegaming/backend-challenge-go/internal/config"
)

// InternalClientID is the Keycloak client of the internal wallet service.
const InternalClientID = "wallet-internal"

// PrincipalKind is the role a token grants.
type PrincipalKind int

const (
	// PrincipalUnknown is a valid token of a client with no role here.
	PrincipalUnknown PrincipalKind = iota
	// PrincipalProvider is a game Provider, identified by provider_id.
	PrincipalProvider
	// PrincipalInternal is the internal wallet service.
	PrincipalInternal
)

// Principal is the authenticated caller of a request.
type Principal struct {
	Kind PrincipalKind
	// ClientID is the OAuth client (azp, or client_id when azp is absent).
	ClientID string
	// ProviderID is the provider_id claim; set only for PrincipalProvider.
	// Provider-facing handlers filter every read and write by it.
	ProviderID string
}

// IsInternal reports whether p is the internal wallet service.
func (p Principal) IsInternal() bool { return p.Kind == PrincipalInternal }

// IsProvider reports whether p is a Provider.
func (p Principal) IsProvider() bool { return p.Kind == PrincipalProvider && p.ProviderID != "" }

type principalKey struct{}

// ContextWithPrincipal returns ctx carrying p.
func ContextWithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the Principal stored by the authentication
// middleware. ok is false outside an authenticated route.
func PrincipalFrom(ctx context.Context) (p Principal, ok bool) {
	p, ok = ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Policy decides whether an authenticated Principal may call a route.
type Policy func(Principal) bool

// Access policies for Authenticator.Require.
var (
	InternalOnly Policy = Principal.IsInternal
	ProviderOnly Policy = Principal.IsProvider
)

// ErrUnauthenticated reports a missing or invalid access token.
var ErrUnauthenticated = errors.New("unauthenticated")

// tokenClaims are the claims read from Keycloak access tokens.
type tokenClaims struct {
	jwt.RegisteredClaims
	AuthorizedParty string `json:"azp"`
	ClientID        string `json:"client_id"`
	ProviderID      string `json:"provider_id"`
}

// Authenticator validates bearer tokens locally: signature against the
// cached JWKS (refreshed hourly and on an unknown kid, at most once every
// 30s), then iss, aud, exp (required) and nbf, with 30s of clock leeway.
type Authenticator struct {
	cfg    config.OIDC
	log    *slog.Logger
	parser *jwt.Parser
	keys   keyfunc.Keyfunc
}

// NewAuthenticator builds the Authenticator. The JWKS is fetched on start
// (failing start when unreachable) and its refresh goroutine and HTTP
// connections are released on stop.
func NewAuthenticator(lc fx.Lifecycle, cfg config.Config, log *slog.Logger) *Authenticator {
	a := &Authenticator{
		cfg: cfg.OIDC,
		log: log,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512"}),
			jwt.WithIssuer(cfg.OIDC.IssuerURL),
			jwt.WithAudience(cfg.OIDC.Audience),
			jwt.WithExpirationRequired(),
			jwt.WithLeeway(30*time.Second),
		),
	}
	refreshCtx, cancel := context.WithCancel(context.Background())
	client := &http.Client{Transport: &http.Transport{}, Timeout: 10 * time.Second}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			fail := false
			keys, err := keyfunc.NewDefaultOverrideCtx(refreshCtx, []string{cfg.OIDC.JWKSURL}, keyfunc.Override{
				Client:                    client,
				HTTPTimeout:               10 * time.Second,
				NoErrorReturnFirstHTTPReq: &fail,
				RateLimitWaitMax:          time.Second,
				RefreshUnknownKID:         rate.NewLimiter(rate.Every(30*time.Second), 1),
				RefreshErrorHandlerFunc: func(u string) func(context.Context, error) {
					return func(ctx context.Context, err error) {
						log.WarnContext(ctx, "jwks refresh failed", "url", u, "error", err)
					}
				},
			})
			if err != nil {
				cancel()
				return fmt.Errorf("oidc: fetch JWKS from %s: %w", cfg.OIDC.JWKSURL, err)
			}
			a.keys = keys
			log.InfoContext(ctx, "oidc keys loaded", "issuer", cfg.OIDC.IssuerURL, "jwks", cfg.OIDC.JWKSURL)
			return nil
		},
		OnStop: func(context.Context) error {
			cancel()
			client.CloseIdleConnections()
			return nil
		},
	})
	return a
}

// Authenticate validates a raw token and returns its Principal.
func (a *Authenticator) Authenticate(ctx context.Context, raw string) (Principal, error) {
	var c tokenClaims
	if _, err := a.parser.ParseWithClaims(raw, &c, a.keys.KeyfuncCtx(ctx)); err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrUnauthenticated, err)
	}
	p := Principal{ClientID: c.AuthorizedParty}
	if p.ClientID == "" {
		p.ClientID = c.ClientID
	}
	switch {
	case c.ProviderID != "":
		p.Kind, p.ProviderID = PrincipalProvider, c.ProviderID
	case p.ClientID == InternalClientID:
		p.Kind = PrincipalInternal
	}
	return p, nil
}

// Require wraps next with authentication (401 UNAUTHENTICATED) and the
// allow policy (403 FORBIDDEN). next finds the Principal with PrincipalFrom.
func (a *Authenticator) Require(allow Policy, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearerToken(r)
		if !ok {
			unauthenticated(w, `Bearer realm="wallet-api"`, "missing bearer token")
			return
		}
		p, err := a.Authenticate(r.Context(), raw)
		if err != nil {
			a.log.DebugContext(r.Context(), "token rejected", "error", err)
			unauthenticated(w, `Bearer realm="wallet-api", error="invalid_token"`, "invalid or expired access token")
			return
		}
		if !allow(p) {
			WriteError(w, http.StatusForbidden, CodeForbidden, "client is not allowed to call this endpoint")
			return
		}
		next.ServeHTTP(w, r.WithContext(ContextWithPrincipal(r.Context(), p)))
	})
}

// Internal is Require(InternalOnly, next).
func (a *Authenticator) Internal(next http.Handler) http.Handler {
	return a.Require(InternalOnly, next)
}

// Provider is Require(ProviderOnly, next).
func (a *Authenticator) Provider(next http.Handler) http.Handler {
	return a.Require(ProviderOnly, next)
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}

func unauthenticated(w http.ResponseWriter, challenge, message string) {
	w.Header().Set("WWW-Authenticate", challenge)
	WriteError(w, http.StatusUnauthorized, CodeUnauthenticated, message)
}
