package repo

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// bucketedMoney holds per-bucket (e.g. month or status) currency subtotals,
// preserving the query's bucket order. buckets[bucket][currency] = minor units.
type bucketedMoney struct {
	order   []string
	buckets map[string]map[string]int64
}

// groupMoneyByBucket runs a "bucket, currency, amount_minor" grouped query into
// a bucketedMoney, preserving first-seen bucket order.
func (r *AnalyticsRepo) groupMoneyByBucket(ctx context.Context, query string, args ...any) (*bucketedMoney, error) {
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bm := &bucketedMoney{buckets: map[string]map[string]int64{}}
	for rows.Next() {
		var bucket, cur string
		var amt int64
		if err := rows.Scan(&bucket, &cur, &amt); err != nil {
			return nil, err
		}
		if bm.buckets[bucket] == nil {
			bm.buckets[bucket] = map[string]int64{}
			bm.order = append(bm.order, bucket)
		}
		bm.buckets[bucket][cur] += amt
	}
	return bm, rows.Err()
}

// markAll records each currency in the set.
func markAll(set map[string]bool, currencies []string) {
	for _, c := range currencies {
		set[c] = true
	}
}

// sortedKeys returns the set's keys sorted, or an empty (non-nil) slice.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mergeCurrencies unions two currency-code slices into one sorted, de-duplicated
// slice.
func mergeCurrencies(a, b []string) []string {
	set := map[string]bool{}
	markAll(set, a)
	markAll(set, b)
	return sortedKeys(set)
}

// AnalyticsRepo runs read-only aggregate queries for the analytics dashboard.
// Money figures are converted into the org reporting currency at the
// aggregation boundary: each cross-currency SUM is grouped by currency and the
// per-currency subtotals are converted via the FX rates (see fxRates). Amounts
// in a currency with no rate to the reporting currency cannot be converted and
// are reported (UnconvertibleCurrencies) rather than summed at par.
type AnalyticsRepo struct {
	db *pgxpool.Pool
	fx fxConverterSource
}

// fxConverterSource supplies currency converters. *FXRepo satisfies it; kept as
// an interface so analytics/budget don't hard-depend on the FX repo's other
// methods. Converter targets the org reporting currency; ConverterFor targets an
// arbitrary currency (used when folding dues schedules into a scenario currency).
type fxConverterSource interface {
	Converter(ctx context.Context) (*model.Converter, error)
	ConverterFor(ctx context.Context, target string) (*model.Converter, error)
}

// NewAnalyticsRepo constructs an AnalyticsRepo. fx supplies the reporting-
// currency converter used to roll up cross-currency amounts.
func NewAnalyticsRepo(db *pgxpool.Pool, fx fxConverterSource) *AnalyticsRepo {
	return &AnalyticsRepo{db: db, fx: fx}
}

// sumByCurrency runs a "currency, amount_minor" grouped query and returns the
// per-currency subtotals as a map, ready to hand to a Converter.
func (r *AnalyticsRepo) sumByCurrency(ctx context.Context, query string, args ...any) (map[string]int64, error) {
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var cur string
		var amt int64
		if err := rows.Scan(&cur, &amt); err != nil {
			return nil, err
		}
		out[cur] += amt
	}
	return out, rows.Err()
}

