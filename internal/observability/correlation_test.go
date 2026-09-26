package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
)

func TestLoggerAddsCorrelationIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	log := observability.NewJSONLogger(&buf, slog.LevelInfo).With("component", "test")

	log.InfoContext(app.WithCorrelationID(context.Background(), "corr-1"), "with id")
	log.InfoContext(context.Background(), "without id")

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2: %s", len(lines), buf.String())
	}
	var with, without map[string]any
	if err := json.Unmarshal(lines[0], &with); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[1], &without); err != nil {
		t.Fatal(err)
	}
	if with["correlationId"] != "corr-1" || with["component"] != "test" || with["service"] != observability.ServiceName {
		t.Errorf("record = %v, want correlationId corr-1 with the logger attributes", with)
	}
	if _, ok := without["correlationId"]; ok {
		t.Errorf("record = %v, want no correlationId", without)
	}
}
