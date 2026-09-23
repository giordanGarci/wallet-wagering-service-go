package money

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	fieldAmount   = "amount"
	fieldCurrency = "currency"
)

// MarshalJSON encodes m as {"amount":"25.00","currency":"BRL"}. The zero
// value fails with ErrUninitialized.
func (m Money) MarshalJSON() ([]byte, error) {
	if !m.IsValid() {
		return nil, wrap(ErrUninitialized, "cannot marshal uninitialized money")
	}
	// Amount and code are plain ASCII digits/letters, no escaping needed.
	return []byte(`{"` + fieldAmount + `":"` + m.Amount() + `","` + fieldCurrency + `":"` + m.currency.code + `"}`), nil
}

// UnmarshalJSON strictly decodes {"amount":"25.00","currency":"BRL"}: exactly
// those two keys (case-sensitive, no duplicates), both JSON strings, amount
// in canonical form. JSON numbers, null, missing and extra fields are
// rejected. On error m is left unchanged.
func (m *Money) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("money: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New(`money: expected JSON object {"amount":"...","currency":"..."}`)
	}

	var amount, currency string
	var haveAmount, haveCurrency bool
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("money: %w", err)
		}
		key, _ := keyTok.(string)
		valTok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("money: %w", err)
		}
		val, isString := valTok.(string)

		switch key {
		case fieldAmount:
			if haveAmount {
				return wrap(ErrInvalidAmount, "duplicate field %q", key)
			}
			if !isString {
				return wrap(ErrInvalidAmount, "amount must be a JSON string, got %v", valTok)
			}
			amount, haveAmount = val, true
		case fieldCurrency:
			if haveCurrency {
				return wrap(ErrInvalidCurrency, "duplicate field %q", key)
			}
			if !isString {
				return wrap(ErrInvalidCurrency, "currency must be a JSON string, got %v", valTok)
			}
			currency, haveCurrency = val, true
		default:
			return fmt.Errorf("money: unknown field %q", key)
		}
	}
	if !haveAmount {
		return wrap(ErrInvalidAmount, "missing field %q", fieldAmount)
	}
	if !haveCurrency {
		return wrap(ErrInvalidCurrency, "missing field %q", fieldCurrency)
	}

	parsed, err := Parse(amount, currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}
