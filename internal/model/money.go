package model

import (
	"fmt"
	"strconv"
	"strings"
)

// Money in Quorum is stored and transported as an integer number of the
// currency's *minor units* (e.g. cents for USD, yen for JPY). This keeps all
// arithmetic exact — no binary-float rounding error — at the cost of needing
// the currency's exponent to convert to/from a human-readable major-unit value.

// zeroDecimalCurrencies have no minor unit (exponent 0): the amount is already
// in whole units. Source: ISO 4217.
var zeroDecimalCurrencies = map[string]bool{
	"BIF": true, "CLP": true, "DJF": true, "GNF": true, "JPY": true, "KMF": true,
	"KRW": true, "MGA": true, "PYG": true, "RWF": true, "UGX": true, "VND": true,
	"VUV": true, "XAF": true, "XOF": true, "XPF": true,
}

// threeDecimalCurrencies use 1000 minor units per major unit (exponent 3).
var threeDecimalCurrencies = map[string]bool{
	"BHD": true, "IQD": true, "JOD": true, "KWD": true, "LYD": true, "OMR": true, "TND": true,
}

// CurrencyExponent returns the number of decimal places for a currency: 0 for
// zero-decimal currencies, 3 for the few three-decimal ones, and 2 otherwise
// (the common case, and a safe default for unknown codes).
func CurrencyExponent(code string) int {
	switch code = strings.ToUpper(strings.TrimSpace(code)); {
	case zeroDecimalCurrencies[code]:
		return 0
	case threeDecimalCurrencies[code]:
		return 3
	default:
		return 2
	}
}

func pow10(n int) int64 {
	p := int64(1)
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}

// FormatMoney renders a minor-unit amount as a plain major-unit decimal string
// (e.g. 10000, "USD" -> "100.00"; 1000, "JPY" -> "1000"). No currency symbol.
func FormatMoney(minor int64, code string) string {
	exp := CurrencyExponent(code)
	if exp == 0 {
		return strconv.FormatInt(minor, 10)
	}
	neg := minor < 0
	if neg {
		minor = -minor
	}
	div := pow10(exp)
	whole := minor / div
	frac := minor % div
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%0*d", sign, whole, exp, frac)
}

// ParseMoney converts a major-unit decimal string (e.g. "100.00", "100", "99.9")
// to minor units for the given currency. It parses digit-by-digit rather than
// through a float, so no precision is lost. It rejects a fractional part longer
// than the currency's exponent (more precision than the currency supports).
func ParseMoney(s, code string) (int64, error) {
	exp := CurrencyExponent(code)
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty amount")
	}

	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}

	intPart, fracPart, hasDot := strings.Cut(s, ".")
	if intPart == "" && fracPart == "" {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	if intPart == "" {
		intPart = "0"
	}
	if !hasDot {
		fracPart = ""
	}
	if len(fracPart) > exp {
		return 0, fmt.Errorf("amount %q has more than %d decimal place(s)", s, exp)
	}
	// Right-pad the fraction to exactly exp digits.
	fracPart += strings.Repeat("0", exp-len(fracPart))

	digits := intPart + fracPart
	v, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q: %w", s, err)
	}
	if neg {
		v = -v
	}
	return v, nil
}
