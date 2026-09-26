package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/config"
)

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":         "postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable",
		"AWS_ENDPOINT_URL":     "http://localhost:4566",
		"SQS_INPUT_QUEUE_URL":  "http://localhost:4566/000000000000/wager-transactions.fifo",
		"SQS_EVENTS_QUEUE_URL": "http://localhost:4566/000000000000/wallet-events.fifo",
		"SQS_DLQ_QUEUE_URL":    "http://localhost:4566/000000000000/wager-transactions-dlq.fifo",
		"OIDC_ISSUER_URL":      "http://localhost:8081/realms/wallet",
	}
}

func load(env map[string]string) (config.Config, error) {
	return config.FromLookup(func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	})
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := load(validEnv())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.AWS.Region != "us-east-1" {
		t.Errorf("Region = %q, want us-east-1", cfg.AWS.Region)
	}
	if cfg.ShutdownTimeout != 25*time.Second {
		t.Errorf("ShutdownTimeout = %s, want 25s", cfg.ShutdownTimeout)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %s, want INFO", cfg.LogLevel)
	}
	for _, r := range config.KnownRoles {
		if !cfg.HasRole(r) {
			t.Errorf("default roles miss %q", r)
		}
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	env := validEnv()
	env["APP_ROLES"] = " API , outbox "
	env["SHUTDOWN_TIMEOUT"] = "3s"
	env["LOG_LEVEL"] = "debug"
	env["HTTP_ADDR"] = "127.0.0.1:0"

	cfg, err := load(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.HasRole(config.RoleAPI) || !cfg.HasRole(config.RoleOutbox) || cfg.HasRole(config.RoleConsumer) {
		t.Errorf("Roles = %v, want [api outbox]", cfg.Roles)
	}
	if cfg.ShutdownTimeout != 3*time.Second {
		t.Errorf("ShutdownTimeout = %s, want 3s", cfg.ShutdownTimeout)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %s, want DEBUG", cfg.LogLevel)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantErr string
	}{
		{"missing database url", func(e map[string]string) { delete(e, "DATABASE_URL") }, "DATABASE_URL: required"},
		{"database url wrong scheme", func(e map[string]string) { e["DATABASE_URL"] = "mysql://x@db/wallet" }, "DATABASE_URL: scheme must be"},
		{"missing input queue", func(e map[string]string) { e["SQS_INPUT_QUEUE_URL"] = " " }, "SQS_INPUT_QUEUE_URL: required"},
		{"missing events queue", func(e map[string]string) { delete(e, "SQS_EVENTS_QUEUE_URL") }, "SQS_EVENTS_QUEUE_URL: required"},
		{"endpoint not a url", func(e map[string]string) { e["AWS_ENDPOINT_URL"] = "ministack" }, "AWS_ENDPOINT_URL: not a valid URL"},
		{"unknown role", func(e map[string]string) { e["APP_ROLES"] = "api,cron" }, `APP_ROLES: unknown role "cron"`},
		{"duplicated role", func(e map[string]string) { e["APP_ROLES"] = "api,api" }, `APP_ROLES: role "api" listed twice`},
		{"no roles", func(e map[string]string) { e["APP_ROLES"] = " , " }, "APP_ROLES: at least one role is required"},
		{"shutdown not a duration", func(e map[string]string) { e["SHUTDOWN_TIMEOUT"] = "25" }, "SHUTDOWN_TIMEOUT"},
		{"shutdown negative", func(e map[string]string) { e["SHUTDOWN_TIMEOUT"] = "-1s" }, "SHUTDOWN_TIMEOUT: must be positive"},
		{"bad log level", func(e map[string]string) { e["LOG_LEVEL"] = "loud" }, "LOG_LEVEL"},
		{"bad http addr", func(e map[string]string) { e["HTTP_ADDR"] = "8080" }, "HTTP_ADDR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			tt.mutate(env)
			_, err := load(env)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.HasPrefix(err.Error(), "invalid configuration: ") {
				t.Errorf("error %q lacks the invalid configuration prefix", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	_, err := load(map[string]string{"APP_ROLES": "nope"})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	for _, want := range []string{"DATABASE_URL", "SQS_INPUT_QUEUE_URL", "SQS_EVENTS_QUEUE_URL", "APP_ROLES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestLoadDoesNotEchoDatabaseCredentials(t *testing.T) {
	env := validEnv()
	env["DATABASE_URL"] = "mysql://user:s3cr3t@db/wallet"
	_, err := load(env)
	if err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("error must exist and hide credentials, got %v", err)
	}
}

func TestLoadOIDCDefaults(t *testing.T) {
	cfg, err := load(validEnv())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OIDC.IssuerURL != "http://localhost:8081/realms/wallet" {
		t.Errorf("IssuerURL = %q", cfg.OIDC.IssuerURL)
	}
	if cfg.OIDC.Audience != "wallet-api" {
		t.Errorf("Audience = %q, want wallet-api", cfg.OIDC.Audience)
	}
	if want := "http://localhost:8081/realms/wallet/protocol/openid-connect/certs"; cfg.OIDC.JWKSURL != want {
		t.Errorf("JWKSURL = %q, want %q (derived from the issuer)", cfg.OIDC.JWKSURL, want)
	}
}

func TestLoadOIDCOverrides(t *testing.T) {
	env := validEnv()
	env["OIDC_ISSUER_URL"] = "http://localhost:8081/realms/wallet/"
	env["OIDC_AUDIENCE"] = "other-api"
	env["OIDC_JWKS_URL"] = "http://keycloak:8080/realms/wallet/protocol/openid-connect/certs"
	cfg, err := load(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OIDC.IssuerURL != "http://localhost:8081/realms/wallet/" {
		t.Errorf("IssuerURL = %q, want it verbatim (it must equal the token iss)", cfg.OIDC.IssuerURL)
	}
	if cfg.OIDC.Audience != "other-api" {
		t.Errorf("Audience = %q", cfg.OIDC.Audience)
	}
	if cfg.OIDC.JWKSURL != env["OIDC_JWKS_URL"] {
		t.Errorf("JWKSURL = %q", cfg.OIDC.JWKSURL)
	}
}

func TestLoadRejectsInvalidOIDC(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantErr string
	}{
		{"missing issuer with api role", func(e map[string]string) { delete(e, "OIDC_ISSUER_URL") }, "OIDC_ISSUER_URL: required"},
		{"issuer not a url", func(e map[string]string) { e["OIDC_ISSUER_URL"] = "keycloak" }, "OIDC_ISSUER_URL: not a valid URL"},
		{"jwks wrong scheme", func(e map[string]string) { e["OIDC_JWKS_URL"] = "ftp://keycloak/certs" }, "OIDC_JWKS_URL: scheme must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			tt.mutate(env)
			_, err := load(env)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadIssuerOptionalWithoutAPIRole(t *testing.T) {
	env := validEnv()
	delete(env, "OIDC_ISSUER_URL")
	env["APP_ROLES"] = "consumer,outbox"
	if _, err := load(env); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
