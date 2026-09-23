package wager

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

// Reference is what the application found for a Transaction's
// referenceExternalTransactionId, looked up within the same Provider. The zero
// value means "not found".
type Reference struct {
	// Transaction is the Reference, or nil when it has not arrived.
	Transaction *Transaction
	// Compensated reports that a PROCESSED REFUND or ROLLBACK already
	// references Transaction: for a BET its Compensation Slot is consumed
	// (equivalently, some row has compensated_bet_id = BET id); for a WIN or
	// a REFUND it was already rolled back. Read it with the Wallet locked:
	// References share the Wallet of their Reversals, so the Wallet lock
	// serializes competing Reversals and the unique indexes are the backstop.
	Compensated bool
}

// Resolution is the outcome of the Reference rules for a Transaction that
// may proceed.
type Resolution struct {
	// Await: the Reference is absent or not finished (PENDING_REFERENCE);
	// the Transaction must wait for it.
	Await bool
	// Direction of the Wallet movement; "" for LOSS (no movement).
	Direction ledger.Direction
	// ReferenceTransactionID is the resolved Reference (uuid.Nil if none).
	ReferenceTransactionID uuid.UUID
	// CompensatedBetID is the BET whose Compensation Slot is consumed: set
	// for REFUND or ROLLBACK of a BET, uuid.Nil otherwise (ROLLBACK of a
	// REFUND or of a WIN claims no slot).
	CompensatedBetID uuid.UUID
}

// ResolveReference applies the kind and Reference rules to t (pure).
//
// Without Reference: BET debits, WIN credits, LOSS does not move.
// With Reference (REFUND, ROLLBACK, WIN with referenceExternalTransactionId),
// checked in this order:
//
//  1. the Reference must come from the same Provider and external id
//     (otherwise ErrInvalidReference, a lookup bug);
//  2. kind compatibility: WIN needs a BET (else REFERENCE_MISMATCH); REFUND
//     needs a BET; ROLLBACK needs a BET, WIN or REFUND (else
//     REFERENCE_NOT_REVERSIBLE: ROLLBACK of ROLLBACK, REFUND of WIN or REFUND,
//     any Reversal of LOSS or OPENING);
//  3. absent, PENDING or PENDING_REFERENCE Reference: Await;
//     REJECTED or FAILED Reference: REFERENCE_UNSUCCESSFUL;
//  4. player, Wallet, currency or Round differ: REFERENCE_MISMATCH;
//  5. Reversal amount differs: REFERENCE_AMOUNT_MISMATCH (WIN amount is free);
//  6. Reversal of an already reversed Reference: REFERENCE_ALREADY_COMPENSATED.
//
// Directions: REFUND of BET and ROLLBACK of BET credit; ROLLBACK of WIN and
// ROLLBACK of REFUND debit. Rejections are *RejectionError; the returned
// Resolution still carries ReferenceTransactionID for audit.
func ResolveReference(t *Transaction, ref Reference) (Resolution, error) {
	if !t.HasReference() {
		switch t.Kind() {
		case KindBet:
			return Resolution{Direction: ledger.Debit}, nil
		case KindWin:
			return Resolution{Direction: ledger.Credit}, nil
		case KindLoss:
			return Resolution{}, nil
		}
		return Resolution{}, fmt.Errorf("%w: %s without reference", ErrInvalidTransaction, t.Kind())
	}

	r := ref.Transaction
	if r == nil {
		return Resolution{Await: true}, nil
	}
	if r.Origin() != OriginExternal || r.ProviderID() != t.ProviderID() ||
		r.ExternalTransactionID() != t.ReferenceExternalTransactionID() || r.ID() == t.ID() {
		return Resolution{}, fmt.Errorf("%w: transaction %s is not the reference %q of provider %q",
			ErrInvalidReference, r.ID(), t.ReferenceExternalTransactionID(), t.ProviderID())
	}
	res := Resolution{ReferenceTransactionID: r.ID()}

	switch t.Kind() {
	case KindWin:
		if r.Kind() != KindBet {
			return res, rejection(FailureReferenceMismatch, "WIN must reference a BET, got %s", r.Kind())
		}
		res.Direction = ledger.Credit
	case KindRefund:
		if r.Kind() != KindBet {
			return res, rejection(FailureReferenceNotReversible, "REFUND only reverses a BET, got %s", r.Kind())
		}
		res.Direction = ledger.Credit
	case KindRollback:
		switch r.Kind() {
		case KindBet:
			res.Direction = ledger.Credit
		case KindWin, KindRefund:
			res.Direction = ledger.Debit
		default:
			return res, rejection(FailureReferenceNotReversible, "ROLLBACK cannot reverse %s", r.Kind())
		}
	default:
		return Resolution{}, fmt.Errorf("%w: %s cannot carry a reference", ErrInvalidTransaction, t.Kind())
	}

	switch r.Status() {
	case StatusPending, StatusPendingReference:
		return Resolution{Await: true}, nil
	case StatusRejected, StatusFailed:
		return res, rejection(FailureReferenceUnsuccessful, "reference %s is %s (%s)", r.ID(), r.Status(), r.FailureCode())
	}

	switch {
	case r.PlayerID() != t.PlayerID():
		return res, rejection(FailureReferenceMismatch, "player differs from reference")
	case r.WalletID() != t.WalletID():
		return res, rejection(FailureReferenceMismatch, "wallet differs from reference")
	case r.Money().Currency() != t.Money().Currency():
		return res, rejection(FailureReferenceMismatch, "currency differs from reference")
	case r.RoundID() != t.RoundID():
		return res, rejection(FailureReferenceMismatch, "round differs from reference")
	}

	if t.Kind().IsReversal() {
		if !r.Money().Equal(t.Money()) {
			return res, rejection(FailureReferenceAmountMismatch, "reversal of %s must be %s", t.Money(), r.Money())
		}
		if ref.Compensated {
			return res, rejection(FailureReferenceAlreadyCompensated, "reference %s was already reversed", r.ID())
		}
		if r.Kind() == KindBet {
			res.CompensatedBetID = r.ID()
		}
	}
	return res, nil
}

