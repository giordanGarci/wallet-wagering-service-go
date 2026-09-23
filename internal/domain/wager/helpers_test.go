package wager_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

var (
	t0       = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	playerID = uuid.MustParse("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1")
	walletID = uuid.MustParse("0192f291-27dd-7d3f-8071-5f8685deef37")
)

func mustMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatalf("Parse(%q, %q): %v", amount, currency, err)
	}
	return m
}

// input returns the challenge's BET example as a CommandInput.
func input(t *testing.T) wager.CommandInput {
	t.Helper()
	return wager.CommandInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "provider-a:transaction-123",
		PlayerID:              playerID.String(),
		WalletID:              walletID.String(),
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Money:                 mustMoney(t, "25.00", "BRL"),
	}
}

// op builds a valid Command of the given kind; amount and ref may be "".
func op(t *testing.T, kind wager.Kind, extID, amount, ref string) wager.Command {
	t.Helper()
	in := input(t)
	in.Kind = string(kind)
	in.ExternalTransactionID = extID
	in.IdempotencyKey = "provider-a:" + extID
	if amount != "" {
		in.Money = mustMoney(t, amount, "BRL")
	}
	if kind == wager.KindLoss {
		in.Money = mustMoney(t, "0.00", "BRL")
	}
	in.ReferenceExternalTransactionID = ref
	cmd, err := wager.NewCommand(in)
	if err != nil {
		t.Fatalf("NewCommand(%s %s): %v", kind, extID, err)
	}
	return cmd
}

func newTx(t *testing.T, cmd wager.Command) *wager.Transaction {
	t.Helper()
	tx, err := wager.NewExternal(cmd, t0)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func openWallet(t *testing.T, balance string) *wallet.Wallet {
	t.Helper()
	w, err := wallet.Rehydrate(walletID, playerID, mustMoney(t, balance, "BRL"), 5, t0, t0)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// processed runs cmd to PROCESSED against a roomy wallet and returns it.
func processed(t *testing.T, cmd wager.Command, ref wager.Reference) *wager.Transaction {
	t.Helper()
	tx := newTx(t, cmd)
	if _, err := tx.Apply(openWallet(t, "1000.00"), ref, t0); err != nil {
		t.Fatal(err)
	}
	if tx.Status() != wager.StatusProcessed {
		t.Fatalf("%s %s status = %s (%s), want PROCESSED", tx.Kind(), tx.ExternalTransactionID(), tx.Status(), tx.FailureCode())
	}
	return tx
}

// withStatus rehydrates a copy of tx with another status.
func withStatus(t *testing.T, tx *wager.Transaction, st wager.Status, code wager.FailureCode) *wager.Transaction {
	t.Helper()
	s := tx.Snapshot()
	s.Status = st
	s.FailureCode = code
	s.CompensatedBetID = uuid.Nil
	if st != wager.StatusProcessed {
		s.BalanceAfter = money.Money{}
	}
	out, err := wager.Rehydrate(s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