// scanCategoryValues runs a "label, value" query into a slice.
func (r *AnalyticsRepo) scanCategoryValues(ctx context.Context, query string, args ...any) ([]model.CategoryValue, error) {
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.CategoryValue, 0)
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
	out := make([]model.SeriesPoint, 0)
	for rows.Next() {
		var p model.SeriesPoint
		if err := rows.Scan(&p.X, &p.Y); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Overview returns the headline KPIs, with money converted into the reporting
// currency.
func (r *AnalyticsRepo) Overview(ctx context.Context) (*model.AnalyticsOverview, error) {
	conv, err := r.fx.Converter(ctx)
	if err != nil {
		return nil, err
	}
	var o model.AnalyticsOverview
	o.Currency = conv.Reporting()

	if err := r.db.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM members WHERE status = 'active'),
			(SELECT count(*) FROM motions WHERE status = 'open'),
			(SELECT count(*) FROM meetings WHERE scheduled_at >= now() AND status = 'scheduled')`).
		Scan(&o.ActiveMembers, &o.OpenMotions, &o.UpcomingMeetings); err != nil {
		return nil, err
	}

	// YTD payments and outstanding dues, each grouped by currency then converted.
	ytdByCur, err := r.sumByCurrency(ctx, `
		SELECT currency, coalesce(sum(amount), 0)
		FROM transactions WHERE occurred_at >= date_trunc('year', now())
		GROUP BY currency`)
	if err != nil {
		return nil, err
	}
	outByCur, err := r.sumByCurrency(ctx, `
		SELECT currency, coalesce(sum(amount), 0)
		FROM dues_invoices WHERE status IN ('pending', 'overdue', 'partial')
		GROUP BY currency`)
	if err != nil {
		return nil, err
	}

	var unconv1, unconv2 []string
	o.YTDPaymentsMinor, unconv1 = conv.Sum(ytdByCur)
	o.OutstandingMinor, unconv2 = conv.Sum(outByCur)
	o.UnconvertibleCurrencies = mergeCurrencies(unconv1, unconv2)

	// Retained for backward compatibility: true when the org's money spans more
	// than one currency at all (the converted figures above stay meaningful, but
	// the UI may still note the mix).
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
	a.Meetings = make([]model.MeetingAttendanceStat, 0)
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
// breakdown, and the total outstanding — all converted into the reporting
// currency. Currencies without a rate are listed in UnconvertibleCurrencies.
func (r *AnalyticsRepo) Payments(ctx context.Context) (*model.PaymentsAnalytics, error) {
	conv, err := r.fx.Converter(ctx)
	if err != nil {
		return nil, err
	}
	var p model.PaymentsAnalytics
	p.Currency = conv.Reporting()
	unconv := map[string]bool{}

	// Monthly collected: group by (month, currency), convert each month's mix.
	byMonth, err := r.groupMoneyByBucket(ctx, `
		SELECT to_char(date_trunc('month', occurred_at), 'YYYY-MM') AS bucket, currency, coalesce(sum(amount), 0)
		FROM transactions
		WHERE occurred_at >= date_trunc('month', now()) - interval '11 months'
		GROUP BY 1, 2 ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	p.Monthly = make([]model.SeriesPoint, 0, len(byMonth.order))
	for _, month := range byMonth.order {
		total, u := conv.Sum(byMonth.buckets[month])
		markAll(unconv, u)
		p.Monthly = append(p.Monthly, model.SeriesPoint{X: month, Y: total})
	}

	// Dues by status: group by (status, currency), convert each status's mix.
	byStatus, err := r.groupMoneyByBucket(ctx, `
		SELECT status AS bucket, currency, coalesce(sum(amount), 0)
		FROM dues_invoices GROUP BY 1, 2 ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	// Per-status counts (currency-independent).
	counts, err := r.scanCategoryValues(ctx,
		`SELECT status, count(*) FROM dues_invoices GROUP BY status ORDER BY status`)
	if err != nil {
		return nil, err
	}
	countByStatus := map[string]int{}
	for _, c := range counts {
		countByStatus[c.Label] = int(c.Value)
	}
	p.DuesByStatus = make([]model.StatusAmount, 0, len(byStatus.order))
	for _, status := range byStatus.order {
		amt, u := conv.Sum(byStatus.buckets[status])
		markAll(unconv, u)
		p.DuesByStatus = append(p.DuesByStatus, model.StatusAmount{
			Status: status, Count: countByStatus[status], AmountMinor: amt,
		})
		if status == "pending" || status == "overdue" || status == "partial" {
			p.OutstandingMinor += amt
		}
	}
	p.UnconvertibleCurrencies = sortedKeys(unconv)
	return &p, nil
}
