package observability

import (
	"context"
	"log/slog"

	"github.com/junglegaming/backend-challenge-go/internal/app"
)

// correlationHandler adds "correlationId" to every record logged with a
// context carrying one (app.WithCorrelationID), so handlers and use cases
// only need the ...Context logging methods.
type correlationHandler struct {
	slog.Handler
}

func (h correlationHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := app.CorrelationID(ctx); id != "" {
		r.AddAttrs(slog.String("correlationId", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h correlationHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return correlationHandler{h.Handler.WithAttrs(attrs)}
}

func (h correlationHandler) WithGroup(name string) slog.Handler {
	return correlationHandler{h.Handler.WithGroup(name)}
}
