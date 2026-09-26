package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/config"
)

func TestLoadOutboxDefaults(t *testing.T) {
	cfg, err := load(validEnv())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := config.Outbox{
		PollInterval: 500 * time.Millisecond,
		BatchSize:    50,
		Lease:        30 * time.Second,
		RetryBase:    time.Second,
		RetryMax:     5 * time.Minute,
	}
	if cfg.Outbox != want {
		t.Errorf("Outbox = %+v, want %+v", cfg.Outbox, want)
	}
}

func TestLoadOutboxOverrides(t *testing.T) {
	env := validEnv()
	env["OUTBOX_POLL_INTERVAL"] = "50ms"
	env["OUTBOX_BATCH_SIZE"] = "7"
	env["OUTBOX_LEASE_DURATION"] = "2s"
	env["OUTBOX_RETRY_BASE"] = "100ms"
	env["OUTBOX_RETRY_MAX"] = "1s"
	cfg, err := load(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := config.Outbox{PollInterval: 50 * time.Millisecond, BatchSize: 7, Lease: 2 * time.Second,
		RetryBase: 100 * time.Millisecond, RetryMax: time.Second}
	if cfg.Outbox != want {
		t.Errorf("Outbox = %+v, want %+v", cfg.Outbox, want)
	}
}

func TestLoadRejectsInvalidOutbox(t *testing.T) {
	tests := []struct {
		key, value, wantErr string
	}{
		{"OUTBOX_POLL_INTERVAL", "soon", "OUTBOX_POLL_INTERVAL: \"soon\" is not a duration"},
		{"OUTBOX_POLL_INTERVAL", "0s", "OUTBOX_POLL_INTERVAL: must be positive"},
		{"OUTBOX_LEASE_DURATION", "-1s", "OUTBOX_LEASE_DURATION: must be positive"},
		{"OUTBOX_BATCH_SIZE", "0", "OUTBOX_BATCH_SIZE: must be an integer from 1 to 1000"},
		{"OUTBOX_BATCH_SIZE", "1001", "OUTBOX_BATCH_SIZE"},
		{"OUTBOX_BATCH_SIZE", "ten", "OUTBOX_BATCH_SIZE"},
		{"OUTBOX_RETRY_BASE", "10m", "OUTBOX_RETRY_BASE: must not exceed OUTBOX_RETRY_MAX"},
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
