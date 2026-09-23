package wager

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// Transaction is the Wager Transaction aggregate. It is created in PENDING
// (NewExternal, NewOpening), moved by its transition methods or by Apply, and
// restored from storage with Rehydrate. Terminal states are immutable.
type Transaction struct {
	s Snapshot
}

// Snapshot is the full persisted state of a Transaction, used by the
// repository to write (Transaction.Snapshot) and read (Rehydrate).
// Optional values use their zero value for "absent": "" for strings and the
// FailureCode, uuid.Nil for ids, the invalid zero money.Money for
// BalanceAfter.
type Snapshot struct {
	ID       uuid.UUID
	Origin   Origin
	Kind     Kind
	WalletID uuid.UUID
	PlayerID uuid.UUID
	Money    money.Money
	Status   Status

	// FailureCode is set exactly when Status is REJECTED or FAILED.
	FailureCode FailureCode
	// BalanceAfter is the Wallet balance returned to the Provider and
	// replayed verbatim: the balance after the movement for PROCESSED, the
	// balance observed at rejection for most REJECTED codes. Absent for
	// PENDING, PENDING_REFERENCE, FAILED, WALLET_NOT_FOUND and
	// WALLET_PLAYER_MISMATCH (the latter would leak another player's
	// balance). Its currency is the Wallet's, which differs from Money's on
	// CURRENCY_MISMATCH.
	BalanceAfter money.Money

	// External metadata: required for OriginExternal, empty for Opening.
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	RoundID                        string
	GameID                         string
	ReferenceExternalTransactionID string

	// ReferenceTransactionID is the resolved Reference, set once Apply finds
	// it (on success and on reference-based rejections).
	ReferenceTransactionID uuid.UUID
	// CompensatedBetID is the BET whose Compensation Slot this Transaction
	// consumed (ADR-0002). Set only on a PROCESSED REFUND or ROLLBACK whose
	// Reference is a BET; nil for every other row, including ROLLBACK of a
	// REFUND (the slot stays consumed by the REFUND). A partial unique index
	// on this column (WHERE compensated_bet_id IS NOT NULL) enforces one
	// successful Reversal per BET.
	CompensatedBetID uuid.UUID
	// Attempts counts Pending Reference retries that still found no usable
	// Reference (see PendingPolicy).
	Attempts int

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewExternal creates a PENDING Transaction from a validated Command, with a
// fresh UUIDv7 and the Command's PayloadHash.
func NewExternal(cmd Command, now time.Time) (*Transaction, error) {
	if !cmd.IsValid() {
		return nil, fmt.Errorf("%w: command was not built by NewCommand", ErrInvalidTransaction)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("wager: generate id: %w", err)
	}
	now = normalizeTime(now)
	return build(Snapshot{
		ID:                             id,
		Origin:                         OriginExternal,
		Kind:                           cmd.kind,
		WalletID:                       cmd.walletID,
		PlayerID:                       cmd.playerID,
		Money:                          cmd.money,
		Status:                         StatusPending,
		ProviderID:                     cmd.providerID,
		ExternalTransactionID:          cmd.externalTransactionID,
		IdempotencyKey:                 cmd.idempotencyKey,
		PayloadHash:                    PayloadHash(cmd),
		RoundID:                        cmd.roundID,
		GameID:                         cmd.gameID,
		ReferenceExternalTransactionID: cmd.referenceExternalTransactionID,
		CreatedAt:                      now,
		UpdatedAt:                      now,
	})
}

// NewOpening creates the PENDING internal Opening that credits a positive
// initial balance. It carries no external metadata. See also Open.
func NewOpening(walletID, playerID uuid.UUID, initial money.Money, now time.Time) (*Transaction, error) {
	if !initial.IsValid() || initial.IsZero() || initial.IsNegative() {
		return nil, fmt.Errorf("%w: opening amount %s must be positive", ErrInvalidTransaction, initial)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("wager: generate id: %w", err)
	}
	now = normalizeTime(now)
	return build(Snapshot{
		ID:        id,
		Origin:    OriginInternal,
		Kind:      KindOpening,
		WalletID:  walletID,
		PlayerID:  playerID,
		Money:     initial,
		Status:    StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	})
}

// Rehydrate restores a persisted Transaction, checking only structural
// consistency; it applies no transition and emits nothing.
func Rehydrate(s Snapshot) (*Transaction, error) {
	s.CreatedAt = s.CreatedAt.UTC()
	s.UpdatedAt = s.UpdatedAt.UTC()
	return build(s)
}

func build(s Snapshot) (*Transaction, error) {
	if err := validate(s); err != nil {
		return nil, fmt.Errorf("%w: transaction %s: %s", ErrInvalidTransaction, s.ID, err)
	}
	return &Transaction{s: s}, nil
}

// validate returns a plain error describing the first inconsistency.
func validate(s Snapshot) error {
	switch {
	case s.ID == uuid.Nil:
		return fmt.Errorf("id is required")
	case s.WalletID == uuid.Nil || s.PlayerID == uuid.Nil:
		return fmt.Errorf("walletId and playerId are required")
	case !s.Kind.IsValid():
		return fmt.Errorf("unknown kind %q", s.Kind)
	case !s.Money.IsValid() || s.Money.IsNegative():
		return fmt.Errorf("money must be a non-negative amount")
	case s.Attempts < 0:
		return fmt.Errorf("attempts must not be negative")
	case s.CreatedAt.IsZero() || s.UpdatedAt.IsZero():
		return fmt.Errorf("timestamps are required")
	}
	if _, err := ParseStatus(string(s.Status)); err != nil {
		return fmt.Errorf("unknown status %q", s.Status)
	}
	failed := s.Status == StatusRejected || s.Status == StatusFailed
	switch {
	case failed && !s.FailureCode.IsValid():
		return fmt.Errorf("%s requires a known failure code, got %q", s.Status, s.FailureCode)
	case !failed && s.FailureCode != "":
		return fmt.Errorf("%s cannot carry failure code %s", s.Status, s.FailureCode)
	case s.Status == StatusProcessed && !s.BalanceAfter.IsValid():
		return fmt.Errorf("PROCESSED requires balanceAfter")
	case s.BalanceAfter.IsNegative():
		return fmt.Errorf("balanceAfter must not be negative")
	}

	switch s.Origin {
	case OriginInternal:
		if s.Kind != KindOpening {
			return fmt.Errorf("internal origin only allows OPENING, got %s", s.Kind)
		}
		if s.ProviderID != "" || s.ExternalTransactionID != "" || s.IdempotencyKey != "" || s.PayloadHash != "" ||
			s.RoundID != "" || s.GameID != "" || s.ReferenceExternalTransactionID != "" ||
			s.ReferenceTransactionID != uuid.Nil || s.CompensatedBetID != uuid.Nil || s.Attempts != 0 {
			return fmt.Errorf("opening cannot carry external metadata")
		}
		if s.Money.IsZero() {
			return fmt.Errorf("opening amount must be positive")
		}
		return nil
	case OriginExternal:
		if !s.Kind.IsExternal() {
			return fmt.Errorf("external origin does not allow %s", s.Kind)
		}
	default:
		return fmt.Errorf("unknown origin %q", s.Origin)
	}

	if s.ProviderID == "" || s.ExternalTransactionID == "" || s.IdempotencyKey == "" || s.RoundID == "" || s.GameID == "" {
		return fmt.Errorf("external metadata (providerId, externalTransactionId, idempotencyKey, roundId, gameId) is required")
	}
	if b, err := hex.DecodeString(s.PayloadHash); err != nil || len(b) != 32 || s.PayloadHash != hex.EncodeToString(b) {
		return fmt.Errorf("payloadHash must be 64 lower-case hex characters")
	}
	hasRef := s.ReferenceExternalTransactionID != ""
	switch {
	case s.Kind.IsReversal() && !hasRef:
		return fmt.Errorf("%s requires referenceExternalTransactionId", s.Kind)
	case (s.Kind == KindBet || s.Kind == KindLoss) && hasRef:
		return fmt.Errorf("%s cannot carry a reference", s.Kind)
	case !hasRef && s.ReferenceTransactionID != uuid.Nil:
		return fmt.Errorf("referenceTransactionId without referenceExternalTransactionId")
	case s.Status == StatusPendingReference && !hasRef:
		return fmt.Errorf("PENDING_REFERENCE requires a reference")
	}
	if s.CompensatedBetID != uuid.Nil &&
		(!s.Kind.IsReversal() || s.Status != StatusProcessed || s.CompensatedBetID != s.ReferenceTransactionID) {
		return fmt.Errorf("compensatedBetId belongs only to a PROCESSED reversal of that BET")
	}
	return nil
}

func normalizeTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// Snapshot returns a copy of the full state for persistence.
func (t *Transaction) Snapshot() Snapshot { return t.s }

func (t *Transaction) ID() uuid.UUID                     { return t.s.ID }
func (t *Transaction) Origin() Origin                    { return t.s.Origin }
func (t *Transaction) Kind() Kind                        { return t.s.Kind }
func (t *Transaction) WalletID() uuid.UUID               { return t.s.WalletID }
func (t *Transaction) PlayerID() uuid.UUID               { return t.s.PlayerID }
func (t *Transaction) Money() money.Money                { return t.s.Money }
func (t *Transaction) Status() Status                    { return t.s.Status }
func (t *Transaction) FailureCode() FailureCode          { return t.s.FailureCode }
func (t *Transaction) ProviderID() string                { return t.s.ProviderID }
func (t *Transaction) ExternalTransactionID() string     { return t.s.ExternalTransactionID }
func (t *Transaction) IdempotencyKey() string            { return t.s.IdempotencyKey }
func (t *Transaction) PayloadHash() string               { return t.s.PayloadHash }
func (t *Transaction) RoundID() string                   { return t.s.RoundID }
func (t *Transaction) GameID() string                    { return t.s.GameID }
func (t *Transaction) ReferenceTransactionID() uuid.UUID { return t.s.ReferenceTransactionID }
func (t *Transaction) CompensatedBetID() uuid.UUID       { return t.s.CompensatedBetID }
func (t *Transaction) Attempts() int                     { return t.s.Attempts }
func (t *Transaction) CreatedAt() time.Time              { return t.s.CreatedAt }
func (t *Transaction) UpdatedAt() time.Time              { return t.s.UpdatedAt }

// ReferenceExternalTransactionID returns "" when there is no Reference.
func (t *Transaction) ReferenceExternalTransactionID() string {
	return t.s.ReferenceExternalTransactionID
}

// HasReference reports whether the Transaction depends on a Reference
// (always for REFUND and ROLLBACK, optionally for WIN). The caller must then
// look it up by (ProviderID, ReferenceExternalTransactionID) before Apply.
func (t *Transaction) HasReference() bool { return t.s.ReferenceExternalTransactionID != "" }

// BalanceAfter returns the balance reported to the Provider, if recorded.
func (t *Transaction) BalanceAfter() (money.Money, bool) {
	return t.s.BalanceAfter, t.s.BalanceAfter.IsValid()
}

func (t *Transaction) transition(next Status, now time.Time) error {
	if !t.s.Status.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s -> %s (transaction %s)", ErrInvalidTransition, t.s.Status, next, t.s.ID)
	}
	t.s.Status = next
	t.s.UpdatedAt = normalizeTime(now)
	return nil
}

