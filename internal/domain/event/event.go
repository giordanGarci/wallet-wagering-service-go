// Package event defines the Outbox Events: a stable envelope
// {eventId, eventType, aggregateId, correlationId, causationId?, occurredAt,
// version, data} around typed data. Constructors fix the type and version,
// generate the UUIDv7 eventId and derive occurredAt from the domain object,
// so the JSON written to the outbox at commit is an immutable snapshot that
// republication sends byte for byte.
//
// aggregateId is always the Wallet id: the outbox publisher uses it as SQS
// MessageGroupId, so every event of a Wallet is delivered in order.
package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// ErrInvalidEvent reports an event that cannot be built from its source.
var ErrInvalidEvent = errors.New("event: invalid event")

// Type is the eventType of the envelope.
type Type string

const (
	TypeWagerTransactionProcessed        Type = "WagerTransactionProcessed"
	TypeWagerTransactionRejected         Type = "WagerTransactionRejected"
	TypeWalletBalanceChanged             Type = "WalletBalanceChanged"
	TypeWagerTransactionPendingReference Type = "WagerTransactionPendingReference"
)

// Schema versions, fixed by the constructors.
const (
	VersionWagerTransactionProcessed        = 1
	VersionWagerTransactionRejected         = 1
	VersionWalletBalanceChanged             = 1
	VersionWagerTransactionPendingReference = 1
)

// TimeLayout is RFC 3339 in UTC with fixed microsecond precision, e.g.
// 2026-09-08T12:00:00.000000Z.
const TimeLayout = "2006-01-02T15:04:05.000000Z07:00"

// Meta carries the tracing identifiers of the operation that caused the
// event. CorrelationID is required (HTTP request id or SQS messageId);
// CausationID is optional.
type Meta struct {
	CorrelationID string
	CausationID   string
}

// Data is the typed payload of an event. It is implemented only by the
// types of this package.
type Data interface {
	EventType() Type
	isData()
}

// Event is an immutable Outbox Event.
type Event struct {
	id            uuid.UUID
	typ           Type
	version       int
	aggregateID   uuid.UUID
	correlationID string
	causationID   string
	occurredAt    time.Time
	data          Data
}

func (e Event) ID() uuid.UUID          { return e.id }
func (e Event) Type() Type             { return e.typ }
func (e Event) Version() int           { return e.version }
func (e Event) AggregateID() uuid.UUID { return e.aggregateID }
func (e Event) CorrelationID() string  { return e.correlationID }
func (e Event) CausationID() string    { return e.causationID }
func (e Event) OccurredAt() time.Time  { return e.occurredAt }
func (e Event) Data() Data             { return e.data }

type envelope struct {
	EventID       uuid.UUID `json:"eventId"`
	EventType     Type      `json:"eventType"`
	AggregateID   uuid.UUID `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId,omitempty"`
	OccurredAt    string    `json:"occurredAt"`
	Version       int       `json:"version"`
	Data          Data      `json:"data"`
}

// MarshalJSON encodes the envelope deterministically (fixed field order,
// fixed time format, money as decimal strings).
func (e Event) MarshalJSON() ([]byte, error) {
	if e.id == uuid.Nil || e.data == nil {
		return nil, fmt.Errorf("%w: zero value event", ErrInvalidEvent)
	}
	return json.Marshal(envelope{
		EventID:       e.id,
		EventType:     e.typ,
		AggregateID:   e.aggregateID,
		CorrelationID: e.correlationID,
		CausationID:   e.causationID,
		OccurredAt:    e.occurredAt.Format(TimeLayout),
		Version:       e.version,
		Data:          e.data,
	})
}

func newEvent(typ Type, version int, aggregateID uuid.UUID, occurredAt time.Time, meta Meta, data Data) (Event, error) {
	if meta.CorrelationID == "" {
		return Event{}, fmt.Errorf("%w: correlationId is required", ErrInvalidEvent)
	}
	if aggregateID == uuid.Nil || occurredAt.IsZero() {
		return Event{}, fmt.Errorf("%w: aggregateId and occurredAt are required", ErrInvalidEvent)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Event{}, fmt.Errorf("event: generate id: %w", err)
	}
	return Event{
		id:            id,
		typ:           typ,
		version:       version,
		aggregateID:   aggregateID,
		correlationID: meta.CorrelationID,
		causationID:   meta.CausationID,
		occurredAt:    occurredAt.UTC().Truncate(time.Microsecond),
		data:          data,
	}, nil
}

