package wallet_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

func TestNewOpensWithZeroBalanceAtVersionOne(t *testing.T) {
	brl, _ := money.ParseCurrency("BRL")
	w, err := wallet.New(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), brl, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !w.Balance().IsZero() || w.Version() != wallet.InitialVersion || w.Currency() != brl {
		t.Fatalf("got balance %v version %d", w.Balance(), w.Version())
	}
}

func TestNewRejectsMissingData(t *testing.T) {
	brl, _ := money.ParseCurrency("BRL")
	var noCurrency money.Currency
	cases := []struct {
		name     string
		id, pid  uuid.UUID
		currency money.Currency
	}{
		{"nil id", uuid.Nil, uuid.Must(uuid.NewV7()), brl},
		{"nil player", uuid.Must(uuid.NewV7()), uuid.Nil, brl},
		{"no currency", uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), noCurrency},
	}
	for _, c := range cases {
		if _, err := wallet.New(c.id, c.pid, c.currency, time.Now()); !errors.Is(err, wallet.ErrInvalidWallet) {
			t.Errorf("%s: error = %v", c.name, err)
		}
	}
}

func TestRehydrateRestoresState(t *testing.T) {
	balance, _ := money.Parse("12.34", "BRL")
	id, pid := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	w, err := wallet.Rehydrate(id, pid, balance, 7, time.Now(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if w.ID() != id || w.PlayerID() != pid || !w.Balance().Equal(balance) || w.Version() != 7 {
		t.Fatalf("unexpected state")
	}
	if _, err := wallet.Rehydrate(id, pid, balance, 0, time.Now(), time.Now()); !errors.Is(err, wallet.ErrInvalidWallet) {
		t.Errorf("version 0 error = %v", err)
	}
}
