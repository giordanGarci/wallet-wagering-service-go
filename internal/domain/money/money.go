// Package money provides an exact monetary value object: int64 minor units
// with a fixed scale of two decimal places and an ISO 4217 Currency.
package money

import (
	"math"
	"strconv"
	"strings"
)

const scale = 100 // minor units per major unit (two decimal places)

// Money is an immutable amount in minor units of a Currency. The zero value
// is invalid: every operation on it fails with ErrUninitialized.
type Money struct {
	minor    int64
	currency Currency
}

// Parse builds Money from a canonical decimal string matching
// ^(0|[1-9][0-9]*)\.[0-9]{2}$ (ASCII digits only, no sign, no normalization)
// and an ISO 4217 code.
func Parse(amount, currency string) (Money, error) {
	c, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	minor, err := parseAmount(amount)
	if err != nil {
		return Money{}, err
	}
	return Money{minor: minor, currency: c}, nil
}

// FromMinorUnits builds Money from a raw minor-unit count.
func FromMinorUnits(minor int64, c Currency) (Money, error) {
	if !c.IsValid() {
		return Money{}, wrap(ErrUninitialized, "currency is uninitialized")
	}
	return Money{minor: minor, currency: c}, nil
}

// Zero returns 0.00 in currency c.
func Zero(c Currency) (Money, error) { return FromMinorUnits(0, c) }

func parseAmount(s string) (int64, error) {
	intPart, frac, ok := strings.Cut(s, ".")
	if !ok || len(frac) != 2 || !allDigits(frac) || intPart == "" || !allDigits(intPart) ||
		(len(intPart) > 1 && intPart[0] == '0') {
		return 0, wrap(ErrInvalidAmount, "%q is not canonical (want e.g. 25.00)", s)
	}
	n, err := strconv.ParseInt(intPart+frac, 10, 64)
	if err != nil {
		return 0, wrap(ErrInvalidAmount, "%q is out of range", s)
	}
	return n, nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// IsValid reports whether m was built through a constructor.
func (m Money) IsValid() bool { return m.currency.IsValid() }

// MinorUnits returns the amount in minor units (cents).
func (m Money) MinorUnits() int64 { return m.minor }

// Currency returns the currency of m.
func (m Money) Currency() Currency { return m.currency }

// IsZero reports whether m is a valid 0.00.
func (m Money) IsZero() bool { return m.IsValid() && m.minor == 0 }

// IsNegative reports whether m is a valid amount below zero.
func (m Money) IsNegative() bool { return m.IsValid() && m.minor < 0 }

// Amount formats m as a fixed two-decimal string, e.g. "25.00" or "-0.01".
// It returns "" for the zero value.
func (m Money) Amount() string {
	if !m.IsValid() {
		return ""
	}
	var abs uint64
	sign := ""
	if m.minor < 0 {
		sign = "-"
		abs = uint64(-(m.minor + 1)) + 1 // safe for math.MinInt64
	} else {
		abs = uint64(m.minor)
	}
	frac := abs % scale
	fracStr := strconv.FormatUint(frac, 10)
	if frac < 10 {
		fracStr = "0" + fracStr
	}
	return sign + strconv.FormatUint(abs/scale, 10) + "." + fracStr
}

// String implements fmt.Stringer, e.g. "25.00 BRL".
func (m Money) String() string {
	if !m.IsValid() {
		return "<uninitialized money>"
	}
	return m.Amount() + " " + m.currency.code
}

func (m Money) checkPair(o Money) error {
	if !m.IsValid() || !o.IsValid() {
		return wrap(ErrUninitialized, "operand is uninitialized")
	}
	if m.currency != o.currency {
		return wrap(ErrCurrencyMismatch, "%s vs %s", m.currency.code, o.currency.code)
	}
	return nil
}

// Add returns m + o.
func (m Money) Add(o Money) (Money, error) {
	if err := m.checkPair(o); err != nil {
		return Money{}, err
	}
	if (o.minor > 0 && m.minor > math.MaxInt64-o.minor) ||
		(o.minor < 0 && m.minor < math.MinInt64-o.minor) {
		return Money{}, wrap(ErrOverflow, "addition out of range")
	}
	return Money{minor: m.minor + o.minor, currency: m.currency}, nil
}

// Sub returns m - o.
func (m Money) Sub(o Money) (Money, error) {
	if err := m.checkPair(o); err != nil {
		return Money{}, err
	}
	if (o.minor < 0 && m.minor > math.MaxInt64+o.minor) ||
		(o.minor > 0 && m.minor < math.MinInt64+o.minor) {
		return Money{}, wrap(ErrOverflow, "subtraction out of range")
	}
	return Money{minor: m.minor - o.minor, currency: m.currency}, nil
}

// Neg returns -m.
func (m Money) Neg() (Money, error) {
	if !m.IsValid() {
		return Money{}, wrap(ErrUninitialized, "operand is uninitialized")
	}
	if m.minor == math.MinInt64 {
		return Money{}, wrap(ErrOverflow, "negation out of range")
	}
	return Money{minor: -m.minor, currency: m.currency}, nil
}

// Compare returns -1, 0 or 1 when m is less than, equal to or greater than o.
func (m Money) Compare(o Money) (int, error) {
	if err := m.checkPair(o); err != nil {
		return 0, err
	}
	switch {
	case m.minor < o.minor:
		return -1, nil
	case m.minor > o.minor:
		return 1, nil
	default:
		return 0, nil
	}
}

// Equal reports whether m and o are valid and have the same amount and
// currency.
func (m Money) Equal(o Money) bool {
	return m.IsValid() && o.IsValid() && m == o
}
