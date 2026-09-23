package money

import "strings"

// Currency is an ISO 4217 alphabetic currency code validated against a known
// list of active codes. The zero value is invalid.
type Currency struct {
	code string
}

// ParseCurrency validates code against the known ISO 4217 list. Codes are
// case-sensitive: only the canonical upper-case form is accepted.
func ParseCurrency(code string) (Currency, error) {
	if _, ok := knownCurrencies[code]; !ok {
		return Currency{}, wrap(ErrInvalidCurrency, "unknown currency code %q", code)
	}
	return Currency{code: code}, nil
}

// Code returns the three-letter ISO 4217 code, or "" for the zero value.
func (c Currency) Code() string { return c.code }

// IsValid reports whether c was built by ParseCurrency.
func (c Currency) IsValid() bool { return c.code != "" }

// String implements fmt.Stringer.
func (c Currency) String() string {
	if !c.IsValid() {
		return "<uninitialized currency>"
	}
	return c.code
}

// activeISO4217 lists active ISO 4217 codes (circulating currencies plus the
// supranational CFA/ECCB/CFP francs). Every currency is handled with the
// package's fixed scale of two decimal places.
const activeISO4217 = `
AED AFN ALL AMD AOA ARS AUD AWG AZN BAM BBD BDT BHD BIF BMD BND BOB BRL BSD BTN
BWP BYN BZD CAD CDF CHF CLP CNY COP CRC CUP CVE CZK DJF DKK DOP DZD EGP ERN ETB
EUR FJD FKP GBP GEL GHS GIP GMD GNF GTQ GYD HKD HNL HTG HUF IDR ILS INR IQD IRR
ISK JMD JOD JPY KES KGS KHR KMF KPW KRW KWD KYD KZT LAK LBP LKR LRD LSL LYD MAD
MDL MGA MKD MMK MNT MOP MRU MUR MVR MWK MXN MYR MZN NAD NGN NIO NOK NPR NZD OMR
PAB PEN PGK PHP PKR PLN PYG QAR RON RSD RUB RWF SAR SBD SCR SDG SEK SGD SHP SLE
SOS SRD SSP STN SVC SYP SZL THB TJS TMT TND TOP TRY TTD TWD TZS UAH UGX USD UYU
UZS VED VES VND VUV WST XAF XCD XCG XOF XPF YER ZAR ZMW ZWG
`

var knownCurrencies = func() map[string]struct{} {
	codes := strings.Fields(activeISO4217)
	m := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		m[c] = struct{}{}
	}
	return m
}()
