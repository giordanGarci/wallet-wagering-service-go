package wager

import (
	"errors"
	"fmt"
)

// Sentinel errors, classifiable with errors.Is.
var (
	// ErrInvalidCommand: the external Command breaks a format rule. Maps to
	// HTTP 400 (or DLQ for SQS) and no Wager Transaction is recorded. The
	// concrete error is a *CommandError naming the field.
	ErrInvalidCommand = errors.New("wager: invalid command")
	// ErrInvalidTransaction: a Transaction built or rehydrated from
	// inconsistent data, or an unknown enum value.
	ErrInvalidTransaction = errors.New("wager: invalid transaction")
	// ErrInvalidTransition: the state machine forbids the transition
	// (terminal states are immutable).
	ErrInvalidTransition = errors.New("wager: invalid status transition")
	// ErrInvalidReference: the caller passed a Reference that does not
	// belong to the Transaction (wrong provider or external id). This is a
	// programming error of the lookup, never a business rejection.
	ErrInvalidReference = errors.New("wager: reference does not match lookup key")
	// ErrRejected matches every *RejectionError.
	ErrRejected = errors.New("wager: rejected")
	// ErrIdempotencyKeyConflict: the Idempotency Key was already used with a
	// different Payload Hash (409 IDEMPOTENCY_KEY_CONFLICT).
	ErrIdempotencyKeyConflict = errors.New("IDEMPOTENCY_KEY_CONFLICT")
	// ErrExternalTransactionConflict: the (providerId, externalTransactionId)
	// was already recorded under another Idempotency Key (409
	// EXTERNAL_TRANSACTION_CONFLICT).
	ErrExternalTransactionConflict = errors.New("EXTERNAL_TRANSACTION_CONFLICT")
)

// CommandError is a format violation of the external Command. It matches
// ErrInvalidCommand with errors.Is.
type CommandError struct {
	Field  string // wire name of the offending field, e.g. "money.amount"
	Reason string
	Err    error // underlying cause, e.g. a money parsing error; may be nil
}

func (e *CommandError) Error() string {
	msg := "wager: invalid command: " + e.Field + ": " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Is makes errors.Is(err, ErrInvalidCommand) true.
func (e *CommandError) Is(target error) bool { return target == ErrInvalidCommand }

// Unwrap exposes the underlying cause.
func (e *CommandError) Unwrap() error { return e.Err }

func invalidField(field, reason string, cause error) error {
	return &CommandError{Field: field, Reason: reason, Err: cause}
}

// FailureCode is the stable code explaining why a Wager Transaction was
// REJECTED or FAILED.
type FailureCode string

const (
	FailureInsufficientFunds           FailureCode = "INSUFFICIENT_FUNDS"
	FailureReversalInsufficientFunds   FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	FailureWalletNotFound              FailureCode = "WALLET_NOT_FOUND"
	FailureWalletPlayerMismatch        FailureCode = "WALLET_PLAYER_MISMATCH"
	FailureCurrencyMismatch            FailureCode = "CURRENCY_MISMATCH"
	FailureReferenceNotFound           FailureCode = "REFERENCE_NOT_FOUND"
	FailureReferenceUnsuccessful       FailureCode = "REFERENCE_UNSUCCESSFUL"
	FailureReferenceMismatch           FailureCode = "REFERENCE_MISMATCH"
	FailureReferenceAmountMismatch     FailureCode = "REFERENCE_AMOUNT_MISMATCH"
	FailureReferenceNotReversible      FailureCode = "REFERENCE_NOT_REVERSIBLE"
	FailureReferenceAlreadyCompensated FailureCode = "REFERENCE_ALREADY_COMPENSATED"
	// FailurePermanentError records a permanent infrastructure failure on a
	// FAILED Transaction (audit only; transient failures never reach a
	// terminal state).
	FailurePermanentError FailureCode = "PERMANENT_ERROR"
)

var correctable = map[FailureCode]bool{
	FailureInsufficientFunds:           false,
	FailureReversalInsufficientFunds:   false,
	FailureWalletNotFound:              true,
	FailureWalletPlayerMismatch:        true,
	FailureCurrencyMismatch:            true,
	FailureReferenceNotFound:           false,
	FailureReferenceUnsuccessful:       false,
	FailureReferenceMismatch:           true,
	FailureReferenceAmountMismatch:     true,
	FailureReferenceNotReversible:      false,
	FailureReferenceAlreadyCompensated: false,
	FailurePermanentError:              false,
}

// ParseFailureCode accepts the documented codes only.
func ParseFailureCode(s string) (FailureCode, error) {
	c := FailureCode(s)
	if !c.IsValid() {
		return "", fmt.Errorf("%w: unknown failure code %q", ErrInvalidTransaction, s)
	}
	return c, nil
}

// IsValid reports whether c is a documented code.
func (c FailureCode) IsValid() bool { _, ok := correctable[c]; return ok }

// Correctable reports whether the Provider can fix the input and send a new,
// corrected operation (e.g. wrong wallet, currency or reference data).
func (c FailureCode) Correctable() bool { return correctable[c] }

// Definitive reports whether the outcome stands whatever the Provider sends
// next (e.g. insufficient funds, slot already consumed).
func (c FailureCode) Definitive() bool { return c.IsValid() && !correctable[c] }

// String implements fmt.Stringer.
func (c FailureCode) String() string { return string(c) }

// RejectionError is a business rejection carrying its Failure Code. It
// matches ErrRejected with errors.Is; use errors.As or CodeOf to read Code.
type RejectionError struct {
	Code   FailureCode
	Detail string
}

func (e *RejectionError) Error() string {
	if e.Detail == "" {
		return "wager: rejected: " + string(e.Code)
	}
	return "wager: rejected: " + string(e.Code) + ": " + e.Detail
}

// Is makes errors.Is(err, ErrRejected) true.
func (e *RejectionError) Is(target error) bool { return target == ErrRejected }

func rejection(code FailureCode, format string, args ...any) *RejectionError {
	return &RejectionError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// CodeOf extracts the Failure Code of a business rejection anywhere in err's
// chain.
func CodeOf(err error) (FailureCode, bool) {
	var rej *RejectionError
	if errors.As(err, &rej) {
		return rej.Code, true
	}
	return "", false
}
