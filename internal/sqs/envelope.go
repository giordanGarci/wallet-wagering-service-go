package sqs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// Envelope contract of the input queue (wager-transactions.fifo):
//
//	{
//	  "messageId": "msg-123",                   required, 1-255 bytes, durable id (Inbox Message key)
//	  "type": "WagerTransactionRequested",      required, the only accepted type
//	  "version": 1,                             optional, 1 when present
//	  "occurredAt": "2026-09-08T12:00:00.000Z", required, RFC 3339
//	  "correlationId": "...",                   optional, else a fresh UUIDv7
//	  "data": { providerId, externalTransactionId, idempotencyKey, playerId,
//	            walletId, roundId, gameId, kind, money,
//	            referenceExternalTransactionId (optional) }
//	}
//
// Envelope and data are decoded strictly (unknown fields are rejected, as
// in the HTTP body) and data goes through wager.NewCommand, so the format
// rules and the Payload Hash are exactly those of HTTP.
// data.idempotencyKey is the Idempotency Key. Producers send with
// MessageGroupId = walletId and MessageDeduplicationId = messageId.
const (
	EnvelopeType    = "WagerTransactionRequested"
	EnvelopeVersion = 1
)

// Reason says why a message went to the dead-letter queue. It is sent as
// the dlqReason message attribute and labels wallet_sqs_dlq_messages_total.
type Reason string

const (
	ReasonMalformedJSON               Reason = "malformed_json"
	ReasonUnknownEnvelope             Reason = "unknown_envelope"
	ReasonInvalidEnvelope             Reason = "invalid_envelope"
	ReasonInvalidCommand              Reason = "invalid_command"
	ReasonOpeningKind                 Reason = "opening_kind"
	ReasonBodyHashMismatch            Reason = "body_hash_mismatch"
	ReasonIdempotencyKeyConflict      Reason = "idempotency_key_conflict"
	ReasonExternalTransactionConflict Reason = "external_transaction_conflict"
	// ReasonPermanentError: the operation was recorded FAILED
	// PERMANENT_ERROR, an infrastructure failure no retry fixes.
	ReasonPermanentError Reason = "permanent_error"
)

// PermanentError is a message that no retry can fix: the consumer sends it
// to the dead-letter queue and deletes it from the input queue.
type PermanentError struct {
	Reason Reason
	Err    error
}

func (e *PermanentError) Error() string { return string(e.Reason) + ": " + e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

func permanent(reason Reason, format string, args ...any) error {
	return &PermanentError{Reason: reason, Err: fmt.Errorf(format, args...)}
}

// Envelope is a decoded, validated WagerTransactionRequested message.
type Envelope struct {
	MessageID     string
	OccurredAt    time.Time
	CorrelationID string
	Command       wager.Command
}

// envelopeHeader is read first, leniently, to tell a foreign message from
// an invalid one.
type envelopeHeader struct {
	Type    any `json:"type"`
	Version any `json:"version"`
}

type envelopeBody struct {
	MessageID     string          `json:"messageId"`
	Type          string          `json:"type"`
	Version       *int            `json:"version"`
	OccurredAt    string          `json:"occurredAt"`
	CorrelationID string          `json:"correlationId"`
	Data          json.RawMessage `json:"data"`
}

// wagerData mirrors the HTTP body of POST /wagering/transactions plus
// idempotencyKey (the Idempotency-Key header over HTTP).
type wagerData struct {
	ProviderID                     string       `json:"providerId"`
	ExternalTransactionID          string       `json:"externalTransactionId"`
	IdempotencyKey                 string       `json:"idempotencyKey"`
	PlayerID                       string       `json:"playerId"`
	WalletID                       string       `json:"walletId"`
	RoundID                        string       `json:"roundId"`
	GameID                         string       `json:"gameId"`
	Kind                           string       `json:"kind"`
	Money                          *money.Money `json:"money"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId"`
}

// ParseEnvelope decodes body. Every error is a *PermanentError.
func ParseEnvelope(body []byte) (Envelope, error) {
	if !json.Valid(body) {
		return Envelope{}, permanent(ReasonMalformedJSON, "body is not valid JSON")
	}
	var head envelopeHeader
	if err := json.Unmarshal(body, &head); err != nil {
		return Envelope{}, permanent(ReasonMalformedJSON, "body is not a JSON object")
	}
	if head.Type != EnvelopeType {
		return Envelope{}, permanent(ReasonUnknownEnvelope, "type must be %q", EnvelopeType)
	}
	if head.Version != nil && head.Version != float64(EnvelopeVersion) {
		return Envelope{}, permanent(ReasonUnknownEnvelope, "version must be %d", EnvelopeVersion)
	}

	var env envelopeBody
	if err := decodeStrict(body, &env); err != nil {
		return Envelope{}, permanent(ReasonInvalidEnvelope, "%v", err)
	}
	if err := checkMessageID(env.MessageID); err != nil {
		return Envelope{}, permanent(ReasonInvalidEnvelope, "%v", err)
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, env.OccurredAt)
	if err != nil {
		return Envelope{}, permanent(ReasonInvalidEnvelope, "occurredAt must be an RFC 3339 timestamp")
	}
	if len(env.CorrelationID) > wager.MaxIdentifierLength || !utf8.ValidString(env.CorrelationID) {
		return Envelope{}, permanent(ReasonInvalidEnvelope, "correlationId exceeds %d bytes or is not UTF-8", wager.MaxIdentifierLength)
	}
	if len(env.Data) == 0 || bytes.Equal(env.Data, []byte("null")) {
		return Envelope{}, permanent(ReasonInvalidEnvelope, "data is required")
	}

	var data wagerData
	if err := decodeStrict(env.Data, &data); err != nil {
		return Envelope{}, permanent(ReasonInvalidCommand, "data: %v", err)
	}
	if data.Kind == string(wager.KindOpening) {
		return Envelope{}, permanent(ReasonOpeningKind, "OPENING is reserved to internal wallet opening")
	}
	in := wager.CommandInput{
		ProviderID:                     data.ProviderID,
		ExternalTransactionID:          data.ExternalTransactionID,
		IdempotencyKey:                 data.IdempotencyKey,
		PlayerID:                       data.PlayerID,
		WalletID:                       data.WalletID,
		RoundID:                        data.RoundID,
		GameID:                         data.GameID,
		Kind:                           data.Kind,
		ReferenceExternalTransactionID: data.ReferenceExternalTransactionID,
	}
	if data.Money != nil {
		in.Money = *data.Money
	}
	cmd, err := wager.NewCommand(in)
	if err != nil {
		return Envelope{}, permanent(ReasonInvalidCommand, "data: %v", err)
	}
	return Envelope{
		MessageID:     env.MessageID,
		OccurredAt:    occurredAt,
		CorrelationID: env.CorrelationID,
		Command:       cmd,
	}, nil
}

// BodyHash is the lower-case hex SHA-256 of the raw body, stored in the
// Inbox Message to recognize a redelivery.
func BodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func decodeStrict(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("must contain a single JSON object")
	}
	return nil
}

func checkMessageID(id string) error {
	switch {
	case id == "":
		return errors.New("messageId is required")
	case len(id) > wager.MaxIdentifierLength:
		return errors.New("messageId exceeds maximum length")
	case !utf8.ValidString(id):
		return errors.New("messageId is not valid UTF-8")
	case strings.TrimSpace(id) != id:
		return errors.New("messageId has leading or trailing whitespace")
	}
	return nil
}
