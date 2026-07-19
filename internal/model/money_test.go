package model

import "testing"

func TestCurrencyExponent(t *testing.T) {
	cases := []struct {
		code string
		want int
	}{
		{"USD", 2},
		{"usd", 2},
		{"JPY", 0},
		{"krw", 0},
		{"BHD", 3},
		{"KWD", 3},
		{"ZZZ", 2},   // unknown → default 2
		{"", 2},      // empty → default 2
		{" eur ", 2}, // trimmed + upper → EUR → 2
	}
	for _, tc := range cases {
		if got := CurrencyExponent(tc.code); got != tc.want {
			t.Errorf("CurrencyExponent(%q) = %d, want %d", tc.code, got, tc.want)
		}
	}
}

func TestParseMoney(t *testing.T) {
	cases := []struct {
		s    string
		code string
		want int64
	}{
		{"100.00", "USD", 10000},
		{"100", "USD", 10000},
		{"99.9", "USD", 9990},
		{"0.01", "USD", 1},
		{"1000", "JPY", 1000},
		{"10.00", "BHD", 10000},
		{"1.234", "BHD", 1234},
		{"-5.00", "USD", -500},
		{"+5.00", "USD", 500},
		{"0.00", "USD", 0},
		{".5", "USD", 50},
	}
	for _, tc := range cases {
		got, err := ParseMoney(tc.s, tc.code)
		if err != nil {
			t.Errorf("ParseMoney(%q, %q) unexpected error: %v", tc.s, tc.code, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseMoney(%q, %q) = %d, want %d", tc.s, tc.code, got, tc.want)
		}
	}
}

func TestParseMoney_Errors(t *testing.T) {
	cases := []struct {
		s    string
		code string
	}{
		{"1.5", "JPY"},    // JPY exponent 0: fractional not allowed
		{"1.234", "USD"},  // too many decimal places for USD
		{"1.2345", "BHD"}, // too many decimal places for BHD (exp 3)
		{"", "USD"},       // empty
		{"abc", "USD"},    // non-numeric
		{".", "USD"},      // no digits
	}
	for _, tc := range cases {
		if got, err := ParseMoney(tc.s, tc.code); err == nil {
			t.Errorf("ParseMoney(%q, %q) = %d, want error", tc.s, tc.code, got)
		}
	}
}

func TestFormatMoney(t *testing.T) {
	cases := []struct {
		minor int64
		code  string
		want  string
	}{
		{10000, "USD", "100.00"},
		{1, "USD", "0.01"},
		{1000, "JPY", "1000"},
		{-500, "USD", "-5.00"}, // -500 cents = -$5.00
		{-50, "USD", "-0.50"},  // -50 cents = -$0.50
		{1234, "BHD", "1.234"},
		{0, "USD", "0.00"},
	}
	for _, tc := range cases {
		if got := FormatMoney(tc.minor, tc.code); got != tc.want {
			t.Errorf("FormatMoney(%d, %q) = %q, want %q", tc.minor, tc.code, got, tc.want)
		}
	}
}

func TestParseFormatRoundTrip(t *testing.T) {
	cases := []struct {
		minor int64
		code  string
	}{
		{10000, "USD"},
		{9990, "USD"},
		{1, "USD"},
		{1000, "JPY"},
		{1234, "BHD"},
		{-500, "USD"},
	}
	for _, tc := range cases {
		s := FormatMoney(tc.minor, tc.code)
		got, err := ParseMoney(s, tc.code)
		if err != nil {
			t.Errorf("round-trip ParseMoney(%q, %q) error: %v", s, tc.code, err)
			continue
		}
		if got != tc.minor {
			t.Errorf("round-trip %d %s: FormatMoney→%q→ParseMoney = %d", tc.minor, tc.code, s, got)
		}
	}
}
