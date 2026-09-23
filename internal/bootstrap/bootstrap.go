// Package bootstrap is the composition root: it assembles the Fx modules for
// a validated config.Config and selects role modules by APP_ROLES.
//
// cmd/app and the integration harness both build the app through Options, so
// tests exercise the exact production graph.
package bootstrap

import (
	"log/slog"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/httpapi"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
	"github.com/junglegaming/backend-challenge-go/internal/postgres"
	"github.com/junglegaming/backend-challenge-go/internal/sqs"
)

// StartTimeout bounds the OnStart hooks (dependency checks, listeners).
const StartTimeout = 30 * time.Second

// roleModules maps each role to its Fx module. A role absent from the map is
// accepted by config but not implemented yet; the app logs a warning and
// runs without it. Later tickets add their module here.
var roleModules = map[config.Role]fx.Option{
	config.RoleAPI: httpapi.Module,
	// config.RoleConsumer: sqs.ConsumerModule,   (ticket 12)
	// config.RoleOutbox:   worker.OutboxModule,  (ticket 09)
	// config.RolePending:  worker.PendingModule, (ticket 11)
}

// Options returns the full application graph for cfg. Extra options are
// appended last (tests use them for fx.Populate or fx.Decorate).
func Options(cfg config.Config, extra ...fx.Option) fx.Option {
	opts := []fx.Option{
		fx.Supply(cfg),
		fx.StartTimeout(StartTimeout),
		fx.StopTimeout(cfg.ShutdownTimeout),
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
			l := &fxevent.SlogLogger{Logger: log}
			l.UseLogLevel(slog.LevelDebug)
			return l
		}),

		// Infrastructure shared by every role. Constructors run in dependency
		// order and stop hooks in reverse, so the HTTP server (which depends
		// on the pool and SQS client through readiness checks) stops first
		// and the pool and AWS client close last.
		observability.Module,
		postgres.Module,
		sqs.Module,
		httpapi.ServerModule,
	}
	for _, r := range cfg.Roles {
		if m, ok := roleModules[r]; ok {
			opts = append(opts, m)
			continue
		}
		opts = append(opts, fx.Invoke(func(log *slog.Logger) {
			log.Warn("role not implemented yet, skipping", "role", string(r))
		}))
	}
	opts = append(opts, extra...)
	return fx.Options(opts...)
}

// New builds the application.
func New(cfg config.Config, extra ...fx.Option) *fx.App {
	return fx.New(Options(cfg, extra...))
}
