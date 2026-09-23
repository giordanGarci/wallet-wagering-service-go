package ledger_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("Parse(%q): %v", amount, err)
	}
	return m
}

func TestParseDirection(t *testing.T) {
	for _, in := range []string{"DEBIT", "CREDIT"} {
		d, err := ledger.ParseDirection(in)
		if err != nil || d.String() != in {
			t.Errorf("ParseDirection(%q) = %q, %v", in, d, err)
		}
	}
	for _, in := range []string{"", "debit", "Credit", "NONE"} {
		if _, err := ledger.ParseDirection(in); !errors.Is(err, ledger.ErrInvalidEntry) {
			t.Errorf("ParseDirection(%q) error = %v, want ErrInvalidEntry", in, err)
		}
	}
}

func TestNewBuildsBalancedEntries(t *testing.T) {
	walletID, txID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	now := time.Date(2026, 9, 8, 12, 0, 0, 123456789, time.FixedZone("BRT", -3*3600))
	cases := []struct {
		name          string
		dir           ledger.Direction
		amount        string
		before, after string
	}{
		{"credit", ledger.Credit, "25.00", "100.00", "125.00"},
		{"credit from zero", ledger.Credit, "0.01", "0.00", "0.01"},
		{"debit", ledger.Debit, "80.00", "100.00", "20.00"},
		{"debit to zero", ledger.Debit, "100.00", "100.00", "0.00"},
	}
	for _, c := range cases {
		e, err := ledger.New(walletID, txID, c.dir, brl(t, c.amount), brl(t, c.before), brl(t, c.after), now)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if e.ID() == uuid.Nil || e.ID().Version() != 7 {
			t.Errorf("%s: id %s is not a UUIDv7", c.name, e.ID())
		}
		if e.WalletID() != walletID || e.TransactionID() != txID || e.Direction() != c.dir {
			t.Errorf("%s: identity fields not preserved", c.name)
		}
		if e.Money().Amount() != c.amount || e.BalanceBefore().Amount() != c.before || e.BalanceAfter().Amount() != c.after {
			t.Errorf("%s: amounts not preserved", c.name)
		}
		if e.CreatedAt().Location() != time.UTC || !e.CreatedAt().Equal(now.Truncate(time.Microsecond)) {
			t.Errorf("%s: createdAt = %v, want UTC microsecond precision", c.name, e.CreatedAt())
		}
	}
}

func TestNewRejectsInconsistentEntries(t *testing.T) {
	walletID, txID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	usd, _ := money.Parse("100.00", "USD")
	var zero money.Money
	neg, _ := brl(t, "1.00").Neg()
	cases := []struct {
		name          string
		walletID      uuid.UUID
		txID          uuid.UUID
		dir           ledger.Direction
		amount        money.Money
		before, after money.Money
	}{
		{"nil wallet", uuid.Nil, txID, ledger.Credit, brl(t, "1.00"), brl(t, "0.00"), brl(t, "1.00")},
		{"nil transaction", walletID, uuid.Nil, ledger.Credit, brl(t, "1.00"), brl(t, "0.00"), brl(t, "1.00")},
		{"unknown direction", walletID, txID, ledger.Direction("SIDEWAYS"), brl(t, "1.00"), brl(t, "0.00"), brl(t, "1.00")},
		{"zero amount", walletID, txID, ledger.Credit, brl(t, "0.00"), brl(t, "1.00"), brl(t, "1.00")},
		{"negative amount", walletID, txID, ledger.Credit, neg, brl(t, "2.00"), brl(t, "1.00")},
		{"uninitialized amount", walletID, txID, ledger.Credit, zero, brl(t, "0.00"), brl(t, "1.00")},
		{"uninitialized before", walletID, txID, ledger.Credit, brl(t, "1.00"), zero, brl(t, "1.00")},
		{"credit does not add up", walletID, txID, ledger.Credit, brl(t, "1.00"), brl(t, "0.00"), brl(t, "2.00")},
		{"debit does not add up", walletID, txID, ledger.Debit, brl(t, "1.00"), brl(t, "5.00"), brl(t, "3.00")},
		{"debit booked as credit", walletID, txID, ledger.Credit, brl(t, "1.00"), brl(t, "5.00"), brl(t, "4.00")},
		{"negative balance after", walletID, txID, ledger.Debit, brl(t, "2.00"), brl(t, "1.00"), neg},
		{"currency mismatch", walletID, txID, ledger.Credit, usd, brl(t, "0.00"), brl(t, "100.00")},
	}
	for _, c := range cases {
		if _, err := ledger.New(c.walletID, c.txID, c.dir, c.amount, c.before, c.after, time.Now()); !errors.Is(err, ledger.ErrInvalidEntry) {
			t.Errorf("%s: error = %v, want ErrInvalidEntry", c.name, err)
		}
	}
}

func TestRehydrateRestoresAndValidates(t *testing.T) {
	id, walletID, txID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	created := time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC)
	e, err := ledger.Rehydrate(id, walletID, txID, ledger.Debit, brl(t, "10.00"), brl(t, "30.00"), brl(t, "20.00"), created)
	if err != nil {
		t.Fatal(err)
	}
	if e.ID() != id || !e.CreatedAt().Equal(created) || e.Direction() != ledger.Debit {
		t.Fatal("rehydrate did not restore state")
	}
	if _, err := ledger.Rehydrate(uuid.Nil, walletID, txID, ledger.Debit, brl(t, "10.00"), brl(t, "30.00"), brl(t, "20.00"), created); !errors.Is(err, ledger.ErrInvalidEntry) {
		t.Errorf("nil id error = %v", err)
	}
	if _, err := ledger.Rehydrate(id, walletID, txID, ledger.Debit, brl(t, "10.00"), brl(t, "30.00"), brl(t, "25.00"), created); !errors.Is(err, ledger.ErrInvalidEntry) {
		t.Errorf("unbalanced error = %v", err)
	}
}

func TestSignedAmount(t *testing.T) {
	walletID, txID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	credit, _ := ledger.New(walletID, txID, ledger.Credit, brl(t, "5.00"), brl(t, "0.00"), brl(t, "5.00"), time.Now())
	debit, _ := ledger.New(walletID, txID, ledger.Debit, brl(t, "5.00"), brl(t, "5.00"), brl(t, "0.00"), time.Now())
	if got := credit.SignedAmount().Amount(); got != "5.00" {
		t.Errorf("credit signed = %s", got)
	}
	if got := debit.SignedAmount().Amount(); got != "-5.00" {
		t.Errorf("debit signed = %s", got)
	}
}
