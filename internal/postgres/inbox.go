package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/app"
)

// inboxMessageKey is the primary key of inbox_messages.
const inboxMessageKey = "inbox_messages_consumer_message_key"

// InboxRepository implements app.InboxRepository over inbox_messages.
type InboxRepository struct {
	db DBTX
}

// NewInboxRepository binds the repository to db (pool or transaction).
func NewInboxRepository(db DBTX) *InboxRepository {
	return &InboxRepository{db: db}
}

var _ app.InboxRepository = (*InboxRepository)(nil)

// Find returns the Inbox Message of (consumer, messageID).
func (r *InboxRepository) Find(ctx context.Context, consumer, messageID string) (app.InboxMessage, error) {
	var (
		m         app.InboxMessage
		completed *time.Time
		txID      *uuid.UUID
	)
	err := r.db.QueryRow(ctx, `
		SELECT consumer_name, message_id, body_hash, received_at, completed_at, transaction_id
		FROM inbox_messages WHERE consumer_name = $1 AND message_id = $2`, consumer, messageID).
		Scan(&m.ConsumerName, &m.MessageID, &m.BodyHash, &m.ReceivedAt, &completed, &txID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.InboxMessage{}, app.ErrInboxMessageNotFound
	}
	if err != nil {
		return app.InboxMessage{}, classify(fmt.Errorf("postgres: find inbox message: %w", err))
	}
	if completed != nil {
		m.CompletedAt = *completed
	}
	if txID != nil {
		m.TransactionID = *txID
	}
	return m, nil
}

// Insert stores m; a taken (consumer, messageId) fails with
// app.ErrInboxMessageExists.
func (r *InboxRepository) Insert(ctx context.Context, m app.InboxMessage) error {
	var completed *time.Time
	if !m.CompletedAt.IsZero() {
		completed = &m.CompletedAt
	}
	var txID *uuid.UUID
	if m.TransactionID != uuid.Nil {
		txID = &m.TransactionID
	}
	_, err := r.db.Exec(ctx, `
		INSERT INTO inbox_messages (consumer_name, message_id, body_hash, received_at, completed_at, transaction_id)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		m.ConsumerName, m.MessageID, m.BodyHash, m.ReceivedAt, completed, txID)
	if uniqueViolationOn(err, inboxMessageKey) {
		return app.ErrInboxMessageExists
	}
	if err != nil {
		return classify(fmt.Errorf("postgres: insert inbox message: %w", err))
	}
	return nil
}
