package wager

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// PayloadHash returns the Payload Hash of cmd: lower-case hex SHA-256 over
// its canonical JSON.
//
// Canonical form (identical for HTTP and SQS, since both build a Command):
//
//   - One JSON object mirroring the wire contract, with money kept as a
//     nested object: {"externalTransactionId","gameId","kind","money":
//     {"amount","currency"},"playerId","providerId",
//     "referenceExternalTransactionId","roundId","walletId"}.
//   - Keys sorted by byte order at every level; no insignificant
//     whitespace; no trailing newline; UTF-8.
//   - All values are JSON strings. money.amount is the canonical two-decimal
//     string (the parser accepts only that form, so no normalization is
//     needed); playerId and walletId are lower-case hyphenated UUIDs;
//     every other string is used exactly as received.
//   - referenceExternalTransactionId is omitted when absent.
//   - String escaping follows encoding/json with HTML escaping disabled
//     (only '"', '\\' and control characters are escaped).
//   - Excluded: the Idempotency Key and every transport field (headers,
//     SQS messageId/type/occurredAt, token claims).
//
// Example for the challenge's BET:
//
//	{"externalTransactionId":"transaction-123","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"},"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","providerId":"provider-a","roundId":"round-987","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"}
func PayloadHash(cmd Command) string {
	sum := sha256.Sum256(CanonicalPayload(cmd))
	return hex.EncodeToString(sum[:])
}

// CanonicalPayload returns the canonical JSON hashed by PayloadHash, exposed
// for documentation and debugging.
func CanonicalPayload(cmd Command) []byte {
	// encoding/json sorts map keys, which yields the canonical key order.
	doc := map[string]any{
		"providerId":            cmd.providerID,
		"externalTransactionId": cmd.externalTransactionID,
		"playerId":              cmd.playerID.String(),
		"walletId":              cmd.walletID.String(),
		"roundId":               cmd.roundID,
		"gameId":                cmd.gameID,
		"kind":                  string(cmd.kind),
		"money": map[string]string{
			"amount":   cmd.money.Amount(),
			"currency": cmd.money.Currency().Code(),
		},
	}
	if cmd.referenceExternalTransactionID != "" {
		doc["referenceExternalTransactionId"] = cmd.referenceExternalTransactionID
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(doc) // strings only: cannot fail
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
