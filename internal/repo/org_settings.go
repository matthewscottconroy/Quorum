package repo

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OrgSettingsRepo stores the small allowlisted org configuration.
type OrgSettingsRepo struct {
	db *pgxpool.Pool
}

// NewOrgSettingsRepo constructs the repo.
func NewOrgSettingsRepo(db *pgxpool.Pool) *OrgSettingsRepo {
	return &OrgSettingsRepo{db: db}
}

// All returns every stored setting.
func (r *OrgSettingsRepo) All(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.Query(ctx, `SELECT key, value FROM org_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// Set upserts one setting.
func (r *OrgSettingsRepo) Set(ctx context.Context, key, value, updatedBy string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO org_settings (key, value, updated_by) VALUES ($1, $2, $3::uuid)
		ON CONFLICT (key) DO UPDATE SET value = $2, updated_by = $3::uuid, updated_at = now()`,
		key, value, updatedBy)
	return err
}
