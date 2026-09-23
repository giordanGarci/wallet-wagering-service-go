package wager_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestNewCommandAcceptsTheChallengeExample(t *testing.T) {
	cmd, err := wager.NewCommand(input(t))
	if err != nil {
		t.Fatal(err)
	}
	if cmd.ProviderID() != "provider-a" || cmd.ExternalTransactionID() != "transaction-123" ||
		cmd.IdempotencyKey() != "provider-a:transaction-123" || cmd.PlayerID() != playerID ||
		cmd.WalletID() != walletID || cmd.RoundID() != "round-987" || cmd.GameID() != "fortune-chimp" ||
		cmd.Kind() != wager.KindBet || cmd.Money().Amount() != "25.00" || cmd.HasReference() {
		t.Fatalf("fields not preserved: %+v", cmd)
	}
	if !cmd.IsValid() {
		t.Fatal("built command must be valid")
	}
	var zero wager.Command
	if zero.IsValid() {
		t.Fatal("zero Command must be invalid")
	}
}

func TestNewCommandKeepsIdempotencyKeyAsSent(t *testing.T) {
	in := input(t)
	in.IdempotencyKey = "any-opaque-KEY/1"
	cmd, err := wager.NewCommand(in)
	if err != nil || cmd.IdempotencyKey() != "any-opaque-KEY/1" {
		t.Fatalf("key = %q, %v", cmd.IdempotencyKey(), err)
	}
}

func TestNewCommandNormalizesUUIDCase(t *testing.T) {
	in := input(t)
	in.PlayerID = strings.ToUpper(in.PlayerID)
	in.WalletID = strings.ToUpper(in.WalletID)
	cmd, err := wager.NewCommand(in)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.PlayerID() != playerID || cmd.WalletID() != walletID {
		t.Fatal("upper-case UUIDs must parse to the same ids")
	}
}

func TestZeroPolicyPerKind(t *testing.T) {
	cases := []struct {
		kind   wager.Kind
		amount string
		ref    string
		ok     bool
	}{
		{wager.KindBet, "0.01", "", true},
		{wager.KindBet, "0.00", "", false},
		{wager.KindWin, "10.00", "", true},
		{wager.KindWin, "0.00", "", false},
		{wager.KindRefund, "25.00", "bet-1", true},
		{wager.KindRefund, "0.00", "bet-1", false},
		{wager.KindRollback, "25.00", "bet-1", true},
		{wager.KindRollback, "0.00", "bet-1", false},
		{wager.KindLoss, "0.00", "", true},
		{wager.KindLoss, "0.01", "", false},
		{wager.KindLoss, "25.00", "", false},
	}
	for _, c := range cases {
		in := input(t)
		in.Kind = string(c.kind)
		in.Money = mustMoney(t, c.amount, "BRL")
		in.ReferenceExternalTransactionID = c.ref
		_, err := wager.NewCommand(in)
		if c.ok && err != nil {
			t.Errorf("%s %s: unexpected error %v", c.kind, c.amount, err)
		}
		if !c.ok && !errors.Is(err, wager.ErrInvalidCommand) {
			t.Errorf("%s %s: error = %v, want ErrInvalidCommand", c.kind, c.amount, err)
		}
	}
}

