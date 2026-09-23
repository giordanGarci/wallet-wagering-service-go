package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junglegaming/backend-challenge-go/internal/app"
)

// SQLSTATE values the repositories react to.
const (
	uniqueViolation = "23505"
)

// uniqueViolationOn reports whether err violates the named constraint.
func uniqueViolationOn(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraint
}

// classify wraps retryable infrastructure failures with app.ErrTransient and
// returns every other error unchanged. Retryable: connection failures and
// exceptions (class 08), serialization failures (40001), deadlocks (40P01),
// server shutdown or overload (57P01-57P03, 53300), timeouts and network
// errors.
func classify(err error) error {
	if err == nil || errors.Is(err, app.ErrTransient) {
		return err
	}
	if isTransient(err) {
		return fmt.Errorf("%w: %w", app.ErrTransient, err)
	}
	return err
}

func isTransient(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01", "57P01", "57P02", "57P03", "53300":
			return true
		}
		return strings.HasPrefix(pgErr.Code, "08")
	}
	var connErr *pgconn.ConnectError
	var netErr net.Error
	return errors.As(err, &connErr) || errors.As(err, &netErr) ||
		pgconn.Timeout(err) || pgconn.SafeToRetry(err) ||
		errors.Is(err, context.DeadlineExceeded)
}
