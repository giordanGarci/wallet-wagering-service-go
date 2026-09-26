package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// Inbox errors, classifiable with errors.Is.
var (
	// ErrInboxMessageNotFound reports that no Inbox Message exists for
	// (consumer, messageId).
	ErrInboxMessageNotFound = errors.New("app: inbox message not found")
	// ErrInboxMessageExists reports an Inbox Message insert that lost the
	// race on (consumer, messageId). It wraps ErrTransient so the
	// operation's SQL transaction rolls back without a FAILED record;
	// ConsumeWagerMessage then resolves it from the winner's Inbox Message.
	ErrInboxMessageExists = fmt.Errorf("%w: inbox message already recorded", ErrTransient)
	// ErrInboxHashMismatch reports a message whose messageId was already
	// consumed with a different body: a permanent error (dead-letter).
	ErrInboxHashMismatch = errors.New("app: inbox message redelivered with a different body")
)

// InboxMessage records that a consumer handled one message (spec: Inbox
// Message), keyed by (ConsumerName, MessageID). It is written in the same
// SQL transaction as the operation it carried.
type InboxMessage struct {
	ConsumerName string
	MessageID    string
	// BodyHash is the lower-case hex SHA-256 of the raw message body.
	BodyHash    string
	ReceivedAt  time.Time
	CompletedAt time.Time
	// TransactionID is the Wager Transaction the message produced or
	// replayed.
	TransactionID uuid.UUID
}

// InboxRepository persists Inbox Messages.
type InboxRepository interface {
	// Find returns the Inbox Message or ErrInboxMessageNotFound.
	Find(ctx context.Context, consumer, messageID string) (InboxMessage, error)
	// Insert stores m; ErrInboxMessageExists when (consumer, messageId) is
	// taken.
	Insert(ctx context.Context, m InboxMessage) error
}

// InboxRequest is one queue message carrying a wager operation.
type InboxRequest struct {
	// ConsumerName and MessageID identify the message durably (the
	// envelope's messageId).
	ConsumerName string
	MessageID    string
	// BodyHash is the hex SHA-256 of the raw body; a redelivery with the
	// same MessageID must carry the same hash.
	BodyHash   string
	ReceivedAt time.Time
	// Command is built by wager.NewCommand, exactly like HTTP, so both
	// channels share the Payload Hash and the idempotency rules.
	Command wager.Command
}

// InboxResult is the outcome of a consumed message.
type InboxResult struct {
	// Duplicate is true when the message was already consumed (same
	// messageId and body hash): nothing was executed.
	Duplicate bool
	// Wager is the operation's result. On a Duplicate it holds the
	// operation the message recorded (Transaction nil if the Inbox Message
	// has none; Replay false, since nothing ran), so the caller settles the
	// redelivery like the first delivery: a FAILED operation is
	// dead-lettered again.
	Wager WagerResult
}

// ConsumeWagerMessage runs ProcessWagerTransaction for a queue message
// with Inbox Message deduplication:
//
//   - a known (consumer, messageId) with the same body hash is a duplicate:
//     nothing runs and the result carries the operation it recorded;
//   - a known messageId with another hash fails with ErrInboxHashMismatch;
//   - otherwise the operation runs with ChannelSQS and CausationID =
//     messageId, and the Inbox Message is inserted inside its SQL
//     transaction (WagerRequest.InTx), so the operation, ledger, outbox and
//     inbox commit together. A FAILED (PERMANENT_ERROR) operation is
//     recorded in its own transaction, so its Inbox Message follows in
//     another one.
//
// Errors: ErrInboxHashMismatch, wrapped wager.ErrIdempotencyKeyConflict /
// wager.ErrExternalTransactionConflict (permanent for the message),
// ErrTransient (retry later) and context errors.
type ConsumeWagerMessage struct {
	uow     UnitOfWork
	process *ProcessWagerTransaction
	now     func() time.Time
}

// NewConsumeWagerMessage builds the use case.
func NewConsumeWagerMessage(uow UnitOfWork, process *ProcessWagerTransaction) *ConsumeWagerMessage {
	return &ConsumeWagerMessage{uow: uow, process: process, now: time.Now}
}

// Execute handles req. See ConsumeWagerMessage.
func (u *ConsumeWagerMessage) Execute(ctx context.Context, req InboxRequest) (InboxResult, error) {
	if dup, err := u.seen(ctx, req); dup.Duplicate || err != nil {
		return dup, err
	}
	res, err := u.process.Execute(ctx, WagerRequest{
		Command:     req.Command,
		Channel:     ChannelSQS,
		CausationID: req.MessageID,
		InTx: func(ctx context.Context, tx Tx, res WagerResult) error {
			return tx.Inbox().Insert(ctx, u.inboxMessage(req, res))
		},
	})
	if errors.Is(err, ErrInboxMessageExists) {
		// Another consumer recorded this messageId meanwhile.
		dup, err := u.seen(ctx, req)
		if err == nil && !dup.Duplicate {
			err = ErrInboxMessageExists
		}
		return dup, err
	}
	if err != nil {
		return InboxResult{}, err
	}
	if res.Transaction.Status() == wager.StatusFailed && !res.Replay {
		// The FAILED record committed without InTx.
		err := u.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
			return tx.Inbox().Insert(ctx, u.inboxMessage(req, res))
		})
		if err != nil && !errors.Is(err, ErrInboxMessageExists) {
			return InboxResult{}, err
		}
	}
	return InboxResult{Wager: res}, nil
}

// seen reports a message already consumed with the same body as a
// Duplicate carrying the operation it recorded (nil Transaction when the
// Inbox Message has none), a zero result when it was not consumed, and
// fails with ErrInboxHashMismatch when the body differs.
func (u *ConsumeWagerMessage) seen(ctx context.Context, req InboxRequest) (InboxResult, error) {
	var res InboxResult
	err := u.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		found, err := tx.Inbox().Find(ctx, req.ConsumerName, req.MessageID)
		if err != nil {
			return err
		}
		if found.BodyHash != req.BodyHash {
			return fmt.Errorf("%w: messageId %q", ErrInboxHashMismatch, req.MessageID)
		}
		res.Duplicate = true
		if found.TransactionID == uuid.Nil {
			return nil
		}
		res.Wager.Transaction, err = tx.Transactions().Get(ctx, found.TransactionID)
		return err
	})
	if errors.Is(err, ErrInboxMessageNotFound) {
		return InboxResult{}, nil
	}
	if err != nil {
		return InboxResult{}, err
	}
	return res, nil
}

func (u *ConsumeWagerMessage) inboxMessage(req InboxRequest, res WagerResult) InboxMessage {
	return InboxMessage{
		ConsumerName:  req.ConsumerName,
		MessageID:     req.MessageID,
		BodyHash:      req.BodyHash,
		ReceivedAt:    dbTime(req.ReceivedAt),
		CompletedAt:   dbTime(u.now()),
		TransactionID: res.Transaction.ID(),
	}
}
