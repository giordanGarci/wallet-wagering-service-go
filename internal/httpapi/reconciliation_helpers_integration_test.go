//go:build integration

package httpapi_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/observability"
	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

type reconciliationBody struct {
	WalletID          string    `json:"walletId"`
	StoredBalance     moneyBody `json:"storedBalance"`
	CalculatedBalance moneyBody `json:"calculatedBalance"`
	Difference        moneyBody `json:"difference"`
	Consistent        bool      `json:"consistent"`
	CheckedEntries    int64     `json:"checkedEntries"`
}

// reconcile runs POST /wallets/{walletID}/reconciliation with the internal
// client and returns the report, failing the test unless it answers 200.
func (e *env) reconcile(t *testing.T, walletID string) reconciliationBody {
	t.Helper()
	resp, body := e.app.Do(t, http.MethodPost, "/wallets/"+walletID+"/reconciliation",
		testsupport.Token(t, testsupport.InternalClient), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reconcile %s = %d %s, want 200", walletID, resp.StatusCode, body)
	}
	return decode[reconciliationBody](t, body)
}

// requireConsistent reconciles walletID and fails the test unless the stored
// balance equals credits minus debits of its Ledger Entries. Financial
// scenarios call it at the end.
func (e *env) requireConsistent(t *testing.T, walletID string) reconciliationBody {
	t.Helper()
	r := e.reconcile(t, walletID)
	if !r.Consistent || r.StoredBalance != r.CalculatedBalance || r.Difference.Amount != "0.00" {
		t.Fatalf("reconciliation of %s = %+v, want consistent", walletID, r)
	}
	return r
}

// metricValue scrapes /metrics and returns the value of one series, given
// exactly as exposed (e.g. `wallet_reconciliations_total{result="consistent"}`).
// A series that is not exposed fails the test.
func (e *env) metricValue(t *testing.T, series string) float64 {
	t.Helper()
	code, body := e.app.Get(t, "/metrics")
	if code != http.StatusOK {
		t.Fatalf("GET /metrics = %d", code)
	}
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, series+" "); ok {
			v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
			if err != nil {
				t.Fatalf("metric line %q: %v", line, err)
			}
			return v
		}
	}
	t.Fatalf("series %s not exposed in /metrics", series)
	return 0
}

// logBuffer collects the app's JSON log lines; safe for concurrent writes.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// records returns the decoded log records whose "msg" is msg.
func (b *logBuffer) records(t *testing.T, msg string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range bytes.Split(b.buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

// captureLogs replaces the app's logger with the production JSON logger
// writing to the returned buffer. Pass the option to newEnv.
func captureLogs() (*logBuffer, fx.Option) {
	b := &logBuffer{}
	return b, fx.Decorate(func() *slog.Logger { return observability.NewJSONLogger(b, slog.LevelInfo) })
}
