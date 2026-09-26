package money_test

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

func TestZeroValueRejectedByEveryOperation(t *testing.T) {
	var zero money.Money
	brl := mustParse(t, "1.00", "BRL")

	ops := []struct {
		name string
		call func() error
	}{
		{"zero.Sub", func() error { _, err := zero.Sub(brl); return err }},
		{"Sub(zero)", func() error { _, err := brl.Sub(zero); return err }},
		{"zero.Neg", func() error { _, err := zero.Neg(); return err }},
		{"zero.Compare", func() error { _, err := zero.Compare(brl); return err }},
		{"Compare(zero)", func() error { _, err := brl.Compare(zero); return err }},
		{"zero.Add(zero)", func() error { _, err := zero.Add(zero); return err }},
		{"zero.MarshalJSON", func() error { _, err := zero.MarshalJSON(); return err }},
	}
	for _, op := range ops {
		if err := op.call(); !errors.Is(err, money.ErrUninitialized) {
			t.Errorf("%s error = %v, want ErrUninitialized", op.name, err)
		}
	}
	if zero.Equal(zero) || zero.Equal(brl) || brl.Equal(zero) {
		t.Error("Equal involving zero value Money must be false")
	}
	if zero.IsZero() {
		t.Error("zero value Money must not report IsZero")
	}
	if zero.Currency().IsValid() {
		t.Error("zero value Money must carry an invalid Currency")
	}
}

func TestZeroValueCurrencyIsInvalid(t *testing.T) {
	var c money.Currency
	if c.IsValid() {
		t.Fatal("zero value Currency must be invalid")
	}
	if c.Code() != "" {
		t.Errorf("zero Currency Code = %q, want empty", c.Code())
	}
}

func TestParseCurrencyAcceptsKnownCodes(t *testing.T) {
	for _, code := range []string{"BRL", "USD", "EUR", "GBP", "JPY", "ARS", "MXN", "CHF"} {
		c, err := money.ParseCurrency(code)
		if err != nil {
			t.Errorf("ParseCurrency(%q): %v", code, err)
			continue
		}
		if !c.IsValid() || c.Code() != code || c.String() != code {
			t.Errorf("ParseCurrency(%q) = %q, valid=%v", code, c.Code(), c.IsValid())
		}
	}
}

func TestParseCurrencyRejectsUnknownCodes(t *testing.T) {
	for _, in := range []string{"", "brl", "Brl", "BR", "BRLL", "XYZ", "XXX", "BRL ", "B1L", "ÀBC"} {
		if _, err := money.ParseCurrency(in); !errors.Is(err, money.ErrInvalidCurrency) {
			t.Errorf("ParseCurrency(%q) error = %v, want ErrInvalidCurrency", in, err)
		}
	}
}

func TestFromMinorUnitsRoundTrip(t *testing.T) {
	brl, _ := money.ParseCurrency("BRL")
	cases := []struct {
		minor int64
		want  string
	}{
		{0, "0.00"},
		{5, "0.05"},
		{-5, "-0.05"},
		{-100, "-1.00"},
		{123456, "1234.56"},
	}
	for _, c := range cases {
		m, err := money.FromMinorUnits(c.minor, brl)
		if err != nil {
			t.Fatalf("FromMinorUnits(%d): %v", c.minor, err)
		}
		if m.Amount() != c.want || m.MinorUnits() != c.minor {
			t.Errorf("FromMinorUnits(%d) = %q/%d, want %q", c.minor, m.Amount(), m.MinorUnits(), c.want)
		}
		if m.IsNegative() != (c.minor < 0) {
			t.Errorf("FromMinorUnits(%d).IsNegative() = %v", c.minor, m.IsNegative())
		}
	}
}

func TestString(t *testing.T) {
	m := mustParse(t, "25.00", "BRL")
	if got := m.String(); got != "25.00 BRL" {
		t.Errorf("String = %q", got)
	}
	var zero money.Money
	if got := zero.String(); got == "0.00 BRL" || got == "" {
		t.Errorf("zero value String = %q, want a marker of invalidity", got)
	}
}

