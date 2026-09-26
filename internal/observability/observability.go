// Package observability provides the JSON slog logger and the Prometheus
// registry shared by every module.
//
// Each Fx app owns its own registry (never prometheus.DefaultRegisterer), so
// several apps can run in one test process without duplicate registrations.
// Modules register collectors by depending on prometheus.Registerer.
package observability

import (
	"io"
	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/config"
)

// ServiceName is attached to every log line.
const ServiceName = "wallet"

// Module provides *slog.Logger, *prometheus.Registry, prometheus.Registerer,
// prometheus.Gatherer, app.ReconciliationMetrics and app.WagerMetrics.
var Module = fx.Module("observability",
	fx.Provide(
		NewLogger,
		NewRegistry,
		func(r *prometheus.Registry) prometheus.Registerer { return r },
		func(r *prometheus.Registry) prometheus.Gatherer { return r },
		fx.Annotate(NewReconciliationMetrics, fx.As(new(app.ReconciliationMetrics))),
		fx.Annotate(NewWagerMetrics, fx.As(new(app.WagerMetrics))),
	),
)

// NewLogger returns a JSON logger writing to stdout at the configured level.
func NewLogger(cfg config.Config) *slog.Logger {
	return NewJSONLogger(os.Stdout, cfg.LogLevel)
}

// NewJSONLogger returns a JSON logger writing to w. Records logged with a
// context carrying a correlation id (app.WithCorrelationID) include it as
// "correlationId".
func NewJSONLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(correlationHandler{slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})}).
		With("service", ServiceName)
}

// NewRegistry returns a registry with the Go runtime and process collectors.
func NewRegistry() *prometheus.Registry {
	r := prometheus.NewRegistry()
	r.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return r
}
