//go:build integration || e2e

package testsupport

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/junglegaming/backend-challenge-go/internal/bootstrap"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/httpapi"
)

// App is the Fx application running inside the test process.
type App struct {
	*fxtest.App
	// BaseURL is the root of the HTTP server, e.g. http://127.0.0.1:53211.
	BaseURL string
	// Pool is the app's own pgx pool (for assertions such as "closed on stop").
	Pool *pgxpool.Pool
	// HTTP is a client with its own transport, closed on Stop.
	HTTP *http.Client

	stopped bool
}

// StartApp builds the production graph (bootstrap.Options) for cfg, starts
// it and stops it on test cleanup unless the test already called Stop.
// Extra options are appended to the graph, e.g. fx.Populate(&repo).
func StartApp(t testing.TB, cfg config.Config, extra ...fx.Option) *App {
	t.Helper()
	var (
		srv  *httpapi.Server
		pool *pgxpool.Pool
	)
	opts := append([]fx.Option{fx.Populate(&srv, &pool)}, extra...)
	a := &App{
		App:  fxtest.New(t, bootstrap.Options(cfg, opts...)),
		HTTP: &http.Client{Transport: &http.Transport{}, Timeout: 10 * time.Second},
	}
	a.RequireStart()
	a.BaseURL = "http://" + srv.Addr().String()
	a.Pool = pool
	t.Cleanup(func() {
		if !a.stopped {
			a.Stop()
		}
	})
	return a
}

// Stop stops the app (as SIGTERM would) and releases the test HTTP client.
func (a *App) Stop() {
	a.stopped = true
	a.HTTP.CloseIdleConnections()
	a.RequireStop()
}

// Get issues GET BaseURL+path and returns the status code and full body.
func (a *App) Get(t testing.TB, path string) (int, []byte) {
	t.Helper()
	resp, err := a.HTTP.Get(a.BaseURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: read body: %v", path, err)
	}
	return resp.StatusCode, body
}