func TestJSONUnmarshalStrictness(t *testing.T) {
	invalid := []string{
		`{}`,
		`[]`,
		`"x"`,
		`25`,
		`{"amount":"25.00","currency":null}`,
		`{"amount":"25.00","currency":"brl"}`,
		`{"amount":"25.00","currency":"XYZ"}`,
		`{"amount":"25.0","currency":"BRL"}`,
		`{"amount":"-1.00","currency":"BRL"}`,
		`{"amount":"NaN","currency":"BRL"}`,
		`{"amount":true,"currency":"BRL"}`,
		`{"amount":["25.00"],"currency":"BRL"}`,
		`{"Amount":"25.00","currency":"BRL"}`,
		`{"amount":"25.00","Currency":"BRL"}`,
		`{"amount":"25.00","currency":"BRL","amount":"26.00"}`,
		`{"amount":"25.00","currency":"BRL"`,
	}
	for _, in := range invalid {
		m := mustParse(t, "7.00", "USD")
		if err := json.Unmarshal([]byte(in), &m); err == nil {
			t.Errorf("Unmarshal(%s) succeeded with %v", in, m)
			continue
		}
		if m.Amount() != "7.00" || m.Currency().Code() != "USD" {
			t.Errorf("failed Unmarshal(%s) mutated receiver to %v", in, m)
		}
	}
}

func TestJSONUnmarshalClassifiesErrors(t *testing.T) {
	cases := []struct {
		in   string
		want error
	}{
		{`{"amount":"25.0","currency":"BRL"}`, money.ErrInvalidAmount},
		{`{"amount":25.00,"currency":"BRL"}`, money.ErrInvalidAmount},
		{`{"amount":"25.00","currency":"XYZ"}`, money.ErrInvalidCurrency},
	}
	for _, c := range cases {
		var m money.Money
		if err := json.Unmarshal([]byte(c.in), &m); !errors.Is(err, c.want) {
			t.Errorf("Unmarshal(%s) error = %v, want %v", c.in, err, c.want)
		}
	}
}

func TestJSONNestedAndNegative(t *testing.T) {
	type envelope struct {
		Money money.Money `json:"money"`
	}
	neg := mustParse(t, "1.00", "EUR")
	neg, _ = neg.Neg()
	b, err := json.Marshal(envelope{Money: neg})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"money":{"amount":"-1.00","currency":"EUR"}}` {
		t.Fatalf("marshal = %s", b)
	}
	var back envelope
	// Negative amounts are outside the canonical input grammar.
	if err := json.Unmarshal(b, &back); !errors.Is(err, money.ErrInvalidAmount) {
		t.Fatalf("unmarshal negative error = %v, want ErrInvalidAmount", err)
	}
	if err := json.Unmarshal([]byte(`{"money":null}`), &back); err == nil {
		t.Fatal("null nested money must be rejected")
	}
}

// TestPackageHygiene enforces that the package uses no floating point types
// and imports only the standard library.
func TestPackageHygiene(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports non-stdlib package %q", name, path)
			}
		}
		full, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(full, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && (id.Name == "float32" || id.Name == "float64") {
				t.Errorf("%s uses %s at %s", name, id.Name, fset.Position(id.Pos()))
			}
			return true
		})
	}
}

// Error text reaches logs, which must never carry amounts or balances.
func TestOverflowErrorsCarryNoAmounts(t *testing.T) {
	brl, _ := money.ParseCurrency("BRL")
	max, _ := money.FromMinorUnits(math.MaxInt64, brl)
	min, _ := money.FromMinorUnits(math.MinInt64, brl)
	one, _ := money.FromMinorUnits(1, brl)
	_, addErr := max.Add(one)
	_, subErr := min.Sub(one)
	_, negErr := min.Neg()
	for _, err := range []error{addErr, subErr, negErr} {
		if err == nil || strings.ContainsAny(err.Error(), "0123456789") {
			t.Errorf("overflow error %q must exist and carry no amount", err)
		}
	}
}
