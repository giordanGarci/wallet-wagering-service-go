package money_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

func mustParse(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatalf("Parse(%q, %q): %v", amount, currency, err)
	}
	return m
}

func TestParseAcceptsCanonicalAmounts(t *testing.T) {
	cases := []struct {
		in    string
		minor int64
	}{
		{"0.00", 0},
		{"0.01", 1},
		{"25.00", 2500},
		{"1000.00", 100000},
		{"92233720368547758.07", math.MaxInt64},
	}
	for _, c := range cases {
		m := mustParse(t, c.in, "BRL")
		if m.MinorUnits() != c.minor {
			t.Errorf("Parse(%q).MinorUnits() = %d, want %d", c.in, m.MinorUnits(), c.minor)
		}
		if m.Amount() != c.in {
			t.Errorf("Parse(%q).Amount() = %q, want round trip", c.in, m.Amount())
		}
		if m.Currency().Code() != "BRL" {
			t.Errorf("currency = %q", m.Currency().Code())
		}
	}
}

func TestParseRejectsInvalidAmounts(t *testing.T) {
	invalid := []string{
		"", " ", "25", "25.0", "25.000", "25.001", ".50", "25.", "-1.00", "+1.00",
		"025.00", "00.00", "1e3", "1E3", "2.5e1", "NaN", "nan", "Infinity", "-Infinity", "Inf",
		"1,00", "1 000.00", " 1.00", "1.00 ", "0x10.00", "١.٠٠",
		"92233720368547758.08", "99999999999999999999.00",
	}
	for _, in := range invalid {
		if _, err := money.Parse(in, "BRL"); !errors.Is(err, money.ErrInvalidAmount) {
			t.Errorf("Parse(%q) error = %v, want ErrInvalidAmount", in, err)
		}
	}
}

func TestParseRejectsInvalidCurrencies(t *testing.T) {
	for _, in := range []string{"", "brl", "BR", "BRLL", "XYZ", " BRL"} {
		if _, err := money.Parse("1.00", in); !errors.Is(err, money.ErrInvalidCurrency) {
			t.Errorf("Parse currency %q error = %v, want ErrInvalidCurrency", in, err)
		}
	}
}

func TestZeroValueIsRejected(t *testing.T) {
	var zero money.Money
	if zero.IsValid() {
		t.Fatal("zero value Money must be invalid")
	}
	brl := mustParse(t, "1.00", "BRL")
	if _, err := brl.Add(zero); !errors.Is(err, money.ErrUninitialized) {
		t.Errorf("Add(zero) error = %v, want ErrUninitialized", err)
	}
	if _, err := zero.Add(brl); !errors.Is(err, money.ErrUninitialized) {
		t.Errorf("zero.Add error = %v, want ErrUninitialized", err)
	}
	if _, err := json.Marshal(zero); err == nil {
		t.Error("marshal of zero value Money must fail")
	}
	var c money.Currency
	if _, err := money.Zero(c); !errors.Is(err, money.ErrUninitialized) {
		t.Errorf("Zero(uninitialized currency) error = %v", err)
	}
}

func TestArithmetic(t *testing.T) {
	a := mustParse(t, "100.00", "BRL")
	b := mustParse(t, "80.00", "BRL")

	sum, err := a.Add(b)
	if err != nil || sum.Amount() != "180.00" {
		t.Fatalf("Add = %v, %v", sum, err)
	}
	diff, err := b.Sub(a)
	if err != nil || diff.Amount() != "-20.00" || !diff.IsNegative() {
		t.Fatalf("Sub = %v, %v", diff, err)
	}
	neg, err := a.Neg()
	if err != nil || neg.Amount() != "-100.00" {
		t.Fatalf("Neg = %v, %v", neg, err)
	}
	if a.Amount() != "100.00" {
		t.Fatal("operations must not mutate the receiver")
	}
	cmp, err := a.Compare(b)
	if err != nil || cmp != 1 {
		t.Fatalf("Compare = %d, %v", cmp, err)
	}
	cmp, _ = b.Compare(a)
	if cmp != -1 {
		t.Fatalf("Compare reversed = %d", cmp)
	}
	cmp, _ = a.Compare(a)
	if cmp != 0 {
		t.Fatalf("Compare self = %d", cmp)
	}
	zero, err := money.Zero(a.Currency())
	if err != nil || !zero.IsZero() || zero.Amount() != "0.00" {
		t.Fatalf("Zero = %v, %v", zero, err)
	}
	small, _ := diff.Add(mustParse(t, "19.99", "BRL"))
	if small.Amount() != "-0.01" {
		t.Fatalf("negative sub-unit formatting = %q", small.Amount())
	}
}

func TestCurrencyMismatch(t *testing.T) {
	brl := mustParse(t, "1.00", "BRL")
	usd := mustParse(t, "1.00", "USD")
	if _, err := brl.Add(usd); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("Add error = %v", err)
	}
	if _, err := brl.Sub(usd); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("Sub error = %v", err)
	}
	if _, err := brl.Compare(usd); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("Compare error = %v", err)
	}
	if brl.Equal(usd) {
		t.Error("different currencies must not be equal")
	}
}

func TestOverflow(t *testing.T) {
	brl, _ := money.ParseCurrency("BRL")
	max, _ := money.FromMinorUnits(math.MaxInt64, brl)
	min, _ := money.FromMinorUnits(math.MinInt64, brl)
	one, _ := money.FromMinorUnits(1, brl)

	if _, err := max.Add(one); !errors.Is(err, money.ErrOverflow) {
		t.Errorf("max+1 error = %v", err)
	}
	if _, err := min.Sub(one); !errors.Is(err, money.ErrOverflow) {
		t.Errorf("min-1 error = %v", err)
	}
	if _, err := min.Neg(); !errors.Is(err, money.ErrOverflow) {
		t.Errorf("-min error = %v", err)
	}
	negOne, _ := one.Neg()
	if _, err := max.Sub(negOne); !errors.Is(err, money.ErrOverflow) {
		t.Errorf("max-(-1) error = %v", err)
	}
	if got := min.Amount(); got != "-92233720368547758.08" {
		t.Errorf("min Amount = %q", got)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	m := mustParse(t, "25.00", "BRL")
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"amount":"25.00","currency":"BRL"}` {
		t.Fatalf("marshal = %s", b)
	}
	var back money.Money
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Equal(m) {
		t.Fatalf("round trip = %v", back)
	}
}

func TestJSONRejectsNumbersAndInvalidShapes(t *testing.T) {
	invalid := []string{
		`{"amount":25.00,"currency":"BRL"}`,
		`{"amount":"25.00"}`,
		`{"currency":"BRL"}`,
		`{"amount":"2.5e1","currency":"BRL"}`,
		`{"amount":null,"currency":"BRL"}`,
		`{"amount":"25.00","currency":"BRL","extra":1}`,
		`"25.00"`,
		`null`,
	}
	for _, in := range invalid {
		var m money.Money
		if err := json.Unmarshal([]byte(in), &m); err == nil {
			t.Errorf("Unmarshal(%s) succeeded with %v", in, m)
		}
	}
}

func TestFromMinorUnitsRequiresCurrency(t *testing.T) {
	var c money.Currency
	if _, err := money.FromMinorUnits(1, c); !errors.Is(err, money.ErrUninitialized) {
		t.Errorf("error = %v", err)
	}
}
