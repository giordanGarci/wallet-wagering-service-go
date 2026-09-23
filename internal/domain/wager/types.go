// Package wager holds the Wager Transaction aggregate and the pure rules that
// decide its outcome: the external Command (normalized input shared by HTTP
// and SQS) and its format validation, the Payload Hash, the idempotency
// decision, the zero policy, the Reference and Compensation Slot rules
// (ADR-0002), the state machine and the Pending Reference retry policy.
//
// The application layer drives one Wager Transaction per SQL transaction
// (ADR-0001): lock the Wallet, build the Transaction (NewExternal), resolve
// the Reference when HasReference, call Apply, then persist the Transaction,
// the returned Ledger Entry (if any) and the Outbox Events.
package wager

import "fmt"

// Kind is the type of a Wager Transaction.
type Kind string

const (
	KindOpening  Kind = "OPENING"
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

// ParseKind accepts the canonical upper-case names, OPENING included; use
// IsExternal to reject OPENING at the Provider boundary.
func ParseKind(s string) (Kind, error) {
	k := Kind(s)
	if !k.IsValid() {
		return "", fmt.Errorf("%w: unknown kind %q", ErrInvalidTransaction, s)
	}
	return k, nil
}

// IsValid reports whether k is one of the six kinds.
func (k Kind) IsValid() bool {
	switch k {
	case KindOpening, KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return true
	}
	return false
}

// IsExternal reports whether a Provider may send k (every kind but OPENING).
func (k Kind) IsExternal() bool { return k.IsValid() && k != KindOpening }

// IsReversal reports whether k is REFUND or ROLLBACK.
func (k Kind) IsReversal() bool { return k == KindRefund || k == KindRollback }

// String implements fmt.Stringer.
func (k Kind) String() string { return string(k) }

// Origin tells a Provider operation apart from an internal one (Opening).
type Origin string

const (
	OriginExternal Origin = "EXTERNAL"
	OriginInternal Origin = "INTERNAL"
)

// ParseOrigin accepts the canonical upper-case names only.
func ParseOrigin(s string) (Origin, error) {
	o := Origin(s)
	if o != OriginExternal && o != OriginInternal {
		return "", fmt.Errorf("%w: unknown origin %q", ErrInvalidTransaction, s)
	}
	return o, nil
}

// String implements fmt.Stringer.
func (o Origin) String() string { return string(o) }

// Status is the state of a Wager Transaction. Valid transitions:
//
//	PENDING            -> PROCESSED | REJECTED | FAILED | PENDING_REFERENCE
//	PENDING_REFERENCE  -> PROCESSED | REJECTED | FAILED
//	PROCESSED, REJECTED, FAILED: terminal
type Status string

const (
	StatusPending          Status = "PENDING"
	StatusPendingReference Status = "PENDING_REFERENCE"
	StatusProcessed        Status = "PROCESSED"
	StatusRejected         Status = "REJECTED"
	StatusFailed           Status = "FAILED"
)

// ParseStatus accepts the canonical upper-case names only.
func ParseStatus(s string) (Status, error) {
	st := Status(s)
	switch st {
	case StatusPending, StatusPendingReference, StatusProcessed, StatusRejected, StatusFailed:
		return st, nil
	}
	return "", fmt.Errorf("%w: unknown status %q", ErrInvalidTransaction, s)
}

// IsTerminal reports whether s admits no further transition.
func (s Status) IsTerminal() bool {
	return s == StatusProcessed || s == StatusRejected || s == StatusFailed
}

// CanTransitionTo reports whether s -> next is in the transition table.
func (s Status) CanTransitionTo(next Status) bool {
	switch s {
	case StatusPending:
		return next == StatusProcessed || next == StatusRejected || next == StatusFailed || next == StatusPendingReference
	case StatusPendingReference:
		return next == StatusProcessed || next == StatusRejected || next == StatusFailed
	}
	return false
}

// String implements fmt.Stringer.
func (s Status) String() string { return string(s) }
