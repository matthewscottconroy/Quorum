package model

import (
	"math/big"
	"reflect"
	"testing"
)

func rat(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		panic("bad rat: " + s)
	}
	return r
}

func TestConverter_SameCurrencyIsIdentity(t *testing.T) {
	c := NewConverter("USD", nil)
	got, ok := c.Convert(12345, "USD")
	if !ok || got != 12345 {
		t.Fatalf("same-currency convert = (%d, %v), want (12345, true)", got, ok)
	}
	// Case/space-insensitive.
	if got, ok := c.Convert(100, "  usd "); !ok || got != 100 {
		t.Fatalf("normalized same-currency convert = (%d, %v)", got, ok)
	}
}

func TestConverter_NoRateIsUnconvertible(t *testing.T) {
	c := NewConverter("USD", map[string]*big.Rat{"EUR": rat("1.1")})
	if _, ok := c.Convert(100, "GBP"); ok {
		t.Fatal("GBP has no rate to USD; convert should report ok=false")
	}
}

func TestConverter_ConvertSameExponent(t *testing.T) {
	// 100.00 EUR at 1.10 -> 110.00 USD. Both exponent 2.
	c := NewConverter("USD", map[string]*big.Rat{"EUR": rat("1.10")})
	got, ok := c.Convert(10000, "EUR")
	if !ok || got != 11000 {
		t.Fatalf("10000 EUR -> USD = (%d, %v), want (11000, true)", got, ok)
	}
}

func TestConverter_ConvertAcrossExponents(t *testing.T) {
	// JPY (exp 0) -> USD (exp 2). 1000 JPY at 0.0067 USD/JPY.
	// 1000 * 0.0067 = 6.70 USD = 670 minor.
	c := NewConverter("USD", map[string]*big.Rat{"JPY": rat("0.0067")})
	got, ok := c.Convert(1000, "JPY")
	if !ok || got != 670 {
		t.Fatalf("1000 JPY -> USD = (%d, %v), want (670, true)", got, ok)
	}

	// USD (exp 2) -> JPY (exp 0). 12.34 USD at 149 JPY/USD = 1838.66 -> 1839 JPY.
	c2 := NewConverter("JPY", map[string]*big.Rat{"USD": rat("149")})
	got2, ok := c2.Convert(1234, "USD")
	if !ok || got2 != 1839 {
		t.Fatalf("1234 USD -> JPY = (%d, %v), want (1839, true)", got2, ok)
	}
}

func TestConverter_ConvertThreeDecimal(t *testing.T) {
	// BHD (exp 3) -> USD (exp 2). 1.500 BHD at 2.65 USD/BHD = 3.975 -> 398 minor (round half up).
	c := NewConverter("USD", map[string]*big.Rat{"BHD": rat("2.65")})
	got, ok := c.Convert(1500, "BHD")
	if !ok || got != 398 {
		t.Fatalf("1500 BHD -> USD = (%d, %v), want (398, true)", got, ok)
	}
}

func TestConverter_RoundingHalfAwayFromZero(t *testing.T) {
	// Construct an exact half: 1 unit at rate 0.005 into a 2-dp currency.
	// 1 (minor, exp0 source) -> reporting minor = 1 * 0.005 * 100 / 1 = 0.5 -> 1.
	c := NewConverter("USD", map[string]*big.Rat{"JPY": rat("0.005")})
	if got, _ := c.Convert(1, "JPY"); got != 1 {
		t.Fatalf("half rounds away from zero: got %d, want 1", got)
	}
	// Negative half rounds to -1.
	if got, _ := c.Convert(-1, "JPY"); got != -1 {
		t.Fatalf("negative half rounds away from zero: got %d, want -1", got)
	}
}

func TestConverter_Sum(t *testing.T) {
	c := NewConverter("USD", map[string]*big.Rat{"EUR": rat("1.10"), "JPY": rat("0.0067")})
	total, unconv := c.Sum(map[string]int64{
		"USD": 10000, // 100.00 -> 100.00
		"EUR": 10000, // 100.00 -> 110.00
		"JPY": 1000,  // 1000   -> 6.70
		"GBP": 5000,  // no rate -> unconvertible
	})
	if total != 10000+11000+670 {
		t.Fatalf("total = %d, want %d", total, 10000+11000+670)
	}
	if !reflect.DeepEqual(unconv, []string{"GBP"}) {
		t.Fatalf("unconvertible = %v, want [GBP]", unconv)
	}
}

func TestConverter_SumAllConvertible(t *testing.T) {
	c := NewConverter("USD", map[string]*big.Rat{"EUR": rat("1.10")})
	total, unconv := c.Sum(map[string]int64{"USD": 500, "EUR": 1000})
	if total != 500+1100 {
		t.Fatalf("total = %d, want 1600", total)
	}
	if len(unconv) != 0 {
		t.Fatalf("unconvertible = %v, want empty", unconv)
	}
}
