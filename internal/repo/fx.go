package repo

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// FXRepo provides data access for the org reporting currency (stored on the
// single governance_settings row) and the effective-dated exchange-rate table.
type FXRepo struct {
	db *pgxpool.Pool
}

// NewFXRepo constructs an FXRepo backed by the given pool.
func NewFXRepo(db *pgxpool.Pool) *FXRepo {
	return &FXRepo{db: db}
}

// normalizeCurrency upper-cases and trims a currency code.
func normalizeCurrency(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// ReportingCurrency returns the org-wide reporting currency (defaults to USD).
func (r *FXRepo) ReportingCurrency(ctx context.Context) (string, error) {
	var cur string
	err := r.db.QueryRow(ctx,
		`SELECT reporting_currency FROM governance_settings WHERE id = 1`).Scan(&cur)
	if err != nil {
		return "", err
	}
	if cur == "" {
		return "USD", nil
	}
	return cur, nil
}

// SetReportingCurrency updates the org-wide reporting currency.
func (r *FXRepo) SetReportingCurrency(ctx context.Context, code string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE governance_settings SET reporting_currency = $1, updated_at = now() WHERE id = 1`,
		normalizeCurrency(code))
	return err
}

// ListRates returns all exchange rates, newest effective date first.
func (r *FXRepo) ListRates(ctx context.Context) ([]model.FXRate, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, from_currency, to_currency, rate::text,
		       to_char(effective_at, 'YYYY-MM-DD'), created_at, created_by::text
		FROM fx_rates
		ORDER BY effective_at DESC, from_currency, to_currency`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.FXRate, 0)
	for rows.Next() {
		var fr model.FXRate
		if err := rows.Scan(&fr.ID, &fr.FromCurrency, &fr.ToCurrency, &fr.Rate,
			&fr.EffectiveAt, &fr.CreatedAt, &fr.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, fr)
	}
	return out, rows.Err()
}

// CreateRate inserts a new exchange rate. from/to are normalized; rate is a
// decimal string and effectiveAt is a YYYY-MM-DD date. A duplicate
// (from, to, effective_at) is rejected by the unique constraint.
func (r *FXRepo) CreateRate(ctx context.Context, from, to, rate, effectiveAt, createdBy string) (*model.FXRate, error) {
	from, to = normalizeCurrency(from), normalizeCurrency(to)
	var createdByArg any
	if createdBy != "" {
		createdByArg = createdBy
	}
	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO fx_rates (from_currency, to_currency, rate, effective_at, created_by)
		VALUES ($1, $2, $3::numeric, $4::date, $5)
		RETURNING id::text`,
		from, to, rate, effectiveAt, createdByArg).Scan(&id)
	if err != nil {
		return nil, err
	}
	return r.getRate(ctx, id)
}

func (r *FXRepo) getRate(ctx context.Context, id string) (*model.FXRate, error) {
	var fr model.FXRate
	err := r.db.QueryRow(ctx, `
		SELECT id::text, from_currency, to_currency, rate::text,
		       to_char(effective_at, 'YYYY-MM-DD'), created_at, created_by::text
		FROM fx_rates WHERE id = $1::uuid`, id).
		Scan(&fr.ID, &fr.FromCurrency, &fr.ToCurrency, &fr.Rate, &fr.EffectiveAt, &fr.CreatedAt, &fr.CreatedBy)
	if err != nil {
		return nil, err
	}
	return &fr, nil
}

// DeleteRate removes an exchange rate by id, returning pgx.ErrNoRows if absent.
func (r *FXRepo) DeleteRate(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM fx_rates WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// Converter builds a model.Converter for the current org reporting currency.
func (r *FXRepo) Converter(ctx context.Context) (*model.Converter, error) {
	reporting, err := r.ReportingCurrency(ctx)
	if err != nil {
		return nil, err
	}
	return r.ConverterFor(ctx, reporting)
}

// ConverterFor builds a model.Converter that converts INTO the given target
// currency, loaded with the latest effective rate for every source currency (as
// of today). Source currencies without such a rate are absent from the
// converter and will be reported as unconvertible by callers.
func (r *FXRepo) ConverterFor(ctx context.Context, target string) (*model.Converter, error) {
	target = normalizeCurrency(target)
	// DISTINCT ON keeps only the most recent effective row per source currency.
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT ON (from_currency) from_currency, rate::text
		FROM fx_rates
		WHERE to_currency = $1 AND effective_at <= current_date
		ORDER BY from_currency, effective_at DESC`, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rates := map[string]*big.Rat{}
	for rows.Next() {
		var from, rateStr string
		if err := rows.Scan(&from, &rateStr); err != nil {
			return nil, err
		}
		rat, ok := new(big.Rat).SetString(rateStr)
		if !ok {
			return nil, fmt.Errorf("fx: unparseable rate %q for %s", rateStr, from)
		}
		rates[from] = rat
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return model.NewConverter(target, rates), nil
}
