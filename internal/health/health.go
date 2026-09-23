// Package health defines readiness checks and the HTTP handlers for
// /health/live and /health/ready.
//
// Infrastructure modules contribute a Check to the Fx value group
// "readiness" with AsCheck; the HTTP server serves them all.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"go.uber.org/fx"
)

// Group is the Fx value group that collects readiness checks.
const Group = `group:"readiness"`

// Check verifies one dependency. Check must honour ctx cancellation.
type Check struct {
	Name  string
	Check func(ctx context.Context) error
}

// AsCheck annotates a constructor returning a Check so that it joins the
// readiness group.
func AsCheck(constructor any) any {
	return fx.Annotate(constructor, fx.ResultTags(Group))
}

// DefaultTimeout bounds each readiness probe.
const DefaultTimeout = 2 * time.Second

type response struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// LiveHandler answers 200 while the process is able to serve HTTP.
func LiveHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, response{Status: "ok"})
	})
}

// ReadyHandler runs every check concurrently, each bounded by timeout, and
// answers 200 when all pass or 503 otherwise. Failure details go to the log
// only; the body carries just the check names and "up"/"down".
func ReadyHandler(checks []Check, timeout time.Duration, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		results := make(map[string]string, len(checks))
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, c := range checks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(r.Context(), timeout)
				defer cancel()
				status := "up"
				if err := c.Check(ctx); err != nil {
					status = "down"
					log.WarnContext(r.Context(), "readiness check failed", "check", c.Name, "error", err)
				}
				mu.Lock()
				results[c.Name] = status
				mu.Unlock()
			}()
		}
		wg.Wait()

		resp := response{Status: "ok", Checks: results}
		code := http.StatusOK
		for _, s := range results {
			if s != "up" {
				resp.Status = "unavailable"
				code = http.StatusServiceUnavailable
			}
		}
		writeJSON(w, code, resp)
	})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
