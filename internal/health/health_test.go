package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/health"
)

func ok(context.Context) error   { return nil }
func fail(context.Context) error { return errors.New("connection refused") }

func serve(t *testing.T, h http.Handler) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	return rec.Code, body
}

func TestLiveIsAlwaysOK(t *testing.T) {
	code, body := serve(t, health.LiveHandler())
	if code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("got %d %v", code, body)
	}
}

func TestReady(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name     string
		checks   []health.Check
		wantCode int
		wantDown string
	}{
		{"all up", []health.Check{{Name: "postgres", Check: ok}, {Name: "sqs", Check: ok}}, http.StatusOK, ""},
		{"postgres down", []health.Check{{Name: "postgres", Check: fail}, {Name: "sqs", Check: ok}}, http.StatusServiceUnavailable, "postgres"},
		{"sqs down", []health.Check{{Name: "postgres", Check: ok}, {Name: "sqs", Check: fail}}, http.StatusServiceUnavailable, "sqs"},
		{"check hangs past timeout", []health.Check{{Name: "sqs", Check: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}}}, http.StatusServiceUnavailable, "sqs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := serve(t, health.ReadyHandler(tt.checks, 50*time.Millisecond, log))
			if code != tt.wantCode {
				t.Fatalf("code = %d, want %d (%v)", code, tt.wantCode, body)
			}
			if tt.wantDown != "" {
				checks := body["checks"].(map[string]any)
				if checks[tt.wantDown] != "down" {
					t.Errorf("check %s = %v, want down", tt.wantDown, checks[tt.wantDown])
				}
			}
		})
	}
}
