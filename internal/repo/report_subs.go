package repo

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// ReportSubsRepo stores which scheduled report digests each user wants.
type ReportSubsRepo struct {
	db *pgxpool.Pool
}

// NewReportSubsRepo constructs the repo.
func NewReportSubsRepo(db *pgxpool.Pool) *ReportSubsRepo {
	return &ReportSubsRepo{db: db}
}

// ForUser lists a user's subscriptions.
func (r *ReportSubsRepo) ForUser(ctx context.Context, userID string) ([]model.ReportSubscription, error) {
	rows, err := r.db.Query(ctx,
		`SELECT report, cadence FROM report_subscriptions WHERE user_id = $1::uuid ORDER BY report`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ReportSubscription{}
	for rows.Next() {
		var s model.ReportSubscription
		if err := rows.Scan(&s.Report, &s.Cadence); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Set upserts a subscription (report → cadence) for a user.
func (r *ReportSubsRepo) Set(ctx context.Context, userID, report, cadence string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO report_subscriptions (user_id, report, cadence)
		VALUES ($1::uuid, $2, $3)
		ON CONFLICT (user_id, report) DO UPDATE SET cadence = EXCLUDED.cadence`,
		userID, report, cadence)
	return err
}

// Delete removes a subscription.
func (r *ReportSubsRepo) Delete(ctx context.Context, userID, report string) error {
	_, err := r.db.Exec(ctx,
		`DELETE FROM report_subscriptions WHERE user_id = $1::uuid AND report = $2`, userID, report)
	return err
}

// DueRecipients returns the email addresses subscribed to a report at a given
// cadence — used by the nightly job on the matching day.
func (r *ReportSubsRepo) DueRecipients(ctx context.Context, report, cadence string) ([]string, error) {
	rows, err := r.db.Query(ctx, `
		SELECT u.email FROM report_subscriptions rs
		JOIN users u ON u.id = rs.user_id
		WHERE rs.report = $1 AND rs.cadence = $2 AND u.email <> ''`, report, cadence)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
