package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestLoadPendingDefaults(t *testing.T) {
	cfg, err := load(validEnv())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := config.Pending{
		PollInterval: time.Second,
		BatchSize:    20,
		Lease:        30 * time.Second,
		MaxAttempts:  8,
		RetryBase:    2 * time.Second,
		RetryMax:     2 * time.Minute,
	}
	if cfg.Pending != want {
		t.Errorf("Pending = %+v, want %+v", cfg.Pending, want)
	}
	if got := cfg.Pending.Policy(); got != wager.DefaultPendingPolicy() {
		t.Errorf("Policy() = %+v, want the domain defaults %+v", got, wager.DefaultPendingPolicy())
	}
}

func TestLoadPendingOverrides(t *testing.T) {
	env := validEnv()
	env["PENDING_POLL_INTERVAL"] = "50ms"
	env["PENDING_BATCH_SIZE"] = "5"
	env["PENDING_LEASE_DURATION"] = "3s"
	env["PENDING_MAX_ATTEMPTS"] = "2"
	env["PENDING_RETRY_BASE"] = "10ms"
	env["PENDING_RETRY_MAX"] = "40ms"
	cfg, err := load(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := config.Pending{PollInterval: 50 * time.Millisecond, BatchSize: 5, Lease: 3 * time.Second,
		MaxAttempts: 2, RetryBase: 10 * time.Millisecond, RetryMax: 40 * time.Millisecond}
	if cfg.Pending != want {
		t.Errorf("Pending = %+v, want %+v", cfg.Pending, want)
	}
	wantPolicy := wager.PendingPolicy{MaxAttempts: 2, BaseDelay: 10 * time.Millisecond, MaxDelay: 40 * time.Millisecond}
	if got := cfg.Pending.Policy(); got != wantPolicy {
		t.Errorf("Policy() = %+v, want %+v", got, wantPolicy)
	}
}

func TestLoadRejectsInvalidPending(t *testing.T) {
	tests := []struct {
		key, value, wantErr string
	}{
		{"PENDING_MAX_ATTEMPTS", "0", "PENDING_MAX_ATTEMPTS: must be an integer from 1 to 1000"},
		{"PENDING_MAX_ATTEMPTS", "-3", "PENDING_MAX_ATTEMPTS"},
		{"PENDING_MAX_ATTEMPTS", "many", "PENDING_MAX_ATTEMPTS"},
		{"PENDING_MAX_ATTEMPTS", "1001", "PENDING_MAX_ATTEMPTS"},
		{"PENDING_POLL_INTERVAL", "0s", "PENDING_POLL_INTERVAL: must be positive"},
		{"PENDING_LEASE_DURATION", "later", "PENDING_LEASE_DURATION: \"later\" is not a duration"},
		{"PENDING_BATCH_SIZE", "0", "PENDING_BATCH_SIZE: must be an integer from 1 to 1000"},
		{"PENDING_RETRY_BASE", "-1s", "PENDING_RETRY_BASE: must be positive"},
		{"PENDING_RETRY_BASE", "5m", "PENDING_RETRY_BASE: must not exceed PENDING_RETRY_MAX"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			env := validEnv()
			env[tt.key] = tt.value
			_, err := load(env)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}
