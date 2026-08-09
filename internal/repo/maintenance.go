package repo

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
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

// PruneBallotTokens deletes spent or expired async-ballot tokens. Returns rows removed.
func (r *MaintenanceRepo) PruneBallotTokens(ctx context.Context) (int64, error) {
	tag, err := r.db.Exec(ctx,
		`DELETE FROM ballot_tokens WHERE used = TRUE OR expires_at < now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// PrunePasswordResetTokens deletes used or expired reset tokens. Returns rows removed.
func (r *MaintenanceRepo) PrunePasswordResetTokens(ctx context.Context) (int64, error) {
	tag, err := r.db.Exec(ctx,
		`DELETE FROM password_reset_tokens WHERE used = TRUE OR expires_at < now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// WithLeaderLock runs fn only if it can acquire the given Postgres session-level
// advisory lock on a dedicated pooled connection (held for fn's duration, then
// released). Returns (true, nil) when fn ran, (false, nil) when another process
// already holds the lock — so a job runs on exactly one instance at a time.
func (r *MaintenanceRepo) WithLeaderLock(ctx context.Context, key int64, fn func(context.Context)) (bool, error) {
	conn, err := r.db.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()

	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
		return false, err
	}
	if !got {
		return false, nil
	}
	defer conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, key) //nolint:errcheck
	fn(ctx)
	return true, nil
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

// nightlyJobName keys the nightly job's row in job_runs.
const nightlyJobName = "nightly"

// LastNightlyRun returns when the nightly job last completed successfully, or
// the zero time if it has never run (so the caller treats it as "stale" and
// catches up).
func (r *MaintenanceRepo) LastNightlyRun(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := r.db.QueryRow(ctx,
		`SELECT last_success_at FROM job_runs WHERE job = $1`, nightlyJobName).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil // never run: treat as stale
	}
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

// RecordNightlyRun stamps the nightly job's last-success time to now.
func (r *MaintenanceRepo) RecordNightlyRun(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO job_runs (job, last_success_at) VALUES ($1, now())
		ON CONFLICT (job) DO UPDATE SET last_success_at = now()`, nightlyJobName)
	return err
}
