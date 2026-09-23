// Command app runs the Wallet & Wagering service. The components it runs are
// selected by APP_ROLES; see README.md for every environment variable.
package main

import (
	"log/slog"
	"os"

	"github.com/junglegaming/backend-challenge-go/internal/bootstrap"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		observability.NewJSONLogger(os.Stderr, slog.LevelInfo).Error("startup aborted", "error", err.Error())
		os.Exit(2)
	}
	slog.SetDefault(observability.NewJSONLogger(os.Stdout, cfg.LogLevel))

	// Run blocks until SIGINT/SIGTERM, then stops every component within
	// SHUTDOWN_TIMEOUT. A failed start exits with status 1.
	bootstrap.New(cfg).Run()
}
