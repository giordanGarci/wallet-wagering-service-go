package bootstrap_test

import (
	"testing"

	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/bootstrap"
	"github.com/junglegaming/backend-challenge-go/internal/config"
)

// TestGraphIsComplete validates the dependency graph for every role
// combination without running constructors or touching infrastructure.
func TestGraphIsComplete(t *testing.T) {
	for _, roles := range []string{"api", "consumer", "outbox,pending", config.DefaultRoles} {
		t.Run(roles, func(t *testing.T) {
			cfg, err := config.FromLookup(func(k string) (string, bool) {
				v, ok := map[string]string{
					"APP_ROLES":            roles,
					"DATABASE_URL":         "postgres://wallet:wallet@localhost:5432/wallet",
					"SQS_INPUT_QUEUE_URL":  "http://localhost:4566/000000000000/in.fifo",
					"SQS_EVENTS_QUEUE_URL": "http://localhost:4566/000000000000/out.fifo",
				}[k]
				return v, ok
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := fx.ValidateApp(bootstrap.Options(cfg)); err != nil {
				t.Fatalf("invalid graph: %v", err)
			}
		})
	}
}
