package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/config"
)

func TestLoadConsumerDefaults(t *testing.T) {
	cfg, err := load(validEnv())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := config.Consumer{
		Name:              "wager-transactions",
		Concurrency:       10,
		WaitTime:          20 * time.Second,
		VisibilityTimeout: 30 * time.Second,
		RetryBase:         time.Second,
		RetryMax:          2 * time.Minute,
	}
	if cfg.Consumer != want {
		t.Errorf("Consumer = %+v, want %+v", cfg.Consumer, want)
	}
	if cfg.SQS.DLQQueueURL != validEnv()["SQS_DLQ_QUEUE_URL"] {
		t.Errorf("DLQQueueURL = %q", cfg.SQS.DLQQueueURL)
	}
}

func TestLoadConsumerOverrides(t *testing.T) {
	env := validEnv()
	env["CONSUMER_NAME"] = "blue"
	env["CONSUMER_CONCURRENCY"] = "3"
	env["CONSUMER_WAIT_TIME"] = "1s"
	env["CONSUMER_VISIBILITY_TIMEOUT"] = "5s"
	env["CONSUMER_RETRY_BASE"] = "100ms"
	env["CONSUMER_RETRY_MAX"] = "1s"
	cfg, err := load(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := config.Consumer{Name: "blue", Concurrency: 3, WaitTime: time.Second, VisibilityTimeout: 5 * time.Second,
		RetryBase: 100 * time.Millisecond, RetryMax: time.Second}
	if cfg.Consumer != want {
		t.Errorf("Consumer = %+v, want %+v", cfg.Consumer, want)
	}
}

func TestLoadRejectsInvalidConsumer(t *testing.T) {
	tests := []struct {
		key, value, wantErr string
	}{
		{"SQS_DLQ_QUEUE_URL", " ", "SQS_DLQ_QUEUE_URL: required"},
		{"SQS_DLQ_QUEUE_URL", "not a url", "SQS_DLQ_QUEUE_URL: not a valid URL"},
		{"CONSUMER_CONCURRENCY", "0", "CONSUMER_CONCURRENCY: must be an integer from 1 to 100"},
		{"CONSUMER_CONCURRENCY", "many", "CONSUMER_CONCURRENCY"},
		{"CONSUMER_WAIT_TIME", "21s", "CONSUMER_WAIT_TIME: must be whole seconds from 0s to 20s"},
		{"CONSUMER_WAIT_TIME", "500ms", "CONSUMER_WAIT_TIME"},
		{"CONSUMER_VISIBILITY_TIMEOUT", "1500ms", "CONSUMER_VISIBILITY_TIMEOUT: must be whole seconds"},
		{"CONSUMER_VISIBILITY_TIMEOUT", "0s", "CONSUMER_VISIBILITY_TIMEOUT: must be positive"},
		{"CONSUMER_VISIBILITY_TIMEOUT", "13h", "CONSUMER_VISIBILITY_TIMEOUT"},
		{"CONSUMER_RETRY_BASE", "10m", "CONSUMER_RETRY_BASE: must not exceed CONSUMER_RETRY_MAX"},
		{"CONSUMER_RETRY_MAX", "13h", "CONSUMER_RETRY_MAX: must not exceed 12h"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			env := validEnv()
			env[tt.key] = tt.value
			_, err := load(env)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadDLQOptionalWithoutConsumerRole(t *testing.T) {
	env := validEnv()
	delete(env, "SQS_DLQ_QUEUE_URL")
	env["APP_ROLES"] = "api,outbox"
	if _, err := load(env); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
