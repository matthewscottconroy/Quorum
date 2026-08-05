package repo

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// ContinuityRepo: secret-custody registry, continuity checks, and the
// superadmin-inactivity watchdog (roadmap/continuity.md E2-E3).
type ContinuityRepo struct {
	db *pgxpool.Pool
}

// NewContinuityRepo constructs the repo.
func NewContinuityRepo(db *pgxpool.Pool) *ContinuityRepo {
	return &ContinuityRepo{db: db}
}

const custodySelect = `
	SELECT c.id::text, c.name, c.location, c.holder, c.last_verified_at,
	       coalesce(m.display_name, u.email, ''), c.created_at
	FROM secret_custody c
	LEFT JOIN users u ON u.id = c.last_verified_by
	LEFT JOIN members m ON m.id = u.member_id`

// ListCustody returns the registry, alphabetically.
func (r *ContinuityRepo) ListCustody(ctx context.Context) ([]model.SecretCustody, error) {
	rows, err := r.db.Query(ctx, custodySelect+` ORDER BY lower(c.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SecretCustody
	for rows.Next() {
		var c model.SecretCustody
		if err := rows.Scan(&c.ID, &c.Name, &c.Location, &c.Holder,
			&c.LastVerifiedAt, &c.LastVerifiedBy, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateCustody adds a registry row.
func (r *ContinuityRepo) CreateCustody(ctx context.Context, name, location, holder string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO secret_custody (name, location, holder) VALUES ($1, $2, $3)`, name, location, holder)
	return err
}

// UpdateCustody edits a row (attestation resets are deliberate: changing
// location/holder clears the verification - it describes a different copy).
func (r *ContinuityRepo) UpdateCustody(ctx context.Context, id, name, location, holder string) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE secret_custody SET name = $1, location = $2, holder = $3,
			last_verified_at = NULL, last_verified_by = NULL
		WHERE id = $4::uuid`, name, location, holder, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// DeleteCustody removes a row.
func (r *ContinuityRepo) DeleteCustody(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM secret_custody WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// Attest records "this copy verified today" by the caller.
func (r *ContinuityRepo) Attest(ctx context.Context, id, userID string) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE secret_custody SET last_verified_at = now(), last_verified_by = $2::uuid
		WHERE id = $1::uuid`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *ContinuityRepo) setting(ctx context.Context, key string) string {
	var v string
	_ = r.db.QueryRow(ctx, `SELECT value FROM org_settings WHERE key = $1`, key).Scan(&v)
	return v
}

// Checks computes the continuity health picture.
func (r *ContinuityRepo) Checks(ctx context.Context) (*model.ContinuityChecks, error) {
	out := &model.ContinuityChecks{AttestDays: 90}
	if n, err := strconv.Atoi(r.setting(ctx, "continuity_attest_days")); err == nil && n >= 7 && n <= 365 {
		out.AttestDays = n
	}
	if err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE role = 'superadmin'`).Scan(&out.Superadmins); err != nil {
		return nil, err
	}
	if err := r.db.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE last_verified_at IS NULL
			OR last_verified_at < now() - make_interval(days => $1))
		FROM secret_custody`, out.AttestDays).Scan(&out.CustodyRows, &out.CustodyStale); err != nil {
		return nil, err
	}
	if d := r.setting(ctx, "continuity_watch_days"); d != "" && d != "0" {
		out.WatchConfigured = true
	}
	return out, nil
}

// WatchdogEvaluate reports whether the inactivity notice should be sent:
// configured, superadmins silent past the threshold, and not re-noticed
// within 7 days. Contacts come from the continuity_contacts setting.
func (r *ContinuityRepo) WatchdogEvaluate(ctx context.Context) (send bool, contacts []string, silentDays int, err error) {
	days, convErr := strconv.Atoi(r.setting(ctx, "continuity_watch_days"))
	if convErr != nil || days < 7 {
		return false, nil, 0, nil
	}
	raw := strings.TrimSpace(r.setting(ctx, "continuity_contacts"))
	if raw == "" {
		return false, nil, 0, nil
	}
	for _, c := range strings.Split(raw, ",") {
		if c = strings.TrimSpace(c); c != "" {
			contacts = append(contacts, c)
		}
	}
	var last *time.Time
	if err := r.db.QueryRow(ctx,
		`SELECT max(last_login_at) FROM users WHERE role = 'superadmin'`).Scan(&last); err != nil {
		return false, nil, 0, err
	}
	if last == nil {
		return false, nil, 0, nil // never-logged-in bootstrap state: not a bus signal
	}
	silent := int(time.Since(*last).Hours() / 24)
	if silent < days {
		return false, nil, 0, nil
	}
	if ln := r.setting(ctx, "continuity_last_notice"); ln != "" {
		if t, e := time.Parse(time.RFC3339, ln); e == nil && time.Since(t) < 7*24*time.Hour {
			return false, nil, 0, nil
		}
	}
	return true, contacts, silent, nil
}

// WatchdogMarkNotified suppresses repeat notices for a week.
func (r *ContinuityRepo) WatchdogMarkNotified(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO org_settings (key, value) VALUES ('continuity_last_notice', $1)
		ON CONFLICT (key) DO UPDATE SET value = $1, updated_at = now()`,
		time.Now().UTC().Format(time.RFC3339))
	return err
}