func TestNewCommandFormatRules(t *testing.T) {
	var noMoney money.Money
	cases := []struct {
		name  string
		field string
		edit  func(*wager.CommandInput)
	}{
		{"missing provider", "providerId", func(in *wager.CommandInput) { in.ProviderID = "" }},
		{"missing external id", "externalTransactionId", func(in *wager.CommandInput) { in.ExternalTransactionID = "" }},
		{"missing key", "idempotencyKey", func(in *wager.CommandInput) { in.IdempotencyKey = "" }},
		{"missing round", "roundId", func(in *wager.CommandInput) { in.RoundID = "" }},
		{"missing game", "gameId", func(in *wager.CommandInput) { in.GameID = "" }},
		{"padded provider", "providerId", func(in *wager.CommandInput) { in.ProviderID = " provider-a" }},
		{"oversized round", "roundId", func(in *wager.CommandInput) { in.RoundID = strings.Repeat("r", wager.MaxIdentifierLength+1) }},
		{"invalid utf-8", "gameId", func(in *wager.CommandInput) { in.GameID = "game-\xff" }},
		{"missing player", "playerId", func(in *wager.CommandInput) { in.PlayerID = "" }},
		{"malformed player", "playerId", func(in *wager.CommandInput) { in.PlayerID = "not-a-uuid" }},
		{"nil player", "playerId", func(in *wager.CommandInput) { in.PlayerID = "00000000-0000-0000-0000-000000000000" }},
		{"urn wallet", "walletId", func(in *wager.CommandInput) { in.WalletID = "urn:uuid:" + walletID.String() }},
		{"compact wallet", "walletId", func(in *wager.CommandInput) { in.WalletID = strings.ReplaceAll(walletID.String(), "-", "") }},
		{"missing kind", "kind", func(in *wager.CommandInput) { in.Kind = "" }},
		{"unknown kind", "kind", func(in *wager.CommandInput) { in.Kind = "JACKPOT" }},
		{"lower-case kind", "kind", func(in *wager.CommandInput) { in.Kind = "bet" }},
		{"external opening", "kind", func(in *wager.CommandInput) { in.Kind = "OPENING" }},
		{"missing money", "money", func(in *wager.CommandInput) { in.Money = noMoney }},
		{"refund without reference", "referenceExternalTransactionId", func(in *wager.CommandInput) { in.Kind = "REFUND" }},
		{"rollback without reference", "referenceExternalTransactionId", func(in *wager.CommandInput) { in.Kind = "ROLLBACK" }},
		{"bet with reference", "referenceExternalTransactionId", func(in *wager.CommandInput) { in.ReferenceExternalTransactionID = "bet-0" }},
		{"loss with reference", "referenceExternalTransactionId", func(in *wager.CommandInput) {
			in.Kind = "LOSS"
			in.Money = mustMoney(t, "0.00", "BRL")
			in.ReferenceExternalTransactionID = "bet-0"
		}},
		{"self reference", "referenceExternalTransactionId", func(in *wager.CommandInput) {
			in.Kind = "REFUND"
			in.ReferenceExternalTransactionID = in.ExternalTransactionID
		}},
		{"padded reference", "referenceExternalTransactionId", func(in *wager.CommandInput) {
			in.Kind = "REFUND"
			in.ReferenceExternalTransactionID = "bet-1 "
		}},
	}
	for _, c := range cases {
		in := input(t)
		c.edit(&in)
		_, err := wager.NewCommand(in)
		if !errors.Is(err, wager.ErrInvalidCommand) {
			t.Errorf("%s: error = %v, want ErrInvalidCommand", c.name, err)
			continue
		}
		var ce *wager.CommandError
		if !errors.As(err, &ce) || ce.Field != c.field {
			t.Errorf("%s: field = %v, want %q", c.name, err, c.field)
		}
	}
}

func TestWinReferenceIsOptional(t *testing.T) {
	for _, ref := range []string{"", "bet-1"} {
		in := input(t)
		in.Kind = "WIN"
		in.ReferenceExternalTransactionID = ref
		cmd, err := wager.NewCommand(in)
		if err != nil || cmd.HasReference() != (ref != "") || cmd.ReferenceExternalTransactionID() != ref {
			t.Errorf("WIN ref %q: %v", ref, err)
		}
	}
}

func TestMissingMoneyWrapsMoneyError(t *testing.T) {
	in := input(t)
	in.Money = money.Money{}
	_, err := wager.NewCommand(in)
	if !errors.Is(err, money.ErrUninitialized) {
		t.Errorf("error = %v, want money.ErrUninitialized in chain", err)
	}
}