// Apply runs t against its locked Wallet w (nil when not found) and its
// Reference (ignored unless t.HasReference), mutating both. Afterwards
// t.Status() tells the application what to persist and emit:
//
//   - PROCESSED: the returned Ledger Entry (nil for LOSS) and the updated
//     Wallet; WagerTransactionProcessed, plus WalletBalanceChanged with an
//     entry;
//   - REJECTED: WagerTransactionRejected; the Wallet is untouched;
//   - PENDING_REFERENCE: WagerTransactionPendingReference when t was just
//     parked (it was PENDING). When t was already PENDING_REFERENCE (worker
//     retry) nothing changes; call RecordReferenceMiss.
//
// Checks, in order: WALLET_NOT_FOUND, WALLET_PLAYER_MISMATCH,
// CURRENCY_MISMATCH, ResolveReference, then funds: a debit beyond the balance
// is INSUFFICIENT_FUNDS for a BET and REVERSAL_INSUFFICIENT_FUNDS for a
// ROLLBACK. Business rejections are recorded on t and return a nil error;
// errors are reserved for misuse (terminal or internal Transaction, wrong
// Wallet or Reference passed in, overflow).
func (t *Transaction) Apply(w *wallet.Wallet, ref Reference, now time.Time) (*ledger.Entry, error) {
	if t.s.Origin != OriginExternal {
		return nil, fmt.Errorf("%w: Apply is for external transactions; use Open", ErrInvalidTransaction)
	}
	if t.s.Status != StatusPending && t.s.Status != StatusPendingReference {
		return nil, fmt.Errorf("%w: cannot apply %s transaction %s", ErrInvalidTransition, t.s.Status, t.s.ID)
	}
	if w == nil {
		return nil, t.Reject(FailureWalletNotFound, money.Money{}, now)
	}
	if w.ID() != t.s.WalletID {
		return nil, fmt.Errorf("%w: wallet %s passed for transaction on wallet %s", ErrInvalidTransaction, w.ID(), t.s.WalletID)
	}
	if w.PlayerID() != t.s.PlayerID {
		// No balance: it belongs to another player.
		return nil, t.Reject(FailureWalletPlayerMismatch, money.Money{}, now)
	}
	if w.Currency() != t.s.Money.Currency() {
		return nil, t.Reject(FailureCurrencyMismatch, w.Balance(), now)
	}

	res, err := ResolveReference(t, ref)
	if res.ReferenceTransactionID != uuid.Nil {
		t.s.ReferenceTransactionID = res.ReferenceTransactionID
	}
	if code, ok := CodeOf(err); ok {
		return nil, t.Reject(code, w.Balance(), now)
	}
	if err != nil {
		return nil, err
	}
	if res.Await {
		if t.s.Status == StatusPending {
			return nil, t.AwaitReference(now)
		}
		return nil, nil
	}

	var entry *ledger.Entry
	if res.Direction != "" {
		var e ledger.Entry
		if res.Direction == ledger.Debit {
			e, err = w.Debit(t.s.ID, t.s.Money, now)
		} else {
			e, err = w.Credit(t.s.ID, t.s.Money, now)
		}
		if errors.Is(err, wallet.ErrInsufficientFunds) {
			code := FailureInsufficientFunds
			if t.s.Kind.IsReversal() {
				code = FailureReversalInsufficientFunds
			}
			return nil, t.Reject(code, w.Balance(), now)
		}
		if err != nil {
			return nil, err
		}
		entry = &e
	}
	t.s.CompensatedBetID = res.CompensatedBetID
	if err := t.Process(w.Balance(), now); err != nil {
		return nil, err
	}
	return entry, nil
}

// Open credits a freshly created Wallet with its initial balance: it returns
// the PROCESSED Opening and its CREDIT Ledger Entry, keeping the Wallet at
// version 1. A zero initial balance creates no Opening and returns nils.
func Open(w *wallet.Wallet, initial money.Money, now time.Time) (*Transaction, *ledger.Entry, error) {
	if w == nil {
		return nil, nil, fmt.Errorf("%w: wallet is required", ErrInvalidTransaction)
	}
	if initial.IsZero() && initial.Currency() == w.Currency() {
		return nil, nil, nil
	}
	tx, err := NewOpening(w.ID(), w.PlayerID(), initial, now)
	if err != nil {
		return nil, nil, err
	}
	entry, err := w.ApplyOpening(tx.ID(), initial, now)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Process(w.Balance(), now); err != nil {
		return nil, nil, err
	}
	return tx, &entry, nil
}
