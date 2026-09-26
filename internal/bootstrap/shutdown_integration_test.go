//go:build integration

package bootstrap_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

// lockedBuffer is an io.Writer safe for the concurrent stop hooks.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) lines() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(b.buf.Bytes()))
	for sc.Scan() {
		var rec map[string]any
		if json.Unmarshal(sc.Bytes(), &rec) == nil {
			out = append(out, rec)
		}
	}
	return out
}

// TestShutdownOrder pins the stop sequence required by the spec: the HTTP
// server stops accepting first, then the consumer stops polling, then the
// workers finish or release their leases, and only then the pgx pool and the
// AWS client close.
func TestShutdownOrder(t *testing.T) {
	infra := testsupport.NewInfra(t)
	cfg := infra.Config
	cfg.Roles = []config.Role{config.RoleAPI, config.RoleConsumer, config.RoleOutbox, config.RolePending}

	logs := &lockedBuffer{}
	app := testsupport.StartApp(t, cfg, fx.Decorate(func(*slog.Logger) *slog.Logger {
		return slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}))
	app.Stop()

	var order []string
	for _, rec := range logs.lines() {
		switch rec["msg"] {
		case "http server stopped", "consumer stopped", "postgres pool closed", "sqs client closed":
			order = append(order, rec["msg"].(string))
		case "worker stopped":
			order = append(order, "worker stopped")
		}
	}
	pos := map[string][]int{}
	for i, m := range order {
		pos[m] = append(pos[m], i)
	}
	if len(pos["http server stopped"]) != 1 || len(pos["consumer stopped"]) != 1 || len(pos["worker stopped"]) != 2 ||
		len(pos["postgres pool closed"]) != 1 || len(pos["sqs client closed"]) != 1 {
		t.Fatalf("stop log sequence = %v, want each component once (two workers)", order)
	}
	httpAt, consumerAt := pos["http server stopped"][0], pos["consumer stopped"][0]
	lastWorker := pos["worker stopped"][1]
	if !(httpAt < consumerAt && consumerAt < pos["worker stopped"][0] &&
		lastWorker < pos["postgres pool closed"][0] && lastWorker < pos["sqs client closed"][0]) {
		t.Fatalf("stop order = %v, want http server, consumer, workers, then pool and sqs client", order)
	}
}
