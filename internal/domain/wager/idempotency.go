package wager

import "fmt"

// CheckReplay classifies a Command against an existing external Transaction
// of the same Provider, found by (providerId, idempotencyKey) or by
// (providerId, externalTransactionId). Look up by key first.
//
//   - same Idempotency Key and same Payload Hash: nil, a Replay (answer with
//     the persisted result and idempotentReplay: true);
//   - same Idempotency Key, different Payload Hash: ErrIdempotencyKeyConflict;
//   - other key, same externalTransactionId: ErrExternalTransactionConflict.
//
// An existing Transaction from another Provider, or matching neither key nor
// externalTransactionId, is a lookup bug and returns ErrInvalidTransaction:
// cross-provider lookups must answer 404 before reaching this point.
func CheckReplay(existing *Transaction, cmd Command) error {
	if existing == nil || !cmd.IsValid() {
		return fmt.Errorf("%w: CheckReplay needs an existing transaction and a valid command", ErrInvalidTransaction)
	}
	if existing.Origin() != OriginExternal || existing.ProviderID() != cmd.ProviderID() {
		return fmt.Errorf("%w: transaction %s does not belong to provider %q", ErrInvalidTransaction, existing.ID(), cmd.ProviderID())
	}
	switch {
	case existing.IdempotencyKey() == cmd.IdempotencyKey():
		if existing.PayloadHash() != PayloadHash(cmd) {
			return fmt.Errorf("%w: key %q was used with a different payload", ErrIdempotencyKeyConflict, cmd.IdempotencyKey())
		}
		return nil
	case existing.ExternalTransactionID() == cmd.ExternalTransactionID():
		return fmt.Errorf("%w: externalTransactionId %q was recorded under another idempotency key", ErrExternalTransactionConflict, cmd.ExternalTransactionID())
	}
	return fmt.Errorf("%w: transaction %s matches neither idempotency key nor externalTransactionId", ErrInvalidTransaction, existing.ID())
}
