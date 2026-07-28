package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
)

// PeriodFor returns the invoice period label, the period's start, and the due
// date for a cadence as of time t. dueDays is added to the period start to get
// the due date. Pure and deterministic so recurring-dues generation is testable.
func PeriodFor(cadence string, dueDays int, t time.Time) (label string, start, due time.Time) {
	y := t.Year()
	loc := t.Location()
	switch cadence {
	case "monthly":
		start = time.Date(y, t.Month(), 1, 0, 0, 0, 0, loc)
		label = start.Format("2006-01")
	case "quarterly":
		q := (int(t.Month()) - 1) / 3 // 0..3
		start = time.Date(y, time.Month(q*3+1), 1, 0, 0, 0, 0, loc)
		label = fmt.Sprintf("%d-Q%d", y, q+1)
	default: // annual
		start = time.Date(y, 1, 1, 0, 0, 0, 0, loc)
		label = fmt.Sprintf("%d", y)
	}
	due = start.AddDate(0, 0, dueDays)
	return label, start, due
}

// ListSchedules returns all dues schedules, newest first.
func (r *DuesRepo) ListSchedules(ctx context.Context) ([]model.DuesSchedule, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, tier, amount_minor, currency, cadence, due_days, active, created_at, updated_at
		FROM dues_schedules ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSchedules(rows)
}

// ListActiveDuesSchedules returns only the active schedules (used by the job).
func (r *DuesRepo) ListActiveDuesSchedules(ctx context.Context) ([]model.DuesSchedule, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, tier, amount_minor, currency, cadence, due_days, active, created_at, updated_at
		FROM dues_schedules WHERE active ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSchedules(rows)
}

func scanSchedules(rows pgx.Rows) ([]model.DuesSchedule, error) {
	var out []model.DuesSchedule
	for rows.Next() {
		var s model.DuesSchedule
		if err := rows.Scan(&s.ID, &s.Tier, &s.AmountMinor, &s.Currency, &s.Cadence,
			&s.DueDays, &s.Active, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSchedule returns one schedule, or pgx.ErrNoRows.
func (r *DuesRepo) GetSchedule(ctx context.Context, id string) (*model.DuesSchedule, error) {
	var s model.DuesSchedule
	err := r.db.QueryRow(ctx, `
		SELECT id::text, tier, amount_minor, currency, cadence, due_days, active, created_at, updated_at
		FROM dues_schedules WHERE id = $1::uuid`, id).
		Scan(&s.ID, &s.Tier, &s.AmountMinor, &s.Currency, &s.Cadence, &s.DueDays, &s.Active, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// CreateSchedule inserts a dues schedule.
func (r *DuesRepo) CreateSchedule(ctx context.Context, s *model.DuesSchedule) (*model.DuesSchedule, error) {
	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO dues_schedules (tier, amount_minor, currency, cadence, due_days, active)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`,
		s.Tier, s.AmountMinor, s.Currency, s.Cadence, s.DueDays, s.Active).Scan(&id)
	if err != nil {
		return nil, err
	}
	return r.GetSchedule(ctx, id)
}

// UpdateSchedule applies field changes to a schedule.
func (r *DuesRepo) UpdateSchedule(ctx context.Context, id string, tier *string, amountMinor *int64, currency, cadence *string, dueDays *int, active *bool) (*model.DuesSchedule, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE dues_schedules SET
			tier         = coalesce($1, tier),
			amount_minor = coalesce($2, amount_minor),
			currency     = coalesce($3, currency),
			cadence      = coalesce($4, cadence),
			due_days     = coalesce($5, due_days),
			active       = coalesce($6, active),
			updated_at   = now()
		WHERE id = $7::uuid`, tier, amountMinor, currency, cadence, dueDays, active, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, pgx.ErrNoRows
	}
	return r.GetSchedule(ctx, id)
}

// DeleteSchedule removes a schedule.
func (r *DuesRepo) DeleteSchedule(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM dues_schedules WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// GenerateInvoicesForSchedule creates a pending invoice for every active member
// of the schedule's tier who does not already have an invoice for the given
// period label. It returns the number of invoices created. Idempotent: running
// it again for the same period is a no-op.
func (r *DuesRepo) GenerateInvoicesForSchedule(ctx context.Context, s model.DuesSchedule, label string, due time.Time) (int, error) {
	// NOTE: the invoice amount column is named "amount" but stores integer minor
	// units (see migration 0006); model.DuesInvoice maps it to AmountMinor.
	tag, err := r.db.Exec(ctx, `
		INSERT INTO dues_invoices (member_id, amount, currency, period_label, due_date, status)
		SELECT m.id, $1, $2, $3, $4, 'pending'
		FROM members m
		WHERE m.status = 'active' AND m.tier = $5
		  AND NOT EXISTS (
		      SELECT 1 FROM dues_invoices di
		      WHERE di.member_id = m.id AND di.period_label = $3
		  )`,
		s.AmountMinor, s.Currency, label, due, s.Tier)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
