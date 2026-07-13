package repo

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type AuditRepo struct {
	db *pgxpool.Pool
}

func NewAuditRepo(db *pgxpool.Pool) *AuditRepo {
	return &AuditRepo{db: db}
}

func (r *AuditRepo) Log(ctx context.Context, userID, action, entityID string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO audit_log (user_id, action, entity_id) VALUES ($1::uuid, $2, $3)`,
		userID, action, entityID)
	return err
}
