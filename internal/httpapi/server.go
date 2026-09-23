// Package httpapi hosts the HTTP server, the operational endpoints
// (/health/live, /health/ready, /metrics) and, in later tickets, the business
// handlers with authentication and authorization.
//
// The server runs in every instance, whatever APP_ROLES says, so orchestrators
// can always probe it. Business routes join through the "http_routes" value
// group (see AsRoute) and are registered only by role modules.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/health"
)

// RoutesGroup is the Fx value group of business routes.
const RoutesGroup = `group:"http_routes"`

// Route is one ServeMux pattern (Go 1.22+ syntax, e.g. "POST /wallets").
type Route struct {
	Pattern string
	Handler http.Handler
}

// AsRoute annotates a constructor returning a Route so that it joins the
// server's routes.
func AsRoute(constructor any) any {
	return fx.Annotate(constructor, fx.ResultTags(RoutesGroup))
}

// ServerModule provides and starts *Server. Its lifecycle hook is appended
// after the hooks of everything it depends on (pool, SQS client), so on stop
// the server stops accepting connections first.
var ServerModule = fx.Module("httpserver",
	fx.Provide(NewServer),
	fx.Invoke(func(*Server) {}),
)

// Server wraps http.Server with a lifecycle bound to Fx.
type Server struct {
	srv  *http.Server
	addr net.Addr
}

// Addr is the bound listener address, known once the app has started. It
// matters when HTTP_ADDR uses port 0.
func (s *Server) Addr() net.Addr { return s.addr }

// ServerParams are the dependencies of NewServer.
type ServerParams struct {
	fx.In
	Lifecycle fx.Lifecycle
	Config    config.Config
	Log       *slog.Logger
	Gatherer  prometheus.Gatherer
	Checks    []health.Check `group:"readiness"`
	Routes    []Route        `group:"http_routes"`
}

// NewServer builds the mux and registers the start and stop hooks.
func NewServer(p ServerParams) (*Server, error) {
	mux := http.NewServeMux()
	mux.Handle("GET /health/live", health.LiveHandler())
	mux.Handle("GET /health/ready", health.ReadyHandler(p.Checks, health.DefaultTimeout, p.Log))
	mux.Handle("GET /metrics", promhttp.HandlerFor(p.Gatherer, promhttp.HandlerOpts{}))
	for _, r := range p.Routes {
		if err := register(mux, r); err != nil {
			return nil, err
		}
	}

	s := &Server{srv: &http.Server{
		Addr:              p.Config.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          slog.NewLogLogger(p.Log.Handler(), slog.LevelError),
	}}
	serveDone := make(chan struct{})

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", p.Config.HTTPAddr)
			if err != nil {
				return fmt.Errorf("http: listen on %s: %w", p.Config.HTTPAddr, err)
			}
			s.addr = ln.Addr()
			go func() {
				defer close(serveDone)
				if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					p.Log.Error("http server stopped unexpectedly", "error", err)
				}
			}()
			p.Log.InfoContext(ctx, "http server listening", "addr", s.addr.String())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			p.Log.InfoContext(ctx, "http server shutting down")
			err := s.srv.Shutdown(ctx)
			if err != nil {
				// Deadline hit with requests still running: force-close them.
				_ = s.srv.Close()
			}
			<-serveDone
			if err != nil {
				return fmt.Errorf("http: shutdown: %w", err)
			}
			p.Log.InfoContext(ctx, "http server stopped")
			return nil
		},
	})
	return s, nil
}

// register adds r to mux, turning ServeMux's pattern-conflict panic into an
// error that fails app construction.
func register(mux *http.ServeMux, r Route) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("http: register route %q: %v", r.Pattern, rec)
		}
	}()
	mux.Handle(r.Pattern, r.Handler)
	return nil
}
