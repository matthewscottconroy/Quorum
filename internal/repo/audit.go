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

// Log records a mutating action by a user against a typed entity. entityType
// is the resource kind (e.g. "members"); entityID is the affected row's id, or
// "" when the request targets no specific row. A blank entityID is stored as
// SQL NULL so the column carries only real ids.
func (r *AuditRepo) Log(ctx context.Context, userID, action, entityType, entityID string) error {
	var eid, etype *string
	if entityID != "" {
		eid = &entityID
	}
	if entityType != "" {
		etype = &entityType
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO audit_log (user_id, action, entity_type, entity_id) VALUES ($1::uuid, $2, $3, $4)`,
		userID, action, etype, eid)
	return err
}
