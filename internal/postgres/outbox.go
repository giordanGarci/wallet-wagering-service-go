package postgres

import (
	"context"
	"fmt"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/domain/event"
)

// OutboxRepository implements app.OutboxRepository over outbox_events.
type OutboxRepository struct {
	db DBTX
}

// NewOutboxRepository binds the repository to db (pool or transaction).
func NewOutboxRepository(db DBTX) *OutboxRepository {
	return &OutboxRepository{db: db}
}

var _ app.OutboxRepository = (*OutboxRepository)(nil)

// Append inserts the events in order (seq follows the argument order). The
// payload is the exact event.Event.MarshalJSON output, stored as text so
// the publisher sends the same bytes on every attempt. The row is due for
// publication at once (next_attempt_at defaults to now()).
func (r *OutboxRepository) Append(ctx context.Context, events ...event.Event) error {
	for _, e := range events {
		payload, err := e.MarshalJSON()
		if err != nil {
			return fmt.Errorf("postgres: outbox event %s: %w", e.ID(), err)
		}
		_, err = r.db.Exec(ctx, `
			INSERT INTO outbox_events (event_id, aggregate_id, event_type, version, correlation_id, payload, occurred_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			e.ID(), e.AggregateID(), string(e.Type()), e.Version(), e.CorrelationID(), string(payload), e.OccurredAt())
		if err != nil {
			return classify(fmt.Errorf("postgres: append outbox event %s: %w", e.ID(), err))
		}
	}
	return nil
}
