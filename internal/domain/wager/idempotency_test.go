package wager_test

import (
	"errors"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestCheckReplay(t *testing.T) {
	original := input(t)
	existing := processed(t, mustCommand(t, original), wager.Reference{})

	cases := []struct {
		name string
		edit func(*wager.CommandInput)
		want error // nil = Replay
	}{
		{"same key, same payload", func(*wager.CommandInput) {}, nil},
		{"same key, upper-case UUIDs", func(in *wager.CommandInput) { in.WalletID = "0192F291-27DD-7D3F-8071-5F8685DEEF37" }, nil},
		{"same key, other amount", func(in *wager.CommandInput) { in.Money = mustMoney(t, "26.00", "BRL") }, wager.ErrIdempotencyKeyConflict},
		{"same key, other external id", func(in *wager.CommandInput) { in.ExternalTransactionID = "transaction-999" }, wager.ErrIdempotencyKeyConflict},
		{"other key, same external id", func(in *wager.CommandInput) { in.IdempotencyKey = "retry-2" }, wager.ErrExternalTransactionConflict},
		{"other key, same external id and other payload", func(in *wager.CommandInput) {
			in.IdempotencyKey = "retry-2"
			in.Money = mustMoney(t, "1.00", "BRL")
		}, wager.ErrExternalTransactionConflict},
	}
	for _, c := range cases {
		in := original
		c.edit(&in)
		err := wager.CheckReplay(existing, mustCommand(t, in))
		if c.want == nil && err != nil {
			t.Errorf("%s: error = %v, want Replay", c.name, err)
		}
		if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: error = %v, want %v", c.name, err, c.want)
		}
	}
}

func TestCheckReplayOfRejectedAndPendingTransactions(t *testing.T) {
	cmd := op(t, wager.KindRefund, "refund-1", "25.00", "bet-1")
	pending := newTx(t, cmd)
	if err := pending.AwaitReference(t0); err != nil {
		t.Fatal(err)
	}
	if err := wager.CheckReplay(pending, cmd); err != nil {
		t.Errorf("pending replay: %v", err)
	}
	rejected := newTx(t, op(t, wager.KindBet, "bet-9", "25.00", ""))
	_, _ = rejected.Apply(nil, wager.Reference{}, t0)
	if err := wager.CheckReplay(rejected, op(t, wager.KindBet, "bet-9", "25.00", "")); err != nil {
		t.Errorf("rejected replay: %v", err)
	}
	if rejected.FailureCode() != wager.FailureWalletNotFound {
		t.Error("replay reads the persisted failure code")
	}
}

func TestCheckReplayRejectsLookupBugs(t *testing.T) {
	existing := processed(t, mustCommand(t, input(t)), wager.Reference{})
	otherProvider := input(t)
	otherProvider.ProviderID = "provider-b"
	unrelated := input(t)
	unrelated.IdempotencyKey = "k2"
	unrelated.ExternalTransactionID = "t2"
	opening, _, _ := wager.Open(freshWallet(t), mustMoney(t, "1.00", "BRL"), t0)

	checks := map[string]error{
		"other provider": wager.CheckReplay(existing, mustCommand(t, otherProvider)),
		"unrelated":      wager.CheckReplay(existing, mustCommand(t, unrelated)),
		"opening":        wager.CheckReplay(opening, mustCommand(t, input(t))),
		"nil":            wager.CheckReplay(nil, mustCommand(t, input(t))),
		"zero command":   wager.CheckReplay(existing, wager.Command{}),
	}
	for name, err := range checks {
		if !errors.Is(err, wager.ErrInvalidTransaction) {
			t.Errorf("%s: error = %v, want ErrInvalidTransaction", name, err)
		}
	}
}
