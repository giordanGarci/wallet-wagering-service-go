package app

import (
	"context"

	"github.com/google/uuid"
)

// The correlation id ties together the logs and the Outbox Events of one
// operation. Adapters put it in the context before calling a use case:
// the HTTP API from the X-Correlation-Id header (or a fresh UUIDv7), the
// SQS consumer from the message. Use cases read it with CorrelationID and
// copy it into event.Meta; the JSON logger adds it to every record logged
// with that context.

type correlationKey struct{}

// WithCorrelationID returns ctx carrying id.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey{}, id)
}

// CorrelationID returns the id stored by WithCorrelationID, or "".
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}

// NewCorrelationID returns a fresh UUIDv7 string.
func NewCorrelationID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// NewV7 fails only when the system random source does; a v4 still
		// correlates.
		return uuid.NewString()
	}
	return id.String()
}

// ensureCorrelationID returns the correlation id of ctx, generating one when
// the caller set none (e.g. a use case run outside an adapter).
func ensureCorrelationID(ctx context.Context) (context.Context, string) {
	if id := CorrelationID(ctx); id != "" {
		return ctx, id
	}
	id := NewCorrelationID()
	return WithCorrelationID(ctx, id), id
}