// TransactionData identifies the Wager Transaction in the transaction
// events. External metadata is omitted for the internal Opening.
type TransactionData struct {
	TransactionID                  uuid.UUID    `json:"transactionId"`
	Origin                         wager.Origin `json:"origin"`
	Kind                           wager.Kind   `json:"kind"`
	Status                         wager.Status `json:"status"`
	WalletID                       uuid.UUID    `json:"walletId"`
	PlayerID                       uuid.UUID    `json:"playerId"`
	Money                          money.Money  `json:"money"`
	ProviderID                     string       `json:"providerId,omitempty"`
	ExternalTransactionID          string       `json:"externalTransactionId,omitempty"`
	RoundID                        string       `json:"roundId,omitempty"`
	GameID                         string       `json:"gameId,omitempty"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         *uuid.UUID   `json:"referenceTransactionId,omitempty"`
}

func transactionData(tx *wager.Transaction) TransactionData {
	d := TransactionData{
		TransactionID:                  tx.ID(),
		Origin:                         tx.Origin(),
		Kind:                           tx.Kind(),
		Status:                         tx.Status(),
		WalletID:                       tx.WalletID(),
		PlayerID:                       tx.PlayerID(),
		Money:                          tx.Money(),
		ProviderID:                     tx.ProviderID(),
		ExternalTransactionID:          tx.ExternalTransactionID(),
		RoundID:                        tx.RoundID(),
		GameID:                         tx.GameID(),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
	}
	if ref := tx.ReferenceTransactionID(); ref != uuid.Nil {
		d.ReferenceTransactionID = &ref
	}
	return d
}

// WagerTransactionProcessedData: a Wager Transaction concluded successfully
// (LOSS and Opening included). BalanceAfter is the balance returned to the
// Provider.
type WagerTransactionProcessedData struct {
	TransactionData
	BalanceAfter money.Money `json:"balanceAfter"`
}

// WagerTransactionRejectedData: a definitive business rejection.
// BalanceAfter is omitted when no balance was reported.
type WagerTransactionRejectedData struct {
	TransactionData
	FailureCode  wager.FailureCode `json:"failureCode"`
	Correctable  bool              `json:"correctable"`
	BalanceAfter *money.Money      `json:"balanceAfter,omitempty"`
}

// WagerTransactionPendingReferenceData: the Transaction waits for its
// Reference (referenceExternalTransactionId is always present).
type WagerTransactionPendingReferenceData struct {
	TransactionData
}

// WalletBalanceChangedData: an effective balance change, one per Ledger
// Entry.
type WalletBalanceChangedData struct {
	WalletID      uuid.UUID        `json:"walletId"`
	TransactionID uuid.UUID        `json:"transactionId"`
	Direction     ledger.Direction `json:"direction"`
	Money         money.Money      `json:"money"`
	BalanceBefore money.Money      `json:"balanceBefore"`
	BalanceAfter  money.Money      `json:"balanceAfter"`
	WalletVersion int64            `json:"walletVersion"`
}

func (WagerTransactionProcessedData) EventType() Type { return TypeWagerTransactionProcessed }
func (WagerTransactionRejectedData) EventType() Type  { return TypeWagerTransactionRejected }
func (WagerTransactionPendingReferenceData) EventType() Type {
	return TypeWagerTransactionPendingReference
}
func (WalletBalanceChangedData) EventType() Type { return TypeWalletBalanceChanged }

func (WagerTransactionProcessedData) isData()        {}
func (WagerTransactionRejectedData) isData()         {}
func (WagerTransactionPendingReferenceData) isData() {}
func (WalletBalanceChangedData) isData()             {}

func requireStatus(tx *wager.Transaction, want wager.Status) error {
	if tx == nil {
		return fmt.Errorf("%w: transaction is required", ErrInvalidEvent)
	}
	if tx.Status() != want {
		return fmt.Errorf("%w: transaction %s is %s, want %s", ErrInvalidEvent, tx.ID(), tx.Status(), want)
	}
	return nil
}

// NewWagerTransactionProcessed builds the event of a PROCESSED Transaction.
func NewWagerTransactionProcessed(tx *wager.Transaction, meta Meta) (Event, error) {
	if err := requireStatus(tx, wager.StatusProcessed); err != nil {
		return Event{}, err
	}
	balance, _ := tx.BalanceAfter() // always present on PROCESSED
	data := WagerTransactionProcessedData{TransactionData: transactionData(tx), BalanceAfter: balance}
	return newEvent(TypeWagerTransactionProcessed, VersionWagerTransactionProcessed, tx.WalletID(), tx.UpdatedAt(), meta, data)
}

// NewWagerTransactionRejected builds the event of a REJECTED Transaction.
func NewWagerTransactionRejected(tx *wager.Transaction, meta Meta) (Event, error) {
	if err := requireStatus(tx, wager.StatusRejected); err != nil {
		return Event{}, err
	}
	data := WagerTransactionRejectedData{
		TransactionData: transactionData(tx),
		FailureCode:     tx.FailureCode(),
		Correctable:     tx.FailureCode().Correctable(),
	}
	if balance, ok := tx.BalanceAfter(); ok {
		data.BalanceAfter = &balance
	}
	return newEvent(TypeWagerTransactionRejected, VersionWagerTransactionRejected, tx.WalletID(), tx.UpdatedAt(), meta, data)
}

// NewWagerTransactionPendingReference builds the event of a Transaction just
// parked in PENDING_REFERENCE.
func NewWagerTransactionPendingReference(tx *wager.Transaction, meta Meta) (Event, error) {
	if err := requireStatus(tx, wager.StatusPendingReference); err != nil {
		return Event{}, err
	}
	data := WagerTransactionPendingReferenceData{TransactionData: transactionData(tx)}
	return newEvent(TypeWagerTransactionPendingReference, VersionWagerTransactionPendingReference, tx.WalletID(), tx.UpdatedAt(), meta, data)
}

// NewWalletBalanceChanged builds the event of a Ledger Entry; walletVersion
// is the Wallet version after the movement.
func NewWalletBalanceChanged(entry ledger.Entry, walletVersion int64, meta Meta) (Event, error) {
	if entry.ID() == uuid.Nil {
		return Event{}, fmt.Errorf("%w: ledger entry is required", ErrInvalidEvent)
	}
	if walletVersion < 1 {
		return Event{}, fmt.Errorf("%w: walletVersion %d must be >= 1", ErrInvalidEvent, walletVersion)
	}
	data := WalletBalanceChangedData{
		WalletID:      entry.WalletID(),
		TransactionID: entry.TransactionID(),
		Direction:     entry.Direction(),
		Money:         entry.Money(),
		BalanceBefore: entry.BalanceBefore(),
		BalanceAfter:  entry.BalanceAfter(),
		WalletVersion: walletVersion,
	}
	return newEvent(TypeWalletBalanceChanged, VersionWalletBalanceChanged, entry.WalletID(), entry.CreatedAt(), meta, data)
}

// ForTransaction returns the events to write in the same commit as tx,
// according to its status after wager.Apply / wager.Open /
// RecordReferenceMiss:
//
//   - PROCESSED: WagerTransactionProcessed, then WalletBalanceChanged when
//     entry is not nil (walletVersion is the Wallet version after Apply);
//   - REJECTED: WagerTransactionRejected;
//   - PENDING_REFERENCE: WagerTransactionPendingReference. Call it only when
//     the Transaction was just parked; a worker retry that leaves it pending
//     emits nothing;
//   - PENDING, FAILED: no event.
func ForTransaction(tx *wager.Transaction, entry *ledger.Entry, walletVersion int64, meta Meta) ([]Event, error) {
	if tx == nil {
		return nil, fmt.Errorf("%w: transaction is required", ErrInvalidEvent)
	}
	if entry != nil && (tx.Status() != wager.StatusProcessed || entry.TransactionID() != tx.ID()) {
		return nil, fmt.Errorf("%w: entry %s does not belong to processed transaction %s", ErrInvalidEvent, entry.ID(), tx.ID())
	}
	var build []func() (Event, error)
	switch tx.Status() {
	case wager.StatusProcessed:
		build = append(build, func() (Event, error) { return NewWagerTransactionProcessed(tx, meta) })
		if entry != nil {
			build = append(build, func() (Event, error) { return NewWalletBalanceChanged(*entry, walletVersion, meta) })
		}
	case wager.StatusRejected:
		build = append(build, func() (Event, error) { return NewWagerTransactionRejected(tx, meta) })
	case wager.StatusPendingReference:
		build = append(build, func() (Event, error) { return NewWagerTransactionPendingReference(tx, meta) })
	}
	events := make([]Event, 0, len(build))
	for _, b := range build {
		e, err := b()
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}
