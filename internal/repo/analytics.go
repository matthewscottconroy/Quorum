package repo

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// AnalyticsRepo runs read-only aggregate queries for the analytics dashboard.
// It assumes a single reporting currency (amounts are summed across rows); when
// more than one currency is present the summed figures are not meaningful, and
// Overview reports MixedCurrencies so the UI can warn. Per-currency reporting is
// a future enhancement.
type AnalyticsRepo struct {
	db *pgxpool.Pool
}

// NewAnalyticsRepo constructs an AnalyticsRepo.
func NewAnalyticsRepo(db *pgxpool.Pool) *AnalyticsRepo {
	return &AnalyticsRepo{db: db}
}

const defaultCurrency = "USD"

// reportingCurrency returns the currency of the most recent transaction, or USD.
func (r *AnalyticsRepo) reportingCurrency(ctx context.Context) string {
	var cur string
	err := r.db.QueryRow(ctx, `SELECT currency FROM transactions ORDER BY occurred_at DESC LIMIT 1`).Scan(&cur)
	if err != nil || cur == "" {
		return defaultCurrency
	}
	return cur
}

// scanCategoryValues runs a "label, value" query into a slice.
func (r *AnalyticsRepo) scanCategoryValues(ctx context.Context, query string, args ...any) ([]model.CategoryValue, error) {
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.CategoryValue
	for rows.Next() {
		var c model.CategoryValue
		if err := rows.Scan(&c.Label, &c.Value); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// scanSeries runs an "x, y" query into a time series.
func (r *AnalyticsRepo) scanSeries(ctx context.Context, query string, args ...any) ([]model.SeriesPoint, error) {
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SeriesPoint
	for rows.Next() {
		var p model.SeriesPoint
		if err := rows.Scan(&p.X, &p.Y); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Overview returns the headline KPIs.
func (r *AnalyticsRepo) Overview(ctx context.Context) (*model.AnalyticsOverview, error) {
	var o model.AnalyticsOverview
	o.Currency = r.reportingCurrency(ctx)
	err := r.db.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM members WHERE status = 'active'),
			(SELECT coalesce(sum(amount), 0) FROM transactions WHERE occurred_at >= date_trunc('year', now())),
			(SELECT coalesce(sum(amount), 0) FROM dues_invoices WHERE status IN ('pending', 'overdue', 'partial')),
			(SELECT count(*) FROM motions WHERE status = 'open'),
			(SELECT count(*) FROM meetings WHERE scheduled_at >= now() AND status = 'scheduled')`).
		Scan(&o.ActiveMembers, &o.YTDPaymentsMinor, &o.OutstandingMinor, &o.OpenMotions, &o.UpcomingMeetings)
	if err != nil {
		return nil, err
	}
	// Flag when money is spread across more than one currency, since the summed
	// figures above ignore currency and would be misleading.
	if err := r.db.QueryRow(ctx, `
		SELECT count(*) > 1 FROM (
			SELECT currency FROM transactions
			UNION
			SELECT currency FROM dues_invoices
		) c`).Scan(&o.MixedCurrencies); err != nil {
		return nil, err
	}
	return &o, nil
}

// Membership returns roster breakdowns and monthly new-member counts (12 months).
func (r *AnalyticsRepo) Membership(ctx context.Context) (*model.MembershipAnalytics, error) {
	var m model.MembershipAnalytics
	var err error
	if m.ByStatus, err = r.scanCategoryValues(ctx,
		`SELECT status, count(*) FROM members GROUP BY status ORDER BY status`); err != nil {
		return nil, err
	}
	if m.ByTier, err = r.scanCategoryValues(ctx,
		`SELECT tier, count(*) FROM members WHERE status = 'active' GROUP BY tier ORDER BY count(*) DESC`); err != nil {
		return nil, err
	}
	if m.Growth, err = r.scanSeries(ctx, `
		SELECT to_char(date_trunc('month', joined_at), 'YYYY-MM') AS m, count(*)
		FROM members
		WHERE joined_at >= date_trunc('month', now()) - interval '11 months'
		GROUP BY 1 ORDER BY 1`); err != nil {
		return nil, err
	}
	for _, s := range m.ByStatus {
		if s.Label == "active" {
			m.ActiveTotal = int(s.Value)
		}
	}
	return &m, nil
}

// Attendance returns present/total counts for the most recent meetings and the
// average present count across them.
func (r *AnalyticsRepo) Attendance(ctx context.Context) (*model.AttendanceAnalytics, error) {
	rows, err := r.db.Query(ctx, `
		SELECT m.title, m.scheduled_at,
		       count(*) FILTER (WHERE ma.present) AS present,
		       count(ma.*) AS attendees
		FROM meetings m
		LEFT JOIN meeting_attendees ma ON ma.meeting_id = m.id
		GROUP BY m.id, m.title, m.scheduled_at
		HAVING count(ma.*) > 0
		ORDER BY m.scheduled_at DESC
		LIMIT 12`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var a model.AttendanceAnalytics
	total := 0
	for rows.Next() {
		var s model.MeetingAttendanceStat
		var ignored any
		if err := rows.Scan(&s.Label, &ignored, &s.Present, &s.Attendees); err != nil {
			return nil, err
		}
		a.Meetings = append(a.Meetings, s)
		total += s.Present
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Present the meetings oldest-first for the chart's left-to-right timeline.
	for i, j := 0, len(a.Meetings)-1; i < j; i, j = i+1, j-1 {
		a.Meetings[i], a.Meetings[j] = a.Meetings[j], a.Meetings[i]
	}
	if len(a.Meetings) > 0 {
		a.AvgPresent = float64(total) / float64(len(a.Meetings))
	}
	return &a, nil
}

// Governance returns motion outcomes and the overall vote split.
func (r *AnalyticsRepo) Governance(ctx context.Context) (*model.GovernanceAnalytics, error) {
	var g model.GovernanceAnalytics
	var err error
	if g.ByOutcome, err = r.scanCategoryValues(ctx,
		`SELECT status, count(*) FROM motions GROUP BY status ORDER BY count(*) DESC`); err != nil {
		return nil, err
	}
	if g.Votes, err = r.scanCategoryValues(ctx,
		`SELECT choice, count(*) FROM motion_votes GROUP BY choice ORDER BY choice`); err != nil {
		return nil, err
	}
	for _, o := range g.ByOutcome {
		g.TotalMotions += int(o.Value)
	}
	return &g, nil
}

// Payments returns monthly collected amounts (12 months), the dues-invoice status
// breakdown, and the total outstanding.
func (r *AnalyticsRepo) Payments(ctx context.Context) (*model.PaymentsAnalytics, error) {
	var p model.PaymentsAnalytics
	p.Currency = r.reportingCurrency(ctx)
	var err error
	if p.Monthly, err = r.scanSeries(ctx, `
		SELECT to_char(date_trunc('month', occurred_at), 'YYYY-MM') AS m, coalesce(sum(amount), 0)
		FROM transactions
		WHERE occurred_at >= date_trunc('month', now()) - interval '11 months'
		GROUP BY 1 ORDER BY 1`); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `
		SELECT status, count(*), coalesce(sum(amount), 0)
		FROM dues_invoices GROUP BY status ORDER BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s model.StatusAmount
		if err := rows.Scan(&s.Status, &s.Count, &s.AmountMinor); err != nil {
			return nil, err
		}
		p.DuesByStatus = append(p.DuesByStatus, s)
		if s.Status == "pending" || s.Status == "overdue" || s.Status == "partial" {
			p.OutstandingMinor += s.AmountMinor
		}
	}
	return &p, rows.Err()
}
