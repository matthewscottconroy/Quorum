package repo

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditRepo writes entries to the audit_log table.
type AuditRepo struct {
	db *pgxpool.Pool
}

// NewAuditRepo constructs an AuditRepo backed by the given pool.
func NewAuditRepo(db *pgxpool.Pool) *AuditRepo {
	return &AuditRepo{db: db}
}

// Log records a mutating action by a user against an entity id.
func (r *AuditRepo) Log(ctx context.Context, userID, action, entityID string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO audit_log (user_id, action, entity_id) VALUES ($1::uuid, $2, $3)`,
		userID, action, entityID)
	return err
}
