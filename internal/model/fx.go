package model

import (
	"math/big"
	"sort"
	"strings"
	"time"
)

// FXRate is an effective-dated exchange rate: on and after EffectiveAt, one
// major unit of FromCurrency is worth Rate major units of ToCurrency. Rate is
// carried as an exact decimal string (never a float) so no precision is lost in
// transport or storage.
type FXRate struct {
	ID           string    `json:"id"`
	FromCurrency string    `json:"from_currency"`
	ToCurrency   string    `json:"to_currency"`
	Rate         string    `json:"rate"`
	EffectiveAt  string    `json:"effective_at"` // YYYY-MM-DD
	CreatedAt    time.Time `json:"created_at"`
	CreatedBy    *string   `json:"created_by,omitempty"`
}

// Converter converts money amounts from their own currency into a single
// reporting currency for aggregation. It holds the latest effective rate INTO
// the reporting currency for each source currency; a currency with no such rate
// (and that is not the reporting currency itself) is "unconvertible" and is
// reported rather than silently summed at par.
//
// All arithmetic is exact (math/big.Rat), rounded to the nearest reporting
// minor unit at the end — mirroring the integer-minor-unit discipline of the
// rest of the money code.
type Converter struct {
	reporting string
	rates     map[string]*big.Rat // source currency (upper) -> rate to reporting
}

// NewConverter builds a Converter for the given reporting currency. rates maps
// each source currency to its exact rate into the reporting currency; callers
// typically load these from the fx_rates table. A nil/empty map means only the
// reporting currency itself converts (everything else is unconvertible).
func NewConverter(reporting string, rates map[string]*big.Rat) *Converter {
	norm := make(map[string]*big.Rat, len(rates))
	for k, v := range rates {
		if v == nil {
			continue
		}
		// Copy: sharing the caller's *big.Rat would let later mutation of the
		// caller's map silently change conversion results.
		norm[strings.ToUpper(strings.TrimSpace(k))] = new(big.Rat).Set(v)
	}
	return &Converter{reporting: strings.ToUpper(strings.TrimSpace(reporting)), rates: norm}
}

// Reporting returns the reporting currency code.
func (c *Converter) Reporting() string { return c.reporting }

// Convert converts minor units in `from` to minor units in the reporting
// currency. ok is false when `from` has no rate to the reporting currency (and
// is not itself the reporting currency), in which case the amount must not be
// added to a reporting-currency total.
func (c *Converter) Convert(minor int64, from string) (int64, bool) {
	from = strings.ToUpper(strings.TrimSpace(from))
	if from == c.reporting {
		return minor, true
	}
	rate, ok := c.rates[from]
	if !ok || rate.Sign() <= 0 {
		return 0, false
	}
	// reportingMinor = minor * rate * 10^expTo / 10^expFrom
	//   (minor/10^expFrom is major `from`; × rate is major reporting;
	//    × 10^expTo is reporting minor units)
	v := new(big.Rat).SetInt64(minor)
	v.Mul(v, rate)
	v.Mul(v, new(big.Rat).SetInt64(pow10(CurrencyExponent(c.reporting))))
	v.Quo(v, new(big.Rat).SetInt64(pow10(CurrencyExponent(from))))
	return ratToNearestInt(v), true
}

// Sum converts a set of per-currency subtotals (currency -> minor units in that
// currency) into one total in the reporting currency. It returns the total and
// the sorted, de-duplicated set of currencies it could not convert (and so left
// out of the total).
func (c *Converter) Sum(byCurrency map[string]int64) (total int64, unconvertible []string) {
	seen := map[string]bool{}
	for cur, minor := range byCurrency {
		if v, ok := c.Convert(minor, cur); ok {
			total += v
			continue
		}
		u := strings.ToUpper(strings.TrimSpace(cur))
		if !seen[u] {
			seen[u] = true
			unconvertible = append(unconvertible, u)
		}
	}
	sort.Strings(unconvertible)
	return total, unconvertible
}

// ratToNearestInt rounds an exact rational to the nearest integer, halves away
// from zero (standard commercial rounding).
func ratToNearestInt(x *big.Rat) int64 {
	num := new(big.Int).Abs(x.Num())
	den := x.Denom()
	q := new(big.Int)
	r := new(big.Int)
	q.QuoRem(num, den, r)
	// Round up when the remainder is at least half the denominator (2r >= den).
	if new(big.Int).Lsh(r, 1).Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if x.Sign() < 0 {
		q.Neg(q)
	}
	return q.Int64()
}
