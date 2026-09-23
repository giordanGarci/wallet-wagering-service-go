package wallet_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

func mustMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatalf("Parse(%q, %q): %v", amount, currency, err)
	}
	return m
}

func walletWith(t *testing.T, balance string, version int64) *wallet.Wallet {
	t.Helper()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	w, err := wallet.Rehydrate(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), mustMoney(t, balance, "BRL"), version, created, created)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestDebitAndCreditMoveBalanceAndReturnEntry(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		start  string
		op     func(*wallet.Wallet, uuid.UUID, money.Money, time.Time) (ledger.Entry, error)
		amount string
		dir    ledger.Direction
		after  string
	}{
		{"debit", "100.00", (*wallet.Wallet).Debit, "80.00", ledger.Debit, "20.00"},
		{"debit whole balance", "100.00", (*wallet.Wallet).Debit, "100.00", ledger.Debit, "0.00"},
		{"credit", "20.00", (*wallet.Wallet).Credit, "5.50", ledger.Credit, "25.50"},
		{"credit empty wallet", "0.00", (*wallet.Wallet).Credit, "0.01", ledger.Credit, "0.01"},
	}
	for _, c := range cases {
		w := walletWith(t, c.start, 3)
		txID := uuid.Must(uuid.NewV7())
		e, err := c.op(w, txID, mustMoney(t, c.amount, "BRL"), now)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if w.Balance().Amount() != c.after || w.Version() != 4 || !w.UpdatedAt().Equal(now) {
			t.Errorf("%s: wallet = %v v%d updated %v", c.name, w.Balance(), w.Version(), w.UpdatedAt())
		}
		if e.WalletID() != w.ID() || e.TransactionID() != txID || e.Direction() != c.dir ||
			e.BalanceBefore().Amount() != c.start || e.BalanceAfter().Amount() != c.after || e.Money().Amount() != c.amount {
			t.Errorf("%s: entry does not describe the movement", c.name)
		}
	}
}

func TestMovementRejectionsLeaveWalletUntouched(t *testing.T) {
	var zero money.Money
	cases := []struct {
		name   string
		op     func(*wallet.Wallet, uuid.UUID, money.Money, time.Time) (ledger.Entry, error)
		txID   uuid.UUID
		amount money.Money
		want   error
	}{
		{"debit beyond balance", (*wallet.Wallet).Debit, uuid.Must(uuid.NewV7()), mustMoney(t, "100.01", "BRL"), wallet.ErrInsufficientFunds},
		{"debit currency mismatch", (*wallet.Wallet).Debit, uuid.Must(uuid.NewV7()), mustMoney(t, "1.00", "USD"), wallet.ErrCurrencyMismatch},
		{"credit currency mismatch", (*wallet.Wallet).Credit, uuid.Must(uuid.NewV7()), mustMoney(t, "1.00", "USD"), wallet.ErrCurrencyMismatch},
		{"credit zero", (*wallet.Wallet).Credit, uuid.Must(uuid.NewV7()), mustMoney(t, "0.00", "BRL"), wallet.ErrInvalidMovement},
		{"debit zero", (*wallet.Wallet).Debit, uuid.Must(uuid.NewV7()), mustMoney(t, "0.00", "BRL"), wallet.ErrInvalidMovement},
		{"credit uninitialized", (*wallet.Wallet).Credit, uuid.Must(uuid.NewV7()), zero, wallet.ErrInvalidMovement},
		{"credit without transaction", (*wallet.Wallet).Credit, uuid.Nil, mustMoney(t, "1.00", "BRL"), wallet.ErrInvalidMovement},
	}
	for _, c := range cases {
		w := walletWith(t, "100.00", 2)
		if _, err := c.op(w, c.txID, c.amount, time.Now()); !errors.Is(err, c.want) {
			t.Errorf("%s: error = %v, want %v", c.name, err, c.want)
		}
		if w.Balance().Amount() != "100.00" || w.Version() != 2 {
			t.Errorf("%s: wallet mutated to %v v%d", c.name, w.Balance(), w.Version())
		}
	}
}

func TestCurrencyMismatchIsAlsoMoneyMismatch(t *testing.T) {
	w := walletWith(t, "1.00", 1)
	_, err := w.Credit(uuid.Must(uuid.NewV7()), mustMoney(t, "1.00", "EUR"), time.Now())
	if !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("error = %v, want money.ErrCurrencyMismatch in chain", err)
	}
}

func TestCreditOverflowIsRejected(t *testing.T) {
	brlCur, _ := money.ParseCurrency("BRL")
	huge, _ := money.FromMinorUnits(1<<62, brlCur)
	w, _ := wallet.Rehydrate(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), huge, 1, time.Now(), time.Now())
	if _, err := w.Credit(uuid.Must(uuid.NewV7()), huge, time.Now()); !errors.Is(err, money.ErrOverflow) {
		t.Errorf("error = %v, want ErrOverflow", err)
	}
	if w.Version() != 1 {
		t.Error("failed credit must not bump version")
	}
}

func TestSequentialDebitsNeverGoNegative(t *testing.T) {
	w := walletWith(t, "100.00", 1)
	bet := mustMoney(t, "80.00", "BRL")
	if _, err := w.Debit(uuid.Must(uuid.NewV7()), bet, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Debit(uuid.Must(uuid.NewV7()), bet, time.Now()); !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("second debit error = %v", err)
	}
	if w.Balance().Amount() != "20.00" || w.Version() != 2 {
		t.Fatalf("wallet = %v v%d", w.Balance(), w.Version())
	}
}

func TestApplyOpeningCreditsWithoutBumpingVersion(t *testing.T) {
	brlCur, _ := money.ParseCurrency("BRL")
	now := time.Now()
	w, err := wallet.New(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), brlCur, now)
	if err != nil {
		t.Fatal(err)
	}
	txID := uuid.Must(uuid.NewV7())
	e, err := w.ApplyOpening(txID, mustMoney(t, "1000.00", "BRL"), now)
	if err != nil {
		t.Fatal(err)
	}
	if w.Balance().Amount() != "1000.00" || w.Version() != wallet.InitialVersion {
		t.Fatalf("wallet = %v v%d, want 1000.00 v1", w.Balance(), w.Version())
	}
	if e.Direction() != ledger.Credit || e.BalanceBefore().Amount() != "0.00" || e.TransactionID() != txID {
		t.Fatal("opening entry is not the initial credit")
	}
	if _, err := w.ApplyOpening(uuid.Must(uuid.NewV7()), mustMoney(t, "1.00", "BRL"), now); !errors.Is(err, wallet.ErrInvalidMovement) {
		t.Errorf("second opening error = %v, want ErrInvalidMovement", err)
	}
	moved := walletWith(t, "0.00", 2)
	if _, err := moved.ApplyOpening(uuid.Must(uuid.NewV7()), mustMoney(t, "1.00", "BRL"), now); !errors.Is(err, wallet.ErrInvalidMovement) {
		t.Errorf("opening on a wallet past version 1 error = %v", err)
	}
}
