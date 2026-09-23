package wager

import (
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// MaxIdentifierLength bounds the free-form string identifiers of a Command
// (providerId, externalTransactionId, idempotencyKey, roundId, gameId,
// referenceExternalTransactionId).
const MaxIdentifierLength = 255

// CommandInput is the raw, transport-neutral shape of an external operation,
// as decoded from the HTTP body plus Idempotency-Key header or from the SQS
// message data. Money is decoded by money.Money's strict JSON decoder.
type CommandInput struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       string
	WalletID                       string
	RoundID                        string
	GameID                         string
	Kind                           string
	Money                          money.Money
	ReferenceExternalTransactionID string // "" when absent
}

// Command is a validated, normalized external operation. Only NewCommand
// builds it; the zero value is invalid.
type Command struct {
	providerID                     string
	externalTransactionID          string
	idempotencyKey                 string
	playerID                       uuid.UUID
	walletID                       uuid.UUID
	roundID                        string
	gameID                         string
	kind                           Kind
	money                          money.Money
	referenceExternalTransactionID string
}

// NewCommand applies the format rules that answer HTTP 400 with no record:
//
//   - providerId, externalTransactionId, idempotencyKey, roundId and gameId
//     are required, at most MaxIdentifierLength bytes, without leading or
//     trailing whitespace; they are kept exactly as sent (no substitution).
//   - playerId and walletId are canonical 36-character UUIDs in any letter
//     case; they are normalized to lower case.
//   - kind is BET, WIN, LOSS, REFUND or ROLLBACK; OPENING is internal only.
//   - money is required. Zero policy: BET, WIN, REFUND and ROLLBACK need a
//     positive amount; LOSS needs exactly "0.00".
//   - referenceExternalTransactionId is required for REFUND and ROLLBACK,
//     optional for WIN (a BET of the same Round), and forbidden for BET and
//     LOSS, which have no Reference rule. It cannot point to the operation
//     itself.
//
// Errors are *CommandError values matching ErrInvalidCommand.
func NewCommand(in CommandInput) (Command, error) {
	for _, f := range []struct{ name, value string }{
		{"providerId", in.ProviderID},
		{"externalTransactionId", in.ExternalTransactionID},
		{"idempotencyKey", in.IdempotencyKey},
		{"roundId", in.RoundID},
		{"gameId", in.GameID},
	} {
		if err := checkIdentifier(f.name, f.value); err != nil {
			return Command{}, err
		}
	}
	playerID, err := parseUUID("playerId", in.PlayerID)
	if err != nil {
		return Command{}, err
	}
	walletID, err := parseUUID("walletId", in.WalletID)
	if err != nil {
		return Command{}, err
	}
	if in.Kind == "" {
		return Command{}, invalidField("kind", "is required", nil)
	}
	kind := Kind(in.Kind)
	if !kind.IsValid() {
		return Command{}, invalidField("kind", "unknown kind "+quote(in.Kind), nil)
	}
	if !kind.IsExternal() {
		return Command{}, invalidField("kind", "OPENING is reserved to internal wallet opening", nil)
	}
	if err := checkZeroPolicy(kind, in.Money); err != nil {
		return Command{}, err
	}
	ref := in.ReferenceExternalTransactionID
	switch {
	case kind.IsReversal() && ref == "":
		return Command{}, invalidField("referenceExternalTransactionId", "is required for "+string(kind), nil)
	case (kind == KindBet || kind == KindLoss) && ref != "":
		return Command{}, invalidField("referenceExternalTransactionId", "is not accepted for "+string(kind), nil)
	}
	if ref != "" {
		if err := checkIdentifier("referenceExternalTransactionId", ref); err != nil {
			return Command{}, err
		}
		if ref == in.ExternalTransactionID {
			return Command{}, invalidField("referenceExternalTransactionId", "cannot reference the operation itself", nil)
		}
	}
	return Command{
		providerID:                     in.ProviderID,
		externalTransactionID:          in.ExternalTransactionID,
		idempotencyKey:                 in.IdempotencyKey,
		playerID:                       playerID,
		walletID:                       walletID,
		roundID:                        in.RoundID,
		gameID:                         in.GameID,
		kind:                           kind,
		money:                          in.Money,
		referenceExternalTransactionID: ref,
	}, nil
}

// checkZeroPolicy enforces: LOSS is exactly 0.00, every other external kind
// is strictly positive. Negative values never reach here through the strict
// money parser but are rejected anyway.
func checkZeroPolicy(kind Kind, m money.Money) error {
	if !m.IsValid() {
		return invalidField("money", "is required", money.ErrUninitialized)
	}
	if kind == KindLoss {
		if !m.IsZero() {
			return invalidField("money.amount", "must be 0.00 for LOSS", nil)
		}
		return nil
	}
	if m.IsZero() || m.IsNegative() {
		return invalidField("money.amount", "must be greater than zero for "+string(kind), nil)
	}
	return nil
}

func checkIdentifier(field, v string) error {
	switch {
	case v == "":
		return invalidField(field, "is required", nil)
	case len(v) > MaxIdentifierLength:
		return invalidField(field, "exceeds maximum length", nil)
	case !utf8.ValidString(v):
		return invalidField(field, "is not valid UTF-8", nil)
	case strings.TrimSpace(v) != v:
		return invalidField(field, "has leading or trailing whitespace", nil)
	}
	return nil
}

func parseUUID(field, v string) (uuid.UUID, error) {
	if v == "" {
		return uuid.Nil, invalidField(field, "is required", nil)
	}
	// uuid.Parse also accepts urn:uuid:, braces and the 32-digit form; only
	// the canonical hyphenated form is part of the contract.
	if len(v) != 36 {
		return uuid.Nil, invalidField(field, "must be a canonical UUID", nil)
	}
	id, err := uuid.Parse(v)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, invalidField(field, "must be a canonical non-nil UUID", err)
	}
	return id, nil
}

func quote(s string) string { return `"` + s + `"` }

func (c Command) ProviderID() string            { return c.providerID }
func (c Command) ExternalTransactionID() string { return c.externalTransactionID }
func (c Command) IdempotencyKey() string        { return c.idempotencyKey }
func (c Command) PlayerID() uuid.UUID           { return c.playerID }
func (c Command) WalletID() uuid.UUID           { return c.walletID }
func (c Command) RoundID() string               { return c.roundID }
func (c Command) GameID() string                { return c.gameID }
func (c Command) Kind() Kind                    { return c.kind }
func (c Command) Money() money.Money            { return c.money }

// ReferenceExternalTransactionID returns "" when the Command has no Reference.
func (c Command) ReferenceExternalTransactionID() string { return c.referenceExternalTransactionID }

// HasReference reports whether the Command points to a Reference.
func (c Command) HasReference() bool { return c.referenceExternalTransactionID != "" }

// IsValid reports whether c was built by NewCommand.
func (c Command) IsValid() bool { return c.kind.IsExternal() }
