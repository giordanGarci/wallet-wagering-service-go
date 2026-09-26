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

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/httpapi"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
	"github.com/junglegaming/backend-challenge-go/internal/postgres"
	"github.com/junglegaming/backend-challenge-go/internal/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/worker"
)

// StartTimeout bounds the OnStart hooks (dependency checks, listeners).
const StartTimeout = 30 * time.Second

// roleModules maps each role (config.KnownRoles, validated by config.Load)
// to its Fx module.
var roleModules = map[config.Role]fx.Option{
	config.RoleAPI:      httpapi.Module,
	config.RoleConsumer: sqs.ConsumerModule,
	config.RoleOutbox:   worker.OutboxModule,
	config.RolePending:  worker.PendingModule,
}

// roleOrder is the registration order of the role modules, whatever the
// order in APP_ROLES. Fx runs stop hooks in reverse registration order, so
// the consumer stops polling before the workers finish or release leases.
var roleOrder = []config.Role{config.RolePending, config.RoleOutbox, config.RoleConsumer, config.RoleAPI}

// Options returns the full application graph for cfg. Extra options are
// appended last (tests use them for fx.Populate or fx.Decorate).
func Options(cfg config.Config, extra ...fx.Option) fx.Option {
	opts := []fx.Option{
		fx.Supply(cfg),
		// Every role schedules Pending Reference retries with PENDING_*.
		fx.Provide(func(cfg config.Config) wager.PendingPolicy { return cfg.Pending.Policy() }),
		fx.StartTimeout(StartTimeout),
		fx.StopTimeout(cfg.ShutdownTimeout),
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
			l := &fxevent.SlogLogger{Logger: log}
			l.UseLogLevel(slog.LevelDebug)
			return l
		}),

		// Infrastructure shared by every role. Stop hooks run in reverse
		// registration order: the HTTP server, registered last, stops
		// accepting first; then the consumer, then the workers; the pool and
		// the AWS client, registered first, close last.
		observability.Module,
		postgres.Module,
		app.Module,
		sqs.Module,
	}
	for _, r := range roleOrder {
		if cfg.HasRole(r) {
			opts = append(opts, roleModules[r])
		}
	}
	opts = append(opts, httpapi.ServerModule)
	opts = append(opts, extra...)
	return fx.Options(opts...)
}

// New builds the application.
func New(cfg config.Config, extra ...fx.Option) *fx.App {
	return fx.New(Options(cfg, extra...))
}
