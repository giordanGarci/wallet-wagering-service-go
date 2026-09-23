package wager_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func mustCommand(t *testing.T, in wager.CommandInput) wager.Command {
	t.Helper()
	cmd, err := wager.NewCommand(in)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestCanonicalPayloadOfTheChallengeExample(t *testing.T) {
	want := `{"externalTransactionId":"transaction-123","gameId":"fortune-chimp","kind":"BET",` +
		`"money":{"amount":"25.00","currency":"BRL"},"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",` +
		`"providerId":"provider-a","roundId":"round-987","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"}`
	cmd := mustCommand(t, input(t))
	if got := string(wager.CanonicalPayload(cmd)); got != want {
		t.Fatalf("canonical payload\n got %s\nwant %s", got, want)
	}
	sum := sha256.Sum256([]byte(want))
	if got := wager.PayloadHash(cmd); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash = %s", got)
	}
}

func TestPayloadHashIsDeterministicAndIndependentOfInputOrder(t *testing.T) {
	a := mustCommand(t, input(t))
	// Same content assembled field by field in another order.
	var in wager.CommandInput
	in.Money = mustMoney(t, "25.00", "BRL")
	in.Kind = "BET"
	in.GameID = "fortune-chimp"
	in.RoundID = "round-987"
	in.WalletID = walletID.String()
	in.PlayerID = playerID.String()
	in.IdempotencyKey = "provider-a:transaction-123"
	in.ExternalTransactionID = "transaction-123"
	in.ProviderID = "provider-a"
	b := mustCommand(t, in)
	h := wager.PayloadHash(a)
	for i := 0; i < 100; i++ {
		if wager.PayloadHash(a) != h || wager.PayloadHash(b) != h {
			t.Fatal("hash must be deterministic")
		}
	}
	if len(h) != 64 || strings.ToLower(h) != h {
		t.Fatalf("hash %q is not lower-case hex SHA-256", h)
	}
}

func TestPayloadHashExcludesIdempotencyKey(t *testing.T) {
	base := input(t)
	other := input(t)
	other.IdempotencyKey = "totally-different-key"
	if wager.PayloadHash(mustCommand(t, base)) != wager.PayloadHash(mustCommand(t, other)) {
		t.Fatal("idempotency key must not affect the hash")
	}
}

func TestPayloadHashNormalizesUUIDCase(t *testing.T) {
	upper := input(t)
	upper.PlayerID = strings.ToUpper(upper.PlayerID)
	upper.WalletID = strings.ToUpper(upper.WalletID)
	if wager.PayloadHash(mustCommand(t, input(t))) != wager.PayloadHash(mustCommand(t, upper)) {
		t.Fatal("UUID letter case must not affect the hash")
	}
}

func TestPayloadHashCoversEveryBusinessField(t *testing.T) {
	base := wager.PayloadHash(mustCommand(t, input(t)))
	edits := map[string]func(*wager.CommandInput){
		"providerId":            func(in *wager.CommandInput) { in.ProviderID = "provider-b" },
		"externalTransactionId": func(in *wager.CommandInput) { in.ExternalTransactionID = "transaction-124" },
		"playerId":              func(in *wager.CommandInput) { in.PlayerID = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a2" },
		"walletId":              func(in *wager.CommandInput) { in.WalletID = "0192f291-27dd-7d3f-8071-5f8685deef38" },
		"roundId":               func(in *wager.CommandInput) { in.RoundID = "round-988" },
		"gameId":                func(in *wager.CommandInput) { in.GameID = "other-game" },
		"kind":                  func(in *wager.CommandInput) { in.Kind = "WIN" },
		"money.amount":          func(in *wager.CommandInput) { in.Money = mustMoney(t, "25.01", "BRL") },
		"money.currency":        func(in *wager.CommandInput) { in.Money = mustMoney(t, "25.00", "USD") },
		"referenceExternalTransactionId": func(in *wager.CommandInput) {
			in.Kind = "WIN"
			in.ReferenceExternalTransactionID = "bet-1"
		},
	}
	seen := map[string]string{base: "base"}
	for field, edit := range edits {
		in := input(t)
		edit(&in)
		h := wager.PayloadHash(mustCommand(t, in))
		if prev, dup := seen[h]; dup {
			t.Errorf("changing %s gives the same hash as %s", field, prev)
		}
		seen[h] = field
	}
}

func TestCanonicalPayloadOmitsAbsentReferenceAndKeepsUnicode(t *testing.T) {
	in := input(t)
	in.GameID = "jogo-<ação>&"
	payload := string(wager.CanonicalPayload(mustCommand(t, in)))
	if strings.Contains(payload, "referenceExternalTransactionId") {
		t.Error("absent reference must be omitted")
	}
	if !strings.Contains(payload, `"gameId":"jogo-<ação>&"`) {
		t.Errorf("strings must be kept verbatim without HTML escaping: %s", payload)
	}
	in.Kind = "REFUND"
	in.ReferenceExternalTransactionID = "bet-1"
	payload = string(wager.CanonicalPayload(mustCommand(t, in)))
	if !strings.Contains(payload, `"referenceExternalTransactionId":"bet-1","roundId"`) {
		t.Errorf("reference must be present in sorted position: %s", payload)
	}
}