// Process concludes the Transaction successfully, recording the balance
// returned to the Provider. Prefer Apply, which also records the Reference
// and the Compensation Slot.
func (t *Transaction) Process(balanceAfter money.Money, now time.Time) error {
	if !balanceAfter.IsValid() || balanceAfter.IsNegative() {
		return fmt.Errorf("%w: balanceAfter %s must be a non-negative amount", ErrInvalidTransaction, balanceAfter)
	}
	if err := t.transition(StatusProcessed, now); err != nil {
		return err
	}
	t.s.BalanceAfter = balanceAfter
	return nil
}

// Reject concludes the Transaction with a business rejection. balance is the
// balance reported to the Provider; pass the zero money.Money when none.
func (t *Transaction) Reject(code FailureCode, balance money.Money, now time.Time) error {
	if !code.IsValid() {
		return fmt.Errorf("%w: unknown failure code %q", ErrInvalidTransaction, code)
	}
	if balance.IsNegative() {
		return fmt.Errorf("%w: balance %s must not be negative", ErrInvalidTransaction, balance)
	}
	if err := t.transition(StatusRejected, now); err != nil {
		return err
	}
	t.s.FailureCode = code
	t.s.BalanceAfter = balance
	return nil
}

// Fail records a permanent infrastructure failure for audit. Transient
// failures must never call it: they leave no terminal record.
func (t *Transaction) Fail(code FailureCode, now time.Time) error {
	if !code.IsValid() {
		return fmt.Errorf("%w: unknown failure code %q", ErrInvalidTransaction, code)
	}
	if err := t.transition(StatusFailed, now); err != nil {
		return err
	}
	t.s.FailureCode = code
	t.s.BalanceAfter = money.Money{}
	return nil
}

// AwaitReference parks a PENDING Transaction whose Reference is not usable
// yet. Only Transactions with a Reference may wait.
func (t *Transaction) AwaitReference(now time.Time) error {
	if !t.HasReference() {
		return fmt.Errorf("%w: %s without reference cannot await one", ErrInvalidTransition, t.s.Kind)
	}
	return t.transition(StatusPendingReference, now)
}
