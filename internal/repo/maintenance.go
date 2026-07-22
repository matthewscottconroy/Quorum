package repo

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MaintenanceRepo handles periodic housekeeping of append-only/bookkeeping
// tables that would otherwise grow without bound.
type MaintenanceRepo struct {
	db *pgxpool.Pool
}

// NewMaintenanceRepo constructs a MaintenanceRepo backed by the given pool.
func NewMaintenanceRepo(db *pgxpool.Pool) *MaintenanceRepo {
	return &MaintenanceRepo{db: db}
}

// PruneRefreshTokens deletes tokens that can no longer authenticate anyone:
// every revoked token and every token past its expiry. Returns rows removed.
func (r *MaintenanceRepo) PruneRefreshTokens(ctx context.Context) (int64, error) {
	tag, err := r.db.Exec(ctx,
		`DELETE FROM refresh_tokens WHERE revoked = TRUE OR expires_at < now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// PruneProcessedEvents deletes webhook idempotency records older than the given
// retention window. Providers retry for a few days at most, so a generous
// window keeps dedup effective while bounding growth.
func (r *MaintenanceRepo) PruneProcessedEvents(ctx context.Context, retain time.Duration) (int64, error) {
	tag, err := r.db.Exec(ctx,
		`DELETE FROM processed_events WHERE processed_at < now() - $1::interval`,
		retain.String())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// PruneAuditLog deletes audit entries older than the given retention window.
func (r *MaintenanceRepo) PruneAuditLog(ctx context.Context, retain time.Duration) (int64, error) {
	tag, err := r.db.Exec(ctx,
		`DELETE FROM audit_log WHERE created_at < now() - $1::interval`,
		retain.String())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
